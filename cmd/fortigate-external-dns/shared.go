package main

import (
	"context"
	"errors"
	"fmt"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/ownership"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	"github.com/kgskr/fortigate-external-dns/internal/target"
)

type sharedOwnershipHandles struct {
	manager    *ownership.Manager
	repository *ownership.Repository
}

type revisionedTargetClient interface {
	ListRecordsWithRevision(context.Context) ([]dns.Endpoint, string, error)
}

type sharedDNSClient struct {
	client     target.ProviderClient
	handles    *sharedOwnershipHandles
	namespace  string
	targetName string
	controller string
}

func (c *sharedDNSClient) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	records, err := c.client.ListRecords(ctx)
	if err != nil {
		return nil, err
	}
	return c.bindConfirmedOwnership(ctx, records)
}

func (c *sharedDNSClient) ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error) {
	revisioned, ok := c.client.(revisionedTargetClient)
	if !ok {
		return nil, "", fmt.Errorf("shared ownership requires revisioned provider snapshots")
	}
	records, revision, err := revisioned.ListRecordsWithRevision(ctx)
	if err != nil {
		return nil, "", err
	}
	records, err = c.bindConfirmedOwnership(ctx, records)
	return records, revision, err
}

func (c *sharedDNSClient) bindConfirmedOwnership(ctx context.Context, records []dns.Endpoint) ([]dns.Endpoint, error) {
	result := append([]dns.Endpoint(nil), records...)
	for i := range result {
		identity, err := ownership.IdentityFor(c.targetName, result[i])
		if err != nil {
			continue
		}
		claim, err := c.handles.repository.Get(ctx, ownership.ClaimName(identity))
		if errors.Is(err, ownership.ErrClaimNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if claim.Status.Phase == v1alpha1.OwnershipPhaseConfirmed && claim.Spec.ControllerID == c.controller && ownership.ClaimMatches(claim, c.targetName, result[i]) {
			result[i].OwnerID = c.controller
			if len(claim.Spec.Sources) > 0 {
				sourceRef := claim.Spec.Sources[0]
				result[i].Source = dns.SourceRef{Kind: sourceRef.Kind, Namespace: sourceRef.Namespace, Name: sourceRef.Name}
			}
		}
	}
	return result, nil
}

func (c *sharedDNSClient) PlanOwnershipPreconditions(ctx context.Context, operations []plan.Operation, providerRevision string) ([]plan.OwnershipPrecondition, error) {
	if providerRevision == "" {
		return nil, ownership.ErrProviderSnapshot
	}
	seen := map[string]struct{}{}
	result := make([]plan.OwnershipPrecondition, 0, len(operations))
	for _, operation := range operations {
		if operation.Type == plan.OperationCreate || operation.Type == plan.OperationConflict {
			continue
		}
		identity, err := ownership.IdentityFor(c.targetName, operation.Current)
		if err != nil {
			return nil, err
		}
		name := ownership.ClaimName(identity)
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		claim, err := c.handles.repository.Get(ctx, name)
		if err != nil {
			return nil, err
		}
		precondition := ownership.PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, providerRevision)
		result = append(result, precondition.Ownership)
		seen[name] = struct{}{}
	}
	return result, nil
}

func (c *sharedDNSClient) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	if dryRun {
		// Delegate only for bounded operation logging/metrics. The provider client
		// performs no writes and this wrapper creates no ownership resources.
		return c.client.Apply(ctx, operations, true)
	}
	provider := sharedProvider{client: c.client}
	var errs []error
	failedGroups := map[string]struct{}{}
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if operation.Type == plan.OperationConflict {
			continue
		}
		if operation.Type == plan.OperationDelete || operation.Type == plan.OperationDeactivate {
			if _, blocked := failedGroups[operation.Current.MutationGroupKey()]; blocked {
				continue
			}
		}
		if err := c.applyOne(ctx, provider, operation); err != nil {
			errs = append(errs, err)
			if operation.Type == plan.OperationCreate || operation.Type == plan.OperationReplace {
				failedGroups[operation.Desired.MutationGroupKey()] = struct{}{}
			}
		}
	}
	return errors.Join(errs...)
}

func (c *sharedDNSClient) applyOne(ctx context.Context, provider sharedProvider, operation plan.Operation) error {
	switch operation.Type {
	case plan.OperationCreate:
		_, err := c.handles.manager.ReconcileCreate(ctx, provider, ownership.CreateRequest{ReserveRequest: ownership.ReserveRequest{
			Namespace: c.namespace, TargetName: c.targetName, ControllerID: c.controller, Endpoint: operation.Desired,
			Sources: []v1alpha1.SourceObjectReference{{Kind: operation.Desired.Source.Kind, Namespace: operation.Desired.Source.Namespace, Name: operation.Desired.Source.Name}},
		}})
		return err
	case plan.OperationReplace:
		// A target/type change has a different claim identity. It needs an explicit
		// adoption/replacement plan so the old and new claims cannot both bind one
		// provider ID. Fail closed until that approved transition is represented.
		return fmt.Errorf("shared ownership replacement requires an explicit adoption plan")
	case plan.OperationUpdate, plan.OperationDeactivate, plan.OperationDelete:
		return c.applyExisting(ctx, provider, operation)
	default:
		return nil
	}
}

func (c *sharedDNSClient) applyExisting(ctx context.Context, provider sharedProvider, operation plan.Operation) error {
	identity, err := ownership.IdentityFor(c.targetName, operation.Current)
	if err != nil {
		return err
	}
	claim, err := c.handles.repository.Get(ctx, ownership.ClaimName(identity))
	if err != nil {
		return err
	}
	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return err
	}
	precondition := ownership.PreconditionForClaim(v1alpha1.OwnershipModeShared, claim, snapshot.Revision)
	if err := c.handles.manager.ExecuteMutation(ctx, provider, operation.Type, precondition, func(ctx context.Context) error {
		return c.client.Apply(ctx, []plan.Operation{operation}, false)
	}); err != nil {
		return err
	}
	after, err := provider.Snapshot(ctx)
	if err != nil {
		return err
	}
	if operation.Type == plan.OperationDelete {
		_, err = c.handles.manager.ReconcileClaim(ctx, claim.Name, claim.ResourceVersion, c.targetName, after)
		return err
	}
	observed, err := exactProviderRecord(after.Records, operation.Current.ProviderID, c.targetName, operation.Desired)
	if err != nil {
		return err
	}
	_, err = c.handles.repository.RebindConfirmed(ctx, claim.Name, claim.ResourceVersion, c.targetName, observed, operation.Current.ProviderID, after.Revision)
	return err
}

func exactProviderRecord(records []dns.Endpoint, providerID, targetName string, desired dns.Endpoint) (dns.Endpoint, error) {
	wanted, err := ownership.Fingerprint(targetName, desired)
	if err != nil {
		return dns.Endpoint{}, err
	}
	var matches []dns.Endpoint
	for _, record := range records {
		if record.ProviderID != providerID {
			continue
		}
		fingerprint, fingerprintErr := ownership.Fingerprint(targetName, record)
		if fingerprintErr == nil && fingerprint == wanted {
			matches = append(matches, record)
		}
	}
	if len(matches) != 1 {
		return dns.Endpoint{}, ownership.ErrProviderConflict
	}
	return matches[0], nil
}

type sharedProvider struct {
	client target.ProviderClient
}

func (p sharedProvider) Snapshot(ctx context.Context) (ownership.Snapshot, error) {
	revisioned, ok := p.client.(revisionedTargetClient)
	if !ok {
		return ownership.Snapshot{}, ownership.ErrProviderSnapshot
	}
	records, revision, err := revisioned.ListRecordsWithRevision(ctx)
	if err != nil {
		return ownership.Snapshot{}, err
	}
	return ownership.Snapshot{Revision: revision, Stable: revision != "", Records: records}, nil
}

func (p sharedProvider) Create(ctx context.Context, endpoint dns.Endpoint) error {
	return p.client.Apply(ctx, []plan.Operation{{Type: plan.OperationCreate, Desired: endpoint}}, false)
}
