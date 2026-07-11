package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
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
	currentByLogicalKey := map[string][]dns.Endpoint{}
	currentByMutationGroup := map[string][]dns.Endpoint{}
	desiredByMutationGroup := map[string][]dns.Endpoint{}

	for _, endpoint := range desired {
		endpoint = endpoint.Normalize()
		desiredByKey[endpoint.Key()] = endpoint
	}
	for _, endpoint := range current {
		endpoint = endpoint.Normalize()
		currentByKey[endpoint.Key()] = append(currentByKey[endpoint.Key()], endpoint)
		currentByLogicalKey[endpoint.LogicalKey()] = append(currentByLogicalKey[endpoint.LogicalKey()], endpoint)
		currentByMutationGroup[endpoint.MutationGroupKey()] = append(currentByMutationGroup[endpoint.MutationGroupKey()], endpoint)
	}
	for _, endpoint := range desiredByKey {
		desiredByMutationGroup[endpoint.MutationGroupKey()] = append(desiredByMutationGroup[endpoint.MutationGroupKey()], endpoint)
	}
	for mutationGroup := range desiredByMutationGroup {
		sortEndpoints(desiredByMutationGroup[mutationGroup])
	}

	var operations []Operation
	var createCandidates []dns.Endpoint
	var staleCandidates []dns.Endpoint
	conflictLogicalKeys := map[string]struct{}{}
	reportedMutationConflicts := map[string]struct{}{}
	mutationConflicts := map[string]Operation{}
	for mutationGroup, desiredEndpoints := range desiredByMutationGroup {
		if desiredSetHasCNAMEConflict(desiredEndpoints) {
			mutationConflicts[mutationGroup] = Operation{
				Type:    OperationConflict,
				Desired: desiredEndpoints[0],
				Reason:  "desired records contain a CNAME and another record type for the same DNS name",
			}
		}
	}
	for _, desiredEndpoint := range desiredByKey {
		mutationGroup := desiredEndpoint.MutationGroupKey()
		_, mutationGroupUnowned := partitionOwned(currentByMutationGroup[mutationGroup], ownerID)
		currentEndpoint, conflicted := unownedCNAMEConflict(desiredEndpoint, mutationGroupUnowned)
		if !conflicted {
			continue
		}
		candidate := Operation{
			Type:    OperationConflict,
			Desired: desiredEndpoint,
			Current: currentEndpoint,
			Reason:  "CNAME conflicts with an unowned record for this DNS name",
		}
		if existing, exists := mutationConflicts[mutationGroup]; !exists || operationKey(candidate) < operationKey(existing) {
			mutationConflicts[mutationGroup] = candidate
		}
	}

	// A CNAME cannot coexist with other record types at one DNS owner name. An
	// exact 1:1 owned transition can be changed atomically with a keyed PUT; wider
	// transitions are ambiguous and fail closed instead of attempting an invalid
	// create-before-cleanup sequence.
	crossTypeReplacements := map[string]Operation{}
	for mutationGroup, desiredEndpoints := range desiredByMutationGroup {
		if _, conflicted := mutationConflicts[mutationGroup]; conflicted {
			continue
		}
		owned, _ := partitionOwned(currentByMutationGroup[mutationGroup], ownerID)
		if !hasCNAMETypeTransition(desiredEndpoints, owned) {
			continue
		}
		if len(desiredEndpoints) == 1 {
			_, logicalUnowned := partitionOwned(currentByLogicalKey[desiredEndpoints[0].LogicalKey()], ownerID)
			if len(logicalUnowned) > 0 {
				mutationConflicts[mutationGroup] = Operation{
					Type:    OperationConflict,
					Desired: desiredEndpoints[0],
					Current: logicalUnowned[0],
					Reason:  "logical record is not owned by this controller",
				}
				continue
			}
		}
		if len(desiredEndpoints) == 1 && len(owned) == 1 && strings.TrimSpace(owned[0].ProviderID) != "" && cleanupAllowed(owned[0]) {
			crossTypeReplacements[mutationGroup] = Operation{
				Type:    OperationReplace,
				Desired: desiredEndpoints[0],
				Current: owned[0],
				Reason:  "record type changed",
			}
			continue
		}
		currentEndpoint := dns.Endpoint{}
		if len(owned) > 0 {
			currentEndpoint = owned[0]
		}
		mutationConflicts[mutationGroup] = Operation{
			Type:    OperationConflict,
			Desired: desiredEndpoints[0],
			Current: currentEndpoint,
			Reason:  "CNAME record-type transition requires exactly one owned current row with a provider ID",
		}
	}

	for key, desiredEndpoint := range desiredByKey {
		mutationGroup := desiredEndpoint.MutationGroupKey()
		if conflictOperation, conflicted := mutationConflicts[mutationGroup]; conflicted {
			if _, reported := reportedMutationConflicts[mutationGroup]; !reported {
				operations = append(operations, conflictOperation)
			}
			reportedMutationConflicts[mutationGroup] = struct{}{}
			continue
		}
		if _, replacing := crossTypeReplacements[mutationGroup]; replacing {
			continue
		}

		logicalKey := desiredEndpoint.LogicalKey()
		_, logicalUnowned := partitionOwned(currentByLogicalKey[logicalKey], ownerID)
		if len(logicalUnowned) > 0 {
			operations = append(operations, Operation{
				Type:    OperationConflict,
				Desired: desiredEndpoint,
				Current: logicalUnowned[0],
				Reason:  "logical record is not owned by this controller",
			})
			conflictLogicalKeys[logicalKey] = struct{}{}
			continue
		}
		owned, _ := partitionOwned(currentByKey[key], ownerID)
		if len(owned) == 0 {
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
			if _, conflicted := mutationConflicts[currentEndpoint.MutationGroupKey()]; conflicted {
				continue
			}
			if _, replacing := crossTypeReplacements[currentEndpoint.MutationGroupKey()]; replacing {
				continue
			}
			if _, conflicted := conflictLogicalKeys[currentEndpoint.LogicalKey()]; conflicted {
				continue
			}
			if !ownedBy(currentEndpoint, ownerID) || !cleanupAllowed(currentEndpoint) {
				continue
			}
			staleCandidates = append(staleCandidates, currentEndpoint)
		}
	}
	for _, replacement := range crossTypeReplacements {
		operations = append(operations, replacement)
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
			if _, conflicted := conflictLogicalKeys[logical]; conflicted {
				continue
			}
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

	// Compatible non-1:1 target transitions converge in two phases. If any desired
	// target for one DNS owner name is still missing, create it this cycle but
	// retain every stale record for that name until a later snapshot proves all
	// desired records are observable. CNAME type transitions were already resolved
	// above as an atomic 1:1 replacement or an explicit conflict.
	pendingCreateGroups := map[string]struct{}{}
	for _, desiredEndpoint := range createCandidates {
		pendingCreateGroups[desiredEndpoint.MutationGroupKey()] = struct{}{}
	}

	for _, currentEndpoint := range staleCandidates {
		if _, pending := pendingCreateGroups[currentEndpoint.MutationGroupKey()]; pending {
			continue
		}
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

func sortEndpoints(endpoints []dns.Endpoint) {
	sort.Slice(endpoints, func(i, j int) bool {
		return endpoints[i].Key() < endpoints[j].Key()
	})
}

func desiredSetHasCNAMEConflict(endpoints []dns.Endpoint) bool {
	for i := range endpoints {
		for j := i + 1; j < len(endpoints); j++ {
			if cnameTypesConflict(endpoints[i].RecordType, endpoints[j].RecordType) {
				return true
			}
		}
	}
	return false
}

func hasCNAMETypeTransition(desired, current []dns.Endpoint) bool {
	for _, desiredEndpoint := range desired {
		for _, currentEndpoint := range current {
			if cnameTypesConflict(desiredEndpoint.RecordType, currentEndpoint.RecordType) {
				return true
			}
		}
	}
	return false
}

func cnameTypesConflict(first, second string) bool {
	return first != second && (first == dns.RecordCNAME || second == dns.RecordCNAME)
}

// removeEndpoint drops the FIRST element whose Key matches target and returns
// the rest. It removes a single element (not every Key match) so it stays correct
// even if a duplicate-Key endpoint ever reaches the replace-pairing candidates.
func removeEndpoint(endpoints []dns.Endpoint, target dns.Endpoint) []dns.Endpoint {
	targetKey := target.Key()
	out := endpoints[:0:0]
	removed := false
	for _, endpoint := range endpoints {
		if !removed && endpoint.Key() == targetKey {
			removed = true
			continue
		}
		out = append(out, endpoint)
	}
	return out
}

func ownedBy(endpoint dns.Endpoint, ownerID string) bool {
	return strings.TrimSpace(endpoint.OwnerID) == strings.TrimSpace(ownerID)
}

func unownedCNAMEConflict(desired dns.Endpoint, unowned []dns.Endpoint) (dns.Endpoint, bool) {
	for _, current := range unowned {
		if cnameTypesConflict(desired.RecordType, current.RecordType) {
			return current, true
		}
	}
	return dns.Endpoint{}, false
}

func operationKey(operation Operation) string {
	if operation.Desired.DNSName != "" {
		return operation.Desired.Key()
	}
	return operation.Current.Key()
}
