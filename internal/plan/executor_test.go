package plan

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestExecutorPreservesIndependentProgressAndBlocksDependants(t *testing.T) {
	create := SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("create.example.com", "203.0.113.10"), Reason: "record missing"})
	cleanup := SanitizeOperation(Operation{Type: OperationDelete, Current: testEndpoint("old.example.com", "203.0.113.11"), Reason: "record stale"})
	independent := SanitizeOperation(Operation{Type: OperationUpdate, Desired: testEndpoint("api.example.com", "203.0.113.12"), Current: testEndpoint("api.example.com", "203.0.113.13"), Reason: "record differs"})
	document := testDocument([]SanitizedOperation{cleanup, independent, create})
	document.Prerequisites = []PrerequisiteEdge{{OperationID: cleanup.ID, RequiresOperationID: create.ID}}
	applier := &recordingOperationApplier{fail: map[string]error{create.ID: errors.New("provider failed")}}
	outcomes, err := (Executor{Revalidator: fixedRevalidator{}, Applier: applier}).Execute(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	want := []OperationOutcome{
		{OperationID: create.ID, Result: ApplyFailed, Reason: "provider-request-failed"},
		{OperationID: cleanup.ID, Result: ApplyBlocked, Reason: "prerequisite-failed"},
		{OperationID: independent.ID, Result: ApplySucceeded},
	}
	if !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("outcomes = %#v, want %#v", outcomes, want)
	}
	if !reflect.DeepEqual(applier.applied, []string{create.ID, independent.ID}) {
		t.Fatalf("provider requests = %#v", applier.applied)
	}
}

func TestExecutorRevalidatesImmediatelyBeforeEveryMutation(t *testing.T) {
	first := SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("a.example.com", "203.0.113.10"), Reason: "record missing"})
	second := SanitizeOperation(Operation{Type: OperationCreate, Desired: testEndpoint("b.example.com", "203.0.113.11"), Reason: "record missing"})
	document := testDocument([]SanitizedOperation{first, second})
	revalidator := &driftingRevalidator{driftOperation: second.ID}
	applier := &recordingOperationApplier{}
	outcomes, err := (Executor{Revalidator: revalidator, Applier: applier}).Execute(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if revalidator.initial != 1 || revalidator.perOperation != 2 {
		t.Fatalf("revalidation counts = initial %d, per-operation %d", revalidator.initial, revalidator.perOperation)
	}
	if len(applier.applied) != 1 || applier.applied[0] != first.ID {
		t.Fatalf("provider requests after drift = %#v", applier.applied)
	}
	if outcomes[1] != (OperationOutcome{OperationID: second.ID, Result: ApplyFailed, Reason: "precondition-drift"}) {
		t.Fatalf("drift outcome = %#v", outcomes[1])
	}
}

func TestSnapshotRevalidatorRejectsProviderPolicyAndOwnershipDrift(t *testing.T) {
	document := testDocument(nil)
	document.Preconditions.Policy = PolicyPrecondition{Generation: 7, Complete: true, Resources: []PolicyResourcePrecondition{{Namespace: "apps", Name: "policy", ResourceVersion: "4"}}}
	document.Preconditions.Ownership = []OwnershipPrecondition{{Namespace: "system", Name: "claim", ResourceVersion: "8", Fingerprint: "fingerprint", Phase: "Confirmed"}}
	tests := map[string]func(*Preconditions){
		"provider revision": func(value *Preconditions) { value.Provider.Revision = "rev-2" },
		"policy resource":   func(value *Preconditions) { value.Policy.Resources[0].ResourceVersion = "5" },
		"ownership claim":   func(value *Preconditions) { value.Ownership[0].ResourceVersion = "9" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			current := document.Preconditions
			current.Policy.Resources = append([]PolicyResourcePrecondition(nil), document.Preconditions.Policy.Resources...)
			current.Ownership = append([]OwnershipPrecondition(nil), document.Preconditions.Ownership...)
			mutate(&current)
			validator := SnapshotRevalidator{Reader: staticStateReader{target: document.Target, preconditions: current}}
			if err := validator.Revalidate(context.Background(), document, nil); !errors.Is(err, ErrPreconditionDrift) {
				t.Fatalf("drift error = %v", err)
			}
		})
	}
}

func testDocument(operations []SanitizedOperation) Document {
	return Document{
		APIVersion: DocumentAPIVersion, Kind: DocumentKind,
		Target:        TargetIdentity{Name: "edge", Zone: "example.com"},
		Preconditions: Preconditions{Provider: ProviderPrecondition{Revision: "rev-1", Stable: true, Complete: true}},
		Operations:    operations,
	}
}

func testEndpoint(name, target string) dns.Endpoint {
	return dns.Endpoint{DNSName: name, RecordType: dns.RecordA, Targets: []string{target}, TTL: 300, Zone: "example.com"}
}

type fixedRevalidator struct{}

func (fixedRevalidator) Revalidate(context.Context, Document, *SanitizedOperation) error { return nil }

type driftingRevalidator struct {
	driftOperation string
	initial        int
	perOperation   int
}

func (r *driftingRevalidator) Revalidate(_ context.Context, _ Document, operation *SanitizedOperation) error {
	if operation == nil {
		r.initial++
		return nil
	}
	r.perOperation++
	if operation.ID == r.driftOperation {
		return ErrPreconditionDrift
	}
	return nil
}

type recordingOperationApplier struct {
	applied []string
	fail    map[string]error
}

type staticStateReader struct {
	target        TargetIdentity
	preconditions Preconditions
}

func (r staticStateReader) CurrentPlanState(context.Context, TargetIdentity) (TargetIdentity, Preconditions, error) {
	return r.target, r.preconditions, nil
}

func (a *recordingOperationApplier) ApplyOperation(_ context.Context, operation SanitizedOperation) error {
	a.applied = append(a.applied, operation.ID)
	return a.fail[operation.ID]
}
