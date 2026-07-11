package ownership

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

var (
	ErrProviderSnapshot = errors.New("provider snapshot is not stable and complete")
	ErrProviderDrift    = errors.New("provider snapshot revision changed")
	ErrProviderConflict = errors.New("provider record conflicts with ownership claim")
	ErrApprovalRequired = errors.New("exact adoption plan approval is required")
)

// Snapshot is the stable provider evidence used by shared-zone ownership. A
// non-empty Revision and Stable=true are mandatory for confirmation, adoption,
// and existing-record mutation.
type Snapshot struct {
	Revision string
	Stable   bool
	Records  []dns.Endpoint
}

type Provider interface {
	Snapshot(context.Context) (Snapshot, error)
	Create(context.Context, dns.Endpoint) error
}

type Manager struct {
	repository *Repository
}

func NewManager(repository *Repository) (*Manager, error) {
	if repository == nil {
		return nil, fmt.Errorf("ownership repository is required")
	}
	return &Manager{repository: repository}, nil
}

type CreateRequest struct {
	ReserveRequest
}

// ReconcileCreate implements the two-phase shared-zone create protocol. It
// always reserves first. Before issuing POST it relists, allowing a prior lost
// response to converge by confirming the exact row instead of creating a
// duplicate. A successful POST is also confirmed only after a stable relist.
func (m *Manager) ReconcileCreate(ctx context.Context, provider Provider, request CreateRequest) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	if provider == nil {
		return nil, fmt.Errorf("ownership provider is required")
	}
	claim, err := m.repository.Reserve(ctx, request.ReserveRequest)
	if err != nil {
		return nil, err
	}

	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("list provider before create: %w", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	if confirmed, handled, inspectErr := m.confirmObserved(ctx, claim, request.TargetName, request.Endpoint, snapshot); handled {
		return confirmed, inspectErr
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseReserved {
		return nil, fmt.Errorf("%w: phase %q cannot authorize create", ErrClaimConflict, claim.Status.Phase)
	}

	createErr := provider.Create(ctx, request.Endpoint.Normalize())
	after, listErr := provider.Snapshot(ctx)
	if listErr != nil {
		if createErr != nil {
			return nil, fmt.Errorf("create response lost and provider relist failed: %w", createErr)
		}
		return nil, fmt.Errorf("relist provider after create: %w", listErr)
	}
	if err := validateSnapshot(after); err != nil {
		if createErr != nil {
			return nil, fmt.Errorf("create response lost and provider relist is unsafe: %w", createErr)
		}
		return nil, err
	}
	// Refresh the claim because another controller may have changed it while the
	// provider request was in flight.
	currentClaim, getErr := m.repository.Get(ctx, claim.Name)
	if getErr != nil {
		return nil, getErr
	}
	confirmed, handled, inspectErr := m.confirmObserved(ctx, currentClaim, request.TargetName, request.Endpoint, after)
	if handled {
		return confirmed, inspectErr
	}
	if createErr != nil {
		// Reserved is intentionally retained: the next full audit relists before
		// any retry and may converge a committed-but-not-yet-visible POST.
		return nil, fmt.Errorf("provider create did not produce an observable exact row: %w", createErr)
	}
	return nil, fmt.Errorf("provider create succeeded but exact row was not observable; claim remains Reserved")
}

func (m *Manager) confirmObserved(ctx context.Context, claim *v1alpha1.FortiGateDNSRecordOwnership, targetName string, endpoint dns.Endpoint, snapshot Snapshot) (*v1alpha1.FortiGateDNSRecordOwnership, bool, error) {
	exact, divergent, reused, err := inspectRecord(snapshot.Records, targetName, endpoint, "")
	if err != nil {
		return nil, true, err
	}
	if reused || len(exact) > 1 || len(divergent) > 0 {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return nil, true, ErrProviderConflict
	}
	if len(exact) == 0 {
		if claim.Status.Phase == v1alpha1.OwnershipPhaseConfirmed {
			_, _ = m.repository.MarkOrphaned(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
			return nil, true, fmt.Errorf("%w: confirmed provider row disappeared", ErrClaimNotDestructive)
		}
		return nil, false, nil
	}
	if strings.TrimSpace(exact[0].ProviderID) == "" {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return nil, true, fmt.Errorf("%w: exact provider row has no ID", ErrProviderConflict)
	}
	if len(providerRecordsByID(snapshot.Records, exact[0].ProviderID)) != 1 {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return nil, true, fmt.Errorf("%w: provider ID is reused", ErrProviderConflict)
	}
	confirmed, err := m.repository.Confirm(ctx, claim.Name, claim.ResourceVersion, exact[0].ProviderID, snapshot.Revision)
	return confirmed, true, err
}

type MutationPrecondition struct {
	Mode             v1alpha1.OwnershipMode
	TargetName       string
	ControllerID     string
	ProviderRevision string
	Ownership        plan.OwnershipPrecondition
}

func PreconditionForClaim(mode v1alpha1.OwnershipMode, claim *v1alpha1.FortiGateDNSRecordOwnership, providerRevision string) MutationPrecondition {
	precondition := MutationPrecondition{Mode: mode, ProviderRevision: providerRevision}
	if claim == nil {
		return precondition
	}
	precondition.TargetName = claim.Spec.TargetRef.Name
	precondition.ControllerID = claim.Spec.ControllerID
	precondition.Ownership = plan.OwnershipPrecondition{
		Namespace:       claim.Namespace,
		Name:            claim.Name,
		UID:             string(claim.UID),
		ResourceVersion: claim.ResourceVersion,
		ProviderID:      claim.Spec.ProviderID,
		Fingerprint:     claim.Spec.Fingerprint,
		Phase:           string(claim.Status.Phase),
	}
	return precondition
}

// AuthorizeMutation performs the exact Confirmed-claim and live-record checks
// for update, replace, deactivate, and delete. Exclusive mode deliberately
// bypasses the repository to preserve the existing behavior.
func (m *Manager) AuthorizeMutation(ctx context.Context, operationType string, precondition MutationPrecondition, snapshot Snapshot) error {
	if precondition.Mode == v1alpha1.OwnershipModeExclusive {
		return nil
	}
	if precondition.Mode != v1alpha1.OwnershipModeShared {
		return fmt.Errorf("unknown ownership mode %q", precondition.Mode)
	}
	if !existingRecordMutation(operationType) {
		return fmt.Errorf("operation %q is not an existing-record mutation", operationType)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	if precondition.ProviderRevision == "" || snapshot.Revision != precondition.ProviderRevision {
		return ErrProviderDrift
	}
	if precondition.Ownership.Phase != string(v1alpha1.OwnershipPhaseConfirmed) {
		return fmt.Errorf("%w: planned phase %q", ErrClaimNotDestructive, precondition.Ownership.Phase)
	}
	claim, err := m.repository.RevalidateUnique(ctx, precondition.Ownership.Name, precondition.Ownership.ResourceVersion)
	if err != nil {
		return err
	}
	if claim.Status.Phase != v1alpha1.OwnershipPhaseConfirmed {
		return fmt.Errorf("%w: phase %q", ErrClaimNotDestructive, claim.Status.Phase)
	}
	if claim.Spec.TargetRef.Name != strings.TrimSpace(precondition.TargetName) ||
		claim.Spec.ControllerID != strings.TrimSpace(precondition.ControllerID) ||
		claim.Spec.ProviderID == "" || claim.Spec.ProviderID != precondition.Ownership.ProviderID ||
		claim.Spec.Fingerprint == "" || claim.Spec.Fingerprint != precondition.Ownership.Fingerprint ||
		(precondition.Ownership.Namespace != "" && claim.Namespace != precondition.Ownership.Namespace) ||
		(precondition.Ownership.UID != "" && string(claim.UID) != precondition.Ownership.UID) {
		return ErrClaimConflict
	}

	recordsByID := providerRecordsByID(snapshot.Records, precondition.Ownership.ProviderID)
	if len(recordsByID) != 1 {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return fmt.Errorf("%w: provider ID is missing or reused", ErrProviderConflict)
	}
	liveFingerprint, fingerprintErr := Fingerprint(precondition.TargetName, recordsByID[0])
	if fingerprintErr != nil || liveFingerprint != precondition.Ownership.Fingerprint {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return fmt.Errorf("%w: live fingerprint differs", ErrProviderConflict)
	}
	identity, identityErr := IdentityFor(precondition.TargetName, recordsByID[0])
	if identityErr != nil || claim.Name != ClaimName(identity) || claim.Spec.Record != RecordKey(identity) {
		_, _ = m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		return fmt.Errorf("%w: provider ID was reused for another logical record", ErrProviderConflict)
	}
	return nil
}

// ExecuteMutation acquires the provider snapshot and revalidates the claim in
// the same call immediately before invoking the provider request callback.
func (m *Manager) ExecuteMutation(ctx context.Context, provider Provider, operationType string, precondition MutationPrecondition, mutate func(context.Context) error) error {
	if provider == nil || mutate == nil {
		return fmt.Errorf("provider and mutation callback are required")
	}
	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("relist provider before mutation: %w", err)
	}
	if err := m.AuthorizeMutation(ctx, operationType, precondition, snapshot); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return mutate(ctx)
}

type AdoptRequest struct {
	AdoptionRequest
}

// Adopt revalidates exact approval, provider revision, fingerprint, provider
// ID uniqueness, and unclaimed state immediately before creating a Confirmed
// adoption claim. It never changes the provider record.
func (m *Manager) Adopt(ctx context.Context, provider Provider, request AdoptRequest) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	if provider == nil {
		return nil, fmt.Errorf("ownership provider is required")
	}
	if request.PlanHash == "" || request.PlanHash != request.ApprovedPlanHash {
		return nil, ErrApprovalRequired
	}
	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("relist provider before adoption: %w", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	if request.ProviderRevision == "" || snapshot.Revision != request.ProviderRevision {
		return nil, ErrProviderDrift
	}
	exact, divergent, reused, inspectErr := inspectRecord(snapshot.Records, request.TargetName, request.Endpoint, request.ProviderID)
	if inspectErr != nil {
		return nil, inspectErr
	}
	if reused || len(exact) != 1 || len(divergent) != 0 || exact[0].ProviderID != request.ProviderID {
		return nil, ErrProviderConflict
	}
	return m.repository.Adopt(ctx, request.AdoptionRequest)
}

// ReconcileClaim converts Reserved lost-response evidence to Confirmed,
// recovers an Orphaned claim only from the same exact provider row, and marks
// missing/divergent/duplicate provider evidence fail-closed.
func (m *Manager) ReconcileClaim(ctx context.Context, claimName, expectedResourceVersion, targetName string, snapshot Snapshot) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	claim, err := m.repository.RevalidateUnique(ctx, claimName, expectedResourceVersion)
	if err != nil {
		return nil, err
	}
	exact, divergent, reused, inspectErr := inspectClaimRecords(snapshot.Records, targetName, claim)
	if inspectErr != nil {
		return nil, inspectErr
	}
	if reused || len(exact) > 1 || len(divergent) > 0 {
		updated, updateErr := m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		if updateErr != nil {
			return nil, updateErr
		}
		return updated, ErrProviderConflict
	}
	if len(exact) == 0 {
		if claim.Status.Phase == v1alpha1.OwnershipPhaseReserved {
			return claim, nil
		}
		return m.repository.MarkOrphaned(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
	}
	if exact[0].ProviderID == "" {
		updated, updateErr := m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		if updateErr != nil {
			return nil, updateErr
		}
		return updated, ErrProviderConflict
	}
	if claim.Spec.ProviderID != "" && exact[0].ProviderID != claim.Spec.ProviderID {
		updated, updateErr := m.repository.MarkConflict(ctx, claim.Name, claim.ResourceVersion, snapshot.Revision)
		if updateErr != nil {
			return nil, updateErr
		}
		return updated, ErrProviderConflict
	}
	return m.repository.Confirm(ctx, claim.Name, claim.ResourceVersion, exact[0].ProviderID, snapshot.Revision)
}

// ReleaseClaimFinalizer permits claim deletion only after a stable provider
// snapshot proves both the bound provider ID and the exact claimed fingerprint
// are absent. Claim deletion never triggers provider deletion.
func (m *Manager) ReleaseClaimFinalizer(ctx context.Context, claimName, expectedResourceVersion, targetName string, snapshot Snapshot) (*v1alpha1.FortiGateDNSRecordOwnership, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	claim, err := m.repository.RevalidateUnique(ctx, claimName, expectedResourceVersion)
	if err != nil {
		return nil, err
	}
	if claim.Spec.ProviderID != "" && len(providerRecordsByID(snapshot.Records, claim.Spec.ProviderID)) != 0 {
		return nil, fmt.Errorf("%w: bound provider row still exists", ErrClaimNotDestructive)
	}
	exact, _, _, inspectErr := inspectClaimRecords(snapshot.Records, targetName, claim)
	if inspectErr != nil {
		return nil, inspectErr
	}
	if len(exact) != 0 {
		return nil, fmt.Errorf("%w: exact claimed provider row still exists", ErrClaimNotDestructive)
	}
	return m.repository.releaseFinalizer(ctx, claim.Name, claim.ResourceVersion)
}

func validateSnapshot(snapshot Snapshot) error {
	if !snapshot.Stable || strings.TrimSpace(snapshot.Revision) == "" {
		return ErrProviderSnapshot
	}
	return nil
}

func inspectRecord(records []dns.Endpoint, targetName string, desired dns.Endpoint, providerID string) (exact, divergent []dns.Endpoint, providerIDReused bool, err error) {
	desiredIdentity, err := IdentityFor(targetName, desired)
	if err != nil {
		return nil, nil, false, err
	}
	desiredFingerprint, err := Fingerprint(targetName, desired)
	if err != nil {
		return nil, nil, false, err
	}
	for _, record := range records {
		if providerID != "" && record.ProviderID == providerID {
			providerMatches := providerRecordsByID(records, providerID)
			providerIDReused = len(providerMatches) != 1
		}
		identity, identityErr := IdentityFor(targetName, record)
		if identityErr != nil || identity != desiredIdentity {
			continue
		}
		fingerprint, fingerprintErr := Fingerprint(targetName, record)
		if fingerprintErr != nil {
			return nil, nil, false, fingerprintErr
		}
		if fingerprint == desiredFingerprint {
			exact = append(exact, record)
		} else {
			divergent = append(divergent, record)
		}
	}
	return exact, divergent, providerIDReused, nil
}

func inspectClaimRecords(records []dns.Endpoint, targetName string, claim *v1alpha1.FortiGateDNSRecordOwnership) (exact, divergent []dns.Endpoint, providerIDReused bool, err error) {
	for _, record := range records {
		if claim.Spec.ProviderID != "" && record.ProviderID == claim.Spec.ProviderID {
			providerIDReused = len(providerRecordsByID(records, claim.Spec.ProviderID)) != 1
		}
		identity, identityErr := IdentityFor(targetName, record)
		if identityErr != nil || RecordKey(identity) != claim.Spec.Record {
			continue
		}
		fingerprint, fingerprintErr := Fingerprint(targetName, record)
		if fingerprintErr != nil {
			return nil, nil, false, fingerprintErr
		}
		if fingerprint == claim.Spec.Fingerprint {
			exact = append(exact, record)
		} else {
			divergent = append(divergent, record)
		}
	}
	return exact, divergent, providerIDReused, nil
}

func providerRecordsByID(records []dns.Endpoint, providerID string) []dns.Endpoint {
	result := make([]dns.Endpoint, 0, 1)
	for _, record := range records {
		if strings.TrimSpace(record.ProviderID) == strings.TrimSpace(providerID) {
			result = append(result, record)
		}
	}
	return result
}

func existingRecordMutation(operationType string) bool {
	switch operationType {
	case plan.OperationUpdate, plan.OperationReplace, plan.OperationDeactivate, plan.OperationDelete:
		return true
	default:
		return false
	}
}
