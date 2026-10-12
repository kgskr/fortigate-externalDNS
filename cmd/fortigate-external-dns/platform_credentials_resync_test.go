package main

import (
	"context"
	"slices"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestEventCredentialResyncSelectsOnlyChangedOrRecoveredTargets(t *testing.T) {
	for _, change := range []string{"token", "ca", "revoked-binding", "deleted-secret", "recovered-secret"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			left := crTarget(t, "left", "left.example.com", []string{"left.example.com"}, func(object *api.FortiGateDNSTarget) {
				object.Spec.Interval.Duration = time.Hour
				if change == "ca" {
					object.Spec.CARef = &api.LocalKeyReference{Kind: "ConfigMap", Name: "left-ca", Key: "ca.crt"}
				}
			})
			right := crTarget(t, "right", "right.example.com", []string{"right.example.com"}, nil)
			invalid := crTarget(t, "invalid", "invalid.example.com", []string{"invalid.example.com"}, nil)
			secret := tokenSecret("left")
			coreObjects := []runtime.Object{tokenSecret("right")}
			if change == "ca" {
				secret.Annotations[target.TokenCARefAnnotation] = "configmap/left-ca/ca.crt"
				coreObjects = append(coreObjects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "left-ca", ResourceVersion: "1"}, Data: map[string]string{"ca.crt": "first-ca"}})
			}
			if change != "recovered-secret" {
				coreObjects = append(coreObjects, secret)
			}
			clients := integrationKubernetes(t, coreObjects, []runtime.Object{left, right, invalid})
			cfg := isolationConfig()
			factory := newIntegrationClientFactory()
			manager := integrationManager(t, clients, nil, factory)
			defer func() { _, _ = manager.Sync(ctx, nil) }()
			load, err := loadTargetDefinitions(ctx, cfg, clients, manager)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Sync(ctx, load.definitions); err != nil {
				t.Fatal(err)
			}
			var enqueued []string
			enqueue := func(key platformqueue.TargetKey) bool { enqueued = append(enqueued, key.String()); return true }
			enqueueCredentialAudits(ctx, load, manager, enqueue)
			if len(enqueued) != 0 {
				t.Fatalf("unchanged/invalid targets enqueued: %v", enqueued)
			}
			switch change {
			case "recovered-secret":
				_, err = clients.Core.CoreV1().Secrets("dns-system").Create(ctx, secret, metav1.CreateOptions{})
			case "deleted-secret":
				err = clients.Core.CoreV1().Secrets("dns-system").Delete(ctx, secret.Name, metav1.DeleteOptions{})
			case "ca":
				var object *corev1.ConfigMap
				object, err = clients.Core.CoreV1().ConfigMaps("dns-system").Get(ctx, "left-ca", metav1.GetOptions{})
				if err == nil {
					object.Data["ca.crt"], object.ResourceVersion = "second-ca", "2"
					_, err = clients.Core.CoreV1().ConfigMaps("dns-system").Update(ctx, object, metav1.UpdateOptions{})
				}
			default:
				secret.ResourceVersion = "2"
				if change == "revoked-binding" {
					delete(secret.Annotations, target.TokenTargetAnnotation)
				} else {
					secret.Data["api-token"] = []byte("rotated-token")
				}
				_, err = clients.Core.CoreV1().Secrets("dns-system").Update(ctx, secret, metav1.UpdateOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			load, err = loadTargetDefinitions(ctx, cfg, clients, manager)
			if err != nil {
				t.Fatal(err)
			}
			enqueueCredentialAudits(ctx, load, manager, enqueue)
			if !slices.Equal(enqueued, []string{"dns-system/left"}) {
				t.Fatalf("credential resync selected %v", enqueued)
			}
			// The enqueue is only a trigger: normal audit gates still rebuild or
			// revoke the runtime, and a valid audit still reaches normal Apply.
			executor := eventTargetExecutor{cfg: cfg, clients: clients, manager: manager, recorder: metrics.New(), logger: discardLogger(), heartbeat: controller.NewHeartbeat(), scope: platformEventScope(cfg, load.definitions)}
			key := platformqueue.TargetKey{Namespace: "dns-system", Name: "left"}
			audit, auditErr := executor.Audit(ctx, key)
			if change == "revoked-binding" || change == "deleted-secret" {
				if auditErr == nil {
					t.Fatal("revoked target audit succeeded")
				}
				if _, exists := manager.Runtime(key.String()); exists {
					t.Fatal("revoked runtime remained active")
				}
			} else {
				if auditErr != nil {
					t.Fatal(auditErr)
				}
				if err := executor.Apply(ctx, key, audit); err != nil {
					t.Fatal(err)
				}
				current, exists := manager.Runtime(key.String())
				if !exists || !manager.CredentialsCurrent(ctx, current) {
					t.Fatal("audit did not refresh credentials")
				}
			}
			enqueued = nil
			enqueueCredentialAudits(ctx, load, manager, enqueue)
			if len(enqueued) != 0 || factory.callsFor("dns-system/right") != 1 {
				t.Fatalf("settled target retried or unrelated runtime rebuilt: %v", enqueued)
			}
		})
	}
}

func TestEventHeartbeatRotatesCredentialsBeforeLongTargetInterval(t *testing.T) {
	object := crTarget(t, "long", "example.com", []string{"example.com"}, func(object *api.FortiGateDNSTarget) {
		object.Spec.Interval.Duration = time.Hour
		object.Spec.Debounce.Duration = time.Hour
	})
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("long")}, []runtime.Object{object})
	cfg := isolationConfig()
	cfg.Resync = 20 * time.Millisecond
	factory := newIntegrationClientFactory()
	manager := integrationManager(t, clients, nil, factory)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runEventTargetMode(ctx, cfg, clients, manager, metrics.New(), discardLogger(), controller.NewHeartbeat())
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("event runtime did not stop")
		}
		_, _ = manager.Sync(context.Background(), nil)
	}()
	waitCredentialRuntime := func(wantCalls int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			current, exists := manager.Runtime("dns-system/long")
			if exists && factory.callsFor("dns-system/long") == wantCalls && manager.CredentialsCurrent(ctx, current) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("credential runtime rebuilds = %d, want %d", factory.callsFor("dns-system/long"), wantCalls)
	}
	waitCredentialRuntime(1)
	secret, err := clients.Core.CoreV1().Secrets("dns-system").Get(ctx, "long-token", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["api-token"], secret.ResourceVersion = []byte("rotated-token"), "2"
	if _, err := clients.Core.CoreV1().Secrets("dns-system").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitCredentialRuntime(2)
}
