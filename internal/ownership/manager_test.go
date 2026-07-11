package ownership

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRepositoryReserveUsesFinalizerAndOptimisticResourceVersion(t *testing.T) {
	repository, store := testRepository(t)
	claim, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint()))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseReserved {
		t.Fatalf("phase = %q, want Reserved", claim.Status.Phase)
	}
	if !containsString(claim.Finalizers, ClaimFinalizer) {
		t.Fatalf("finalizers = %v, want %q", claim.Finalizers, ClaimFinalizer)
	}
	if claim.ResourceVersion == "" {
		t.Fatal("reservation must have a resourceVersion")
	}
	if _, err := repository.Confirm(context.Background(), claim.Name, "stale", "41", "rev-1"); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("Confirm(stale resourceVersion) error = %v, want ErrStaleClaim", err)
	}
	stored := store.mustGet(t, claim.Name)
	if stored.Status.Phase != v1alpha1.OwnershipPhaseReserved || stored.Spec.ProviderID != "" {
		t.Fatalf("stale write changed claim: %#v", stored)
	}
}

func TestRepositoryReleasesFinalizerOnlyAfterProviderAbsenceProof(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	claim := reserveClaim(t, repository)
	present := withProviderID(testEndpoint(), "41")
	if _, err := manager.ReleaseClaimFinalizer(context.Background(), claim.Name, claim.ResourceVersion, "default", Snapshot{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{present}}); !errors.Is(err, ErrClaimNotDestructive) {
		t.Fatalf("ReleaseClaimFinalizer(record present) error = %v, want ErrClaimNotDestructive", err)
	}
	released, err := manager.ReleaseClaimFinalizer(context.Background(), claim.Name, claim.ResourceVersion, "default", Snapshot{Stable: true, Revision: "rev-2"})
	if err != nil {
		t.Fatalf("ReleaseClaimFinalizer() error = %v", err)
	}
	if containsString(released.Finalizers, ClaimFinalizer) {
		t.Fatalf("finalizers = %v, ownership finalizer was not removed", released.Finalizers)
	}
}

func TestRepositoryDetectsDuplicateAndConcurrentClaims(t *testing.T) {
	repository, store := testRepository(t)
	first, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint()))
	if err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}
	duplicate := first.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	duplicate.Name = "manually-created-duplicate"
	duplicate.ResourceVersion = ""
	duplicate.Status.Phase = v1alpha1.OwnershipPhaseConfirmed
	store.forceCreate(duplicate)
	if _, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint())); !errors.Is(err, ErrDuplicateClaim) {
		t.Fatalf("Reserve() error = %v, want ErrDuplicateClaim", err)
	}
	if phase := store.mustGet(t, first.Name).Status.Phase; phase != v1alpha1.OwnershipPhaseConflict {
		t.Fatalf("original phase = %q, want Conflict", phase)
	}
	if phase := store.mustGet(t, duplicate.Name).Status.Phase; phase != v1alpha1.OwnershipPhaseConflict {
		t.Fatalf("duplicate phase = %q, want Conflict", phase)
	}
}

func TestConcurrentDifferentControllerReservationConflicts(t *testing.T) {
	repository, _ := testRepository(t)
	requestA := reserveRequest(testEndpoint())
	requestB := requestA
	requestB.ControllerID = "controller-b"

	var wait sync.WaitGroup
	wait.Add(2)
	errorsSeen := make(chan error, 2)
	for _, request := range []ReserveRequest{requestA, requestB} {
		request := request
		go func() {
			defer wait.Done()
			_, err := repository.Reserve(context.Background(), request)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	conflicts := 0
	successes := 0
	for err := range errorsSeen {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrClaimConflict):
			conflicts++
		default:
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestLostCreateResponseConvergesWithoutDuplicate(t *testing.T) {
	repository, _ := testRepository(t)
	manager, err := NewManager(repository)
	if err != nil {
		t.Fatal(err)
	}
	provider := newFakeProvider()
	provider.loseNextCreate = true
	request := CreateRequest{ReserveRequest: reserveRequest(testEndpoint())}
	claim, err := manager.ReconcileCreate(context.Background(), provider, request)
	if err != nil {
		t.Fatalf("ReconcileCreate() lost response error = %v", err)
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || claim.Spec.ProviderID == "" {
		t.Fatalf("claim = %#v, want Confirmed with provider ID", claim)
	}
	if provider.createCalls != 1 || len(provider.records) != 1 {
		t.Fatalf("createCalls=%d records=%d, want 1/1", provider.createCalls, len(provider.records))
	}
	if _, err := manager.ReconcileCreate(context.Background(), provider, request); err != nil {
		t.Fatalf("second ReconcileCreate() error = %v", err)
	}
	if provider.createCalls != 1 || len(provider.records) != 1 {
		t.Fatalf("lost-response convergence duplicated row: createCalls=%d records=%d", provider.createCalls, len(provider.records))
	}
}

func TestConfirmedCreateClaimBecomesOrphanedWhenProviderRowDisappears(t *testing.T) {
	repository, store := testRepository(t)
	manager, _ := NewManager(repository)
	claim := reserveClaim(t, repository)
	claim, _ = repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
	provider := &scriptedProvider{snapshots: []Snapshot{{Stable: true, Revision: "rev-2"}}}
	if _, err := manager.ReconcileCreate(context.Background(), provider, CreateRequest{ReserveRequest: reserveRequest(testEndpoint())}); !errors.Is(err, ErrClaimNotDestructive) {
		t.Fatalf("ReconcileCreate(missing confirmed row) error = %v, want ErrClaimNotDestructive", err)
	}
	if phase := store.mustGet(t, claim.Name).Status.Phase; phase != v1alpha1.OwnershipPhaseOrphaned {
		t.Fatalf("phase = %q, want Orphaned", phase)
	}
}

func TestCreateConfirmsOnlyOneExactStableProviderRow(t *testing.T) {
	repository, store := testRepository(t)
	manager, _ := NewManager(repository)
	provider := newFakeProvider()
	first := testEndpoint()
	first.ProviderID = "1"
	second := first
	second.ProviderID = "2"
	provider.records = []dns.Endpoint{first, second}
	provider.revision = 2
	if _, err := manager.ReconcileCreate(context.Background(), provider, CreateRequest{ReserveRequest: reserveRequest(testEndpoint())}); !errors.Is(err, ErrProviderConflict) {
		t.Fatalf("ReconcileCreate(duplicates) error = %v, want ErrProviderConflict", err)
	}
	identity, _ := IdentityFor("default", testEndpoint())
	claim := store.mustGet(t, ClaimName(identity))
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConflict {
		t.Fatalf("phase = %q, want Conflict", claim.Status.Phase)
	}
	if provider.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", provider.createCalls)
	}
}

func TestReservedAndOrphanedClaimsNeverAuthorizeDestructiveMutation(t *testing.T) {
	for _, phase := range []v1alpha1.OwnershipPhase{v1alpha1.OwnershipPhaseReserved, v1alpha1.OwnershipPhaseOrphaned} {
		for _, operationType := range []string{plan.OperationDelete, plan.OperationDeactivate} {
			t.Run(string(phase)+"/"+operationType, func(t *testing.T) {
				repository, _ := testRepository(t)
				manager, _ := NewManager(repository)
				claim := reserveClaim(t, repository)
				if phase == v1alpha1.OwnershipPhaseOrphaned {
					claim, _ = repository.MarkOrphaned(context.Background(), claim.Name, claim.ResourceVersion, "rev-1")
				}
				precondition := PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, "rev-1")
				snapshot := Snapshot{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{withProviderID(testEndpoint(), "41")}}
				called := false
				provider := &scriptedProvider{snapshots: []Snapshot{snapshot}}
				err := manager.ExecuteMutation(context.Background(), provider, operationType, precondition, func(context.Context) error {
					called = true
					return nil
				})
				if !errors.Is(err, ErrClaimNotDestructive) {
					t.Fatalf("ExecuteMutation() error = %v, want ErrClaimNotDestructive", err)
				}
				if called {
					t.Fatal("destructive callback ran for non-Confirmed claim")
				}
			})
		}
	}
}

func TestConfirmedClaimPreconditionsAndImmediateRevalidation(t *testing.T) {
	repository, store := testRepository(t)
	manager, _ := NewManager(repository)
	claim := reserveClaim(t, repository)
	claim, err := repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	live := withProviderID(testEndpoint(), "41")
	precondition := PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, "rev-1")
	for _, operationType := range []string{plan.OperationUpdate, plan.OperationReplace, plan.OperationDeactivate, plan.OperationDelete} {
		if err := manager.AuthorizeMutation(context.Background(), operationType, precondition, Snapshot{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{live}}); err != nil {
			t.Fatalf("AuthorizeMutation(%s) error = %v", operationType, err)
		}
	}
	provider := &scriptedProvider{snapshots: []Snapshot{{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{live}}}}
	called := false
	if err := manager.ExecuteMutation(context.Background(), provider, plan.OperationReplace, precondition, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("ExecuteMutation() error = %v", err)
	}
	if !called {
		t.Fatal("matching Confirmed claim did not authorize mutation")
	}

	store.delete(claim.Name)
	called = false
	provider.snapshots = []Snapshot{{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{live}}}
	if err := manager.ExecuteMutation(context.Background(), provider, plan.OperationDelete, precondition, func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("deleted claim error = %v, want ErrClaimNotFound", err)
	}
	if called {
		t.Fatal("deleted claim authorized provider request")
	}
}

func TestConfirmedClaimRejectsResourceVersionAndProviderRevisionRaces(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	claim := reserveClaim(t, repository)
	claim, _ = repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
	precondition := PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, "rev-1")
	live := withProviderID(testEndpoint(), "41")

	if err := manager.AuthorizeMutation(context.Background(), plan.OperationDelete, precondition, Snapshot{Stable: true, Revision: "rev-2", Records: []dns.Endpoint{live}}); !errors.Is(err, ErrProviderDrift) {
		t.Fatalf("provider revision race error = %v, want ErrProviderDrift", err)
	}
	if _, err := repository.MarkOrphaned(context.Background(), claim.Name, claim.ResourceVersion, "rev-1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AuthorizeMutation(context.Background(), plan.OperationDelete, precondition, Snapshot{Stable: true, Revision: "rev-1", Records: []dns.Endpoint{live}}); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("claim resourceVersion race error = %v, want ErrStaleClaim", err)
	}
}

func TestConfirmedClaimRejectsFingerprintDriftAndProviderIDReuse(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		records []dns.Endpoint
	}{
		{name: "fingerprint drift", records: []dns.Endpoint{func() dns.Endpoint { value := withProviderID(testEndpoint(), "41"); value.TTL++; return value }()}},
		{name: "provider ID reuse", records: []dns.Endpoint{withProviderID(testEndpoint(), "41"), withProviderID(dns.Endpoint{Zone: "example.com", DNSName: "other.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.50"}, TTL: 300}, "41")}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository, store := testRepository(t)
			manager, _ := NewManager(repository)
			claim := reserveClaim(t, repository)
			claim, _ = repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
			precondition := PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, "rev-1")
			err := manager.AuthorizeMutation(context.Background(), plan.OperationUpdate, precondition, Snapshot{Stable: true, Revision: "rev-1", Records: testCase.records})
			if !errors.Is(err, ErrProviderConflict) {
				t.Fatalf("AuthorizeMutation() error = %v, want ErrProviderConflict", err)
			}
			if phase := store.mustGet(t, claim.Name).Status.Phase; phase != v1alpha1.OwnershipPhaseConflict {
				t.Fatalf("phase = %q, want Conflict", phase)
			}
		})
	}
}

func TestAdoptionRequiresExactApprovalRevisionFingerprintAndUnclaimedCandidate(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	endpoint := withProviderID(testEndpoint(), "77")
	fingerprint, _ := Fingerprint("default", endpoint)
	base := AdoptionRequest{
		ReserveRequest:      ReserveRequest{Namespace: "controller", TargetName: "default", ControllerID: "controller-a", Endpoint: endpoint},
		ProviderID:          "77",
		ProviderRevision:    "rev-7",
		PlanHash:            "plan-hash",
		ApprovedPlanHash:    "plan-hash",
		ExpectedFingerprint: fingerprint,
	}
	provider := &scriptedProvider{snapshots: []Snapshot{{Stable: true, Revision: "rev-7", Records: []dns.Endpoint{endpoint}}}}
	claim, err := manager.Adopt(context.Background(), provider, AdoptRequest{AdoptionRequest: base})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed || !claim.Spec.AdoptionRequested {
		t.Fatalf("adopted claim = %#v", claim)
	}
	provider.snapshots = []Snapshot{{Stable: true, Revision: "rev-7", Records: []dns.Endpoint{endpoint}}}
	if _, err := manager.Adopt(context.Background(), provider, AdoptRequest{AdoptionRequest: base}); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("second Adopt() error = %v, want ErrClaimConflict", err)
	}

	otherRepository, _ := testRepository(t)
	otherManager, _ := NewManager(otherRepository)
	changed := base
	changed.ProviderRevision = "rev-old"
	provider.snapshots = []Snapshot{{Stable: true, Revision: "rev-new", Records: []dns.Endpoint{endpoint}}}
	if _, err := otherManager.Adopt(context.Background(), provider, AdoptRequest{AdoptionRequest: changed}); !errors.Is(err, ErrProviderDrift) {
		t.Fatalf("Adopt(revision race) error = %v, want ErrProviderDrift", err)
	}
	changed = base
	changed.ApprovedPlanHash = "different"
	provider.snapshots = []Snapshot{{Stable: true, Revision: "rev-7", Records: []dns.Endpoint{endpoint}}}
	if _, err := otherManager.Adopt(context.Background(), provider, AdoptRequest{AdoptionRequest: changed}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Adopt(approval mismatch) error = %v, want ErrApprovalRequired", err)
	}
	changed = base
	changedRecord := endpoint
	changedRecord.TTL++
	provider.snapshots = []Snapshot{{Stable: true, Revision: "rev-7", Records: []dns.Endpoint{changedRecord}}}
	if _, err := otherManager.Adopt(context.Background(), provider, AdoptRequest{AdoptionRequest: changed}); !errors.Is(err, ErrProviderConflict) {
		t.Fatalf("Adopt(fingerprint race) error = %v, want ErrProviderConflict", err)
	}
}

func TestOrphanRecoveryRequiresSameExactProviderRow(t *testing.T) {
	repository, _ := testRepository(t)
	manager, _ := NewManager(repository)
	claim := reserveClaim(t, repository)
	claim, _ = repository.Confirm(context.Background(), claim.Name, claim.ResourceVersion, "41", "rev-1")
	claim, _ = repository.MarkOrphaned(context.Background(), claim.Name, claim.ResourceVersion, "rev-2")
	recovered, err := manager.ReconcileClaim(context.Background(), claim.Name, claim.ResourceVersion, "default", Snapshot{
		Stable: true, Revision: "rev-3", Records: []dns.Endpoint{withProviderID(testEndpoint(), "41")},
	})
	if err != nil {
		t.Fatalf("ReconcileClaim() error = %v", err)
	}
	if recovered.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		t.Fatalf("phase = %q, want Confirmed", recovered.Status.Phase)
	}
	if recovered.Status.ObservedProviderRevision != "rev-3" {
		t.Fatalf("observed revision = %q, want rev-3", recovered.Status.ObservedProviderRevision)
	}
}

func TestExclusiveModeCompatibilityDoesNotReadClaimRepository(t *testing.T) {
	repository, store := testRepository(t)
	store.failReads = true
	manager, _ := NewManager(repository)
	err := manager.AuthorizeMutation(context.Background(), plan.OperationDelete, MutationPrecondition{Mode: v1alpha1.OwnershipModeExclusive}, Snapshot{})
	if err != nil {
		t.Fatalf("exclusive AuthorizeMutation() error = %v", err)
	}
}

func testRepository(t *testing.T) (*Repository, *memoryStore) {
	t.Helper()
	store := &memoryStore{claims: map[string]*v1alpha1.FortiGateDNSRecordOwnership{}}
	repository, err := NewRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	return repository, store
}

func reserveClaim(t *testing.T, repository *Repository) *v1alpha1.FortiGateDNSRecordOwnership {
	t.Helper()
	claim, err := repository.Reserve(context.Background(), reserveRequest(testEndpoint()))
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func reserveRequest(endpoint dns.Endpoint) ReserveRequest {
	return ReserveRequest{Namespace: "controller", TargetName: "default", ControllerID: "controller-a", Endpoint: endpoint}
}

func testEndpoint() dns.Endpoint {
	return dns.Endpoint{Zone: "example.com", DNSName: "api.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.10"}, TTL: 300}
}

func withProviderID(endpoint dns.Endpoint, providerID string) dns.Endpoint {
	endpoint.ProviderID = providerID
	return endpoint
}

type memoryStore struct {
	mu        sync.Mutex
	claims    map[string]*v1alpha1.FortiGateDNSRecordOwnership
	nextRV    int
	failReads bool
}

func (s *memoryStore) Get(_ context.Context, name string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failReads {
		return nil, errors.New("unexpected repository read")
	}
	claim, ok := s.claims[name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, name)
	}
	return claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership), nil
}

func (s *memoryStore) List(context.Context) ([]v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failReads {
		return nil, errors.New("unexpected repository read")
	}
	claims := make([]v1alpha1.FortiGateDNSRecordOwnership, 0, len(s.claims))
	for _, claim := range s.claims {
		claims = append(claims, *claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership))
	}
	return claims, nil
}

func (s *memoryStore) Create(_ context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.claims[claim.Name]; exists {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, claim.Name)
	}
	return s.storeCopy(claim), nil
}

func (s *memoryStore) Update(_ context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	return s.update(claim)
}

func (s *memoryStore) UpdateStatus(_ context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	return s.update(claim)
}

func (s *memoryStore) update(claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.claims[claim.Name]
	if !exists {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, claim.Name)
	}
	if claim.ResourceVersion == "" || claim.ResourceVersion != current.ResourceVersion {
		return nil, apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "fortigatednsrecordownerships"}, claim.Name, errors.New("resourceVersion changed"))
	}
	return s.storeCopy(claim), nil
}

func (s *memoryStore) storeCopy(claim *v1alpha1.FortiGateDNSRecordOwnership) *v1alpha1.FortiGateDNSRecordOwnership {
	s.nextRV++
	copy := claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	copy.ResourceVersion = strconv.Itoa(s.nextRV)
	s.claims[copy.Name] = copy
	return copy.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
}

func (s *memoryStore) forceCreate(claim *v1alpha1.FortiGateDNSRecordOwnership) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeCopy(claim)
}

func (s *memoryStore) mustGet(t *testing.T, name string) *v1alpha1.FortiGateDNSRecordOwnership {
	t.Helper()
	claim, err := s.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func (s *memoryStore) delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claims, name)
}

type fakeProvider struct {
	mu             sync.Mutex
	records        []dns.Endpoint
	revision       int
	createCalls    int
	loseNextCreate bool
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{revision: 1}
}

func (p *fakeProvider) Snapshot(context.Context) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Snapshot{Stable: true, Revision: fmt.Sprintf("rev-%d", p.revision), Records: append([]dns.Endpoint(nil), p.records...)}, nil
}

func (p *fakeProvider) Create(_ context.Context, endpoint dns.Endpoint) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createCalls++
	p.revision++
	endpoint.ProviderID = strconv.Itoa(40 + p.createCalls)
	p.records = append(p.records, endpoint)
	if p.loseNextCreate {
		p.loseNextCreate = false
		return errors.New("connection lost after commit")
	}
	return nil
}

type scriptedProvider struct {
	snapshots []Snapshot
}

func (p *scriptedProvider) Snapshot(context.Context) (Snapshot, error) {
	if len(p.snapshots) == 0 {
		return Snapshot{}, errors.New("no scripted snapshot")
	}
	snapshot := p.snapshots[0]
	p.snapshots = p.snapshots[1:]
	return snapshot, nil
}

func (*scriptedProvider) Create(context.Context, dns.Endpoint) error {
	return errors.New("unexpected create")
}

var _ Store = (*memoryStore)(nil)
var _ Provider = (*fakeProvider)(nil)
var _ Provider = (*scriptedProvider)(nil)
var _ = metav1.Now
