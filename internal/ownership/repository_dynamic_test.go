package ownership

import (
	"context"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDynamicStorePreservesResourceVersionFinalizerAndStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClient(scheme)
	store, err := NewDynamicStore(client, "controller")
	if err != nil {
		t.Fatal(err)
	}
	claim := &v1alpha1.FortiGateDNSRecordOwnership{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSRecordOwnership"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "controller",
			Name:            "record-test",
			ResourceVersion: "17",
			Finalizers:      []string{ClaimFinalizer},
		},
		Status: v1alpha1.FortiGateDNSRecordOwnershipStatus{Phase: v1alpha1.OwnershipPhaseReserved},
	}
	created, err := store.Create(context.Background(), claim)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	created.Status.Phase = v1alpha1.OwnershipPhaseConfirmed
	updated, err := store.UpdateStatus(context.Background(), created)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if updated.ResourceVersion != "17" || updated.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || !containsString(updated.Finalizers, ClaimFinalizer) {
		t.Fatalf("dynamic round trip lost coordination metadata: %#v", updated)
	}
	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Name != claim.Name {
		t.Fatalf("List() = %#v", listed)
	}
}
