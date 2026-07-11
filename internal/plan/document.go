package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

const (
	DocumentSchemaVersion = "v1alpha1"
	// DocumentAPIVersion identifies the canonical plan schema. Changing the
	// canonical representation requires a new version so previously approved
	// hashes never acquire new meaning.
	DocumentAPIVersion = "fortigate-external-dns.kgskr.io/" + DocumentSchemaVersion
	DocumentKind       = "ReconciliationPlan"
)

// TargetIdentity scopes a plan to exactly one controller target. URL and
// credential fields are deliberately absent: object identity plus the
// non-secret FortiGate scope is sufficient to reject cross-target apply.
type TargetIdentity struct {
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
	Generation int64  `json:"generation"`
	VDOM       string `json:"vdom,omitempty"`
	Zone       string `json:"zone"`
}

// Preconditions capture the snapshots that an apply implementation must
// revalidate. Each collection uses slices rather than maps so its canonical
// ordering is explicit and testable.
type Preconditions struct {
	Provider  ProviderPrecondition    `json:"provider"`
	Discovery DiscoveryPrecondition   `json:"discovery"`
	Policy    PolicyPrecondition      `json:"policy"`
	Ownership []OwnershipPrecondition `json:"ownership,omitempty"`
}

type ProviderPrecondition struct {
	Revision string `json:"revision"`
	Stable   bool   `json:"stable"`
	Complete bool   `json:"complete"`
}

type DiscoveryPrecondition struct {
	Generation int64                         `json:"generation"`
	Complete   bool                          `json:"complete"`
	Sources    []DiscoverySourcePrecondition `json:"sources,omitempty"`
}

type DiscoverySourcePrecondition struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Complete        bool   `json:"complete"`
}

type PolicyPrecondition struct {
	Generation int64                        `json:"generation"`
	Complete   bool                         `json:"complete"`
	Resources  []PolicyResourcePrecondition `json:"resources,omitempty"`
}

type PolicyResourcePrecondition struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	Generation      int64  `json:"generation"`
	ResourceVersion string `json:"resourceVersion"`
}

type OwnershipPrecondition struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion"`
	ProviderID      string `json:"providerID,omitempty"`
	Fingerprint     string `json:"fingerprint"`
	Phase           string `json:"phase"`
}

// SanitizedRecord is the provider mutation payload represented without
// controller ownership markers, Kubernetes source metadata, credentials, raw
// provider bodies, or other diagnostic data. ProviderID is retained because a
// keyed update/delete cannot be revalidated or applied without it.
type SanitizedRecord struct {
	Zone       string   `json:"zone"`
	DNSName    string   `json:"dnsName"`
	RecordType string   `json:"recordType"`
	Targets    []string `json:"targets,omitempty"`
	TTL        int64    `json:"ttl"`
	ProviderID string   `json:"providerID,omitempty"`
	Disabled   bool     `json:"disabled"`
}

type OperationReason string

const (
	OperationReasonUnspecified              OperationReason = "unspecified"
	OperationReasonRecordMissing            OperationReason = "record-missing"
	OperationReasonRecordDiffers            OperationReason = "record-differs"
	OperationReasonRecordStale              OperationReason = "record-stale"
	OperationReasonRecordTargetChanged      OperationReason = "record-target-changed"
	OperationReasonRecordTypeChanged        OperationReason = "record-type-changed"
	OperationReasonLogicalRecordUnowned     OperationReason = "logical-record-unowned"
	OperationReasonCNAMEUnownedConflict     OperationReason = "cname-unowned-conflict"
	OperationReasonCNAMETypeConflict        OperationReason = "cname-type-conflict"
	OperationReasonCNAMETransitionAmbiguous OperationReason = "cname-transition-ambiguous"
)

// SanitizedOperation is a stable, non-diagnostic representation of an existing
// Operation. ID is a content-derived lowercase SHA-256 digest unless the caller
// explicitly provides a stable ID for prerequisite references.
type SanitizedOperation struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"`
	Desired *SanitizedRecord `json:"desired,omitempty"`
	Current *SanitizedRecord `json:"current,omitempty"`
	Reason  OperationReason  `json:"reason"`
}

// PrerequisiteEdge means OperationID may run only after RequiresOperationID
// succeeds. Edges can only refer to operations in the same target document.
type PrerequisiteEdge struct {
	OperationID         string `json:"operationID"`
	RequiresOperationID string `json:"requiresOperationID"`
}

type SafetyDecisionCode string

const (
	SafetyDecisionUnspecified            SafetyDecisionCode = "unspecified"
	SafetyDecisionDiscoveryComplete      SafetyDecisionCode = "discovery-complete"
	SafetyDecisionProviderSnapshotStable SafetyDecisionCode = "provider-snapshot-stable"
	SafetyDecisionPolicyAccepted         SafetyDecisionCode = "policy-accepted"
	SafetyDecisionOwnershipAuthorized    SafetyDecisionCode = "ownership-authorized"
	SafetyDecisionEmptyDesiredGuard      SafetyDecisionCode = "empty-desired-guard"
	SafetyDecisionCleanupLimit           SafetyDecisionCode = "cleanup-limit"
	SafetyDecisionLogicalConflict        SafetyDecisionCode = "logical-conflict"
	SafetyDecisionApprovalRequired       SafetyDecisionCode = "approval-required"
)

// SafetyDecision records a bounded decision code, its result, and the affected
// operation IDs. Free-form messages are intentionally not part of the plan.
type SafetyDecision struct {
	Code         SafetyDecisionCode `json:"code"`
	Allowed      bool               `json:"allowed"`
	OperationIDs []string           `json:"operationIDs,omitempty"`
}

// Document is the immutable, target-scoped mutation contract. It intentionally
// contains no identifier field because the identifier is the digest of the
// entire canonical document and including it would be recursive.
type Document struct {
	APIVersion      string               `json:"apiVersion"`
	Kind            string               `json:"kind"`
	Target          TargetIdentity       `json:"target"`
	Preconditions   Preconditions        `json:"preconditions"`
	Operations      []SanitizedOperation `json:"operations,omitempty"`
	Prerequisites   []PrerequisiteEdge   `json:"prerequisites,omitempty"`
	SafetyDecisions []SafetyDecision     `json:"safetyDecisions,omitempty"`
}

// NewDocument converts the legacy operation API without changing it. Callers
// can append prerequisite edges and safety decisions before serialization.
func NewDocument(target TargetIdentity, preconditions Preconditions, operations []Operation) Document {
	documentOperations := make([]SanitizedOperation, 0, len(operations))
	for _, operation := range operations {
		documentOperations = append(documentOperations, SanitizeOperation(operation))
	}
	return Document{
		APIVersion:    DocumentAPIVersion,
		Kind:          DocumentKind,
		Target:        target,
		Preconditions: preconditions,
		Operations:    documentOperations,
	}
}

// SanitizeOperation copies only fields needed to review, revalidate, and apply
// a provider mutation. In particular, arbitrary Operation.Reason text is
// converted to a fixed code and Endpoint.OwnerID/Source are not copied.
func SanitizeOperation(operation Operation) SanitizedOperation {
	sanitized := SanitizedOperation{
		Type:   strings.TrimSpace(operation.Type),
		Reason: operationReason(operation.Reason),
	}
	if !endpointIsZero(operation.Desired) {
		record := sanitizeRecord(operation.Desired)
		sanitized.Desired = &record
	}
	if !endpointIsZero(operation.Current) {
		record := sanitizeRecord(operation.Current)
		sanitized.Current = &record
	}
	sanitized.ID = operationID(sanitized)
	return sanitized
}

// CanonicalJSON returns the exact bytes used for plan approval and persistence.
func (d Document) CanonicalJSON() ([]byte, error) {
	return json.Marshal(d)
}

// CanonicalBytes is an alias that names the mutation-contract semantics
// directly for callers that do not otherwise deal in JSON.
func (d Document) CanonicalBytes() ([]byte, error) {
	return d.CanonicalJSON()
}

// ID returns the lowercase SHA-256 digest of the canonical JSON bytes.
func (d Document) ID() (string, error) {
	canonical, err := d.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func CanonicalJSON(document Document) ([]byte, error) {
	return document.CanonicalJSON()
}

func CanonicalID(document Document) (string, error) {
	return document.ID()
}

// MarshalJSON makes every ordinary JSON serialization canonical as well. This
// prevents a caller from accidentally persisting input-order-dependent bytes.
func (d Document) MarshalJSON() ([]byte, error) {
	canonical := d.canonicalCopy()
	if err := canonical.Validate(); err != nil {
		return nil, err
	}
	type plainDocument Document
	return json.Marshal(plainDocument(canonical))
}

func (d Document) Validate() error {
	if d.APIVersion != DocumentAPIVersion {
		return fmt.Errorf("unsupported plan apiVersion %q", d.APIVersion)
	}
	if d.Kind != DocumentKind {
		return fmt.Errorf("unsupported plan kind %q", d.Kind)
	}
	if strings.TrimSpace(d.Target.Name) == "" {
		return fmt.Errorf("plan target name is required")
	}
	if strings.TrimSpace(d.Target.Zone) == "" {
		return fmt.Errorf("plan target zone is required")
	}

	operationIDs := make(map[string]struct{}, len(d.Operations))
	for _, operation := range d.Operations {
		if strings.TrimSpace(operation.ID) == "" {
			return fmt.Errorf("plan operation id is required")
		}
		if _, duplicate := operationIDs[operation.ID]; duplicate {
			return fmt.Errorf("duplicate plan operation id %q", operation.ID)
		}
		operationIDs[operation.ID] = struct{}{}
		if !supportedOperationType(operation.Type) {
			return fmt.Errorf("unsupported plan operation type %q", operation.Type)
		}
	}

	dependencies := make(map[string][]string, len(d.Operations))
	for _, edge := range d.Prerequisites {
		if _, exists := operationIDs[edge.OperationID]; !exists {
			return fmt.Errorf("prerequisite edge references unknown operation %q", edge.OperationID)
		}
		if _, exists := operationIDs[edge.RequiresOperationID]; !exists {
			return fmt.Errorf("prerequisite edge references unknown requirement %q", edge.RequiresOperationID)
		}
		if edge.OperationID == edge.RequiresOperationID {
			return fmt.Errorf("operation %q cannot require itself", edge.OperationID)
		}
		dependencies[edge.OperationID] = append(dependencies[edge.OperationID], edge.RequiresOperationID)
	}
	if cycle := dependencyCycle(dependencies); cycle != "" {
		return fmt.Errorf("prerequisite cycle includes operation %q", cycle)
	}
	for _, decision := range d.SafetyDecisions {
		for _, operationID := range decision.OperationIDs {
			if _, exists := operationIDs[operationID]; !exists {
				return fmt.Errorf("safety decision references unknown operation %q", operationID)
			}
		}
	}
	return nil
}

func (d Document) canonicalCopy() Document {
	d.APIVersion = strings.TrimSpace(d.APIVersion)
	if d.APIVersion == "" {
		d.APIVersion = DocumentAPIVersion
	}
	d.Kind = strings.TrimSpace(d.Kind)
	if d.Kind == "" {
		d.Kind = DocumentKind
	}
	d.Target = canonicalTarget(d.Target)
	d.Preconditions = canonicalPreconditions(d.Preconditions)

	d.Operations = append([]SanitizedOperation(nil), d.Operations...)
	for index := range d.Operations {
		d.Operations[index] = canonicalOperation(d.Operations[index])
	}
	sort.Slice(d.Operations, func(i, j int) bool {
		return operationSortKey(d.Operations[i]) < operationSortKey(d.Operations[j])
	})

	d.Prerequisites = deduplicatePrerequisites(d.Prerequisites)
	d.SafetyDecisions = append([]SafetyDecision(nil), d.SafetyDecisions...)
	for index := range d.SafetyDecisions {
		d.SafetyDecisions[index].Code = canonicalSafetyDecisionCode(d.SafetyDecisions[index].Code)
		d.SafetyDecisions[index].OperationIDs = sortedUniqueStrings(d.SafetyDecisions[index].OperationIDs)
	}
	sort.Slice(d.SafetyDecisions, func(i, j int) bool {
		return safetyDecisionSortKey(d.SafetyDecisions[i]) < safetyDecisionSortKey(d.SafetyDecisions[j])
	})
	return d
}

func canonicalTarget(target TargetIdentity) TargetIdentity {
	target.Namespace = strings.TrimSpace(target.Namespace)
	target.Name = strings.TrimSpace(target.Name)
	target.UID = strings.TrimSpace(target.UID)
	target.VDOM = strings.TrimSpace(target.VDOM)
	target.Zone = dns.NormalizeDNSName(target.Zone)
	return target
}

func canonicalPreconditions(preconditions Preconditions) Preconditions {
	preconditions.Provider.Revision = strings.TrimSpace(preconditions.Provider.Revision)
	preconditions.Discovery.Sources = append([]DiscoverySourcePrecondition(nil), preconditions.Discovery.Sources...)
	for index := range preconditions.Discovery.Sources {
		source := &preconditions.Discovery.Sources[index]
		source.Kind = strings.TrimSpace(source.Kind)
		source.Namespace = strings.TrimSpace(source.Namespace)
		source.Name = strings.TrimSpace(source.Name)
		source.ResourceVersion = strings.TrimSpace(source.ResourceVersion)
	}
	sort.Slice(preconditions.Discovery.Sources, func(i, j int) bool {
		return discoverySourceSortKey(preconditions.Discovery.Sources[i]) < discoverySourceSortKey(preconditions.Discovery.Sources[j])
	})

	preconditions.Policy.Resources = append([]PolicyResourcePrecondition(nil), preconditions.Policy.Resources...)
	for index := range preconditions.Policy.Resources {
		resource := &preconditions.Policy.Resources[index]
		resource.Namespace = strings.TrimSpace(resource.Namespace)
		resource.Name = strings.TrimSpace(resource.Name)
		resource.UID = strings.TrimSpace(resource.UID)
		resource.ResourceVersion = strings.TrimSpace(resource.ResourceVersion)
	}
	sort.Slice(preconditions.Policy.Resources, func(i, j int) bool {
		return policyResourceSortKey(preconditions.Policy.Resources[i]) < policyResourceSortKey(preconditions.Policy.Resources[j])
	})

	preconditions.Ownership = append([]OwnershipPrecondition(nil), preconditions.Ownership...)
	for index := range preconditions.Ownership {
		ownership := &preconditions.Ownership[index]
		ownership.Namespace = strings.TrimSpace(ownership.Namespace)
		ownership.Name = strings.TrimSpace(ownership.Name)
		ownership.UID = strings.TrimSpace(ownership.UID)
		ownership.ResourceVersion = strings.TrimSpace(ownership.ResourceVersion)
		ownership.ProviderID = strings.TrimSpace(ownership.ProviderID)
		ownership.Fingerprint = strings.TrimSpace(ownership.Fingerprint)
		ownership.Phase = strings.TrimSpace(ownership.Phase)
	}
	sort.Slice(preconditions.Ownership, func(i, j int) bool {
		return ownershipSortKey(preconditions.Ownership[i]) < ownershipSortKey(preconditions.Ownership[j])
	})
	return preconditions
}

func canonicalOperation(operation SanitizedOperation) SanitizedOperation {
	operation.ID = strings.TrimSpace(operation.ID)
	operation.Type = strings.TrimSpace(operation.Type)
	operation.Reason = canonicalOperationReason(operation.Reason)
	if operation.Desired != nil {
		record := canonicalRecord(*operation.Desired)
		operation.Desired = &record
	}
	if operation.Current != nil {
		record := canonicalRecord(*operation.Current)
		operation.Current = &record
	}
	if operation.ID == "" {
		operation.ID = operationID(operation)
	}
	return operation
}

func sanitizeRecord(endpoint dns.Endpoint) SanitizedRecord {
	endpoint = endpoint.Normalize()
	return SanitizedRecord{
		Zone:       endpoint.Zone,
		DNSName:    endpoint.DNSName,
		RecordType: endpoint.RecordType,
		Targets:    append([]string(nil), endpoint.Targets...),
		TTL:        endpoint.TTL,
		ProviderID: strings.TrimSpace(endpoint.ProviderID),
		Disabled:   endpoint.Disabled,
	}
}

func canonicalRecord(record SanitizedRecord) SanitizedRecord {
	endpoint := dns.Endpoint{
		Zone:       record.Zone,
		DNSName:    record.DNSName,
		RecordType: record.RecordType,
		Targets:    append([]string(nil), record.Targets...),
	}.Normalize()
	record.Zone = endpoint.Zone
	record.DNSName = endpoint.DNSName
	record.RecordType = endpoint.RecordType
	record.Targets = sortedUniqueStrings(endpoint.Targets)
	record.ProviderID = strings.TrimSpace(record.ProviderID)
	return record
}

func endpointIsZero(endpoint dns.Endpoint) bool {
	return endpoint.DNSName == "" && endpoint.RecordType == "" && len(endpoint.Targets) == 0 && endpoint.TTL == 0 && endpoint.Zone == "" && endpoint.ProviderID == "" && !endpoint.Disabled
}

func operationReason(reason string) OperationReason {
	switch strings.TrimSpace(reason) {
	case "record is missing":
		return OperationReasonRecordMissing
	case "record differs from desired state":
		return OperationReasonRecordDiffers
	case "managed record is stale":
		return OperationReasonRecordStale
	case "record target changed":
		return OperationReasonRecordTargetChanged
	case "record type changed":
		return OperationReasonRecordTypeChanged
	case "logical record is not owned by this controller":
		return OperationReasonLogicalRecordUnowned
	case "CNAME conflicts with an unowned record for this DNS name":
		return OperationReasonCNAMEUnownedConflict
	case "desired records contain a CNAME and another record type for the same DNS name":
		return OperationReasonCNAMETypeConflict
	case "CNAME record-type transition requires exactly one owned current row with a provider ID":
		return OperationReasonCNAMETransitionAmbiguous
	default:
		return OperationReasonUnspecified
	}
}

func canonicalOperationReason(reason OperationReason) OperationReason {
	switch reason {
	case OperationReasonRecordMissing,
		OperationReasonRecordDiffers,
		OperationReasonRecordStale,
		OperationReasonRecordTargetChanged,
		OperationReasonRecordTypeChanged,
		OperationReasonLogicalRecordUnowned,
		OperationReasonCNAMEUnownedConflict,
		OperationReasonCNAMETypeConflict,
		OperationReasonCNAMETransitionAmbiguous:
		return reason
	default:
		return OperationReasonUnspecified
	}
}

func canonicalSafetyDecisionCode(code SafetyDecisionCode) SafetyDecisionCode {
	switch code {
	case SafetyDecisionDiscoveryComplete,
		SafetyDecisionProviderSnapshotStable,
		SafetyDecisionPolicyAccepted,
		SafetyDecisionOwnershipAuthorized,
		SafetyDecisionEmptyDesiredGuard,
		SafetyDecisionCleanupLimit,
		SafetyDecisionLogicalConflict,
		SafetyDecisionApprovalRequired:
		return code
	default:
		return SafetyDecisionUnspecified
	}
}

func operationID(operation SanitizedOperation) string {
	type operationIdentity struct {
		Type    string           `json:"type"`
		Desired *SanitizedRecord `json:"desired,omitempty"`
		Current *SanitizedRecord `json:"current,omitempty"`
		Reason  OperationReason  `json:"reason"`
	}
	payload, err := json.Marshal(operationIdentity{
		Type:    operation.Type,
		Desired: operation.Desired,
		Current: operation.Current,
		Reason:  operation.Reason,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal sanitized operation identity: %v", err))
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func supportedOperationType(operationType string) bool {
	switch operationType {
	case OperationCreate, OperationUpdate, OperationReplace, OperationDelete, OperationDeactivate, OperationConflict:
		return true
	default:
		return false
	}
}

func deduplicatePrerequisites(edges []PrerequisiteEdge) []PrerequisiteEdge {
	edges = append([]PrerequisiteEdge(nil), edges...)
	for index := range edges {
		edges[index].OperationID = strings.TrimSpace(edges[index].OperationID)
		edges[index].RequiresOperationID = strings.TrimSpace(edges[index].RequiresOperationID)
	}
	sort.Slice(edges, func(i, j int) bool {
		return prerequisiteSortKey(edges[i]) < prerequisiteSortKey(edges[j])
	})
	out := edges[:0]
	for _, edge := range edges {
		if len(out) > 0 && edge == out[len(out)-1] {
			continue
		}
		out = append(out, edge)
	}
	return out
}

func sortedUniqueStrings(values []string) []string {
	values = append([]string(nil), values...)
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) > 0 && value == out[len(out)-1] {
			continue
		}
		out = append(out, value)
	}
	return out
}

func dependencyCycle(dependencies map[string][]string) string {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(dependencies))
	var visit func(string) string
	visit = func(operationID string) string {
		switch state[operationID] {
		case visiting:
			return operationID
		case visited:
			return ""
		}
		state[operationID] = visiting
		for _, requirementID := range dependencies[operationID] {
			if cycle := visit(requirementID); cycle != "" {
				return cycle
			}
		}
		state[operationID] = visited
		return ""
	}
	for operationID := range dependencies {
		if cycle := visit(operationID); cycle != "" {
			return cycle
		}
	}
	return ""
}

func discoverySourceSortKey(source DiscoverySourcePrecondition) string {
	return strings.Join([]string{source.Kind, source.Namespace, source.Name, source.ResourceVersion, fmt.Sprint(source.Complete)}, "\x00")
}

func policyResourceSortKey(resource PolicyResourcePrecondition) string {
	return strings.Join([]string{resource.Namespace, resource.Name, resource.UID, fmt.Sprint(resource.Generation), resource.ResourceVersion}, "\x00")
}

func ownershipSortKey(ownership OwnershipPrecondition) string {
	return strings.Join([]string{ownership.Namespace, ownership.Name, ownership.UID, ownership.ResourceVersion, ownership.ProviderID, ownership.Fingerprint, ownership.Phase}, "\x00")
}

func operationSortKey(operation SanitizedOperation) string {
	return strings.Join([]string{operation.ID, operation.Type, string(operation.Reason)}, "\x00")
}

func prerequisiteSortKey(edge PrerequisiteEdge) string {
	return strings.Join([]string{edge.OperationID, edge.RequiresOperationID}, "\x00")
}

func safetyDecisionSortKey(decision SafetyDecision) string {
	return strings.Join([]string{string(decision.Code), fmt.Sprint(decision.Allowed), strings.Join(decision.OperationIDs, "\x01")}, "\x00")
}
