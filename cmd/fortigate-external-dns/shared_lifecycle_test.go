package main

import (
	"context"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func sharedClaimFor(t *testing.T, repository *ownership.Repository, endpoint dns.Endpoint) *v1alpha1.FortiGateDNSRecordOwnership {
	t.Helper()
	identity, _ := ownership.IdentityFor("edge", endpoint)
	claim, err := repository.Get(context.Background(), ownership.ClaimName(identity))
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestSharedDNSClientDeleteReleasesClaimAndHostnameCanBeRecreated(t *testing.T) {
	ctx := context.Background()
	client, repository := newSharedTestClient(t)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatal(err)
	}
	records, _ := client.ListRecords(ctx)
	current := records[0]
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationDelete, Current: current}}, false); err != nil {
		t.Fatal(err)
	}
	claims, _ := repository.List(ctx)
	if len(claims) != 0 {
		t.Fatalf("claim must be released and deleted after delete, got %#v", claims)
	}
	// Recreating the same hostname with a different TTL works.
	recreated := sharedTestEndpoint("api.example.com", "203.0.113.10", 600)
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: recreated}}, false); err != nil {
		t.Fatal(err)
	}
	claim := sharedClaimFor(t, repository, recreated)
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || claim.Spec.ProviderID == "" {
		t.Fatalf("recreated claim = %#v", claim)
	}
}

func TestSharedDNSClientRecreateAfterOrphanedClaimWithoutRelease(t *testing.T) {
	ctx := context.Background()
	client, repository := newSharedTestClient(t)
	provider := client.client.(*sharedFakeProvider)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatal(err)
	}
	// Row removed out-of-band, then a create-time audit orphans the claim.
	claim := sharedClaimFor(t, repository, endpoint)
	provider.records = nil
	provider.revision++
	sharedProv := sharedProvider{client: provider}
	snapshot, _ := sharedProv.Snapshot(ctx)
	if _, err := client.handles.manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, "edge", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatalf("recreate over orphaned claim: %v", err)
	}
	if got := sharedClaimFor(t, repository, endpoint); got.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		t.Fatalf("claim = %#v", got)
	}
}

func TestSharedDNSClientOrphanedClaimWithLiveRowStaysRefused(t *testing.T) {
	ctx := context.Background()
	client, repository := newSharedTestClient(t)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatal(err)
	}
	claim := sharedClaimFor(t, repository, endpoint)
	if _, err := repository.MarkOrphaned(ctx, claim.Name, claim.ResourceVersion, "rev"); err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err == nil {
		t.Fatal("orphaned claim with a live provider row must stay refused")
	}
	if got := sharedClaimFor(t, repository, endpoint); got.Status.Phase == v1alpha1.OwnershipPhaseReserved {
		t.Fatalf("orphan must not be revived with a live row: %#v", got)
	}
}

func interruptedRebindFixture(t *testing.T) (*sharedDNSClient, *ownership.Repository, dns.Endpoint, dns.Endpoint) {
	t.Helper()
	ctx := context.Background()
	client, repository := newSharedTestClient(t)
	provider := client.client.(*sharedFakeProvider)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatal(err)
	}
	// The provider PUT succeeded but RebindConfirmed never ran.
	provider.records[0].TTL = 600
	provider.revision++
	desired := endpoint
	desired.TTL = 600
	live := provider.records[0]
	return client, repository, live, desired
}

func TestSharedDNSClientRecoversInterruptedRebind(t *testing.T) {
	ctx := context.Background()
	client, repository, live, desired := interruptedRebindFixture(t)
	outcomes, err := client.ApplyWithResults(ctx, []plan.Operation{{Type: plan.OperationConflict, Current: live, Desired: desired}}, false)
	if err != nil || len(outcomes) != 1 || outcomes[0].Result != plan.ApplyBlocked {
		t.Fatalf("outcomes=%#v err=%v", outcomes, err)
	}
	claim := sharedClaimFor(t, repository, desired)
	want, _ := ownership.Fingerprint("edge", desired)
	if claim.Spec.Fingerprint != want || claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		t.Fatalf("claim not rebound: %#v", claim)
	}
}

func TestSharedDNSClientInterruptedRebindStaysConflictWhenUnsafe(t *testing.T) {
	ctx := context.Background()
	mutations := map[string]func(t *testing.T, client *sharedDNSClient, repository *ownership.Repository, live, desired *dns.Endpoint){
		"live row differs from desired": func(t *testing.T, _ *sharedDNSClient, _ *ownership.Repository, _, desired *dns.Endpoint) {
			desired.TTL = 900
		},
		"provider id differs from claim": func(t *testing.T, _ *sharedDNSClient, _ *ownership.Repository, live, _ *dns.Endpoint) {
			live.ProviderID = "999"
		},
		"different source": func(t *testing.T, _ *sharedDNSClient, _ *ownership.Repository, _, desired *dns.Endpoint) {
			desired.Source.UID = "someone-else"
		},
		"claim not confirmed": func(t *testing.T, _ *sharedDNSClient, repository *ownership.Repository, _, desired *dns.Endpoint) {
			claim := sharedClaimFor(t, repository, *desired)
			if _, err := repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, "rev"); err != nil {
				t.Fatal(err)
			}
		},
		"different controller": func(t *testing.T, client *sharedDNSClient, _ *ownership.Repository, _, _ *dns.Endpoint) {
			client.controller = "controller-b"
		},
		"identity differs": func(t *testing.T, _ *sharedDNSClient, _ *ownership.Repository, _, desired *dns.Endpoint) {
			desired.Targets = []string{"203.0.113.99"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			client, repository, live, desired := interruptedRebindFixture(t)
			original := desired
			before := sharedClaimFor(t, repository, original)
			mutate(t, client, repository, &live, &desired)
			if _, err := client.ApplyWithResults(ctx, []plan.Operation{{Type: plan.OperationConflict, Current: live, Desired: desired}}, false); err != nil {
				t.Fatal(err)
			}
			after := sharedClaimFor(t, repository, original)
			if after.Spec.Fingerprint != before.Spec.Fingerprint {
				t.Fatalf("unsafe rebind changed fingerprint: %#v", after)
			}
		})
	}
}

func TestSharedDNSClientDryRunDoesNotRebind(t *testing.T) {
	ctx := context.Background()
	client, repository, live, desired := interruptedRebindFixture(t)
	before := sharedClaimFor(t, repository, desired)
	if _, err := client.ApplyWithResults(ctx, []plan.Operation{{Type: plan.OperationConflict, Current: live, Desired: desired}}, true); err != nil {
		t.Fatal(err)
	}
	if after := sharedClaimFor(t, repository, desired); after.Spec.Fingerprint != before.Spec.Fingerprint {
		t.Fatal("dry-run must not rebind")
	}
}

func TestSharedDNSClientBindsOwnershipFromSingleList(t *testing.T) {
	ctx := context.Background()
	client, _ := newSharedTestClient(t)
	for _, name := range []string{"a.example.com", "b.example.com"} {
		endpoint := sharedTestEndpoint(name, "203.0.113.10", 300)
		if err := client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
			t.Fatal(err)
		}
	}
	records, err := client.ListRecords(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	for _, record := range records {
		if record.OwnerID != "controller-a" || record.Source.UID != "service-api-uid" {
			t.Fatalf("record not bound: %#v", record)
		}
	}
}
