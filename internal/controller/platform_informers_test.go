package controller

import (
	"testing"

	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	"k8s.io/client-go/tools/cache"
)

type controlledSyncInformer struct {
	cache.SharedIndexInformer
	synced bool
}

func (i *controlledSyncInformer) HasSynced() bool { return i.synced }

func TestNamespacedCacheGateRequiresEveryInformerOfEachKind(t *testing.T) {
	serviceA, serviceB := &controlledSyncInformer{}, &controlledSyncInformer{}
	policyA, policyB := &controlledSyncInformer{}, &controlledSyncInformer{}
	factories := informerFactories{bindings: []informerBinding{
		{kind: platformqueue.EventService, informer: serviceA},
		{kind: platformqueue.EventService, informer: serviceB},
		{kind: platformqueue.EventPolicy, informer: policyA},
		{kind: platformqueue.EventPolicy, informer: policyB},
	}}
	gate, err := factories.SyncGate()
	if err != nil {
		t.Fatalf("multiple scoped informers of the same kind must be supported: %v", err)
	}
	for _, informer := range []*controlledSyncInformer{serviceA, policyA, serviceB, policyB} {
		if err := gate.RequireSynchronized(); err == nil {
			t.Fatal("cleanup gate opened with an unsynchronized namespace")
		}
		informer.synced = true
	}
	if err := gate.RequireSynchronized(); err != nil {
		t.Fatalf("all source namespaces synchronized: %v", err)
	}
}
