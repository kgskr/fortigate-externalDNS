package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/fortigate"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/policy"
	"github.com/kgskr/fortigate-external-dns/internal/serve"
)

// version and commit are stamped at build time via
// -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// os.Exit is called only here, after run() has returned and all of its
	// deferred cleanup (readiness drain, probe-server shutdown, signal stop) has
	// executed. run() must never call os.Exit itself.
	os.Exit(run())
}

func run() int {
	// --version must work without any other configuration (and regardless of
	// malformed environment variables), so it is detected before config.Load.
	if versionRequested(os.Args[1:]) {
		fmt.Fprintf(os.Stdout, "fortigate-external-dns %s (%s)\n", version, commit)
		return 0
	}

	// Bootstrap logger for configuration errors; replaced by the configured
	// handler once flags and environment have parsed.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		logger.Error("configuration failed", "error", err)
		return 2
	}
	// Switch to the configured handler before validation so validation errors
	// already come out in the requested format; buildLogger falls back to
	// text/info for values that validation is about to reject.
	logger = buildLogger(os.Stdout, cfg.LogFormat, cfg.LogLevel)
	// client-go (leader election, informers) logs through klog; route it through
	// the configured handler so --log-format=json covers every line.
	klog.SetSlogLogger(logger)
	if err := cfg.Validate(); err != nil {
		logger.Error("configuration invalid", "error", err)
		return 2
	}
	logger.Info("starting fortigate-external-dns", "version", version, "commit", commit)
	logger.Info("fortigate configuration", "fortigate", cfg.FortiGate.Redacted())
	if cfg.APITokenFromFlag {
		logger.Warn("FortiGate API token was supplied via --fortigate-api-token; this exposes the token in process arguments and rendered manifests. Prefer FORTIGATE_API_TOKEN from a Kubernetes Secret.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kubeClients, err := controller.NewKubernetesClients(cfg.Kubeconfig)
	if err != nil {
		logger.Error("kubernetes client setup failed", "error", err)
		return 1
	}

	recorder := metrics.New()
	recorder.SetBuildInfo(version, commit)

	server, err := startProbeServer(cfg, recorder, logger)
	if err != nil {
		logger.Error("probe server setup failed", "error", err)
		return 1
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

	heartbeat := controller.NewHeartbeat()
	var loop func(context.Context) error
	if cfg.TargetMode {
		loop = func(ctx context.Context) error {
			heartbeat.SetActive(true)
			defer heartbeat.SetActive(false)
			return runTargetMode(ctx, cfg, kubeClients, recorder, logger, heartbeat)
		}
	} else {
		fortiClient, clientErr := fortigate.NewClient(cfg.FortiGate, logger, recorder)
		if clientErr != nil {
			logger.Error("fortigate client setup failed", "error", clientErr)
			return 1
		}
		runner := controller.Runner{
			Config:    cfg,
			Kube:      kubeClients,
			DNSClient: fortiClient,
			Logger:    logger,
			Metrics:   recorder,
			Heartbeat: heartbeat,
		}
		if cfg.PolicyEnforcement {
			provider, providerErr := policy.NewDynamicProvider(kubeClients.Dynamic)
			if providerErr != nil {
				logger.Error("policy client setup failed", "error", providerErr)
				return 1
			}
			runner.PolicyProvider = provider
		}
		loop = func(ctx context.Context) error {
			heartbeat.SetActive(true)
			defer heartbeat.SetActive(false)
			if cfg.Once {
				return runner.RunOnce(ctx)
			}
			return runner.Run(ctx)
		}
	}

	// Clients and configuration are ready; the pod can serve its role. Liveness
	// additionally tracks the reconcile heartbeat: it fails only while this
	// replica is responsible for reconciling but completes no attempt within the
	// staleness window (a wedged loop), never merely because attempts error.
	if server != nil {
		staleness := cfg.ResolvedHealthzMaxStaleness()
		server.SetLivenessCheck(func() bool { return heartbeat.Healthy(staleness) })
		server.SetReady(true)
	}

	if useLeaderElection(cfg) {
		err = runWithLeaderElection(ctx, cfg, kubeClients.Core, logger, loop)
	} else {
		err = loop(ctx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("controller stopped with error", "error", err)
		return 1
	}

	logger.Info("controller stopped")
	return 0
}

// versionRequested reports whether the argument list asks for --version. Only
// arguments before a "--" terminator are considered.
func versionRequested(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--":
			return false
		case "--version", "-version":
			return true
		}
	}
	return false
}

// buildLogger constructs the process logger from validated log configuration.
func buildLogger(w io.Writer, format, level string) *slog.Logger {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: slogLevel}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
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

// errLeadershipLost is returned when this replica loses the lease while the
// process is not shutting down, so the process exits non-zero instead of
// logging a clean stop and being restarted as if it had finished.
var errLeadershipLost = errors.New("leader election lost")

// leaderElectionTiming is a variable so tests can shorten the lease timings.
var leaderElectionTiming = struct{ leaseDuration, renewDeadline, retryPeriod time.Duration }{
	leaseDuration: 15 * time.Second, renewDeadline: 10 * time.Second, retryPeriod: 2 * time.Second,
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
			identity = strings.TrimSpace(host)
		}
	}
	if identity == "" {
		// An empty identity makes leaderelection.RunOrDie panic; fail with a clear
		// error instead so main can exit cleanly.
		return errors.New("leader election identity is empty: set POD_NAME or ensure the pod hostname is available")
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: cfg.LeaderElectionID, Namespace: namespace},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
	}

	// The elector runs on its own context, deliberately not derived from the
	// signal context. With ReleaseOnCancel, canceling the elector releases the
	// Lease, and client-go warns that this must not happen while the leader may
	// still be writing. On shutdown the work context is canceled first, run()
	// returns, and only then is the elector canceled to release the Lease.
	electCtx, cancelElect := context.WithCancel(context.Background())
	defer cancelElect()

	var mu sync.Mutex
	leading := false
	// If we are told to stop before ever leading, there is no work to drain and
	// the elector can be canceled immediately.
	stopWaiting := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !leading {
			cancelElect()
		}
	})
	defer stopWaiting()

	// started is closed when leadership is acquired; result carries run()'s
	// outcome. Once OnStartedLeading has begun we block on result so the process
	// never returns while run() may still be applying changes.
	started := make(chan struct{})
	result := make(chan error, 1)
	leaderelection.RunOrDie(electCtx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionTiming.leaseDuration,
		RenewDeadline:   leaderElectionTiming.renewDeadline,
		RetryPeriod:     leaderElectionTiming.retryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				mu.Lock()
				leading = true
				mu.Unlock()
				close(started)
				logger.Info("became leader; starting reconciliation", "identity", identity)
				result <- runAsLeader(ctx, leaderCtx, run, cancelElect)
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
		// Never became leader (for example the process was stopped while waiting).
		return nil
	}
}

// runAsLeader runs the reconcile loop under a work context that ends when the
// process is stopping (stop) or leadership is lost (leaderCtx, canceled by
// client-go). It waits for run to return before calling releaseLease, which
// cancels the elector and so releases the Lease, guaranteeing the Lease is
// never released while run may still be writing. Losing leadership while not
// stopping yields errLeadershipLost.
func runAsLeader(stop, leaderCtx context.Context, run func(context.Context) error, releaseLease func()) error {
	workCtx, cancelWork := context.WithCancel(leaderCtx)
	defer cancelWork()
	unlink := context.AfterFunc(stop, cancelWork)
	defer unlink()

	err := run(workCtx)
	// Evaluated before releasing: client-go cancels leaderCtx only when the
	// lease could not be renewed, and releasing below cancels it too.
	lost := leaderCtx.Err() != nil && stop.Err() == nil
	releaseLease()
	if lost && (err == nil || errors.Is(err, context.Canceled)) {
		return errLeadershipLost
	}
	return err
}
