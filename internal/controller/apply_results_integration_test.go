package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

func TestDryRunProviderResultsPreserveConflictAndFailure(t *testing.T) {
	operation := plan.Operation{Type: plan.OperationCreate, Desired: dns.Endpoint{DNSName: "web.example.com", RecordType: dns.RecordA, Targets: []string{"192.0.2.1"}, Zone: "example.com"}}
	conflict := operation
	conflict.Type = plan.OperationConflict
	providerErr := errors.New("dry-run logging failed")
	for _, tc := range []struct {
		name      string
		operation plan.Operation
		outcome   plan.ApplyOutcome
		reason    string
		err       error
	}{
		{name: "conflict", operation: conflict, outcome: plan.ApplyBlocked, reason: "planning-conflict"},
		{name: "provider-error", operation: operation, outcome: plan.ApplyFailed, reason: "provider-request-failed", err: providerErr},
		{name: "canceled-before-apply", operation: operation, outcome: plan.ApplyBlocked, reason: "context-canceled", err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mixedResultDNSClient{err: tc.err, outcomes: []plan.OperationOutcome{{OperationID: plan.SanitizeOperation(tc.operation).ID, Result: tc.outcome, Reason: tc.reason}}}
			runner := Runner{Config: config.Config{DryRun: true}, DNSClient: client}
			err := runner.ApplyPrepared(context.Background(), ReconcileAudit{Operations: []plan.Operation{tc.operation}})
			if !errors.Is(err, tc.err) {
				t.Fatalf("error=%v want %v", err, tc.err)
			}
			if err != nil && strings.Contains(err.Error(), "invalid operation results") {
				t.Fatalf("valid non-applied outcome was rejected: %v", err)
			}
		})
	}
}
