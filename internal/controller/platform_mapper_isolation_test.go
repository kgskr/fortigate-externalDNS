package controller

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	clocktesting "k8s.io/utils/clock/testing"
)

func malformedMapperTarget(t *testing.T, malformedSpec bool) *unstructured.Unstructured {
	t.Helper()
	target := newAPITarget("dns-system", "broken", []string{"restricted"})
	target.UID = types.UID("broken-uid")
	target.Generation = 7
	object, err := api.ToUnstructured(target)
	if err != nil {
		t.Fatal(err)
	}
	if malformedSpec {
		object.Object["spec"] = "not an object"
	} else if err := unstructured.SetNestedField(object.Object, "100000000000000000h", "spec", "interval"); err != nil {
		t.Fatal(err)
	}
	var decoded api.FortiGateDNSTarget
	if err := api.FromUnstructured(object, &decoded); err == nil {
		t.Fatal("fixture must fail typed target decoding")
	}
	return object
}

func TestInformerTargetMapperIsolatesMalformedTargets(t *testing.T) {
	for _, malformedSpec := range []bool{false, true} {
		name := "overflow interval"
		if malformedSpec {
			name = "malformed spec"
		}
		t.Run(name, func(t *testing.T) {
			clients, _, _ := newPlatformTestClients(t)
			runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
				Namespace: "dns-system", Sources: []string{"service"}, PeriodicInterval: time.Minute,
			}, newRecordingExecutor())
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.queue.ShutDown()
			healthy := newAPITarget("dns-system", "healthy", []string{"apps"})
			healthy.Spec.Interval.Duration = 5 * time.Second
			object, err := api.ToUnstructured(healthy)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range []*unstructured.Unstructured{object, malformedMapperTarget(t, malformedSpec)} {
				if err := runtime.mapper.targets.GetIndexer().Add(item); err != nil {
					t.Fatal(err)
				}
			}
			keys, err := runtime.mapper.ListTargetKeys(context.Background())
			wantKeys := []platformqueue.TargetKey{{Namespace: "dns-system", Name: "broken"}, {Namespace: "dns-system", Name: "healthy"}}
			if err != nil || !reflect.DeepEqual(keys, wantKeys) {
				t.Fatalf("initial audit keys = %v, %v; want %v", keys, err, wantKeys)
			}
			schedules, err := runtime.mapper.ListTargetSchedules(context.Background())
			if err != nil || len(schedules) != 2 {
				t.Fatalf("periodic schedules = %v, %v", schedules, err)
			}
			if schedules[0].Key != wantKeys[0] || schedules[0].Revision != "broken-uid/7" || schedules[0].Interval != 0 {
				t.Fatalf("invalid target lost identity or fallback interval: %+v", schedules[0])
			}
			if schedules[1].Key != wantKeys[1] || schedules[1].Interval != 5*time.Second {
				t.Fatalf("healthy schedule changed: %+v", schedules[1])
			}
			for _, namespace := range []string{"apps", "restricted", "unrelated"} {
				keys, err := runtime.mapper.TargetsForEvent(context.Background(), platformqueue.Event{Kind: platformqueue.EventService, Namespace: namespace})
				want := []platformqueue.TargetKey{}
				if namespace == "apps" {
					want = wantKeys[1:]
				}
				if err != nil || !reflect.DeepEqual(keys, want) {
					t.Fatalf("source event in %s mapped to %v, %v; want %v", namespace, keys, err, want)
				}
			}
		})
	}
}

func TestPlatformRuntimeContinuesHealthyApplyWithMalformedSibling(t *testing.T) {
	for _, malformedSpec := range []bool{false, true} {
		name := "overflow interval"
		if malformedSpec {
			name = "malformed spec"
		}
		t.Run(name, func(t *testing.T) {
			clients, kubeClient, _ := newPlatformTestClients(t)
			healthy, err := api.ToUnstructured(newAPITarget("dns-system", "healthy", []string{"apps"}))
			if err != nil {
				t.Fatal(err)
			}
			clients.Dynamic = fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				api.TargetGVR: "FortiGateDNSTargetList",
			}, healthy, malformedMapperTarget(t, malformedSpec))
			clock := clocktesting.NewFakeClock(time.Unix(0, 0))
			executor := newRecordingExecutor()
			var invalidAudits atomic.Int32
			executor.audit = func(ctx context.Context, key platformqueue.TargetKey) (TargetAudit, error) {
				object, err := clients.Dynamic.Resource(api.TargetGVR).Namespace(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return TargetAudit{}, err
				}
				var target api.FortiGateDNSTarget
				if err := api.FromUnstructured(object, &target); err != nil {
					invalidAudits.Add(1)
					return TargetAudit{}, errors.New("invalid target")
				}
				return TargetAudit{DiscoveryComplete: true, ProviderSnapshotStable: true}, nil
			}
			runtime, err := NewPlatformRuntime(clients, PlatformRuntimeConfig{
				Namespace: "dns-system", Sources: []string{"service"}, SourceNamespaces: []string{"apps"},
				PeriodicInterval: time.Minute, Workers: 2, Clock: clock,
				Queue: platformqueue.Config{DisableDebounce: true, Clock: clock},
			}, executor)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runPlatformRuntime(runtime, ctx)
			if key := receiveKey(t, executor.applied); key.Name != "healthy" {
				t.Fatalf("malformed target reached Apply: %s", key)
			}
			waitUntil(t, func() bool { return invalidAudits.Load() > 0 && runtime.Idle() && clock.Waiters() >= 2 })
			before := executor.applyCalls.Load()
			if _, err := kubeClient.CoreV1().Services("apps").Create(ctx, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, func() bool { return executor.applyCalls.Load() > before && runtime.Idle() })
			before = executor.applyCalls.Load()
			clock.Step(time.Minute)
			waitUntil(t, func() bool { return executor.applyCalls.Load() > before && runtime.Idle() })
			cancel()
			if err := receiveRunResult(t, done); err != nil {
				t.Fatalf("malformed sibling stopped runtime: %v", err)
			}
		})
	}
}
