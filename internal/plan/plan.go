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
	// A FortiGate dns-entry mkey is a server-assigned integer, so the same
	// zone/name/type/target can legitimately exist as more than one row. Keying
	// current state by a single Endpoint would silently drop such duplicates and
	// leave them unmanaged, so current records are grouped as a slice per key.
	currentByKey := map[string][]dns.Endpoint{}

	for _, endpoint := range desired {
		endpoint = endpoint.Normalize()
		desiredByKey[endpoint.Key()] = endpoint
	}
	for _, endpoint := range current {
		endpoint = endpoint.Normalize()
		currentByKey[endpoint.Key()] = append(currentByKey[endpoint.Key()], endpoint)
	}

	var operations []Operation
	var createCandidates []dns.Endpoint
	var staleCandidates []dns.Endpoint
	for key, desiredEndpoint := range desiredByKey {
		owned, unowned := partitionOwned(currentByKey[key], ownerID)
		if len(owned) == 0 {
			if len(unowned) > 0 {
				operations = append(operations, Operation{
					Type:    OperationConflict,
					Desired: desiredEndpoint,
					Current: unowned[0],
					Reason:  "matching record is not owned by this controller",
				})
				continue
			}
			createCandidates = append(createCandidates, desiredEndpoint)
			continue
		}
		// Keep one owned record to represent this desired key and reconcile it to
		// the desired state; any additional owned copies are duplicates to remove.
		match := owned[0]
		if !match.EqualRecord(desiredEndpoint) {
			operations = append(operations, Operation{
				Type:    OperationUpdate,
				Desired: desiredEndpoint,
				Current: match,
				Reason:  "record differs from desired state",
			})
		}
		for _, duplicate := range owned[1:] {
			if cleanupAllowed(duplicate) {
				staleCandidates = append(staleCandidates, duplicate)
			}
		}
	}

	for key, currents := range currentByKey {
		if _, exists := desiredByKey[key]; exists {
			continue
		}
		for _, currentEndpoint := range currents {
			if !ownedBy(currentEndpoint, ownerID) || !cleanupAllowed(currentEndpoint) {
				continue
			}
			staleCandidates = append(staleCandidates, currentEndpoint)
		}
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
			if currentEndpoint.Disabled {
				// Already disabled on the device; re-deactivating would re-PUT the
				// same state every reconcile loop. The desired state already matches.
				continue
			}
			desiredEndpoint := currentEndpoint
			desiredEndpoint.Disabled = true
			operations = append(operations, Operation{Type: OperationDeactivate, Desired: desiredEndpoint, Current: currentEndpoint, Reason: "managed record is stale"})
		case CleanupKeep:
		}
	}

	sort.Slice(operations, func(i, j int) bool {
		ri, rj := operationPhaseRank(operations[i].Type), operationPhaseRank(operations[j].Type)
		if ri != rj {
			return ri < rj
		}
		if operations[i].Type != operations[j].Type {
			return operations[i].Type < operations[j].Type
		}
		return operationKey(operations[i]) < operationKey(operations[j])
	})
	return operations
}

// operationPhaseRank orders operations into an intentional, safety-oriented
// apply sequence: surface conflicts first, reconcile records in place, then
// create missing records, then remove stale ones. Sorting by this rank (with the
// operation key as a deterministic tiebreaker) keeps the apply order stable and
// robust to new operation types rather than relying on alphabetical type names.
func operationPhaseRank(opType string) int {
	switch opType {
	case OperationConflict:
		return 0
	case OperationUpdate, OperationReplace:
		return 1
	case OperationCreate:
		return 2
	case OperationDelete, OperationDeactivate:
		return 3
	default:
		return 4
	}
}

// partitionOwned splits current records sharing one key into those owned by this
// controller and the rest. Owned records are ordered so that one carrying a
// provider ID is kept as the match (updates/deletes need the ID) and duplicate
// selection is deterministic.
func partitionOwned(endpoints []dns.Endpoint, ownerID string) (owned, unowned []dns.Endpoint) {
	for _, endpoint := range endpoints {
		if ownedBy(endpoint, ownerID) {
			owned = append(owned, endpoint)
		} else {
			unowned = append(unowned, endpoint)
		}
	}
	sort.SliceStable(owned, func(i, j int) bool {
		iHas := strings.TrimSpace(owned[i].ProviderID) != ""
		jHas := strings.TrimSpace(owned[j].ProviderID) != ""
		if iHas != jHas {
			return iHas
		}
		return owned[i].ProviderID < owned[j].ProviderID
	})
	return owned, unowned
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
