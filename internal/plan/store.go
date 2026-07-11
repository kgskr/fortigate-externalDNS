package plan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

type ChangePlanStore struct {
	client dynamic.Interface
	now    func() time.Time
}

func NewChangePlanStore(client dynamic.Interface) (*ChangePlanStore, error) {
	if client == nil {
		return nil, fmt.Errorf("dynamic change plan client is required")
	}
	return &ChangePlanStore{client: client, now: time.Now}, nil
}

// PersistCurrent creates an immutable plan and marks older nonterminal plans
// for the same target stale. Reconciliation of an already persisted exact hash
// is idempotent and verifies the stored canonical document before reuse.
func (s *ChangePlanStore) PersistCurrent(ctx context.Context, namespace string, document Document, expiresAt *metav1.Time, retention int) (*v1alpha1.FortiGateDNSChangePlan, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("dynamic change plan client is required")
	}
	items, err := s.listTarget(ctx, namespace, document.Target.Name)
	if err != nil {
		return nil, err
	}
	wantedHash, err := document.ID()
	if err != nil {
		return nil, err
	}
	for i := range items {
		candidate := &items[i]
		if candidate.Spec.PlanHash == wantedHash && !terminalPlanPhase(candidate.Status.Phase) && candidate.Status.Phase != v1alpha1.ChangePlanStale {
			if _, err := DocumentFromChangePlan(candidate); err != nil {
				return nil, err
			}
			return candidate, nil
		}
	}
	name := planObjectName(document)
	for i := range items {
		if items[i].Name == name {
			name = recurringPlanObjectName(document, s.now())
			break
		}
	}
	object, err := NewChangePlanObject(namespace, name, document, expiresAt)
	if err != nil {
		return nil, err
	}
	object.Labels = map[string]string{"fortigate-external-dns.kgskr.io/target": labelValue(document.Target.Name)}
	object.Status.Phase = v1alpha1.ChangePlanPendingApproval
	object.Status.ObservedGeneration = object.Generation

	resource := s.client.Resource(v1alpha1.ChangePlanGVR).Namespace(namespace)
	existing, getErr := resource.Get(ctx, object.Name, metav1.GetOptions{})
	if getErr == nil {
		var typed v1alpha1.FortiGateDNSChangePlan
		if err := v1alpha1.FromUnstructured(existing, &typed); err != nil {
			return nil, err
		}
		if _, err := DocumentFromChangePlan(&typed); err != nil {
			return nil, err
		}
		if !terminalPlanPhase(typed.Status.Phase) && typed.Status.Phase != v1alpha1.ChangePlanStale {
			return &typed, nil
		}
		return nil, fmt.Errorf("change plan name collision with terminal plan %q", typed.Name)
	}
	if !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("get current change plan: %w", getErr)
	}
	if err := s.staleSuperseded(ctx, namespace, document.Target.Name, object.Spec.PlanHash); err != nil {
		return nil, err
	}
	unstructured, err := v1alpha1.ToUnstructured(object)
	if err != nil {
		return nil, err
	}
	created, err := resource.Create(ctx, unstructured, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create change plan: %w", err)
	}
	var createdTyped v1alpha1.FortiGateDNSChangePlan
	if err := v1alpha1.FromUnstructured(created, &createdTyped); err != nil {
		return nil, err
	}
	result, err := s.UpdatePhase(ctx, namespace, createdTyped.Name, v1alpha1.ChangePlanPendingApproval, nil)
	if err != nil {
		return nil, err
	}
	if retention >= 0 {
		if err := s.Prune(ctx, namespace, document.Target.Name, retention); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *ChangePlanStore) RequireExactApproval(object *v1alpha1.FortiGateDNSChangePlan) error {
	if object == nil {
		return fmt.Errorf("change plan is required")
	}
	if object.Status.Phase == v1alpha1.ChangePlanStale || terminalPlanPhase(object.Status.Phase) {
		return fmt.Errorf("change plan phase %q cannot be approved", object.Status.Phase)
	}
	if object.Spec.ExpiresAt != nil && !s.now().Before(object.Spec.ExpiresAt.Time) {
		return fmt.Errorf("change plan expired")
	}
	approved := ""
	if object.Annotations != nil {
		approved = object.Annotations[v1alpha1.ApprovalHashAnnotation]
	}
	if approved == "" {
		return fmt.Errorf("exact plan approval is missing")
	}
	if approved != object.Spec.PlanHash {
		return fmt.Errorf("approved plan hash does not match current plan")
	}
	return nil
}

func (s *ChangePlanStore) UpdatePhase(ctx context.Context, namespace, name string, phase v1alpha1.ChangePlanPhase, outcomes []OperationOutcome) (*v1alpha1.FortiGateDNSChangePlan, error) {
	var result v1alpha1.FortiGateDNSChangePlan
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		resource := s.client.Resource(v1alpha1.ChangePlanGVR).Namespace(namespace)
		current, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := v1alpha1.FromUnstructured(current, &result); err != nil {
			return err
		}
		result.Status.Phase = phase
		result.Status.ObservedGeneration = result.Generation
		result.Status.Outcomes = summarizeOutcomes(outcomes)
		if terminalPlanPhase(phase) {
			now := metav1.NewTime(s.now().UTC())
			result.Status.CompletedAt = &now
		} else {
			result.Status.CompletedAt = nil
		}
		updated, err := v1alpha1.ToUnstructured(&result)
		if err != nil {
			return err
		}
		updated, err = resource.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return v1alpha1.FromUnstructured(updated, &result)
	})
	if err != nil {
		return nil, fmt.Errorf("update change plan status: %w", err)
	}
	return &result, nil
}

// Prune retains the newest terminal plans and never deletes pending, approved,
// applying, or interrupted audit evidence.
func (s *ChangePlanStore) Prune(ctx context.Context, namespace, targetName string, retention int) error {
	if retention < 0 {
		return fmt.Errorf("plan retention cannot be negative")
	}
	items, err := s.listTarget(ctx, namespace, targetName)
	if err != nil {
		return err
	}
	terminal := make([]v1alpha1.FortiGateDNSChangePlan, 0, len(items))
	for _, item := range items {
		if terminalPlanPhase(item.Status.Phase) {
			terminal = append(terminal, item)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		left, right := terminal[i].CreationTimestamp.Time, terminal[j].CreationTimestamp.Time
		if left.Equal(right) {
			return terminal[i].Name > terminal[j].Name
		}
		return left.After(right)
	})
	if len(terminal) <= retention {
		return nil
	}
	for _, item := range terminal[retention:] {
		if err := s.client.Resource(v1alpha1.ChangePlanGVR).Namespace(namespace).Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune terminal change plan %q: %w", item.Name, err)
		}
	}
	return nil
}

func (s *ChangePlanStore) staleSuperseded(ctx context.Context, namespace, targetName, keepHash string) error {
	items, err := s.listTarget(ctx, namespace, targetName)
	if err != nil {
		return err
	}
	for i := range items {
		item := &items[i]
		if item.Spec.PlanHash == keepHash || terminalPlanPhase(item.Status.Phase) || item.Status.Phase == v1alpha1.ChangePlanStale {
			continue
		}
		if _, err := s.UpdatePhase(ctx, namespace, item.Name, v1alpha1.ChangePlanStale, nil); err != nil {
			return fmt.Errorf("mark superseded plan stale: %w", err)
		}
	}
	return nil
}

func (s *ChangePlanStore) listTarget(ctx context.Context, namespace, targetName string) ([]v1alpha1.FortiGateDNSChangePlan, error) {
	list, err := s.client.Resource(v1alpha1.ChangePlanGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list change plans: %w", err)
	}
	items := make([]v1alpha1.FortiGateDNSChangePlan, 0, len(list.Items))
	for i := range list.Items {
		var object v1alpha1.FortiGateDNSChangePlan
		if err := v1alpha1.FromUnstructured(&list.Items[i], &object); err != nil {
			return nil, err
		}
		if object.Spec.TargetRef.Name == targetName {
			items = append(items, object)
		}
	}
	return items, nil
}

func summarizeOutcomes(outcomes []OperationOutcome) []v1alpha1.OperationOutcome {
	result := make([]v1alpha1.OperationOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		state := "failed"
		switch outcome.Result {
		case ApplySucceeded:
			state = "applied"
		case ApplyBlocked:
			state = "skipped"
		}
		reason := outcome.Reason
		if len(reason) > 64 {
			reason = reason[:64]
		}
		result = append(result, v1alpha1.OperationOutcome{OperationID: outcome.OperationID, Result: state, Reason: reason})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].OperationID < result[j].OperationID })
	return result
}

func terminalPlanPhase(phase v1alpha1.ChangePlanPhase) bool {
	switch phase {
	case v1alpha1.ChangePlanSucceeded, v1alpha1.ChangePlanFailed, v1alpha1.ChangePlanStale, v1alpha1.ChangePlanInterrupted:
		return true
	default:
		return false
	}
}

func planObjectName(document Document) string {
	hash, _ := document.ID()
	prefix := labelValue(document.Target.Name)
	if len(prefix) > 45 {
		prefix = strings.Trim(prefix[:45], "-")
	}
	return prefix + "-" + hash[:12]
}

func recurringPlanObjectName(document Document, now time.Time) string {
	base := planObjectName(document)
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	if len(base)+1+len(suffix) > 63 {
		base = strings.Trim(base[:63-1-len(suffix)], "-")
	}
	return base + "-" + suffix
}

func labelValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-.")
	if result == "" {
		return "target"
	}
	return result
}
