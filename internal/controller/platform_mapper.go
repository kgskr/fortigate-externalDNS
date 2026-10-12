package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

type informerTargetMapper struct {
	targets cache.SharedIndexInformer
}

func (m *informerTargetMapper) TargetsForEvent(_ context.Context, event platformqueue.Event) ([]platformqueue.TargetKey, error) {
	if event.Kind == platformqueue.EventTarget {
		key, err := platformqueue.NewTargetKey(event.Namespace, event.Name)
		if err != nil {
			return nil, err
		}
		return []platformqueue.TargetKey{key}, nil
	}
	if event.Kind == platformqueue.EventOwnership || event.Kind == platformqueue.EventChangePlan {
		if event.RelatedTarget == "" {
			return nil, nil
		}
		key, err := platformqueue.NewTargetKey(event.Namespace, event.RelatedTarget)
		if err != nil {
			return nil, err
		}
		return []platformqueue.TargetKey{key}, nil
	}

	targets, err := m.listTargets(false)
	if err != nil {
		return nil, err
	}
	keys := make([]platformqueue.TargetKey, 0, len(targets))
	for i := range targets {
		if !targetAffectedByEvent(&targets[i], event) {
			continue
		}
		key, err := platformqueue.NewTargetKey(targets[i].Namespace, targets[i].Name)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys, nil
}

func (m *informerTargetMapper) ListTargetKeys(context.Context) ([]platformqueue.TargetKey, error) {
	targets, err := m.listTargets(true)
	if err != nil {
		return nil, err
	}
	keys := make([]platformqueue.TargetKey, 0, len(targets))
	for i := range targets {
		key, err := platformqueue.NewTargetKey(targets[i].Namespace, targets[i].Name)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys, nil
}

func (m *informerTargetMapper) ListTargetSchedules(context.Context) ([]platformqueue.TargetSchedule, error) {
	targets, err := m.listTargets(true)
	if err != nil {
		return nil, err
	}
	schedules := make([]platformqueue.TargetSchedule, 0, len(targets))
	for _, target := range targets {
		key, err := platformqueue.NewTargetKey(target.Namespace, target.Name)
		if err != nil {
			return nil, err
		}
		interval := target.Spec.Interval.Duration
		if interval < 0 {
			// Invalid targets are isolated during audit; their bad cadence
			// must not stop the scheduling loop for healthy siblings.
			interval = 0
		}
		schedules = append(schedules, platformqueue.TargetSchedule{
			Key: key, Revision: fmt.Sprintf("%s/%d", target.UID, target.Generation), Interval: interval,
		})
	}
	return schedules, nil
}

func (m *informerTargetMapper) HasTarget(key platformqueue.TargetKey) bool {
	if m == nil || m.targets == nil {
		return false
	}
	_, exists, err := m.targets.GetIndexer().GetByKey(key.String())
	return err == nil && exists
}

// TargetDebounce reads the latest cached spec for each enqueue. An omitted or
// invalid duration falls back to the queue's configured debounce.
func (m *informerTargetMapper) TargetDebounce(key platformqueue.TargetKey) time.Duration {
	if m == nil || m.targets == nil {
		return 0
	}
	item, exists, err := m.targets.GetIndexer().GetByKey(key.String())
	if err != nil || !exists {
		return 0
	}
	object, ok := item.(*unstructured.Unstructured)
	if !ok {
		return 0
	}
	raw, found, err := unstructured.NestedString(object.Object, "spec", "debounce")
	if err != nil || !found {
		return 0
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0
	}
	return duration
}

func (m *informerTargetMapper) listTargets(includeInvalid bool) ([]api.FortiGateDNSTarget, error) {
	if m == nil || m.targets == nil {
		return nil, fmt.Errorf("target informer is unavailable")
	}
	items := m.targets.GetIndexer().List()
	result := make([]api.FortiGateDNSTarget, 0, len(items))
	for _, item := range items {
		object, ok := item.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("target informer returned %T", item)
		}
		var target api.FortiGateDNSTarget
		if err := api.FromUnstructured(object, &target); err != nil {
			if !includeInvalid {
				// A partially decoded spec must never widen source or namespace
				// event mapping through the defaults for omitted fields.
				continue
			}
			// Preserve only identity so initial/periodic audits can report the
			// invalid target and notice its recovery. The zero interval uses
			// the controller fallback without stopping healthy siblings.
			target = api.FortiGateDNSTarget{ObjectMeta: metav1.ObjectMeta{
				Namespace: object.GetNamespace(), Name: object.GetName(),
				UID: object.GetUID(), Generation: object.GetGeneration(),
			}}
		}
		result = append(result, target)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func targetAffectedByEvent(target *api.FortiGateDNSTarget, event platformqueue.Event) bool {
	if target == nil {
		return false
	}
	switch event.Kind {
	case platformqueue.EventSecret:
		if target.Namespace != event.Namespace {
			return false
		}
		if target.Spec.APITokenSecretRef.Name == event.Name {
			return true
		}
		return target.Spec.CARef != nil && strings.EqualFold(target.Spec.CARef.Kind, "Secret") && target.Spec.CARef.Name == event.Name
	case platformqueue.EventPolicy:
		return namespaceSelected(target.Spec.Namespaces, event.Namespace)
	case platformqueue.EventService:
		return sourceSelected(target.Spec.Sources, "service") && namespaceSelected(target.Spec.Namespaces, event.Namespace)
	case platformqueue.EventEndpointSlice:
		return target.Spec.HeadlessEnabled && sourceSelected(target.Spec.Sources, "service") && namespaceSelected(target.Spec.Namespaces, event.Namespace)
	case platformqueue.EventIngress:
		return sourceSelected(target.Spec.Sources, "ingress") && namespaceSelected(target.Spec.Namespaces, event.Namespace)
	case platformqueue.EventGateway:
		return sourceSelected(target.Spec.Sources, "gateway") &&
			(namespaceSelected(target.Spec.Namespaces, event.Namespace) || containsFold(target.Spec.GatewayTargetNamespaces, event.Namespace))
	case platformqueue.EventHTTPRoute:
		return sourceSelected(target.Spec.Sources, "gateway") && namespaceSelected(target.Spec.Namespaces, event.Namespace)
	default:
		return false
	}
}

func sourceSelected(sources []string, source string) bool {
	if len(sources) == 0 {
		return source == "service" || source == "ingress" || source == "gateway"
	}
	return containsFold(sources, source)
}

func namespaceSelected(namespaces []string, namespace string) bool {
	return len(namespaces) == 0 || containsFold(namespaces, namespace)
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}
