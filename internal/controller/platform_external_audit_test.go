package controller

import (
	"testing"
	"time"

	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

func TestExternalAuditBypassesDebounceCoalescesAndRejectsShutdown(t *testing.T) {
	clients, _, _ := newPlatformTestClients(t)
	runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
		Namespace: "dns-system", PeriodicInterval: time.Hour,
		Queue: platformqueue.Config{Debounce: time.Hour},
	}, newRecordingExecutor())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.queue.ShutDown()
	key := platformqueue.TargetKey{Namespace: "dns-system", Name: "edge"}
	runtime.queue.Enqueue(key)
	for range 2 {
		if !runtime.EnqueueAudit(key) || runtime.queue.Len() != 1 {
			t.Fatal("credential audit was delayed or not coalesced")
		}
	}
	got, shutdown := runtime.queue.Get()
	if shutdown || got != key {
		t.Fatalf("queue.Get() = %v, shutdown=%v", got, shutdown)
	}
	runtime.queue.Complete(got, nil)
	runtime.queue.ForgetTarget(key)
	if runtime.EnqueueAudit(key) {
		t.Fatal("deleted target accepted an external audit")
	}
	runtime.queue.ShutDown()
	if runtime.EnqueueAudit(platformqueue.TargetKey{Namespace: "dns-system", Name: "another"}) {
		t.Fatal("stopped runtime accepted an external audit")
	}
	var absent *PlatformRuntime
	if absent.EnqueueAudit(key) {
		t.Fatal("nil runtime accepted an external audit")
	}
}
