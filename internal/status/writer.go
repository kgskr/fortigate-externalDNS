// Package status persists bounded, sanitized per-target reconciliation status.
package status

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

const (
	DefaultRetention = 20
	MaxRetention     = 100
)

// ConditionType is intentionally closed: these are the only condition types
// accepted by the status CRD and emitted by Writer.
type ConditionType string

const (
	ConditionReady             ConditionType = "Ready"
	ConditionDiscoveryComplete ConditionType = "DiscoveryComplete"
	ConditionProviderReachable ConditionType = "ProviderReachable"
	ConditionOwnershipHealthy  ConditionType = "OwnershipHealthy"
	ConditionPolicyAccepted    ConditionType = "PolicyAccepted"
	ConditionPlanApproved      ConditionType = "PlanApproved"
	ConditionDriftFree         ConditionType = "DriftFree"
)

var conditionOrder = [...]ConditionType{
	ConditionReady,
	ConditionDiscoveryComplete,
	ConditionProviderReachable,
	ConditionOwnershipHealthy,
	ConditionPolicyAccepted,
	ConditionPlanApproved,
	ConditionDriftFree,
}

// Reason is a fixed diagnostic category. Unknown input is collapsed into
// ReasonUnknown so provider bodies, object names, and other high-cardinality
// values can never become persisted reasons or messages.
type Reason string

const (
	ReasonReady                  Reason = "Ready"
	ReasonReconciling            Reason = "Reconciling"
	ReasonDiscoveryComplete      Reason = "DiscoveryComplete"
	ReasonDiscoveryIncomplete    Reason = "DiscoveryIncomplete"
	ReasonProviderReachable      Reason = "ProviderReachable"
	ReasonProviderUnavailable    Reason = "ProviderUnavailable"
	ReasonCredentialsUnavailable Reason = "CredentialsUnavailable"
	ReasonOwnershipHealthy       Reason = "OwnershipHealthy"
	ReasonOwnershipConflict      Reason = "OwnershipConflict"
	ReasonPolicyAccepted         Reason = "PolicyAccepted"
	ReasonPolicyRejected         Reason = "PolicyRejected"
	ReasonPlanApproved           Reason = "PlanApproved"
	ReasonPendingApproval        Reason = "PendingApproval"
	ReasonDriftFree              Reason = "DriftFree"
	ReasonDriftDetected          Reason = "DriftDetected"
	ReasonApplySucceeded         Reason = "ApplySucceeded"
	ReasonApplyFailed            Reason = "ApplyFailed"
	ReasonInterrupted            Reason = "Interrupted"
	ReasonInvalidConfiguration   Reason = "InvalidConfiguration"
	ReasonUnknown                Reason = "Unknown"
)

var reasonMessages = map[Reason]string{
	ReasonReady:                  "The target is ready.",
	ReasonReconciling:            "The target reconciliation is in progress.",
	ReasonDiscoveryComplete:      "Configured source discovery is complete.",
	ReasonDiscoveryIncomplete:    "Configured source discovery is incomplete.",
	ReasonProviderReachable:      "The provider snapshot is reachable and complete.",
	ReasonProviderUnavailable:    "The provider snapshot is unavailable.",
	ReasonCredentialsUnavailable: "Target credentials are unavailable.",
	ReasonOwnershipHealthy:       "Ownership state is healthy.",
	ReasonOwnershipConflict:      "Ownership state contains a conflict.",
	ReasonPolicyAccepted:         "Publication policy accepted the desired state.",
	ReasonPolicyRejected:         "Publication policy rejected part of the desired state.",
	ReasonPlanApproved:           "The current plan is approved.",
	ReasonPendingApproval:        "The current plan is pending approval.",
	ReasonDriftFree:              "No provider drift is present.",
	ReasonDriftDetected:          "Provider drift is present.",
	ReasonApplySucceeded:         "The current plan was applied successfully.",
	ReasonApplyFailed:            "The current plan apply failed.",
	ReasonInterrupted:            "The current reconciliation was interrupted.",
	ReasonInvalidConfiguration:   "The target configuration is invalid.",
	ReasonUnknown:                "State is not yet available.",
}

// ConditionState is the caller-provided state for one fixed condition. Detail
// is accepted for integration convenience but deliberately never persisted.
type ConditionState struct {
	Status             metav1.ConditionStatus
	Reason             Reason
	ObservedGeneration int64
	Detail             string
}

// Audit records one bounded plan summary. It contains no record-level data.
type Audit struct {
	PlanHash  string
	Phase     api.ChangePlanPhase
	Timestamp time.Time
	Counts    api.ReconcileCounts
}

// Snapshot is a complete target observation written atomically to status.
type Snapshot struct {
	TargetGeneration int64
	ProviderRevision string
	Counts           api.ReconcileCounts
	PlanHash         string
	AuditTime        time.Time
	ApplyTime        *time.Time
	Conditions       map[ConditionType]ConditionState
	Audit            *Audit
}

// Writer owns the status object whose name matches one validated target.
type Writer struct {
	client    dynamic.Interface
	namespace string
	target    string
	retention int32
	now       func() time.Time
	backoff   wait.Backoff
}

// NewWriter constructs a target-scoped writer. Status objects are created on
// first use and updated through the status subresource thereafter.
func NewWriter(client dynamic.Interface, namespace, target string, retention int32) (*Writer, error) {
	if client == nil {
		return nil, errors.New("dynamic client is required")
	}
	if namespace == "" {
		return nil, errors.New("status namespace is required")
	}
	if problems := validation.IsDNS1123Subdomain(target); len(problems) != 0 {
		return nil, fmt.Errorf("invalid target name: %s", strings.Join(problems, "; "))
	}
	return &Writer{
		client:    client,
		namespace: namespace,
		target:    target,
		retention: normalizeRetention(retention),
		now:       time.Now,
		backoff:   retry.DefaultRetry,
	}, nil
}

// Write creates or conflict-safely updates the target's status object.
func (w *Writer) Write(ctx context.Context, snapshot Snapshot) error {
	if w == nil {
		return errors.New("status writer is required")
	}
	resource := w.client.Resource(api.StatusGVR).Namespace(w.namespace)
	return retry.RetryOnConflict(w.backoff, func() error {
		current, err := resource.Get(ctx, w.target, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			created, createErr := w.create(ctx, resource)
			if apierrors.IsAlreadyExists(createErr) {
				return apierrors.NewConflict(api.StatusGVR.GroupResource(), w.target, createErr)
			}
			if createErr != nil {
				return createErr
			}
			current = created
		} else if err != nil {
			return fmt.Errorf("get target status: %w", err)
		}

		var typed api.FortiGateDNSStatus
		if err := api.FromUnstructured(current, &typed); err != nil {
			return err
		}
		typed.Status = w.buildStatus(typed.Status, snapshot)
		updated, err := api.ToUnstructured(&typed)
		if err != nil {
			return err
		}
		if _, err := resource.UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update target status: %w", err)
		}
		return nil
	})
}

func (w *Writer) create(ctx context.Context, resource dynamic.ResourceInterface) (*unstructured.Unstructured, error) {
	object := &api.FortiGateDNSStatus{
		TypeMeta: metav1.TypeMeta{
			APIVersion: api.SchemeGroupVersion.String(),
			Kind:       "FortiGateDNSStatus",
		},
		ObjectMeta: metav1.ObjectMeta{Name: w.target, Namespace: w.namespace},
		Spec: api.FortiGateDNSStatusSpec{
			TargetRef: corev1.LocalObjectReference{Name: w.target},
			Retention: w.retention,
		},
	}
	dynamicObject, err := api.ToUnstructured(object)
	if err != nil {
		return nil, err
	}
	created, err := resource.Create(ctx, dynamicObject, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create target status: %w", err)
	}
	return created, nil
}

func (w *Writer) buildStatus(previous api.FortiGateDNSStatusStatus, snapshot Snapshot) api.FortiGateDNSStatusStatus {
	now := metav1.NewTime(w.now().UTC())
	auditTime := normalizedTime(snapshot.AuditTime, now)
	status := api.FortiGateDNSStatusStatus{
		ObservedTargetGeneration: max64(snapshot.TargetGeneration, 0),
		ProviderRevision:         revisionFingerprint(snapshot.ProviderRevision),
		Counts:                   sanitizeCounts(snapshot.Counts),
		LastPlanHash:             sanitizePlanHash(snapshot.PlanHash),
		LastAuditTime:            &auditTime,
		Conditions:               buildConditions(previous.Conditions, snapshot.Conditions, now),
		History:                  appendHistory(previous.History, snapshot.Audit, w.retention, now),
	}
	if snapshot.ApplyTime != nil {
		applyTime := normalizedTime(*snapshot.ApplyTime, now)
		status.LastApplyTime = &applyTime
	} else if previous.LastApplyTime != nil {
		status.LastApplyTime = previous.LastApplyTime.DeepCopy()
	}
	return status
}

func buildConditions(previous []metav1.Condition, incoming map[ConditionType]ConditionState, now metav1.Time) []metav1.Condition {
	old := make(map[string]metav1.Condition, len(previous))
	for _, condition := range previous {
		old[condition.Type] = condition
	}
	conditions := make([]metav1.Condition, 0, len(conditionOrder))
	for _, conditionType := range conditionOrder {
		state, ok := incoming[conditionType]
		if !ok {
			state = ConditionState{Status: metav1.ConditionUnknown, Reason: ReasonUnknown}
		}
		state.Status = sanitizeConditionStatus(state.Status)
		state.Reason = sanitizeReason(state.Reason)
		condition := metav1.Condition{
			Type:               string(conditionType),
			Status:             state.Status,
			ObservedGeneration: max64(state.ObservedGeneration, 0),
			LastTransitionTime: now,
			Reason:             string(state.Reason),
			Message:            reasonMessages[state.Reason],
		}
		if prior, ok := old[condition.Type]; ok && prior.Status == condition.Status && prior.Reason == condition.Reason && prior.Message == condition.Message {
			condition.LastTransitionTime = prior.LastTransitionTime
		}
		conditions = append(conditions, condition)
	}
	return conditions
}

func appendHistory(previous []api.AuditSummary, audit *Audit, retention int32, now metav1.Time) []api.AuditSummary {
	limit := int(normalizeRetention(retention))
	if limit == 0 {
		return nil
	}
	history := append([]api.AuditSummary(nil), previous...)
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	if audit != nil {
		hash := sanitizePlanHash(audit.PlanHash)
		phase, valid := sanitizePhase(audit.Phase)
		if hash != "" && valid {
			history = append(history, api.AuditSummary{
				PlanHash:  hash,
				Phase:     string(phase),
				Timestamp: normalizedTime(audit.Timestamp, now),
				Counts:    sanitizeCounts(audit.Counts),
			})
		}
	}
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	return history
}

func sanitizeReason(reason Reason) Reason {
	if _, ok := reasonMessages[reason]; !ok {
		return ReasonUnknown
	}
	return reason
}

func sanitizeConditionStatus(status metav1.ConditionStatus) metav1.ConditionStatus {
	switch status {
	case metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown:
		return status
	default:
		return metav1.ConditionUnknown
	}
}

func sanitizeCounts(counts api.ReconcileCounts) api.ReconcileCounts {
	counts.Desired = max32(counts.Desired, 0)
	counts.Current = max32(counts.Current, 0)
	counts.Drift = max32(counts.Drift, 0)
	counts.Conflicts = max32(counts.Conflicts, 0)
	return counts
}

func sanitizePlanHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) != sha256.Size*2 || strings.ToLower(hash) != hash {
		return ""
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return ""
	}
	return hash
}

func revisionFingerprint(revision string) string {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(revision))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sanitizePhase(phase api.ChangePlanPhase) (api.ChangePlanPhase, bool) {
	switch phase {
	case api.ChangePlanPendingApproval, api.ChangePlanApproved, api.ChangePlanApplying,
		api.ChangePlanSucceeded, api.ChangePlanFailed, api.ChangePlanStale, api.ChangePlanInterrupted:
		return phase, true
	default:
		return "", false
	}
}

func normalizedTime(value time.Time, fallback metav1.Time) metav1.Time {
	if value.IsZero() {
		return fallback
	}
	return metav1.NewTime(value.UTC())
}

func normalizeRetention(retention int32) int32 {
	if retention < 0 {
		return 0
	}
	if retention > MaxRetention {
		return MaxRetention
	}
	return retention
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
