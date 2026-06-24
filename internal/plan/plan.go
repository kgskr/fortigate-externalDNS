package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gilsu/fortigate-external-dns/internal/dns"
)

const (
	OperationCreate     = "create"
	OperationUpdate     = "update"
	OperationReplace    = "replace"
	OperationDelete     = "delete"
	OperationDeactivate = "deactivate"
	OperationConflict   = "conflict"
)

type CleanupPolicy string

const (
	CleanupDelete     CleanupPolicy = "delete"
	CleanupDeactivate CleanupPolicy = "deactivate"
	CleanupKeep       CleanupPolicy = "keep"
)

type Operation struct {
	Type    string
	Desired dns.Endpoint
	Current dns.Endpoint
	Reason  string
}

func (o Operation) String() string {
	name := o.Desired.DNSName
	if name == "" {
		name = o.Current.DNSName
	}
	recordType := o.Desired.RecordType
	if recordType == "" {
		recordType = o.Current.RecordType
	}
	return fmt.Sprintf("%s %s %s %s", o.Type, recordType, name, o.Reason)
}

func Build(desired []dns.Endpoint, current []dns.Endpoint, ownerID string, cleanup CleanupPolicy) []Operation {
	return BuildWithCleanupScope(desired, current, ownerID, cleanup, func(dns.Endpoint) bool { return true })
}

func BuildWithCleanupScope(desired []dns.Endpoint, current []dns.Endpoint, ownerID string, cleanup CleanupPolicy, cleanupAllowed func(dns.Endpoint) bool) []Operation {
	desiredByKey := map[string]dns.Endpoint{}
	currentByKey := map[string]dns.Endpoint{}

	for _, endpoint := range desired {
		endpoint = endpoint.Normalize()
		desiredByKey[endpoint.Key()] = endpoint
	}
	for _, endpoint := range current {
		endpoint = endpoint.Normalize()
		currentByKey[endpoint.Key()] = endpoint
	}

	var operations []Operation
	var createCandidates []dns.Endpoint
	for key, desiredEndpoint := range desiredByKey {
		currentEndpoint, exists := currentByKey[key]
		if !exists {
			createCandidates = append(createCandidates, desiredEndpoint)
			continue
		}
		if !ownedBy(currentEndpoint, ownerID) {
			operations = append(operations, Operation{
				Type:    OperationConflict,
				Desired: desiredEndpoint,
				Current: currentEndpoint,
				Reason:  "matching record is not owned by this controller",
			})
			continue
		}
		if !currentEndpoint.EqualRecord(desiredEndpoint) {
			operations = append(operations, Operation{
				Type:    OperationUpdate,
				Desired: desiredEndpoint,
				Current: currentEndpoint,
				Reason:  "record differs from desired state",
			})
		}
	}

	var staleCandidates []dns.Endpoint
	for key, currentEndpoint := range currentByKey {
		if _, exists := desiredByKey[key]; exists || !ownedBy(currentEndpoint, ownerID) || !cleanupAllowed(currentEndpoint) {
			continue
		}
		staleCandidates = append(staleCandidates, currentEndpoint)
	}

	// Pair a stale owned record with the new-target desired record for the same
	// logical record (zone/name/type). When exactly one of each share a logical
	// key and the stale record has a provider ID, emit a single replacement
	// (an in-place PUT) instead of an unordered create+delete that could leave a
	// duplicate if the delete failed after the create succeeded. Replacement only
	// applies under the delete cleanup policy, where the stale record would
	// otherwise be removed.
	if cleanup == CleanupDelete {
		createByLogical := groupByLogicalKey(createCandidates)
		staleByLogical := groupByLogicalKey(staleCandidates)
		for logical, creates := range createByLogical {
			stales := staleByLogical[logical]
			if len(creates) != 1 || len(stales) != 1 || strings.TrimSpace(stales[0].ProviderID) == "" {
				continue
			}
			operations = append(operations, Operation{
				Type:    OperationReplace,
				Desired: creates[0],
				Current: stales[0],
				Reason:  "record target changed",
			})
			createCandidates = removeEndpoint(createCandidates, creates[0])
			staleCandidates = removeEndpoint(staleCandidates, stales[0])
		}
	}

	for _, desiredEndpoint := range createCandidates {
		operations = append(operations, Operation{Type: OperationCreate, Desired: desiredEndpoint, Reason: "record is missing"})
	}

	for _, currentEndpoint := range staleCandidates {
		switch cleanup {
		case CleanupDelete:
			operations = append(operations, Operation{Type: OperationDelete, Current: currentEndpoint, Reason: "managed record is stale"})
		case CleanupDeactivate:
			desiredEndpoint := currentEndpoint
			desiredEndpoint.Disabled = true
			operations = append(operations, Operation{Type: OperationDeactivate, Desired: desiredEndpoint, Current: currentEndpoint, Reason: "managed record is stale"})
		case CleanupKeep:
		}
	}

	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Type != operations[j].Type {
			return operations[i].Type < operations[j].Type
		}
		return operationKey(operations[i]) < operationKey(operations[j])
	})
	return operations
}

func Format(operations []Operation) string {
	if len(operations) == 0 {
		return "no changes"
	}
	lines := make([]string, 0, len(operations))
	for _, operation := range operations {
		lines = append(lines, operation.String())
	}
	return strings.Join(lines, "\n")
}

func groupByLogicalKey(endpoints []dns.Endpoint) map[string][]dns.Endpoint {
	grouped := map[string][]dns.Endpoint{}
	for _, endpoint := range endpoints {
		key := endpoint.LogicalKey()
		grouped[key] = append(grouped[key], endpoint)
	}
	return grouped
}

func removeEndpoint(endpoints []dns.Endpoint, target dns.Endpoint) []dns.Endpoint {
	targetKey := target.Key()
	out := endpoints[:0:0]
	for _, endpoint := range endpoints {
		if endpoint.Key() == targetKey {
			continue
		}
		out = append(out, endpoint)
	}
	return out
}

func ownedBy(endpoint dns.Endpoint, ownerID string) bool {
	return strings.TrimSpace(endpoint.OwnerID) == strings.TrimSpace(ownerID)
}

func operationKey(operation Operation) string {
	if operation.Desired.DNSName != "" {
		return operation.Desired.Key()
	}
	return operation.Current.Key()
}
