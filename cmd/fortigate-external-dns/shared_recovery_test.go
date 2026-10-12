package main

import (
	"context"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type reservedCreateFixture struct {
	client     *sharedDNSClient
	repository *ownership.Repository
	store      *sharedMemoryStore
	provider   *sharedFakeProvider
	desired    dns.Endpoint
	claimName  string
}

// Reproduce the durable state after POST succeeds but relist/Confirm fails.
// Provider rows deliberately have no controller ownership or source metadata.
func newReservedCreateFixture(t *testing.T) *reservedCreateFixture {
	t.Helper()
	ctx := context.Background()
	store := &sharedMemoryStore{objects: map[string]*v1alpha1.FortiGateDNSRecordOwnership{}}
	repository, err := ownership.NewRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := ownership.NewManager(repository)
	if err != nil {
		t.Fatal(err)
	}
	provider := &sharedFakeProvider{revision: 1}
	client := &sharedDNSClient{
		client: provider, handles: &sharedOwnershipHandles{manager: manager, repository: repository},
		namespace: "system", targetName: "edge", controller: "controller-a",
	}
	desired := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	desired.OwnerID = client.controller
	source := desired.Source
	claim, err := repository.Reserve(ctx, ownership.ReserveRequest{
		Namespace: client.namespace, TargetName: client.targetName, ControllerID: client.controller, Endpoint: desired,
		Sources: []v1alpha1.SourceObjectReference{{APIVersion: source.APIVersion, Kind: source.Kind, Namespace: source.Namespace, Name: source.Name, UID: source.UID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := desired
	row.OwnerID, row.Source = "", dns.SourceRef{}
	if err := provider.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: row}}, false); err != nil {
		t.Fatal(err)
	}
	return &reservedCreateFixture{client: client, repository: repository, store: store, provider: provider, desired: desired, claimName: claim.Name}
}

func (f *reservedCreateFixture) changeClaim(t *testing.T, mutate func(*v1alpha1.FortiGateDNSRecordOwnership)) {
	t.Helper()
	claim, err := f.repository.Get(context.Background(), f.claimName)
	if err != nil {
		t.Fatal(err)
	}
	mutate(claim)
	if _, err := f.store.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
}

// Exercise normal planning instead of retrying Apply(create) directly: the
// interrupted row must initially yield a conflict, then become owned next pass.
func (f *reservedCreateFixture) reconcile(t *testing.T, dryRun bool) []plan.Operation {
	t.Helper()
	ctx := context.Background()
	current, err := f.client.ListRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operations := plan.Build([]dns.Endpoint{f.desired}, current, f.client.controller, plan.CleanupDelete)
	if _, err := f.client.ApplyWithResults(ctx, operations, dryRun); err != nil {
		t.Fatal(err)
	}
	return operations
}

func TestSharedReservedCreateRecoversThroughNormalPlanning(t *testing.T) {
	for _, alreadyBound := range []bool{false, true} {
		name := "before-provider-id-binding"
		if alreadyBound {
			name = "after-provider-id-binding"
		}
		t.Run(name, func(t *testing.T) {
			f := newReservedCreateFixture(t)
			if alreadyBound {
				f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) { claim.Spec.ProviderID = "1" })
			}
			revision := f.provider.revision
			operations := f.reconcile(t, false)
			if len(operations) != 1 || operations[0].Type != plan.OperationConflict {
				t.Fatalf("interrupted create must initially plan a conflict: %#v", operations)
			}
			claim, err := f.repository.Get(context.Background(), f.claimName)
			if err != nil || claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || claim.Spec.ProviderID != "1" {
				t.Fatalf("claim=%#v err=%v", claim, err)
			}
			if operations = f.reconcile(t, false); len(operations) != 0 {
				t.Fatalf("recovered row must converge on next normal plan: %#v", operations)
			}
			if len(f.provider.records) != 1 || f.provider.revision != revision {
				t.Fatalf("confirmation must not mutate provider: records=%#v revision=%d", f.provider.records, f.provider.revision)
			}
			f.desired.TTL = 600
			operations = f.reconcile(t, false)
			if len(operations) != 1 || operations[0].Type != plan.OperationUpdate || f.provider.records[0].TTL != 600 {
				t.Fatalf("recovered claim must authorize later normal updates: %#v", operations)
			}
		})
	}
}

type recoverySnapshotProvider struct {
	*sharedFakeProvider
	beforeSnapshot func()
	emptyRevision  bool
}

func (p *recoverySnapshotProvider) ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error) {
	if p.beforeSnapshot != nil {
		hook := p.beforeSnapshot
		p.beforeSnapshot = nil
		hook()
	}
	records, revision, err := p.sharedFakeProvider.ListRecordsWithRevision(ctx)
	if p.emptyRevision {
		revision = ""
	}
	return records, revision, err
}

func TestSharedReservedCreateRecoveryRefusesUnsafeEvidence(t *testing.T) {
	tests := map[string]func(*testing.T, *reservedCreateFixture){
		"different controller":         func(_ *testing.T, f *reservedCreateFixture) { f.client.controller = "other" },
		"different source UID":         func(_ *testing.T, f *reservedCreateFixture) { f.desired.Source.UID = "replacement" },
		"different source API version": func(_ *testing.T, f *reservedCreateFixture) { f.desired.Source.APIVersion = "other/v1" },
		"desired fingerprint changed":  func(_ *testing.T, f *reservedCreateFixture) { f.desired.TTL = 600 },
		"live fingerprint changed":     func(_ *testing.T, f *reservedCreateFixture) { f.provider.records[0].TTL = 600 },
		"desired identity changed":     func(_ *testing.T, f *reservedCreateFixture) { f.desired.Targets = []string{"203.0.113.99"} },
		"different bound provider ID": func(t *testing.T, f *reservedCreateFixture) {
			f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) { claim.Spec.ProviderID = "99" })
		},
		"different target reference": func(t *testing.T, f *reservedCreateFixture) {
			f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) { claim.Spec.TargetRef.Name = "other" })
		},
		"claim is deleting": func(t *testing.T, f *reservedCreateFixture) {
			f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) {
				now := metav1.Now()
				claim.DeletionTimestamp = &now
			})
		},
		"claim is conflicted": func(t *testing.T, f *reservedCreateFixture) {
			f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) {
				claim.Status.Phase = v1alpha1.OwnershipPhaseConflict
			})
		},
		"duplicate exact row": func(_ *testing.T, f *reservedCreateFixture) {
			duplicate := f.provider.records[0]
			duplicate.ProviderID = "2"
			f.provider.records = append(f.provider.records, duplicate)
		},
		"provider ID reused": func(_ *testing.T, f *reservedCreateFixture) {
			duplicate := f.provider.records[0]
			duplicate.DNSName = "other.example.com"
			f.provider.records = append(f.provider.records, duplicate)
		},
		"duplicate ownership claim": func(t *testing.T, f *reservedCreateFixture) {
			claim, err := f.repository.Get(context.Background(), f.claimName)
			if err != nil {
				t.Fatal(err)
			}
			claim.Name = "duplicate-claim"
			claim.ResourceVersion = ""
			if _, err := f.store.Create(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
		},
		"unstable snapshot": func(_ *testing.T, f *reservedCreateFixture) {
			f.client.client = &recoverySnapshotProvider{sharedFakeProvider: f.provider, emptyRevision: true}
		},
		"claim changed during relist": func(t *testing.T, f *reservedCreateFixture) {
			f.client.client = &recoverySnapshotProvider{sharedFakeProvider: f.provider, beforeSnapshot: func() {
				f.changeClaim(t, func(claim *v1alpha1.FortiGateDNSRecordOwnership) { claim.Spec.ControllerID = "other" })
			}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newReservedCreateFixture(t)
			mutate(t, f)
			revision, rows := f.provider.revision, len(f.provider.records)
			operations := f.reconcile(t, false)
			if len(operations) != 1 || operations[0].Type != plan.OperationConflict {
				t.Fatalf("unsafe evidence should remain a planning conflict: %#v", operations)
			}
			claim, err := f.repository.Get(context.Background(), f.claimName)
			if err != nil || claim.Status.Phase == v1alpha1.OwnershipPhaseConfirmed {
				t.Fatalf("unsafe claim was confirmed: claim=%#v err=%v", claim, err)
			}
			if f.provider.revision != revision || len(f.provider.records) != rows {
				t.Fatal("unsafe recovery mutated provider")
			}
		})
	}
}

func TestSharedReservedCreateDryRunDoesNotConfirm(t *testing.T) {
	f := newReservedCreateFixture(t)
	before, _ := f.repository.Get(context.Background(), f.claimName)
	revision := f.provider.revision
	f.reconcile(t, true)
	after, _ := f.repository.Get(context.Background(), f.claimName)
	if after.ResourceVersion != before.ResourceVersion || after.Status.Phase != v1alpha1.OwnershipPhaseReserved || f.provider.revision != revision {
		t.Fatal("dry-run must not confirm claims or mutate provider")
	}
}

func TestSharedRecoveryDoesNotAdoptUnclaimedRow(t *testing.T) {
	ctx := context.Background()
	client, repository := newSharedTestClient(t)
	desired := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	row := desired
	row.Source = dns.SourceRef{}
	if err := client.client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: row}}, false); err != nil {
		t.Fatal(err)
	}
	desired.OwnerID = client.controller
	current, err := client.ListRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operations := plan.Build([]dns.Endpoint{desired}, current, client.controller, plan.CleanupDelete)
	if len(operations) != 1 || operations[0].Type != plan.OperationConflict {
		t.Fatalf("unclaimed record must remain a conflict: %#v", operations)
	}
	if _, err := client.ApplyWithResults(ctx, operations, false); err != nil {
		t.Fatal(err)
	}
	claims, err := repository.List(ctx)
	if err != nil || len(claims) != 0 {
		t.Fatalf("recovery must not create an adoption claim: claims=%#v err=%v", claims, err)
	}
}
