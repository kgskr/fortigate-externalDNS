package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/controller"
	"github.com/gilsu/fortigate-external-dns/internal/fortigate"
	"github.com/gilsu/fortigate-external-dns/internal/metrics"
	"github.com/gilsu/fortigate-external-dns/internal/serve"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(2)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(2)
	}
	logger.Info("fortigate configuration", "fortigate", cfg.FortiGate.Redacted())
	if cfg.APITokenFromFlag {
		logger.Warn("FortiGate API token was supplied via --fortigate-api-token; this exposes the token in process arguments and rendered manifests. Prefer FORTIGATE_API_TOKEN from a Kubernetes Secret.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kubeClients, err := controller.NewKubernetesClients(cfg.Kubeconfig)
	if err != nil {
		logger.Error("kubernetes client setup failed", "error", err)
		os.Exit(1)
	}

	recorder := metrics.New()

	fortiClient, err := fortigate.NewClient(cfg.FortiGate, logger, recorder)
	if err != nil {
		logger.Error("fortigate client setup failed", "error", err)
		os.Exit(1)
	}

	server, err := startProbeServer(cfg, recorder, logger)
	if err != nil {
		logger.Error("probe server setup failed", "error", err)
		os.Exit(1)
	}
	if server != nil {
		defer func() {
			// Report not-ready before tearing down so probes observe the drain.
			server.SetReady(false)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
	}

	runner := controller.Runner{
		Config:    cfg,
		Kube:      kubeClients,
		DNSClient: fortiClient,
		Logger:    logger,
		Metrics:   recorder,
	}

	// Clients and configuration are ready; the pod can serve its role.
	if server != nil {
		server.SetReady(true)
	}

	run := func(ctx context.Context) error {
		if cfg.Once {
			return runner.RunOnce(ctx)
		}
		return runner.Run(ctx)
	}

	if useLeaderElection(cfg) {
		err = runWithLeaderElection(ctx, cfg, kubeClients.Core, logger, run)
	} else {
		err = run(ctx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("controller stopped with error", "error", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stdout, "controller stopped")
}

func startProbeServer(cfg config.Config, recorder *metrics.Metrics, logger *slog.Logger) (*serve.Server, error) {
	if strings.TrimSpace(cfg.MetricsAddr) == "" {
		return nil, nil
	}
	server := serve.New(cfg.MetricsAddr, recorder.Handler())
	// Bind synchronously so a failed bind is a fatal startup error rather than an
	// asynchronous goroutine failure that leaves the controller reporting ready
	// with no health/metrics listener.
	listener, err := server.Listen()
	if err != nil {
		return nil, fmt.Errorf("bind metrics address %q: %w", cfg.MetricsAddr, err)
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("probe server stopped", "error", err)
		}
	}()
	return server, nil
}

// useLeaderElection reports whether the long-running controller should compete
// for leadership. A one-shot --once run has no leader to elect and always runs
// directly, and leader election can be disabled for local testing.
func useLeaderElection(cfg config.Config) bool {
	return !cfg.Once && cfg.LeaderElection
}

func runWithLeaderElection(ctx context.Context, cfg config.Config, client kubernetes.Interface, logger *slog.Logger, run func(context.Context) error) error {
	namespace := strings.TrimSpace(cfg.LeaderElectionNamespace)
	if namespace == "" {
		namespace = strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	}
	if namespace == "" {
		namespace = "kube-system"
	}

	identity := strings.TrimSpace(os.Getenv("POD_NAME"))
	if identity == "" {
		if host, err := os.Hostname(); err == nil {
			identity = host
		}
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: cfg.LeaderElectionID, Namespace: namespace},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
	}

	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// started is closed when leadership is acquired; result carries run()'s
	// outcome. Once OnStartedLeading has begun we block on result for run()'s real
	// outcome instead of racing RunOrDie's return. One narrow window remains: if
	// the context is canceled in the instant between acquiring the lease and the
	// callback goroutine being scheduled, the default branch returns nil while
	// run() (invoked with an already-canceled context) returns ctx.Err() into the
	// buffered result and is discarded. main treats nil and context.Canceled
	// identically on shutdown, so no meaningful outcome is lost.
	started := make(chan struct{})
	result := make(chan error, 1)
	leaderelection.RunOrDie(leaderCtx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(c context.Context) {
				close(started)
				logger.Info("became leader; starting reconciliation", "identity", identity)
				result <- run(c)
				cancel()
			},
			OnStoppedLeading: func() {
				logger.Info("stopped leading", "identity", identity)
			},
		},
	})

	select {
	case <-started:
		// Leadership was acquired; block for run()'s real outcome.
		return <-result
	default:
		// Never became leader (for example the context was canceled while waiting).
		return nil
	}
}
