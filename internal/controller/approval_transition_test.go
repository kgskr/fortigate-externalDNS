package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

type approvalBarrierDNSClient struct {
	recordingDNSClient
	calls   int
	barrier func(context.Context) error
}

func (c *approvalBarrierDNSClient) ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error) {
	c.calls++
	if c.calls == 2 && c.barrier != nil {
		if err := c.barrier(ctx); err != nil {
			return nil, "", err
		}
	}
	return c.recordingDNSClient.ListRecordsWithRevision(ctx)
}

func TestApprovalIsRecheckedAfterProviderRevalidation(t *testing.T) {
	for _, scenario := range []string{"removed", "changed", "expired", "replaced", "unchanged", "automatic"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store, dynamicClient := newPhaseStore(t)
			provider := &approvalBarrierDNSClient{}
			runner := storeRunner(t, provider, store, true)
			if err := runner.RunOnce(ctx); !errors.Is(err, plan.ErrApprovalRequired) {
				t.Fatalf("initial missing approval = %v", err)
			}
			resource := dynamicClient.Resource(v1alpha1.ChangePlanGVR).Namespace("system")
			plans := listPlans(t, dynamicClient)
			object, err := resource.Get(ctx, plans[0].Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			object.SetUID("original-plan")
			object.SetAnnotations(map[string]string{v1alpha1.ApprovalHashAnnotation: plans[0].Spec.PlanHash})
			var expires time.Time
			if scenario == "expired" {
				// A real API server treats the spec as immutable; set expiry before
				// the audit starts, then let time advance at the snapshot barrier.
				expires = time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
				if err := unstructured.SetNestedField(object.Object, expires.Format(time.RFC3339), "spec", "expiresAt"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "automatic" {
				runner.ApprovalRequired = false
				object.SetAnnotations(nil)
			}
			if _, err := resource.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			reached, release := make(chan struct{}), make(chan struct{})
			provider.calls = 0
			provider.barrier = func(ctx context.Context) error {
				close(reached)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			done := make(chan error, 1)
			go func() { done <- runner.RunOnce(ctx) }()
			select {
			case <-reached:
			case err := <-done:
				t.Fatalf("audit ended before revalidation barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			current, err := resource.Get(ctx, object.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "removed":
				current.SetAnnotations(nil)
			case "changed":
				current.SetAnnotations(map[string]string{v1alpha1.ApprovalHashAnnotation: strings.Repeat("0", 64)})
			case "expired":
				timer := time.NewTimer(time.Until(expires) + time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			case "replaced":
				if err := resource.Delete(ctx, current.GetName(), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
				current.SetUID("replacement-plan")
				current.SetResourceVersion("")
				if _, err := resource.Create(ctx, current, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "removed" || scenario == "changed" {
				if _, err := resource.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			select {
			case err := <-done:
				if scenario == "unchanged" || scenario == "automatic" {
					if err != nil || len(provider.operations) != 1 {
						t.Fatalf("valid apply: err=%v operations=%v", err, provider.operations)
					}
				} else if !errors.Is(err, plan.ErrApprovalRequired) || len(provider.operations) != 0 {
					t.Fatalf("revoked approval: err=%v operations=%v", err, provider.operations)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
