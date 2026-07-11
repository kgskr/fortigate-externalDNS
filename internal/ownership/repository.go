package ownership

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

const ClaimFinalizer = v1alpha1.GroupName + "/ownership-protection"

var (
	ErrClaimNotFound       = errors.New("ownership claim not found")
	ErrClaimConflict       = errors.New("ownership claim conflict")
	ErrDuplicateClaim      = errors.New("duplicate ownership claim")
	ErrStaleClaim          = errors.New("ownership claim resourceVersion changed")
	ErrClaimNotConfirmed   = errors.New("ownership claim is not confirmed")
	ErrClaimNotDestructive = errors.New("ownership claim does not authorize destructive mutation")
)

// Store is the persistence boundary for ownership claims. Update and
// UpdateStatus must honor metadata.resourceVersion and return a Kubernetes
// conflict when another writer wins the race.
type Store interface {
	Get(context.Context, string) (*v1alpha1.FortiGateDNSRecordOwnership, error)
	List(context.Context) ([]v1alpha1.FortiGateDNSRecordOwnership, error)
	Create(context.Context, *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error)
	Update(context.Context, *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error)
	UpdateStatus(context.Context, *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error)
}

// DynamicStore persists typed claims through a namespace-scoped dynamic
// client. Conversion preserves resourceVersion and finalizers.
type DynamicStore struct {
	resource dynamic.ResourceInterface
}

func NewDynamicStore(client dynamic.Interface, namespace string) (*DynamicStore, error) {
	if client == nil {
		return nil, fmt.Errorf("dynamic ownership client is required")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("ownership namespace is required")
	}
	return &DynamicStore{resource: client.Resource(v1alpha1.OwnershipGVR).Namespace(namespace)}, nil
}

func (s *DynamicStore) Get(ctx context.Context, name string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	object, err := s.resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return ownershipFromUnstructured(object)
}

func (s *DynamicStore) List(ctx context.Context) ([]v1alpha1.FortiGateDNSRecordOwnership, error) {
	list, err := s.resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	claims := make([]v1alpha1.FortiGateDNSRecordOwnership, 0, len(list.Items))
	for index := range list.Items {
		claim, convertErr := ownershipFromUnstructured(&list.Items[index])
		if convertErr != nil {
			return nil, convertErr
		}
		claims = append(claims, *claim)
	}
	return claims, nil
}

func (s *DynamicStore) Create(ctx context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	object, err := v1alpha1.ToUnstructured(claim)
	if err != nil {
		return nil, err
	}
	created, err := s.resource.Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return ownershipFromUnstructured(created)
}

func (s *DynamicStore) Update(ctx context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	object, err := v1alpha1.ToUnstructured(claim)
	if err != nil {
		return nil, err
	}
	updated, err := s.resource.Update(ctx, object, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return ownershipFromUnstructured(updated)
}

func (s *DynamicStore) UpdateStatus(ctx context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	object, err := v1alpha1.ToUnstructured(claim)
	if err != nil {
		return nil, err
	}
	updated, err := s.resource.UpdateStatus(ctx, object, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return ownershipFromUnstructured(updated)
}

func ownershipFromUnstructured(object *unstructured.Unstructured) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim := new(v1alpha1.FortiGateDNSRecordOwnership)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, claim); err != nil {
		return nil, fmt.Errorf("convert ownership claim %q: %w", object.GetName(), err)
	}
	return claim, nil
}

type Repository struct {
	store Store
	now   func() time.Time
}

func NewRepository(store Store) (*Repository, error) {
	if store == nil {
		return nil, fmt.Errorf("ownership store is required")
	}
	return &Repository{store: store, now: time.Now}, nil
}

type ReserveRequest struct {
	Namespace    string
	TargetName   string
	ControllerID string
	Endpoint     dns.Endpoint
	Sources      []v1alpha1.SourceObjectReference
}

func (r *Repository) Get(ctx context.Context, name string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim, err := r.store.Get(ctx, name)
	if apierrors.IsNotFound(err) {
		return nil, ErrClaimNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get ownership claim: %w", err)
	}
	return claim, nil
}

func (r *Repository) List(ctx context.Context) ([]v1alpha1.FortiGateDNSRecordOwnership, error) {
	claims, err := r.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ownership claims: %w", err)
	}
	return claims, nil
}

// RebindConfirmed updates the fingerprint of the same logical provider row
// after a TTL/status mutation has been observed in a stable snapshot. Identity
// changes require a separately reserved replacement claim and are rejected.
func (r *Repository) RebindConfirmed(ctx context.Context, name, expectedResourceVersion, targetName string, endpoint dns.Endpoint, providerID, providerRevision string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim, err := r.RevalidateUnique(ctx, name, expectedResourceVersion)
	if err != nil {
		return nil, err
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		return nil, ErrClaimNotConfirmed
	}
	identity, err := IdentityFor(targetName, endpoint)
	if err != nil {
		return nil, err
	}
	if claim.Name != ClaimName(identity) || claim.Spec.Record != RecordKey(identity) || claim.Spec.ProviderID != providerID {
		return nil, ErrClaimConflict
	}
	fingerprint, err := Fingerprint(targetName, endpoint)
	if err != nil {
		return nil, err
	}
	updated := claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	updated.Spec.Fingerprint = fingerprint
	updated, err = r.store.Update(ctx, updated)
	if err != nil {
		return nil, translateWriteError("rebind ownership fingerprint", err)
	}
	return r.transition(ctx, updated.Name, updated.ResourceVersion, v1alpha1.OwnershipPhaseConfirmed, providerRevision)
}

// RevalidateUnique verifies both the planned resourceVersion and the invariant
// that one logical record has exactly one deterministically named claim.
func (r *Repository) RevalidateUnique(ctx context.Context, name, expectedResourceVersion string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if expectedResourceVersion == "" || claim.ResourceVersion != expectedResourceVersion {
		return nil, ErrStaleClaim
	}
	claims, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	logical := claimsForRecord(claims, claim.Spec.TargetRef.Name, claim.Spec.Record)
	if len(logical) != 1 || logical[0].Name != claim.Name {
		r.markClaimsConflict(ctx, logical)
		return nil, ErrDuplicateClaim
	}
	return claim, nil
}

// Reserve creates the coordination record before a provider create. The
// operation is idempotent only for the same controller and exact fingerprint.
func (r *Repository) Reserve(ctx context.Context, request ReserveRequest) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	identity, fingerprint, err := requestIdentity(request.TargetName, request.Endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Namespace) == "" || strings.TrimSpace(request.ControllerID) == "" {
		return nil, fmt.Errorf("ownership namespace and controller ID are required")
	}
	name := ClaimName(identity)
	claims, err := r.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list claims before reserve: %w", err)
	}
	logical := claimsForRecord(claims, identity.TargetName, RecordKey(identity))
	if len(logical) > 1 || (len(logical) == 1 && logical[0].Name != name) {
		r.markClaimsConflict(ctx, logical)
		return nil, fmt.Errorf("%w for target %q record %q", ErrDuplicateClaim, identity.TargetName, identity.DNSName)
	}
	if len(logical) == 1 {
		claim := logical[0].DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
		if !claimSpecMatches(claim, identity, fingerprint, request.ControllerID) {
			_, _ = r.transition(ctx, claim.Name, claim.ResourceVersion, v1alpha1.OwnershipPhaseConflict, "")
			return nil, fmt.Errorf("%w: existing claim differs from requested reservation", ErrClaimConflict)
		}
		if !containsString(claim.Finalizers, ClaimFinalizer) {
			claim.Finalizers = append(claim.Finalizers, ClaimFinalizer)
			updated, updateErr := r.store.Update(ctx, claim)
			if updateErr != nil {
				return nil, translateWriteError("add ownership finalizer", updateErr)
			}
			claim = updated
		}
		switch claim.Status.Phase {
		case v1alpha1.OwnershipPhaseReserved, v1alpha1.OwnershipPhaseConfirmed:
			return claim, nil
		case v1alpha1.OwnershipPhaseOrphaned:
			return nil, fmt.Errorf("%w: orphaned claim must be re-confirmed", ErrClaimNotConfirmed)
		default:
			return nil, fmt.Errorf("%w: phase %q", ErrClaimConflict, claim.Status.Phase)
		}
	}

	claim := &v1alpha1.FortiGateDNSRecordOwnership{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSRecordOwnership"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  request.Namespace,
			Name:       name,
			Finalizers: []string{ClaimFinalizer},
		},
		Spec: v1alpha1.FortiGateDNSRecordOwnershipSpec{
			TargetRef:    localTargetReference(identity.TargetName),
			Record:       RecordKey(identity),
			Fingerprint:  fingerprint,
			Sources:      append([]v1alpha1.SourceObjectReference(nil), request.Sources...),
			ControllerID: strings.TrimSpace(request.ControllerID),
		},
	}
	created, err := r.store.Create(ctx, claim)
	if err != nil {
		return nil, translateWriteError("reserve ownership claim", err)
	}
	reserved, err := r.transition(ctx, created.Name, created.ResourceVersion, v1alpha1.OwnershipPhaseReserved, "")
	if err != nil {
		return nil, err
	}
	return reserved, nil
}

// Confirm binds a reserved claim to the one provider record observed by a
// stable relist. Spec and status updates are separate optimistic writes; a race
// between them leaves the claim non-Confirmed and therefore non-destructive.
func (r *Repository) Confirm(ctx context.Context, name, expectedResourceVersion, providerID, providerRevision string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(providerRevision) == "" {
		return nil, fmt.Errorf("provider ID and stable revision are required to confirm ownership")
	}
	claim, err := r.RevalidateUnique(ctx, name, expectedResourceVersion)
	if err != nil {
		return nil, err
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseReserved && claim.Status.Phase != v1alpha1.OwnershipPhaseOrphaned && claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		return nil, fmt.Errorf("%w: phase %q cannot be confirmed", ErrClaimConflict, claim.Status.Phase)
	}
	if claim.Spec.ProviderID != "" && claim.Spec.ProviderID != providerID {
		_, _ = r.transition(ctx, claim.Name, claim.ResourceVersion, v1alpha1.OwnershipPhaseConflict, providerRevision)
		return nil, fmt.Errorf("%w: provider ID changed", ErrClaimConflict)
	}
	if claim.Spec.ProviderID != providerID {
		updated := claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
		updated.Spec.ProviderID = providerID
		claim, err = r.store.Update(ctx, updated)
		if err != nil {
			return nil, translateWriteError("bind ownership provider ID", err)
		}
	}
	return r.transition(ctx, claim.Name, claim.ResourceVersion, v1alpha1.OwnershipPhaseConfirmed, providerRevision)
}

type AdoptionRequest struct {
	ReserveRequest
	ProviderID          string
	ProviderRevision    string
	PlanHash            string
	ApprovedPlanHash    string
	ExpectedFingerprint string
}

// Adopt creates a Confirmed claim without mutating the provider. The caller
// must have revalidated the approved plan and stable provider snapshot first.
func (r *Repository) Adopt(ctx context.Context, request AdoptionRequest) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	identity, fingerprint, err := requestIdentity(request.TargetName, request.Endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Namespace) == "" || strings.TrimSpace(request.ControllerID) == "" {
		return nil, fmt.Errorf("ownership namespace and controller ID are required")
	}
	if request.PlanHash == "" || request.PlanHash != request.ApprovedPlanHash {
		return nil, fmt.Errorf("adoption requires exact approved plan hash")
	}
	if request.ProviderRevision == "" || request.ProviderID == "" {
		return nil, fmt.Errorf("adoption requires stable provider revision and provider ID")
	}
	if request.ExpectedFingerprint == "" || request.ExpectedFingerprint != fingerprint {
		return nil, fmt.Errorf("adoption fingerprint changed")
	}
	claims, err := r.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list claims before adoption: %w", err)
	}
	logical := claimsForRecord(claims, identity.TargetName, RecordKey(identity))
	if len(logical) != 0 {
		if len(logical) > 1 {
			r.markClaimsConflict(ctx, logical)
			return nil, ErrDuplicateClaim
		}
		return nil, fmt.Errorf("%w: adoption candidate is already claimed", ErrClaimConflict)
	}
	claim := &v1alpha1.FortiGateDNSRecordOwnership{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSRecordOwnership"},
		ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: ClaimName(identity), Finalizers: []string{ClaimFinalizer}},
		Spec: v1alpha1.FortiGateDNSRecordOwnershipSpec{
			TargetRef:         localTargetReference(identity.TargetName),
			ProviderID:        request.ProviderID,
			Record:            RecordKey(identity),
			Fingerprint:       fingerprint,
			Sources:           append([]v1alpha1.SourceObjectReference(nil), request.Sources...),
			ControllerID:      strings.TrimSpace(request.ControllerID),
			AdoptionRequested: true,
		},
	}
	created, err := r.store.Create(ctx, claim)
	if err != nil {
		return nil, translateWriteError("create adopted ownership claim", err)
	}
	return r.transition(ctx, created.Name, created.ResourceVersion, v1alpha1.OwnershipPhaseConfirmed, request.ProviderRevision)
}

func (r *Repository) MarkOrphaned(ctx context.Context, name, expectedResourceVersion, providerRevision string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	return r.transition(ctx, name, expectedResourceVersion, v1alpha1.OwnershipPhaseOrphaned, providerRevision)
}

func (r *Repository) MarkConflict(ctx context.Context, name, expectedResourceVersion, providerRevision string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	return r.transition(ctx, name, expectedResourceVersion, v1alpha1.OwnershipPhaseConflict, providerRevision)
}

// releaseFinalizer is called only by Manager after a stable provider audit
// proves that the bound row and exact fingerprint are absent.
func (r *Repository) releaseFinalizer(ctx context.Context, name, expectedResourceVersion string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim, err := r.RevalidateUnique(ctx, name, expectedResourceVersion)
	if err != nil {
		return nil, err
	}
	updated := claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	updated.Finalizers = removeString(updated.Finalizers, ClaimFinalizer)
	result, err := r.store.Update(ctx, updated)
	if err != nil {
		return nil, translateWriteError("release ownership finalizer", err)
	}
	return result, nil
}

func (r *Repository) transition(ctx context.Context, name, expectedResourceVersion string, phase v1alpha1.OwnershipPhase, providerRevision string) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	claim, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if expectedResourceVersion == "" || claim.ResourceVersion != expectedResourceVersion {
		return nil, ErrStaleClaim
	}
	updated := claim.DeepCopyObject().(*v1alpha1.FortiGateDNSRecordOwnership)
	updated.Status.Phase = phase
	updated.Status.ObservedProviderRevision = strings.TrimSpace(providerRevision)
	if phase == v1alpha1.OwnershipPhaseConfirmed {
		now := metav1.NewTime(r.now().UTC())
		updated.Status.LastConfirmedTime = &now
	}
	result, err := r.store.UpdateStatus(ctx, updated)
	if err != nil {
		return nil, translateWriteError("update ownership phase", err)
	}
	return result, nil
}

func (r *Repository) markClaimsConflict(ctx context.Context, claims []v1alpha1.FortiGateDNSRecordOwnership) {
	for index := range claims {
		claim := &claims[index]
		_, _ = r.transition(ctx, claim.Name, claim.ResourceVersion, v1alpha1.OwnershipPhaseConflict, claim.Status.ObservedProviderRevision)
	}
}

func claimsForRecord(claims []v1alpha1.FortiGateDNSRecordOwnership, targetName string, record v1alpha1.DNSRecordKey) []v1alpha1.FortiGateDNSRecordOwnership {
	result := make([]v1alpha1.FortiGateDNSRecordOwnership, 0, 1)
	for index := range claims {
		claim := claims[index]
		if strings.EqualFold(strings.TrimSpace(claim.Spec.TargetRef.Name), targetName) && claim.Spec.Record == record {
			result = append(result, claim)
		}
	}
	return result
}

func claimSpecMatches(claim *v1alpha1.FortiGateDNSRecordOwnership, identity Identity, fingerprint, controllerID string) bool {
	return claim != nil &&
		strings.EqualFold(strings.TrimSpace(claim.Spec.TargetRef.Name), identity.TargetName) &&
		claim.Spec.Record == RecordKey(identity) &&
		claim.Spec.Fingerprint == fingerprint &&
		claim.Spec.ControllerID == strings.TrimSpace(controllerID)
}

func requestIdentity(targetName string, endpoint dns.Endpoint) (Identity, string, error) {
	identity, err := IdentityFor(targetName, endpoint)
	if err != nil {
		return Identity{}, "", err
	}
	fingerprint, err := Fingerprint(targetName, endpoint)
	if err != nil {
		return Identity{}, "", err
	}
	return identity, fingerprint, nil
}

func localTargetReference(name string) corev1.LocalObjectReference {
	return corev1.LocalObjectReference{Name: name}
}

func translateWriteError(action string, err error) error {
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%s: %w", action, ErrStaleClaim)
	}
	if apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("%s: %w", action, ErrClaimConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func removeString(values []string, unwanted string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}
