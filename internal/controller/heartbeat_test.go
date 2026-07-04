package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/source"
)

func TestHeartbeatNonLeaderStaysHealthy(t *testing.T) {
	h := NewHeartbeat()
	now := time.Now()
	h.now = func() time.Time { return now }
	// Never activated: no staleness window can fail it.
	now = now.Add(24 * time.Hour)
	if !h.Healthy(time.Minute) {
		t.Fatal("an inactive (non-leader) heartbeat must always be healthy")
	}
}

func TestHeartbeatWedgedLeaderGoesUnhealthy(t *testing.T) {
	h := NewHeartbeat()
	now := time.Now()
	h.now = func() time.Time { return now }
	h.SetActive(true)
	h.MarkAttempt()
	if !h.Healthy(time.Minute) {
		t.Fatal("a fresh attempt must be healthy")
	}
	now = now.Add(2 * time.Minute)
	if h.Healthy(time.Minute) {
		t.Fatal("a leader with no attempt inside the window must be unhealthy")
	}
	// Losing leadership clears the failure condition.
	h.SetActive(false)
	if !h.Healthy(time.Minute) {
		t.Fatal("deactivation must restore health")
	}
}

func TestHeartbeatNewLeaderGetsFullWindow(t *testing.T) {
	h := NewHeartbeat()
	now := time.Now()
	h.now = func() time.Time { return now }
	// A stale attempt from a previous leadership stint must not count against a
	// new activation.
	h.SetActive(true)
	h.MarkAttempt()
	h.SetActive(false)
	now = now.Add(time.Hour)
	h.SetActive(true)
	if !h.Healthy(time.Minute) {
		t.Fatal("a freshly activated leader must be healthy before its first attempt")
	}
	now = now.Add(2 * time.Minute)
	if h.Healthy(time.Minute) {
		t.Fatal("a new leader that never completes an attempt must eventually fail")
	}
}

func TestHeartbeatNilSafe(t *testing.T) {
	var h *Heartbeat
	h.SetActive(true)
	h.MarkAttempt()
	if !h.Healthy(time.Minute) {
		t.Fatal("a nil heartbeat must report healthy")
	}
}

// TestRunOnceMarksHeartbeatEvenOnFailure asserts the attempt-based semantics:
// a reconcile that fails (FortiGate outage, discovery error) still counts as a
// completed attempt, so liveness does not restart a pod whose loop is running.
func TestRunOnceMarksHeartbeatEvenOnFailure(t *testing.T) {
	core := fake.NewSimpleClientset()
	core.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	h := NewHeartbeat()
	base := time.Now()
	now := base
	h.now = func() time.Time { return now }
	h.SetActive(true)

	runner := Runner{
		Config: config.Config{
			Interval:      time.Second,
			Sources:       []string{source.SourceService},
			DefaultTTL:    300,
			OwnerID:       "cluster-a",
			CleanupPolicy: "delete",
			FortiGate:     config.FortiGateConfig{Zone: "example.com"},
		},
		Kube: source.KubernetesClients{
			Core:    core,
			Gateway: gatewayfake.NewSimpleClientset(),
		},
		DNSClient: &recordingDNSClient{},
		Logger:    slog.Default(),
		Heartbeat: h,
	}

	now = base.Add(10 * time.Minute) // the attempt completes late but completes
	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected the reconcile attempt to fail")
	}
	if !h.Healthy(time.Minute) {
		t.Fatal("a failing-but-completing attempt must keep the heartbeat healthy")
	}
}
