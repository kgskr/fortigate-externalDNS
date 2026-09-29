package main

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	statuswriter "github.com/kgskr/fortigate-external-dns/internal/status"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestPlatformApprovalSurvivesProviderFailureAndRequiresNewPlanApproval(t *testing.T) {
	for _, mode := range []string{"polling", "event"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			clients := integrationKubernetes(t, []runtime.Object{
				tokenSecret("edge"), integrationService("apps", "web", "web.example.com", "192.0.2.30"),
			}, []runtime.Object{crTarget(t, "edge", "example.com", []string{"example.com"}, func(object *v1alpha1.FortiGateDNSTarget) {
				object.Spec.ApprovalMode = v1alpha1.ApprovalModeRequired
			})})
			cfg := isolationConfig()
			recorder := metrics.New()
			factory := newIntegrationClientFactory()
			manager := isolationManager(t, clients, factory, discardLogger(), recorder)
			executor := eventExecutorFor(t, clients, cfg, manager, recorder, controller.NewHeartbeat())
			key, err := platformqueue.NewTargetKey("dns-system", "edge")
			if err != nil {
				t.Fatal(err)
			}
			cycle := func() error {
				if mode == "polling" {
					return runTargetCycle(ctx, cfg, clients, manager, recorder, discardLogger())
				}
				audit, err := executor.Audit(ctx, key)
				if err != nil {
					return err
				}
				return executor.Apply(ctx, key, audit)
			}
			checkStatus := func(approved, ready bool, readyReason statuswriter.Reason) {
				t.Helper()
				object, err := clients.Dynamic.Resource(v1alpha1.StatusGVR).Namespace(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var status v1alpha1.FortiGateDNSStatus
				if err := v1alpha1.FromUnstructured(object, &status); err != nil {
					t.Fatal(err)
				}
				found := 0
				for _, condition := range status.Status.Conditions {
					want, reason := ready, readyReason
					switch condition.Type {
					case string(statuswriter.ConditionReady):
					case string(statuswriter.ConditionPlanApproved):
						want, reason = approved, statuswriter.ReasonPendingApproval
						if approved {
							reason = statuswriter.ReasonPlanApproved
						}
					default:
						continue
					}
					found++
					value := metav1.ConditionFalse
					if want {
						value = metav1.ConditionTrue
					}
					if condition.Status != value || condition.Reason != string(reason) {
						t.Errorf("%s=%s/%s, want %s/%s", condition.Type, condition.Status, condition.Reason, value, reason)
					}
				}
				if found != 2 {
					t.Fatalf("missing approval or ready condition: %v", status.Status.Conditions)
				}
				assertTargetReadiness(t, recorder, key.String(), ready)
			}
			approvePending := func() {
				t.Helper()
				for _, object := range listChangePlans(t, clients.Dynamic, key.Namespace) {
					if object.Status.Phase == v1alpha1.ChangePlanPendingApproval {
						updatePlanApproval(t, clients.Dynamic, &object, object.Spec.PlanHash)
						return
					}
				}
				t.Fatal("no pending plan")
			}
			if err := cycle(); err == nil {
				t.Fatal("missing approval must block apply")
			}
			checkStatus(false, false, statuswriter.ReasonPendingApproval)
			approvePending()
			provider := factory.provider(key.String())
			provider.applyError = errors.New("provider mutation failed")
			if err := cycle(); err == nil {
				t.Fatal("provider failure must fail the cycle")
			}
			if provider.applyCalls != 1 || provider.mutationCount() != 0 {
				t.Fatalf("expected one failed provider attempt, calls=%d", provider.applyCalls)
			}
			checkStatus(true, false, statuswriter.ReasonApplyFailed)
			plans := listChangePlans(t, clients.Dynamic, key.Namespace)
			if len(plans) != 1 || plans[0].Status.Phase != v1alpha1.ChangePlanFailed {
				t.Fatalf("expected failed approved plan: %v", plans)
			}
			provider.applyError = nil
			if err := cycle(); err == nil {
				t.Fatal("a new plan after failure must require fresh approval")
			}
			checkStatus(false, false, statuswriter.ReasonPendingApproval)
			approvePending()
			if err := cycle(); err != nil {
				t.Fatal(err)
			}
			checkStatus(true, true, statuswriter.ReasonReady)
			if err := cycle(); err != nil {
				t.Fatalf("quiet cycle after success: %v", err)
			}
			checkStatus(true, true, statuswriter.ReasonReady)
		})
	}
}
