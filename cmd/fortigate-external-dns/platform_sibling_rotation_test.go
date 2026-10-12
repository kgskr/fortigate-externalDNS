package main

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

func TestEventSyncEnqueuesRotatedSiblingForFreshAudit(t *testing.T) {
	for _, trigger := range []string{"audit", "deletion"} {
		t.Run(trigger, func(t *testing.T) {
			ctx := context.Background()
			var targets, secrets []runtime.Object
			for _, name := range []string{"a", "b", "unchanged"} {
				targets = append(targets, crTarget(t, name, name+".example.com", []string{name + ".example.com"}, func(object *api.FortiGateDNSTarget) {
					object.Spec.Interval.Duration = time.Hour
				}))
				secrets = append(secrets, tokenSecret(name))
			}
			clients := integrationKubernetes(t, secrets, targets)
			cfg := isolationConfig()
			recorder := metrics.New()
			factory := newIntegrationClientFactory()
			manager := isolationManager(t, clients, factory, discardLogger(), recorder)
			defer func() { _, _ = manager.Sync(ctx, nil) }()
			executor := eventExecutorFor(t, clients, cfg, manager, recorder, controller.NewHeartbeat())
			var enqueued []platformqueue.TargetKey
			executor.enqueue = func(key platformqueue.TargetKey) bool {
				enqueued = append(enqueued, key)
				return true
			}
			auditAndApply := func(name string) {
				t.Helper()
				key := platformqueue.TargetKey{Namespace: "dns-system", Name: name}
				audit, err := executor.Audit(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				if err := executor.Apply(ctx, key, audit); err != nil {
					t.Fatal(err)
				}
				assertTargetReadiness(t, recorder, key.String(), true)
			}
			for _, name := range []string{"a", "b", "unchanged"} {
				auditAndApply(name)
			}
			enqueued = nil
			rotate := func(name string) {
				t.Helper()
				secret, err := clients.Core.CoreV1().Secrets("dns-system").Get(ctx, name+"-token", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				secret.ResourceVersion = "2"
				secret.Data["api-token"] = []byte("rotated-token")
				if _, err := clients.Core.CoreV1().Secrets("dns-system").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			rotate("b")
			if trigger == "audit" {
				// Even when both runtimes change, A is already being audited and
				// must not enqueue itself for a duplicate audit.
				rotate("a")
				auditAndApply("a")
			} else {
				if err := clients.Dynamic.Resource(api.TargetGVR).Namespace("dns-system").Delete(ctx, "a", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
				if err := executor.TargetDeleted(ctx, platformqueue.TargetKey{Namespace: "dns-system", Name: "a"}); err != nil {
					t.Fatal(err)
				}
			}
			want := []platformqueue.TargetKey{{Namespace: "dns-system", Name: "b"}}
			if !slices.Equal(enqueued, want) {
				t.Fatalf("changed sibling enqueues = %v, want %v", enqueued, want)
			}
			assertTargetReadiness(t, recorder, "dns-system/b", false)
			if current, exists := manager.Runtime("dns-system/b"); !exists || !manager.CredentialsCurrent(ctx, current) {
				t.Fatal("sibling runtime was not already rotated by Sync")
			}
			// Consume the queued audit immediately; no one-hour periodic tick
			// or source event for B is needed to restore readiness.
			enqueued = nil
			auditAndApply("b")
			if len(enqueued) != 0 || factory.callsFor("dns-system/unchanged") != 1 {
				t.Fatalf("settled/unchanged runtime disturbed: %v", enqueued)
			}
		})
	}
}

func TestEventSyncEnqueuesStoppedSiblingForStatusAudit(t *testing.T) {
	for _, trigger := range []string{"audit", "deletion"} {
		for _, failure := range []string{"revoked", "deleted-secret"} {
			t.Run(trigger+"/"+failure, func(t *testing.T) {
				ctx := context.Background()
				var targets, secrets []runtime.Object
				for _, name := range []string{"a", "b", "unchanged"} {
					targets = append(targets, crTarget(t, name, name+".example.com", []string{name + ".example.com"}, func(object *api.FortiGateDNSTarget) {
						object.Spec.Interval.Duration = time.Hour
					}))
					secrets = append(secrets, tokenSecret(name))
				}
				clients := integrationKubernetes(t, secrets, targets)
				cfg, recorder := isolationConfig(), metrics.New()
				manager := isolationManager(t, clients, newIntegrationClientFactory(), discardLogger(), recorder)
				defer func() { _, _ = manager.Sync(ctx, nil) }()
				executor := eventExecutorFor(t, clients, cfg, manager, recorder, controller.NewHeartbeat())
				var enqueued []platformqueue.TargetKey
				executor.enqueue = func(key platformqueue.TargetKey) bool {
					enqueued = append(enqueued, key)
					return true
				}
				auditAndApply := func(name string) {
					t.Helper()
					key := platformqueue.TargetKey{Namespace: "dns-system", Name: name}
					audit, err := executor.Audit(ctx, key)
					if err != nil {
						t.Fatal(err)
					}
					if err := executor.Apply(ctx, key, audit); err != nil {
						t.Fatal(err)
					}
				}
				for _, name := range []string{"a", "b", "unchanged"} {
					auditAndApply(name)
				}
				if ready, _ := statusReadyReason(t, clients, "b"); ready != metav1.ConditionTrue {
					t.Fatalf("initial B Ready = %s", ready)
				}
				enqueued = nil
				if failure == "deleted-secret" {
					if err := clients.Core.CoreV1().Secrets("dns-system").Delete(ctx, "b-token", metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
				} else {
					secret, err := clients.Core.CoreV1().Secrets("dns-system").Get(ctx, "b-token", metav1.GetOptions{})
					if err != nil {
						t.Fatal(err)
					}
					delete(secret.Annotations, target.TokenTargetAnnotation)
					if _, err := clients.Core.CoreV1().Secrets("dns-system").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if trigger == "audit" {
					auditAndApply("a")
				} else {
					if err := clients.Dynamic.Resource(api.TargetGVR).Namespace("dns-system").Delete(ctx, "a", metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					if err := executor.TargetDeleted(ctx, platformqueue.TargetKey{Namespace: "dns-system", Name: "a"}); err != nil {
						t.Fatal(err)
					}
				}
				key := platformqueue.TargetKey{Namespace: "dns-system", Name: "b"}
				if !slices.Equal(enqueued, []platformqueue.TargetKey{key}) {
					t.Fatalf("stopped sibling enqueues = %v", enqueued)
				}
				if _, exists := manager.Runtime(key.String()); exists {
					t.Fatal("revoked sibling runtime is still active")
				}
				enqueued = nil
				if _, err := executor.Audit(ctx, key); err == nil {
					t.Fatal("revoked sibling audit succeeded")
				}
				assertTargetReadiness(t, recorder, key.String(), false)
				if ready, _ := statusReadyReason(t, clients, "b"); ready != metav1.ConditionFalse {
					t.Fatalf("revoked B Ready = %s", ready)
				}
				auditAndApply("unchanged")
				if len(enqueued) != 0 {
					t.Fatalf("already-stopped sibling repeatedly enqueued: %v", enqueued)
				}
			})
		}
	}
}
