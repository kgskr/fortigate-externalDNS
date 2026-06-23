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
	for key, desiredEndpoint := range desiredByKey {
		currentEndpoint, exists := currentByKey[key]
		if !exists {
			operations = append(operations, Operation{Type: OperationCreate, Desired: desiredEndpoint, Reason: "record is missing"})
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

	for key, currentEndpoint := range currentByKey {
		if _, exists := desiredByKey[key]; exists || !ownedBy(currentEndpoint, ownerID) || !cleanupAllowed(currentEndpoint) {
			continue
		}
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

func MutableOperations(operations []Operation) []Operation {
	var out []Operation
	for _, operation := range operations {
		if operation.Type == OperationConflict {
			continue
		}
		out = append(out, operation)
	}
	return out
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

func ownedBy(endpoint dns.Endpoint, ownerID string) bool {
	return strings.TrimSpace(endpoint.OwnerID) == strings.TrimSpace(ownerID)
}

func operationKey(operation Operation) string {
	if operation.Desired.DNSName != "" {
		return operation.Desired.Key()
	}
	return operation.Current.Key()
}
