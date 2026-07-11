package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ApprovalHashAnnotation = GroupName + "/approved-plan-hash"
	AdoptAnnotation        = GroupName + "/adopt"
)

type OwnershipMode string

const (
	OwnershipModeExclusive OwnershipMode = "exclusive"
	OwnershipModeShared    OwnershipMode = "shared"
)

type ApprovalMode string

const (
	ApprovalModeDisabled ApprovalMode = "disabled"
	ApprovalModeRequired ApprovalMode = "required"
)

type CleanupPolicy string

const (
	CleanupPolicyDelete     CleanupPolicy = "delete"
	CleanupPolicyDeactivate CleanupPolicy = "deactivate"
	CleanupPolicyKeep       CleanupPolicy = "keep"
)

type LocalKeyReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

type FortiGateDNSTargetSpec struct {
	URL                        string                   `json:"url"`
	VDOM                       string                   `json:"vdom,omitempty"`
	Zone                       string                   `json:"zone"`
	APITokenSecretRef          corev1.SecretKeySelector `json:"apiTokenSecretRef"`
	CARef                      *LocalKeyReference       `json:"caRef,omitempty"`
	InsecureSkipVerify         bool                     `json:"insecureSkipVerify,omitempty"`
	OwnershipMode              OwnershipMode            `json:"ownershipMode"`
	ControllerID               string                   `json:"controllerID"`
	Sources                    []string                 `json:"sources,omitempty"`
	Namespaces                 []string                 `json:"namespaces,omitempty"`
	GatewayTargetNamespaces    []string                 `json:"gatewayTargetNamespaces,omitempty"`
	DomainFilters              []string                 `json:"domainFilters,omitempty"`
	CleanupPolicy              CleanupPolicy            `json:"cleanupPolicy,omitempty"`
	DryRun                     bool                     `json:"dryRun,omitempty"`
	ApprovalMode               ApprovalMode             `json:"approvalMode,omitempty"`
	AllowNonDestructiveOverlap bool                     `json:"allowNonDestructiveOverlap,omitempty"`
	Interval                   metav1.Duration          `json:"interval,omitempty"`
	Debounce                   metav1.Duration          `json:"debounce,omitempty"`
	Timeout                    metav1.Duration          `json:"timeout,omitempty"`
	Retries                    int32                    `json:"retries,omitempty"`
	DefaultTTL                 int64                    `json:"defaultTTL,omitempty"`
	ExternalNameEnabled        bool                     `json:"externalNameEnabled,omitempty"`
	HeadlessEnabled            bool                     `json:"headlessEnabled,omitempty"`
}

type FortiGateDNSTargetStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type DNSRecordKey struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

type SourceObjectReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type OwnershipPhase string

const (
	OwnershipPhaseReserved  OwnershipPhase = "Reserved"
	OwnershipPhaseConfirmed OwnershipPhase = "Confirmed"
	OwnershipPhaseOrphaned  OwnershipPhase = "Orphaned"
	OwnershipPhaseConflict  OwnershipPhase = "Conflict"
)

type FortiGateDNSRecordOwnershipSpec struct {
	TargetRef         corev1.LocalObjectReference `json:"targetRef"`
	ProviderID        string                      `json:"providerID,omitempty"`
	Record            DNSRecordKey                `json:"record"`
	Fingerprint       string                      `json:"fingerprint"`
	Sources           []SourceObjectReference     `json:"sources,omitempty"`
	ControllerID      string                      `json:"controllerID"`
	AdoptionRequested bool                        `json:"adoptionRequested,omitempty"`
}

type FortiGateDNSRecordOwnershipStatus struct {
	Phase                    OwnershipPhase     `json:"phase,omitempty"`
	ObservedProviderRevision string             `json:"observedProviderRevision,omitempty"`
	LastConfirmedTime        *metav1.Time       `json:"lastConfirmedTime,omitempty"`
	Conditions               []metav1.Condition `json:"conditions,omitempty"`
}

type TTLRange struct {
	Minimum int64 `json:"minimum,omitempty"`
	Maximum int64 `json:"maximum,omitempty"`
}

type OptInRequirement struct {
	Annotation string `json:"annotation"`
	Value      string `json:"value"`
}

type FortiGateDNSPolicySpec struct {
	Selector                *metav1.LabelSelector `json:"selector,omitempty"`
	SourceKinds             []string              `json:"sourceKinds,omitempty"`
	AllowedHostnameSuffixes []string              `json:"allowedHostnameSuffixes,omitempty"`
	TTL                     *TTLRange             `json:"ttl,omitempty"`
	AllowedTargetCIDRs      []string              `json:"allowedTargetCIDRs,omitempty"`
	AllowedTargetSuffixes   []string              `json:"allowedTargetSuffixes,omitempty"`
	RequireOptIn            *OptInRequirement     `json:"requireOptIn,omitempty"`
	MaxRecordsPerNamespace  int32                 `json:"maxRecordsPerNamespace,omitempty"`
	MaxRecordsPerTarget     int32                 `json:"maxRecordsPerTarget,omitempty"`
	Deny                    bool                  `json:"deny,omitempty"`
}

type PlanOperation struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	Record        DNSRecordKey `json:"record"`
	TTL           int64        `json:"ttl,omitempty"`
	Status        string       `json:"status,omitempty"`
	ProviderID    string       `json:"providerID,omitempty"`
	Prerequisites []string     `json:"prerequisites,omitempty"`
}

type FortiGateDNSChangePlanSpec struct {
	SchemaVersion             string                      `json:"schemaVersion"`
	TargetRef                 corev1.LocalObjectReference `json:"targetRef"`
	PlanHash                  string                      `json:"planHash"`
	CanonicalDocument         string                      `json:"canonicalDocument"`
	ProviderRevision          string                      `json:"providerRevision"`
	DiscoveryGeneration       int64                       `json:"discoveryGeneration"`
	PolicyGeneration          int64                       `json:"policyGeneration,omitempty"`
	OwnershipResourceVersions map[string]string           `json:"ownershipResourceVersions,omitempty"`
	Operations                []PlanOperation             `json:"operations,omitempty"`
	ExpiresAt                 *metav1.Time                `json:"expiresAt,omitempty"`
}

type OperationOutcome struct {
	OperationID string `json:"operationID"`
	Result      string `json:"result"`
	Reason      string `json:"reason,omitempty"`
}

type ChangePlanPhase string

const (
	ChangePlanPendingApproval ChangePlanPhase = "PendingApproval"
	ChangePlanApproved        ChangePlanPhase = "Approved"
	ChangePlanApplying        ChangePlanPhase = "Applying"
	ChangePlanSucceeded       ChangePlanPhase = "Succeeded"
	ChangePlanFailed          ChangePlanPhase = "Failed"
	ChangePlanStale           ChangePlanPhase = "Stale"
	ChangePlanInterrupted     ChangePlanPhase = "Interrupted"
)

type FortiGateDNSChangePlanStatus struct {
	Phase              ChangePlanPhase    `json:"phase,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Outcomes           []OperationOutcome `json:"outcomes,omitempty"`
	CompletedAt        *metav1.Time       `json:"completedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type ReconcileCounts struct {
	Desired   int32 `json:"desired,omitempty"`
	Current   int32 `json:"current,omitempty"`
	Drift     int32 `json:"drift,omitempty"`
	Conflicts int32 `json:"conflicts,omitempty"`
}

type AuditSummary struct {
	PlanHash  string          `json:"planHash"`
	Phase     string          `json:"phase"`
	Timestamp metav1.Time     `json:"timestamp"`
	Counts    ReconcileCounts `json:"counts,omitempty"`
}

type FortiGateDNSStatusSpec struct {
	TargetRef corev1.LocalObjectReference `json:"targetRef"`
	Retention int32                       `json:"retention,omitempty"`
}

type FortiGateDNSStatusStatus struct {
	ObservedTargetGeneration int64              `json:"observedTargetGeneration,omitempty"`
	ProviderRevision         string             `json:"providerRevision,omitempty"`
	Counts                   ReconcileCounts    `json:"counts,omitempty"`
	LastPlanHash             string             `json:"lastPlanHash,omitempty"`
	LastAuditTime            *metav1.Time       `json:"lastAuditTime,omitempty"`
	LastApplyTime            *metav1.Time       `json:"lastApplyTime,omitempty"`
	Conditions               []metav1.Condition `json:"conditions,omitempty"`
	History                  []AuditSummary     `json:"history,omitempty"`
}

type FortiGateDNSTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FortiGateDNSTargetSpec   `json:"spec"`
	Status            FortiGateDNSTargetStatus `json:"status,omitempty"`
}

type FortiGateDNSTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FortiGateDNSTarget `json:"items"`
}

type FortiGateDNSRecordOwnership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FortiGateDNSRecordOwnershipSpec   `json:"spec"`
	Status            FortiGateDNSRecordOwnershipStatus `json:"status,omitempty"`
}

type FortiGateDNSRecordOwnershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FortiGateDNSRecordOwnership `json:"items"`
}

type FortiGateDNSPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FortiGateDNSPolicySpec `json:"spec"`
}

type FortiGateDNSPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FortiGateDNSPolicy `json:"items"`
}

type FortiGateDNSChangePlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FortiGateDNSChangePlanSpec   `json:"spec"`
	Status            FortiGateDNSChangePlanStatus `json:"status,omitempty"`
}

type FortiGateDNSChangePlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FortiGateDNSChangePlan `json:"items"`
}

type FortiGateDNSStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FortiGateDNSStatusSpec   `json:"spec"`
	Status            FortiGateDNSStatusStatus `json:"status,omitempty"`
}

type FortiGateDNSStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FortiGateDNSStatus `json:"items"`
}
