package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

// NewChangePlanObject persists both the exact canonical mutation contract and
// bounded structured summaries used by Kubernetes status and printer columns.
// The duplicated fields are revalidated on read so they cannot drift silently.
func NewChangePlanObject(namespace, name string, document Document, expiresAt *metav1.Time) (*v1alpha1.FortiGateDNSChangePlan, error) {
	canonical, err := document.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("canonicalize plan document: %w", err)
	}
	planHash, err := document.ID()
	if err != nil {
		return nil, fmt.Errorf("identify plan document: %w", err)
	}
	operations, err := summarizeOperations(document)
	if err != nil {
		return nil, err
	}
	resourceVersions := make(map[string]string, len(document.Preconditions.Ownership))
	for _, claim := range document.Preconditions.Ownership {
		key := claim.Name
		if claim.Namespace != "" {
			key = claim.Namespace + "/" + claim.Name
		}
		if _, duplicate := resourceVersions[key]; duplicate {
			return nil, fmt.Errorf("duplicate ownership precondition %q", key)
		}
		resourceVersions[key] = claim.ResourceVersion
	}
	if len(resourceVersions) == 0 {
		resourceVersions = nil
	}
	return &v1alpha1.FortiGateDNSChangePlan{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "FortiGateDNSChangePlan"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.FortiGateDNSChangePlanSpec{
			SchemaVersion:             DocumentSchemaVersion,
			TargetRef:                 corev1.LocalObjectReference{Name: document.Target.Name},
			PlanHash:                  planHash,
			CanonicalDocument:         string(canonical),
			ProviderRevision:          document.Preconditions.Provider.Revision,
			DiscoveryGeneration:       document.Preconditions.Discovery.Generation,
			PolicyGeneration:          document.Preconditions.Policy.Generation,
			OwnershipResourceVersions: resourceVersions,
			Operations:                operations,
			ExpiresAt:                 expiresAt,
		},
	}, nil
}

// DocumentFromChangePlan verifies every duplicated CRD summary against the
// embedded canonical bytes before returning a mutation contract.
func DocumentFromChangePlan(object *v1alpha1.FortiGateDNSChangePlan) (Document, error) {
	if object == nil {
		return Document{}, fmt.Errorf("change plan object is required")
	}
	if object.Spec.SchemaVersion != DocumentSchemaVersion {
		return Document{}, fmt.Errorf("unsupported persisted plan schema %q", object.Spec.SchemaVersion)
	}
	var document Document
	if err := json.Unmarshal([]byte(object.Spec.CanonicalDocument), &document); err != nil {
		return Document{}, fmt.Errorf("decode canonical plan document: %w", err)
	}
	canonical, err := document.CanonicalJSON()
	if err != nil {
		return Document{}, fmt.Errorf("validate canonical plan document: %w", err)
	}
	if string(canonical) != object.Spec.CanonicalDocument {
		return Document{}, fmt.Errorf("persisted plan document is not canonical")
	}
	planHash, err := document.ID()
	if err != nil {
		return Document{}, err
	}
	if planHash != object.Spec.PlanHash {
		return Document{}, fmt.Errorf("persisted plan hash does not match canonical document")
	}
	if object.Spec.TargetRef.Name != document.Target.Name ||
		object.Spec.ProviderRevision != document.Preconditions.Provider.Revision ||
		object.Spec.DiscoveryGeneration != document.Preconditions.Discovery.Generation ||
		object.Spec.PolicyGeneration != document.Preconditions.Policy.Generation {
		return Document{}, fmt.Errorf("persisted plan summary does not match canonical preconditions")
	}
	expected, err := NewChangePlanObject(object.Namespace, object.Name, document, object.Spec.ExpiresAt)
	if err != nil {
		return Document{}, err
	}
	if !reflect.DeepEqual(expected.Spec.OwnershipResourceVersions, object.Spec.OwnershipResourceVersions) ||
		!reflect.DeepEqual(expected.Spec.Operations, object.Spec.Operations) {
		return Document{}, fmt.Errorf("persisted plan operation or ownership summary does not match canonical document")
	}
	return document, nil
}

func summarizeOperations(document Document) ([]v1alpha1.PlanOperation, error) {
	prerequisites := map[string][]string{}
	for _, edge := range document.Prerequisites {
		prerequisites[edge.OperationID] = append(prerequisites[edge.OperationID], edge.RequiresOperationID)
	}
	operations := make([]v1alpha1.PlanOperation, 0, len(document.Operations))
	for _, operation := range document.Operations {
		record := operation.Desired
		if record == nil {
			record = operation.Current
		}
		if record == nil {
			return nil, fmt.Errorf("plan operation %q has no record summary", operation.ID)
		}
		if len(record.Targets) > 1 {
			return nil, fmt.Errorf("plan operation %q has more than one provider record target", operation.ID)
		}
		target := ""
		if len(record.Targets) == 1 {
			target = record.Targets[0]
		}
		requires := append([]string(nil), prerequisites[operation.ID]...)
		sort.Strings(requires)
		status := "enable"
		if record.Disabled {
			status = "disable"
		}
		operations = append(operations, v1alpha1.PlanOperation{
			ID:   operation.ID,
			Type: strings.ToLower(strings.TrimSpace(operation.Type)),
			Record: v1alpha1.DNSRecordKey{
				Name: record.DNSName, Type: record.RecordType, Target: target,
			},
			TTL: record.TTL, Status: status, ProviderID: record.ProviderID,
			Prerequisites: requires,
		})
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })
	return operations, nil
}
