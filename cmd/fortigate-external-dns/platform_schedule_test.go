package main

import (
	"context"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestPollingTargetCadencePreservesIsolationAndCredentialRotation(t *testing.T) {
	fast := crTarget(t, "fast", "fast.example.com", []string{"fast.example.com"}, func(target *api.FortiGateDNSTarget) {
		target.Spec.Interval.Duration = time.Second
	})
	slow := crTarget(t, "slow", "slow.example.com", []string{"slow.example.com"}, func(target *api.FortiGateDNSTarget) {
		target.Spec.Interval.Duration = time.Hour
	})
	invalid := crTarget(t, "invalid", "other.example.com", []string{"other.example.com"}, nil)
	// The invalid sibling has no token and must not alter the healthy cadences.
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("fast"), tokenSecret("slow")}, []runtime.Object{fast, slow, invalid})
	cfg := isolationConfig()
	cfg.Resync = time.Minute
	factory := newIntegrationClientFactory()
	manager := integrationManager(t, clients, nil, factory)
	defer func() { _, _ = manager.Sync(context.Background(), nil) }()
	recorder := metrics.New()
	var schedule platformqueue.AuditSchedule
	cycle := func() time.Duration {
		t.Helper()
		delay, err := runScheduledTargetCycle(context.Background(), cfg, clients, manager, recorder, discardLogger(), &schedule)
		if err == nil {
			t.Fatal("invalid sibling failure was not reported")
		}
		if delay < 0 || delay > time.Second {
			t.Fatalf("healthy one-second target delayed by sibling: %s", delay)
		}
		return delay
	}
	cycle()
	fastProvider, slowProvider := factory.provider("dns-system/fast"), factory.provider("dns-system/slow")
	fastProvider.listStarted = make(chan struct{}, 10)
	slowProvider.listStarted = make(chan struct{}, 10)
	cycle()
	if len(fastProvider.listStarted) != 0 || len(slowProvider.listStarted) != 0 {
		t.Fatal("polling audited targets before their deadlines")
	}
	// Credential rotation must reset the long-cadence runtime immediately.
	secret, err := clients.Core.CoreV1().Secrets("dns-system").Get(context.Background(), "slow-token", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["api-token"] = []byte("rotated")
	secret.ResourceVersion = "2"
	if _, err := clients.Core.CoreV1().Secrets("dns-system").Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	cycle()
	if len(slowProvider.listStarted) == 0 {
		t.Fatal("rotated credential waited for the one-hour interval")
	}
	if len(fastProvider.listStarted) != 0 {
		t.Fatal("credential rotation audited an unrelated target")
	}
	if factory.callsFor("dns-system/slow") != 2 {
		t.Fatal("credential rotation did not replace the runtime")
	}
}
