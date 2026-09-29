package ownership

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func stableSnapshot(rev string, records ...dns.Endpoint) Snapshot {
	return Snapshot{Stable: true, Revision: rev, Records: records}
}

func orphanedClaim(t *testing.T, repository *Repository) *v1alpha1.FortiGateDNSRecordOwnership {
	t.Helper()
	claim := reserveClaim(t, repository)
	claim, err := repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	claim, err = repository.MarkOrphaned(context.Background(), claim.Name, claim.ResourceVersion, "rev-2")
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestReleaseAndDeleteClaimRemovesClaimAfterAbsenceProof(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	claim := orphanedClaim(t, repository)
	present := stableSnapshot("rev-3", withProviderID(testEndpoint(), "41"))
	if err := manager.ReleaseAndDeleteClaim(context.Background(), claim.Name, claim.ResourceVersion, "default", present); !errors.Is(err, ErrClaimNotDestructive) {
		t.Fatalf("release with row present error = %v, want ErrClaimNotDestructive", err)
	}
	if _, err := repository.Get(context.Background(), claim.Name); err != nil {
		t.Fatalf("claim must survive refused release: %v", err)
	}
	if err := manager.ReleaseAndDeleteClaim(context.Background(), claim.Name, claim.ResourceVersion, "default", Snapshot{}); !errors.Is(err, ErrProviderSnapshot) {
		t.Fatalf("release with unstable snapshot error = %v, want ErrProviderSnapshot", err)
	}
	if err := manager.ReleaseAndDeleteClaim(context.Background(), claim.Name, claim.ResourceVersion, "default", stableSnapshot("rev-4")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Get(context.Background(), claim.Name); !errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("claim still present: %v", err)
	}
}

func TestReleaseAndDeleteClaimRejectsStaleResourceVersion(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	claim := orphanedClaim(t, repository)
	if err := manager.ReleaseAndDeleteClaim(context.Background(), claim.Name, "stale", "default", stableSnapshot("rev-3")); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("error = %v, want ErrStaleClaim", err)
	}
}

func TestDeleteClaimTranslatesErrors(t *testing.T) {
	repository, store := testRepository(t)
	claim := reserveClaim(t, repository)
	if err := repository.deleteClaim(context.Background(), claim.Name, ""); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("empty resourceVersion error = %v", err)
	}
	if err := repository.deleteClaim(context.Background(), claim.Name, "stale"); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale error = %v", err)
	}
	store.delete(claim.Name)
	if err := repository.deleteClaim(context.Background(), claim.Name, "1"); err != nil {
		t.Fatalf("already-deleted claim must be success: %v", err)
	}
}

func TestReserveRecoversOrphanedClaimWhenRowAbsent(t *testing.T) {
	repository, store := testRepository(t)
	orphan := orphanedClaim(t, repository)
	request := reserveRequest(testEndpoint())
	request.Endpoint.TTL = 600 // recreated with a different TTL
	request.Sources = []v1alpha1.SourceObjectReference{{APIVersion: "v1", Kind: "Service", Namespace: "apps", Name: "api", UID: "new-uid"}}
	request.Snapshot = func(context.Context) (Snapshot, error) { return stableSnapshot("rev-3"), nil }
	claim, err := repository.Reserve(context.Background(), request)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if claim.Name != orphan.Name || claim.Status.Phase != v1alpha1.OwnershipPhaseReserved || claim.Spec.ProviderID != "" {
		t.Fatalf("recovered claim = %#v", claim)
	}
	want, _ := Fingerprint("default", request.Endpoint)
	stored := store.mustGet(t, claim.Name)
	if stored.Spec.Fingerprint != want || stored.Spec.Sources[0].UID != "new-uid" || !containsString(stored.Finalizers, ClaimFinalizer) {
		t.Fatalf("stored claim not reset: %#v", stored)
	}
}

func TestReserveOrphanedClaimStaysRefusedWhenRowPresentOrSnapshotUnsafe(t *testing.T) {
	cases := map[string]func(context.Context) (Snapshot, error){
		"same id present": func(context.Context) (Snapshot, error) {
			return stableSnapshot("r", withProviderID(testEndpoint(), "41")), nil
		},
		"exact row, new id": func(context.Context) (Snapshot, error) {
			return stableSnapshot("r", withProviderID(testEndpoint(), "99")), nil
		},
		"unstable":       func(context.Context) (Snapshot, error) { return Snapshot{Revision: "r"}, nil },
		"snapshot error": func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("boom") },
	}
	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			repository, store := testRepository(t)
			orphan := orphanedClaim(t, repository)
			request := reserveRequest(testEndpoint())
			request.Snapshot = snapshot
			if _, err := repository.Reserve(context.Background(), request); err == nil {
				t.Fatal("Reserve() must fail closed")
			}
			if phase := store.mustGet(t, orphan.Name).Status.Phase; phase == v1alpha1.OwnershipPhaseReserved || phase == v1alpha1.OwnershipPhaseConfirmed {
				t.Fatalf("phase = %q, orphan must not be revived", phase)
			}
		})
	}
}

func TestReserveOrphanedClaimWithoutSnapshotOrOtherControllerStaysRefused(t *testing.T) {
	repository, _ := testRepository(t)
	orphanedClaim(t, repository)
	if _, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint())); !errors.Is(err, ErrClaimNotConfirmed) {
		t.Fatalf("no snapshot error = %v, want ErrClaimNotConfirmed", err)
	}
	other := reserveRequest(testEndpoint())
	other.ControllerID = "controller-b"
	other.Snapshot = func(context.Context) (Snapshot, error) { return stableSnapshot("r"), nil }
	if _, err := repository.Reserve(context.Background(), other); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("other controller error = %v, want ErrClaimConflict", err)
	}
}

func TestDeleteThenRecreateSameHostnamePublishesAgain(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	provider := newFakeProvider()
	request := CreateRequest{ReserveRequest: reserveRequest(testEndpoint())}
	claim, err := manager.ReconcileCreate(context.Background(), provider, request)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a controller-initiated delete (row gone) without the release.
	provider.records = nil
	provider.revision++
	after, _ := provider.Snapshot(context.Background())
	orphaned, err := manager.ReconcileClaim(context.Background(), claim.Name, claim.ResourceVersion, "default", after)
	if err != nil || orphaned.Status.Phase != v1alpha1.OwnershipPhaseOrphaned {
		t.Fatalf("orphan = %#v err=%v", orphaned, err)
	}
	// Recreate: recovers the orphaned claim, creates, confirms.
	request.Endpoint.TTL = 900
	again, err := manager.ReconcileCreate(context.Background(), provider, request)
	if err != nil {
		t.Fatalf("recreate error = %v", err)
	}
	if again.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || again.Spec.ProviderID == "" || provider.createCalls != 2 || len(provider.records) != 1 {
		t.Fatalf("recreate claim=%#v creates=%d records=%d", again, provider.createCalls, len(provider.records))
	}
}

func TestReserveCompletesInterruptedTwoStepReserve(t *testing.T) {
	repository, store := testRepository(t)
	identity, fingerprint, _ := requestIdentity("default", testEndpoint())
	store.forceCreate(&v1alpha1.FortiGateDNSRecordOwnership{
		ObjectMeta: metav1.ObjectMeta{Namespace: "controller", Name: ClaimName(identity), Finalizers: []string{ClaimFinalizer}},
		Spec: v1alpha1.FortiGateDNSRecordOwnershipSpec{
			TargetRef: localTargetReference("default"), Record: RecordKey(identity), Fingerprint: fingerprint, ControllerID: "controller-a",
		},
	})
	claim, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint()))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseReserved {
		t.Fatalf("phase = %q, want Reserved", claim.Status.Phase)
	}
}

func TestReserveEmptyPhaseWithDifferentSpecIsConflict(t *testing.T) {
	repository, store := testRepository(t)
	identity, _, _ := requestIdentity("default", testEndpoint())
	store.forceCreate(&v1alpha1.FortiGateDNSRecordOwnership{
		ObjectMeta: metav1.ObjectMeta{Namespace: "controller", Name: ClaimName(identity), Finalizers: []string{ClaimFinalizer}},
		Spec: v1alpha1.FortiGateDNSRecordOwnershipSpec{
			TargetRef: localTargetReference("default"), Record: RecordKey(identity), Fingerprint: "other", ControllerID: "controller-a",
		},
	})
	if _, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint())); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("error = %v, want ErrClaimConflict", err)
	}
}

func TestRepositoryGetTranslatesNotFoundAndErrors(t *testing.T) {
	repository, store := testRepository(t)
	if _, err := repository.Get(context.Background(), "missing"); !errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("error = %v, want ErrClaimNotFound", err)
	}
	store.failReads = true
	if _, err := repository.Get(context.Background(), "x"); err == nil || errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("read failure must be surfaced, got %v", err)
	}
	if _, err := repository.List(context.Background()); err == nil {
		t.Fatal("list failure must be surfaced")
	}
}

func TestTranslateWriteError(t *testing.T) {
	gr := schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}
	if err := translateWriteError("x", apierrors.NewConflict(gr, "n", nil)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("conflict = %v", err)
	}
	if err := translateWriteError("x", apierrors.NewAlreadyExists(gr, "n")); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("already exists = %v", err)
	}
	base := errors.New("boom")
	if err := translateWriteError("x", base); !errors.Is(err, base) || errors.Is(err, ErrStaleClaim) {
		t.Fatalf("other = %v", err)
	}
}

func TestRebindConfirmedGuards(t *testing.T) {
	repository, _ := testRepository(t)
	claim := reserveClaim(t, repository)
	updated := testEndpoint()
	updated.TTL = 600
	ctx := context.Background()
	if _, err := repository.RebindConfirmed(ctx, claim.Name, claim.ResourceVersion, "default", updated, "41", "rev-2"); !errors.Is(err, ErrClaimNotConfirmed) {
		t.Fatalf("reserved rebind error = %v", err)
	}
	claim, _ = repository.Confirm(ctx, claim.Name, claim.ResourceVersion, "41", "rev-1")
	if _, err := repository.RebindConfirmed(ctx, claim.Name, "stale", "default", updated, "41", "rev-2"); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale rebind error = %v", err)
	}
	if _, err := repository.RebindConfirmed(ctx, claim.Name, claim.ResourceVersion, "default", updated, "99", "rev-2"); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("provider ID mismatch error = %v", err)
	}
	other := updated
	other.Targets = []string{"192.0.2.99"}
	if _, err := repository.RebindConfirmed(ctx, claim.Name, claim.ResourceVersion, "default", other, "41", "rev-2"); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("identity change error = %v", err)
	}
	rebound, err := repository.RebindConfirmed(ctx, claim.Name, claim.ResourceVersion, "default", updated, "41", "rev-2")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Fingerprint("default", updated)
	if rebound.Spec.Fingerprint != want || rebound.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || rebound.Status.ObservedProviderRevision != "rev-2" {
		t.Fatalf("rebound = %#v", rebound)
	}
}

func TestReconcileClaimOutcomes(t *testing.T) {
	ctx := context.Background()
	t.Run("reserved without row stays reserved", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		got, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r"))
		if err != nil || got.Status.Phase != v1alpha1.OwnershipPhaseReserved {
			t.Fatalf("got %#v err=%v", got, err)
		}
	})
	t.Run("reserved with exact row confirms", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		got, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r", withProviderID(testEndpoint(), "41")))
		if err != nil || got.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || got.Spec.ProviderID != "41" {
			t.Fatalf("got %#v err=%v", got, err)
		}
	})
	t.Run("confirmed without row orphans", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		claim, _ = repository.Confirm(ctx, claim.Name, claim.ResourceVersion, "41", "r1")
		got, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r2"))
		if err != nil || got.Status.Phase != v1alpha1.OwnershipPhaseOrphaned {
			t.Fatalf("got %#v err=%v", got, err)
		}
	})
	t.Run("divergent row conflicts", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		row := withProviderID(testEndpoint(), "41")
		row.TTL = 999
		got, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r", row))
		if !errors.Is(err, ErrProviderConflict) || got.Status.Phase != v1alpha1.OwnershipPhaseConflict {
			t.Fatalf("got %#v err=%v", got, err)
		}
	})
	t.Run("duplicate exact rows conflict", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		_, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r", withProviderID(testEndpoint(), "41"), withProviderID(testEndpoint(), "42")))
		if !errors.Is(err, ErrProviderConflict) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("row without id conflicts", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		_, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r", testEndpoint()))
		if !errors.Is(err, ErrProviderConflict) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("changed provider id conflicts", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		claim, _ = repository.Confirm(ctx, claim.Name, claim.ResourceVersion, "41", "r1")
		_, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", stableSnapshot("r2", withProviderID(testEndpoint(), "77")))
		if !errors.Is(err, ErrProviderConflict) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("unstable snapshot and stale rv rejected", func(t *testing.T) {
		repository, _ := testRepository(t)
		manager, _ := NewManager(repository)
		claim := reserveClaim(t, repository)
		if _, err := manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "default", Snapshot{}); !errors.Is(err, ErrProviderSnapshot) {
			t.Fatalf("err=%v", err)
		}
		if _, err := manager.ReconcileClaim(ctx, claim.Name, "stale", "default", stableSnapshot("r")); !errors.Is(err, ErrStaleClaim) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestDynamicStoreGetUpdateDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := NewDynamicStore(dynamicfake.NewSimpleDynamicClient(scheme), "controller")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claim := &v1alpha1.FortiGateDNSRecordOwnership{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSRecordOwnership"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "controller", Name: "record-x", Finalizers: []string{ClaimFinalizer}},
	}
	created, err := store.Create(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	created.Spec.ProviderID = "7"
	if _, err := store.Update(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "record-x")
	if err != nil || got.Spec.ProviderID != "7" {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if err := store.Delete(ctx, "record-x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "record-x"); !apierrors.IsNotFound(err) {
		t.Fatalf("after delete err = %v", err)
	}
	if _, err := NewDynamicStore(nil, "ns"); err == nil {
		t.Fatal("nil client must fail")
	}
	if _, err := NewDynamicStore(dynamicfake.NewSimpleDynamicClient(scheme), " "); err == nil {
		t.Fatal("empty namespace must fail")
	}
}
