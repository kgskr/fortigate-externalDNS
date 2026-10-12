package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

func TestBeginApprovedApplyRechecksAfterCASConflict(t *testing.T) {
	for _, transition := range []string{"approved", "applying"} {
		for _, mutation := range []string{"approval", "hash", "uid", "stale", "interrupted"} {
			t.Run(transition+"/"+mutation, func(t *testing.T) {
				store := newTestChangePlanStore(t)
				ctx := context.Background()
				document := testDocument([]SanitizedOperation{SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("api.example.com", "203.0.113.10")})})
				expected, err := store.PersistCurrent(ctx, "system", document, nil, 10)
				if err != nil {
					t.Fatal(err)
				}
				expected.UID = "original-plan"
				expected.Annotations = map[string]string{v1alpha1.ApprovalHashAnnotation: expected.Spec.PlanHash}
				object, err := v1alpha1.ToUnstructured(expected)
				if err != nil {
					t.Fatal(err)
				}
				client := store.client.(*dynamicfake.FakeDynamicClient)
				if _, err := client.Resource(v1alpha1.ChangePlanGVR).Namespace(expected.Namespace).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				updates := 0
				client.PrependReactor("update", v1alpha1.ChangePlanGVR.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
					if action.GetSubresource() != "status" {
						return false, nil, nil
					}
					updates++
					// Model an edit between GET and UpdateStatus. The API server's
					// resourceVersion conflict must cause the next GET to be checked.
					changed := object.DeepCopy()
					switch mutation {
					case "approval":
						changed.SetAnnotations(nil)
					case "hash":
						hash := strings.Repeat("0", 64)
						_ = unstructured.SetNestedField(changed.Object, hash, "spec", "planHash")
						changed.SetAnnotations(map[string]string{v1alpha1.ApprovalHashAnnotation: hash})
					case "uid":
						changed.SetUID("replacement-plan")
					case "stale":
						_ = unstructured.SetNestedField(changed.Object, string(v1alpha1.ChangePlanStale), "status", "phase")
					case "interrupted":
						_ = unstructured.SetNestedField(changed.Object, string(v1alpha1.ChangePlanInterrupted), "status", "phase")
					}
					if err := client.Tracker().Update(v1alpha1.ChangePlanGVR, changed, expected.Namespace); err != nil {
						return true, nil, err
					}
					return true, nil, apierrors.NewConflict(v1alpha1.ChangePlanGVR.GroupResource(), expected.Name, errors.New("concurrent edit"))
				})
				var transitionErr error
				if transition == "approved" {
					_, transitionErr = store.MarkApproved(ctx, expected)
				} else {
					_, transitionErr = store.BeginApprovedApply(ctx, expected)
				}
				if !errors.Is(transitionErr, ErrApprovalRequired) {
					t.Fatalf("approval transition = %v, want approval rejection", transitionErr)
				}
				if updates != 1 {
					t.Fatalf("status update attempts = %d, want one before the concurrent edit", updates)
				}
			})
		}
	}
}
