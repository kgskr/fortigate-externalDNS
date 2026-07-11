package plan

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

var ErrPreconditionDrift = errors.New("plan precondition drifted")

type ApplyOutcome string

const (
	ApplySucceeded ApplyOutcome = "Succeeded"
	ApplyFailed    ApplyOutcome = "Failed"
	ApplyBlocked   ApplyOutcome = "Blocked"
)

type OperationOutcome struct {
	OperationID string
	Result      ApplyOutcome
	Reason      string
}

// Revalidator proves that the immutable plan is still valid. Implementations
// relist the provider and policy/ownership objects as necessary; operation is
// nil for the initial whole-plan validation and non-nil immediately before a
// provider request.
type Revalidator interface {
	Revalidate(context.Context, Document, *SanitizedOperation) error
}

type OperationApplier interface {
	ApplyOperation(context.Context, SanitizedOperation) error
}

type StateReader interface {
	CurrentPlanState(context.Context, TargetIdentity) (TargetIdentity, Preconditions, error)
}

// SnapshotRevalidator is the default exact plan-state validator. A reader may
// use caches for discovery/policy metadata but must obtain a stable provider
// snapshot before returning.
type SnapshotRevalidator struct {
	Reader StateReader
}

func (r SnapshotRevalidator) Revalidate(ctx context.Context, document Document, _ *SanitizedOperation) error {
	if r.Reader == nil {
		return fmt.Errorf("plan state reader is required")
	}
	target, preconditions, err := r.Reader.CurrentPlanState(ctx, document.Target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(canonicalTarget(target), canonicalTarget(document.Target)) {
		return fmt.Errorf("%w: target identity changed", ErrPreconditionDrift)
	}
	current := canonicalPreconditions(preconditions)
	planned := canonicalPreconditions(document.Preconditions)
	if !reflect.DeepEqual(current, planned) {
		return fmt.Errorf("%w: provider, discovery, policy, or ownership state changed", ErrPreconditionDrift)
	}
	return nil
}

type Executor struct {
	Revalidator Revalidator
	Applier     OperationApplier
}

// Execute consumes the reviewed document itself, validates it at the start and
// immediately before each mutation, and preserves progress for independent
// operations. Failed prerequisites block their dependants without preventing
// unrelated operations from converging.
func (e Executor) Execute(ctx context.Context, document Document) ([]OperationOutcome, error) {
	if e.Revalidator == nil || e.Applier == nil {
		return nil, fmt.Errorf("plan revalidator and operation applier are required")
	}
	canonical := document.canonicalCopy()
	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf("validate plan document: %w", err)
	}
	if err := e.Revalidator.Revalidate(ctx, canonical, nil); err != nil {
		return nil, fmt.Errorf("initial plan revalidation: %w", err)
	}

	dependencies := make(map[string][]string, len(canonical.Operations))
	for _, edge := range canonical.Prerequisites {
		dependencies[edge.OperationID] = append(dependencies[edge.OperationID], edge.RequiresOperationID)
	}
	operations := append([]SanitizedOperation(nil), canonical.Operations...)
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })

	results := make(map[string]ApplyOutcome, len(operations))
	outcomes := make([]OperationOutcome, 0, len(operations))
	pending := operations
	for len(pending) > 0 {
		progress := false
		next := make([]SanitizedOperation, 0, len(pending))
		for i := range pending {
			operation := pending[i]
			ready, blocked := dependencyState(dependencies[operation.ID], results)
			if blocked {
				results[operation.ID] = ApplyBlocked
				outcomes = append(outcomes, OperationOutcome{OperationID: operation.ID, Result: ApplyBlocked, Reason: "prerequisite-failed"})
				progress = true
				continue
			}
			if !ready {
				next = append(next, operation)
				continue
			}
			if err := ctx.Err(); err != nil {
				return outcomes, err
			}
			if err := e.Revalidator.Revalidate(ctx, canonical, &operation); err != nil {
				results[operation.ID] = ApplyFailed
				outcomes = append(outcomes, OperationOutcome{OperationID: operation.ID, Result: ApplyFailed, Reason: "precondition-drift"})
				progress = true
				continue
			}
			if err := e.Applier.ApplyOperation(ctx, operation); err != nil {
				results[operation.ID] = ApplyFailed
				outcomes = append(outcomes, OperationOutcome{OperationID: operation.ID, Result: ApplyFailed, Reason: "provider-request-failed"})
				progress = true
				continue
			}
			results[operation.ID] = ApplySucceeded
			outcomes = append(outcomes, OperationOutcome{OperationID: operation.ID, Result: ApplySucceeded})
			progress = true
		}
		if !progress {
			return outcomes, fmt.Errorf("plan dependency graph made no progress")
		}
		pending = next
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].OperationID < outcomes[j].OperationID })
	return outcomes, nil
}

func dependencyState(dependencies []string, results map[string]ApplyOutcome) (ready, blocked bool) {
	for _, dependency := range dependencies {
		result, complete := results[dependency]
		if !complete {
			return false, false
		}
		if result != ApplySucceeded {
			return false, true
		}
	}
	return true, false
}
