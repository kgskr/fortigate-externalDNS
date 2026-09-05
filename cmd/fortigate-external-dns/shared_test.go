package main

import (
	"context"
	"strconv"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func TestSharedDNSClientReservesAndConfirmsBeforeCreate(t *testing.T) {
	client, repository := newSharedTestClient(t)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(context.Background(), []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false); err != nil {
		t.Fatal(err)
	}
	identity, _ := ownership.IdentityFor("edge", endpoint)
	claim, err := repository.Get(context.Background(), ownership.ClaimName(identity))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || claim.Spec.ProviderID == "" {
		t.Fatalf("confirmed create claim = %#v", claim)
	}
	if len(claim.Spec.Sources) != 1 || claim.Spec.Sources[0] != (v1alpha1.SourceObjectReference{APIVersion: "v1", Kind: "Service", Namespace: "apps", Name: "api", UID: "service-api-uid"}) {
		t.Fatalf("claim did not preserve CRD-required source identity: %#v", claim.Spec.Sources)
	}
}

func TestSharedDNSClientRevalidatesAndRebindsUpdate(t *testing.T) {
	client, repository := newSharedTestClient(t)
	current := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	if err := client.Apply(context.Background(), []plan.Operation{{Type: plan.OperationCreate, Desired: current}}, false); err != nil {
		t.Fatal(err)
	}
	records, _ := client.ListRecords(context.Background())
	current = records[0]
	desired := current
	desired.TTL = 600
	if err := client.Apply(context.Background(), []plan.Operation{{Type: plan.OperationUpdate, Current: current, Desired: desired}}, false); err != nil {
		t.Fatal(err)
	}
	identity, _ := ownership.IdentityFor("edge", desired)
	claim, err := repository.Get(context.Background(), ownership.ClaimName(identity))
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, _ := ownership.Fingerprint("edge", desired)
	if claim.Spec.Fingerprint != wantFingerprint || claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		t.Fatalf("rebound update claim = %#v", claim)
	}
}

func TestSharedDNSClientDryRunCreatesNoClaimOrProviderRecord(t *testing.T) {
	client, repository := newSharedTestClient(t)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	operation := plan.Operation{Type: plan.OperationCreate, Desired: endpoint}
	outcomes, err := client.ApplyWithResults(context.Background(), []plan.Operation{operation}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0] != (plan.OperationOutcome{OperationID: plan.SanitizeOperation(operation).ID, Result: plan.ApplyBlocked, Reason: "dry-run"}) {
		t.Fatalf("dry-run outcome = %#v", outcomes)
	}
	claims, _ := repository.List(context.Background())
	records, _ := client.ListRecords(context.Background())
	if len(claims) != 0 || len(records) != 0 {
		t.Fatalf("dry run claims=%d records=%d", len(claims), len(records))
	}
}

func TestSharedDNSClientRejectsCreateWithoutCompleteSourceIdentity(t *testing.T) {
	client, repository := newSharedTestClient(t)
	endpoint := sharedTestEndpoint("api.example.com", "203.0.113.10", 300)
	endpoint.Source.UID = ""
	outcomes, err := client.ApplyWithResults(context.Background(), []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false)
	if err == nil || len(outcomes) != 1 || outcomes[0].Result != plan.ApplyFailed || outcomes[0].Reason != "provider-request-failed" {
		t.Fatalf("missing source identity must fail closed: outcomes=%#v err=%v", outcomes, err)
	}
	claims, _ := repository.List(context.Background())
	if len(claims) != 0 {
		t.Fatalf("invalid source identity must not create a claim: %#v", claims)
	}
}

func TestSharedDNSClientReturnsOutcomeForEveryInput(t *testing.T) {
	client, _ := newSharedTestClient(t)
	create := plan.Operation{Type: plan.OperationCreate, Desired: sharedTestEndpoint("api.example.com", "203.0.113.10", 300)}
	conflict := plan.Operation{Type: plan.OperationConflict, Desired: sharedTestEndpoint("conflict.example.com", "203.0.113.20", 300)}
	outcomes, err := client.ApplyWithResults(context.Background(), []plan.Operation{create, conflict}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || outcomes[0].Result != plan.ApplySucceeded || outcomes[1].Result != plan.ApplyBlocked || outcomes[1].Reason != "planning-conflict" {
		t.Fatalf("unexpected operation outcomes: %#v", outcomes)
	}
}

func newSharedTestClient(t *testing.T) (*sharedDNSClient, *ownership.Repository) {
	t.Helper()
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
	return &sharedDNSClient{
		client: provider, handles: &sharedOwnershipHandles{manager: manager, repository: repository},
		namespace: "system", targetName: "edge", controller: "controller-a",
	}, repository
}

func sharedTestEndpoint(name, target string, ttl int64) dns.Endpoint {
	return dns.Endpoint{DNSName: name, RecordType: dns.RecordA, Targets: []string{target}, TTL: ttl, Zone: "example.com", Source: dns.SourceRef{APIVersion: "v1", Kind: "Service", Namespace: "apps", Name: "api", UID: "service-api-uid"}}
}

type sharedFakeProvider struct {
	mu       sync.Mutex
	revision int
	records  []dns.Endpoint
}

func (p *sharedFakeProvider) ListRecords(context.Context) ([]dns.Endpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]dns.Endpoint(nil), p.records...), nil
}

func (p *sharedFakeProvider) ListRecordsWithRevision(context.Context) ([]dns.Endpoint, string, error) {
	records, _ := p.ListRecords(context.Background())
	return records, "revision-" + strconv.Itoa(p.revision), nil
}

func (p *sharedFakeProvider) Apply(_ context.Context, operations []plan.Operation, dryRun bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dryRun {
		return nil
	}
	for _, operation := range operations {
		switch operation.Type {
		case plan.OperationCreate:
			created := operation.Desired.Normalize()
			created.ProviderID = strconv.Itoa(len(p.records) + 1)
			p.records = append(p.records, created)
		case plan.OperationUpdate, plan.OperationDeactivate:
			for i := range p.records {
				if p.records[i].ProviderID == operation.Current.ProviderID {
					updated := operation.Desired.Normalize()
					updated.ProviderID = operation.Current.ProviderID
					p.records[i] = updated
				}
			}
		case plan.OperationDelete:
			for i := range p.records {
				if p.records[i].ProviderID == operation.Current.ProviderID {
					p.records = append(p.records[:i], p.records[i+1:]...)
					break
				}
			}
		}
		p.revision++
	}
	return nil
}

type sharedMemoryStore struct {
	mu      sync.Mutex
	next    int
	objects map[string]*v1alpha1.FortiGateDNSRecordOwnership
}

func (s *sharedMemoryStore) Get(_ context.Context, name string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object := s.objects[name]
	if object == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, name)
	}
	return object.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership), nil
}

func (s *sharedMemoryStore) List(context.Context) ([]v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]v1alpha1.FortiGateDNSRecordOwnership, 0, len(s.objects))
	for _, object := range s.objects {
		result = append(result, *object.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership))
	}
	return result, nil
}

func (s *sharedMemoryStore) Create(_ context.Context, object *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects[object.Name] != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, object.Name)
	}
	return s.write(object), nil
}

func (s *sharedMemoryStore) Update(_ context.Context, object *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(object)
}

func (s *sharedMemoryStore) UpdateStatus(_ context.Context, object *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(object)
}

func (s *sharedMemoryStore) update(object *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	current := s.objects[object.Name]
	if current == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, object.Name)
	}
	if current.ResourceVersion != object.ResourceVersion {
		return nil, apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, object.Name, nil)
	}
	return s.write(object), nil
}

func (s *sharedMemoryStore) write(object *v1alpha1.FortiGateDNSRecordOwnership) *v1alpha1.FortiGateDNSRecordOwnership {
	s.next++
	copy := object.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	copy.ResourceVersion = strconv.Itoa(s.next)
	s.objects[copy.Name] = copy
	return copy.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
}
