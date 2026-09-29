package main

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	statuswriter "github.com/kgskr/fortigate-external-dns/internal/status"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

func TestEventPolicyReadinessMetricAndStatusRecoverTogether(t *testing.T) {
	policy := &v1alpha1.FortiGateDNSPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "apps"},
		Spec:       v1alpha1.FortiGateDNSPolicySpec{AllowedTargetCIDRs: []string{"invalid"}},
	}
	clients := integrationKubernetes(t, []runtime.Object{tokenSecret("edge")}, []runtime.Object{
		policy, crTarget(t, "edge", "example.com", []string{"example.com"}, nil),
	})
	cfg := isolationConfig()
	cfg.PolicyEnforcement = true
	recorder := metrics.New()
	manager := isolationManager(t, clients, newIntegrationClientFactory(), discardLogger(), recorder)
	executor := eventExecutorFor(t, clients, cfg, manager, recorder, controller.NewHeartbeat())
	key, err := platformqueue.NewTargetKey("dns-system", "edge")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, invalid := range []bool{true, false} {
		if !invalid {
			policy.Spec.AllowedTargetCIDRs = []string{"192.0.2.0/24"}
			updated, err := v1alpha1.ToUnstructured(policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := clients.Dynamic.Resource(v1alpha1.PolicyGVR).Namespace("apps").Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		audit, err := executor.Audit(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := executor.Apply(ctx, key, audit); err != nil {
			t.Fatal(err)
		}
		assertTargetReadiness(t, recorder, key.String(), !invalid)
		want, reason := metav1.ConditionTrue, string(statuswriter.ReasonReady)
		if invalid {
			want, reason = metav1.ConditionFalse, string(statuswriter.ReasonPolicyRejected)
		}
		if got, gotReason := statusReadyReason(t, clients, "edge"); got != want || gotReason != reason {
			t.Fatalf("Ready=%s/%s, want %s/%s", got, gotReason, want, reason)
		}
	}
}

func TestReadinessMetricDoesNotDependOnStatusStorage(t *testing.T) {
	for _, storage := range []string{"absent", "forbidden"} {
		t.Run(storage, func(t *testing.T) {
			clients := integrationKubernetes(t, nil, nil)
			runtimeForTarget := &target.Runtime{Definition: target.Definition{Namespace: "dns-system", Name: "edge"}}
			if storage == "forbidden" {
				writer, err := statuswriter.NewWriter(clients.Dynamic, "dns-system", "edge", 10)
				if err != nil {
					t.Fatal(err)
				}
				runtimeForTarget.Stores.StatusStore = writer
				clients.Dynamic.(interface {
					PrependReactor(string, string, k8stesting.ReactionFunc)
				}).PrependReactor("*", "fortigatednsstatuses", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			recorder := metrics.New()
			for _, ready := range []bool{true, false, true} {
				audit := &controller.ReconcileAudit{DiscoveryComplete: true, PolicyComplete: ready}
				writeTargetStatus(context.Background(), runtimeForTarget, audit, controller.ApplyResult{}, nil, true, recorder, discardLogger())
				assertTargetReadiness(t, recorder, runtimeForTarget.Definition.Key(), ready)
			}
		})
	}
}

func TestReadinessAfterCredentialRotationRequiresFreshAudit(t *testing.T) {
	definition := integrationDefinition("edge", "example.com", "root", v1alpha1.OwnershipModeExclusive)
	clients := integrationKubernetes(t, secretsForDefinitions([]target.Definition{definition}), nil)
	manager := integrationManager(t, clients, []target.Definition{definition}, newIntegrationClientFactory())
	runtimeForTarget, _ := manager.Runtime(definition.Key())
	recorder := runtimeForTarget.Metrics.Global
	assertTargetReadiness(t, recorder, definition.Key(), false)
	ctx := context.Background()
	if err := runTargetAudit(ctx, integrationConfig(), clients, runtimeForTarget, recorder, discardLogger()); err != nil {
		t.Fatal(err)
	}
	assertTargetReadiness(t, recorder, definition.Key(), true)
	secret, err := clients.Core.CoreV1().Secrets(definition.Namespace).Get(ctx, definition.APITokenSecretRef.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data[definition.APITokenSecretRef.Key] = []byte("rotated-test-token")
	secret.ResourceVersion = "2"
	if _, err := clients.Core.CoreV1().Secrets(definition.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(ctx, []target.Definition{definition}); err != nil {
		t.Fatal(err)
	}
	assertTargetReadiness(t, recorder, definition.Key(), false)
	runtimeForTarget, _ = manager.Runtime(definition.Key())
	if err := runTargetAudit(ctx, integrationConfig(), clients, runtimeForTarget, recorder, discardLogger()); err != nil {
		t.Fatal(err)
	}
	assertTargetReadiness(t, recorder, definition.Key(), true)
}
