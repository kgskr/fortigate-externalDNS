package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunWithLeaderElectionPropagatesRunResult(t *testing.T) {
	t.Setenv("POD_NAME", "test-identity")
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "test-lease", LeaderElectionNamespace: "default"}
	sentinel := errors.New("run finished")
	ran := false

	err := runWithLeaderElection(context.Background(), cfg, fake.NewSimpleClientset(), discardLogger(), func(context.Context) error {
		ran = true
		return sentinel
	})
	if !ran {
		t.Fatal("run was not invoked after acquiring leadership")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("run's result must propagate once leadership is acquired, got %v", err)
	}
}

func TestRunWithLeaderElectionCanceledBeforeAcquireReturnsNil(t *testing.T) {
	t.Setenv("POD_NAME", "test-identity")
	cfg := config.Config{LeaderElection: true, LeaderElectionID: "test-lease", LeaderElectionNamespace: "default"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false

	err := runWithLeaderElection(ctx, cfg, fake.NewSimpleClientset(), discardLogger(), func(context.Context) error {
		ran = true
		return errors.New("should not run")
	})
	if err != nil {
		t.Fatalf("expected nil when leadership is never acquired, got %v", err)
	}
	if ran {
		t.Fatal("run must not execute when the context is canceled before acquisition")
	}
}

func TestStartProbeServerDisabledWhenAddrEmpty(t *testing.T) {
	server, err := startProbeServer(config.Config{MetricsAddr: ""}, metrics.New(), discardLogger())
	if err != nil || server != nil {
		t.Fatalf("an empty metrics address should disable the server, got server=%v err=%v", server, err)
	}
}

func TestStartProbeServerFatalOnBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	server, err := startProbeServer(config.Config{MetricsAddr: ln.Addr().String()}, metrics.New(), discardLogger())
	if err == nil {
		if server != nil {
			_ = server.Shutdown(context.Background())
		}
		t.Fatal("binding an already-used address must be a fatal startup error, not a silent background failure")
	}
}

func TestUseLeaderElection(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "default long-running", cfg: config.Config{LeaderElection: true}, want: true},
		{name: "once bypasses election", cfg: config.Config{LeaderElection: true, Once: true}, want: false},
		{name: "disabled for local testing", cfg: config.Config{LeaderElection: false}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useLeaderElection(tc.cfg); got != tc.want {
				t.Fatalf("useLeaderElection() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVersionRequested(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--version"}, true},
		{[]string{"-version"}, true},
		{[]string{"--dry-run", "--version"}, true},
		{[]string{"--", "--version"}, false},
		{[]string{"--dry-run"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := versionRequested(tc.args); got != tc.want {
			t.Errorf("versionRequested(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestRunHelpReturnsSuccessDespiteMalformedEnvironment(t *testing.T) {
	originalArgs := os.Args
	os.Args = []string{"fortigate-external-dns", "--help"}
	defer func() { os.Args = originalArgs }()
	t.Setenv("DRY_RUN", "not-a-bool")
	t.Setenv("FORTIGATE_URL", "https://demo-user:demo-password%zz@fortigate.example.com")

	var got int
	output := captureFileOutput(t, &os.Stderr, func() {
		got = run()
	})
	if got != 0 {
		t.Fatalf("--help must exit successfully without validating unrelated environment settings, got %d", got)
	}
	for _, secret := range []string{"demo-user", "demo-password", "%zz"} {
		if strings.Contains(output, secret) {
			t.Fatalf("--help leaked %q from FORTIGATE_URL: %s", secret, output)
		}
	}
}

func TestRunValidationErrorDoesNotLeakMalformedFortiGateURL(t *testing.T) {
	originalArgs := os.Args
	os.Args = []string{"fortigate-external-dns"}
	defer func() { os.Args = originalArgs }()
	t.Setenv("DRY_RUN", "true")
	t.Setenv("FORTIGATE_URL", "https://demo-user:demo-password%zz@fortigate.example.com")
	t.Setenv("FORTIGATE_API_TOKEN", "unit-test-credential")
	t.Setenv("FORTIGATE_ZONE", "example.com")

	var got int
	output := captureFileOutput(t, &os.Stdout, func() {
		got = run()
	})
	if got != 2 {
		t.Fatalf("malformed FortiGate URL must fail configuration validation with exit code 2, got %d", got)
	}
	if !strings.Contains(output, "FortiGate URL is invalid") {
		t.Fatalf("expected a safe generic validation error, got %q", output)
	}
	for _, secret := range []string{"demo-user", "demo-password", "%zz"} {
		if strings.Contains(output, secret) {
			t.Fatalf("validation log leaked %q from FORTIGATE_URL: %s", secret, output)
		}
	}
}

func captureFileOutput(t *testing.T, stream **os.File, fn func()) string {
	t.Helper()
	original := *stream
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*stream = writer
	defer func() {
		*stream = original
		_ = writer.Close()
		_ = reader.Close()
	}()

	fn()
	*stream = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBuildLoggerSelectsHandlerAndLevel(t *testing.T) {
	var jsonOut bytes.Buffer
	logger := buildLogger(&jsonOut, "json", "info")
	logger.Info("hello", "key", "value")
	var decoded map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("json format must emit JSON lines, got %q: %v", jsonOut.String(), err)
	}
	if decoded["msg"] != "hello" || decoded["key"] != "value" {
		t.Fatalf("unexpected JSON log record: %v", decoded)
	}

	var textOut bytes.Buffer
	logger = buildLogger(&textOut, "text", "warn")
	logger.Info("suppressed")
	if textOut.Len() != 0 {
		t.Fatalf("warn level must suppress info logs, got %q", textOut.String())
	}
	logger.Warn("visible")
	if out := textOut.String(); !strings.Contains(out, "msg=visible") {
		t.Fatalf("text format expected, got %q", out)
	}

	var debugOut bytes.Buffer
	logger = buildLogger(&debugOut, "text", "debug")
	logger.Debug("dbg")
	if !strings.Contains(debugOut.String(), "msg=dbg") {
		t.Fatalf("debug level must emit debug logs, got %q", debugOut.String())
	}
}
