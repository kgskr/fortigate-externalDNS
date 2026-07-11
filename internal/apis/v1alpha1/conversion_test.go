package v1alpha1

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTargetUnstructuredRoundTripPreservesConcurrencyMetadata(t *testing.T) {
	optional := false
	original := &FortiGateDNSTarget{
		TypeMeta: metav1.TypeMeta{APIVersion: SchemeGroupVersion.String(), Kind: "FortiGateDNSTarget"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "edge",
			Namespace:       "dns-system",
			ResourceVersion: "42",
			Generation:      7,
			Finalizers:      []string{GroupName + "/target-protection"},
		},
		Spec: FortiGateDNSTargetSpec{
			URL:  "https://fortigate.example.com",
			VDOM: "root",
			Zone: "example.com",
			APITokenSecretRef: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "fortigate-token"},
				Key:                  "api-token",
				Optional:             &optional,
			},
			OwnershipMode: OwnershipModeShared,
			ControllerID:  "cluster-a",
			Sources:       []string{"service", "ingress"},
			DomainFilters: []string{"example.com"},
			CleanupPolicy: CleanupPolicyKeep,
			ApprovalMode:  ApprovalModeRequired,
			Interval:      metav1.Duration{Duration: time.Minute},
		},
		Status: FortiGateDNSTargetStatus{
			ObservedGeneration: 7,
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready",
			}},
		},
	}

	dynamicObject, err := ToUnstructured(original)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip FortiGateDNSTarget
	if err := FromUnstructured(dynamicObject, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, &roundTrip) {
		t.Fatalf("round trip changed object:\n got: %#v\nwant: %#v", &roundTrip, original)
	}
}

func TestDeepCopyDoesNotAliasMutableFields(t *testing.T) {
	original := &FortiGateDNSChangePlan{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"state": "new"}},
		Spec: FortiGateDNSChangePlanSpec{
			OwnershipResourceVersions: map[string]string{"claim-a": "1"},
			Operations:                []PlanOperation{{ID: "op-a", Prerequisites: []string{"op-0"}}},
		},
		Status: FortiGateDNSChangePlanStatus{
			Outcomes: []OperationOutcome{{OperationID: "op-a", Result: "pending"}},
		},
	}
	copyObject := original.DeepCopyObject().(*FortiGateDNSChangePlan)
	copyObject.Labels["state"] = "changed"
	copyObject.Spec.OwnershipResourceVersions["claim-a"] = "2"
	copyObject.Spec.Operations[0].Prerequisites[0] = "different"
	copyObject.Status.Outcomes[0].Result = "failed"

	if original.Labels["state"] != "new" || original.Spec.OwnershipResourceVersions["claim-a"] != "1" ||
		original.Spec.Operations[0].Prerequisites[0] != "op-0" || original.Status.Outcomes[0].Result != "pending" {
		t.Fatalf("deep copy retained aliases: %#v", original)
	}
}

func TestNewForGVKRejectsUnknownAndAllocatesKnownKind(t *testing.T) {
	obj, err := NewForGVK(SchemeGroupVersion.WithKind("FortiGateDNSPolicy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.(*FortiGateDNSPolicy); !ok {
		t.Fatalf("unexpected type %T", obj)
	}
	if _, err := NewForGVK(SchemeGroupVersion.WithKind("Secret")); err == nil {
		t.Fatal("unknown project kind unexpectedly allocated")
	}
}

func TestNilConversionsFail(t *testing.T) {
	if _, err := ToUnstructured(nil); err == nil {
		t.Fatal("nil typed object unexpectedly converted")
	}
	if err := FromUnstructured(nil, &FortiGateDNSPolicy{}); err == nil {
		t.Fatal("nil dynamic object unexpectedly converted")
	}
}
