package controller

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type semanticDispatcher interface {
	Handle(platformqueue.Event) error
}

func semanticEvent(kind platformqueue.EventKind, action platformqueue.EventAction, oldObject, newObject any) (platformqueue.Event, error) {
	identityObject := newObject
	if identityObject == nil {
		identityObject = oldObject
	}
	identityObject = unwrapDeletedObject(identityObject)
	accessor, err := meta.Accessor(identityObject)
	if err != nil {
		return platformqueue.Event{}, fmt.Errorf("read %s event identity: %w", kind, err)
	}
	event := platformqueue.Event{
		Kind: kind, Action: action, Namespace: accessor.GetNamespace(), Name: accessor.GetName(),
		RelatedTarget: relatedTarget(kind, identityObject),
	}
	if oldObject != nil {
		event.OldFingerprint, err = semanticObjectFingerprint(kind, unwrapDeletedObject(oldObject))
		if err != nil {
			return platformqueue.Event{}, err
		}
	}
	if newObject != nil {
		event.NewFingerprint, err = semanticObjectFingerprint(kind, unwrapDeletedObject(newObject))
		if err != nil {
			return platformqueue.Event{}, err
		}
	}
	return event, nil
}

func semanticObjectFingerprint(kind platformqueue.EventKind, object any) (platformqueue.SemanticFingerprint, error) {
	accessor, err := meta.Accessor(object)
	if err != nil {
		return "", fmt.Errorf("read %s metadata: %w", kind, err)
	}
	fields := platformqueue.SemanticFields{
		"generation": strconv.FormatInt(accessor.GetGeneration(), 10),
		"deleting":   strconv.FormatBool(accessor.GetDeletionTimestamp() != nil),
	}

	switch kind {
	case platformqueue.EventService:
		value, ok := object.(*corev1.Service)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		addSourceMetadata(fields, value.Labels, value.Annotations)
		fields["spec"] = canonicalJSON(value.Spec)
		fields["loadBalancer"] = canonicalSlice(value.Status.LoadBalancer.Ingress)
	case platformqueue.EventIngress:
		value, ok := object.(*networkingv1.Ingress)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		addSourceMetadata(fields, value.Labels, value.Annotations)
		fields["spec"] = canonicalJSON(value.Spec)
		fields["loadBalancer"] = canonicalSlice(value.Status.LoadBalancer.Ingress)
	case platformqueue.EventGateway:
		value, ok := object.(*gatewayv1.Gateway)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		addSourceMetadata(fields, value.Labels, value.Annotations)
		fields["spec"] = canonicalJSON(value.Spec)
		fields["addresses"] = canonicalSlice(value.Status.Addresses)
	case platformqueue.EventHTTPRoute:
		value, ok := object.(*gatewayv1.HTTPRoute)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		addSourceMetadata(fields, value.Labels, value.Annotations)
		fields["spec"] = canonicalJSON(value.Spec)
		fields["parents"] = canonicalSlice(relevantRouteParents(value.Status.Parents))
	case platformqueue.EventEndpointSlice:
		value, ok := object.(*discoveryv1.EndpointSlice)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		fields["serviceName"] = value.Labels[discoveryv1.LabelServiceName]
		fields["addressType"] = string(value.AddressType)
		fields["endpoints"] = canonicalSlice(relevantSliceEndpoints(value.Endpoints))
	case platformqueue.EventSecret:
		value, ok := object.(*corev1.Secret)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		// ResourceVersion detects rotation without hashing or retaining Secret data.
		fields["uid"] = string(value.UID)
		fields["resourceVersion"] = value.ResourceVersion
	case platformqueue.EventTarget, platformqueue.EventPolicy, platformqueue.EventOwnership, platformqueue.EventChangePlan:
		value, ok := object.(*unstructured.Unstructured)
		if !ok {
			return "", unexpectedSemanticType(kind, object)
		}
		fields["spec"] = canonicalJSON(value.Object["spec"])
		switch kind {
		case platformqueue.EventOwnership:
			fields["phase"], _, _ = unstructured.NestedString(value.Object, "status", "phase")
			fields["providerRevision"], _, _ = unstructured.NestedString(value.Object, "status", "observedProviderRevision")
		case platformqueue.EventChangePlan:
			fields["approval"] = value.GetAnnotations()[api.ApprovalHashAnnotation]
		}
	default:
		return "", fmt.Errorf("unsupported semantic event kind %q", kind)
	}
	return platformqueue.NewSemanticFingerprint(kind, fields)
}

func unwrapDeletedObject(object any) any {
	if tombstone, ok := object.(cache.DeletedFinalStateUnknown); ok {
		return tombstone.Obj
	}
	if tombstone, ok := object.(*cache.DeletedFinalStateUnknown); ok && tombstone != nil {
		return tombstone.Obj
	}
	return object
}

func relatedTarget(kind platformqueue.EventKind, object any) string {
	if kind != platformqueue.EventOwnership && kind != platformqueue.EventChangePlan {
		return ""
	}
	value, ok := object.(*unstructured.Unstructured)
	if !ok {
		return ""
	}
	targetName, _, _ := unstructured.NestedString(value.Object, "spec", "targetRef", "name")
	return targetName
}

func addSourceMetadata(fields platformqueue.SemanticFields, labels, annotations map[string]string) {
	fields["labels"] = canonicalJSON(labels)
	fields["annotations"] = canonicalJSON(annotations)
}

func canonicalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func canonicalSlice[T any](values []T) string {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, canonicalJSON(value))
	}
	sort.Strings(encoded)
	return canonicalJSON(encoded)
}

type semanticRouteParent struct {
	ParentRef  gatewayv1.ParentReference `json:"parentRef"`
	Conditions []semanticCondition       `json:"conditions,omitempty"`
}

type semanticCondition struct {
	Type               string                 `json:"type"`
	Status             metav1.ConditionStatus `json:"status"`
	ObservedGeneration int64                  `json:"observedGeneration"`
}

func relevantRouteParents(parents []gatewayv1.RouteParentStatus) []semanticRouteParent {
	result := make([]semanticRouteParent, 0, len(parents))
	for _, parent := range parents {
		entry := semanticRouteParent{ParentRef: parent.ParentRef}
		for _, condition := range parent.Conditions {
			if condition.Type != "Accepted" && condition.Type != "ResolvedRefs" {
				continue
			}
			entry.Conditions = append(entry.Conditions, semanticCondition{
				Type: condition.Type, Status: condition.Status, ObservedGeneration: condition.ObservedGeneration,
			})
		}
		sort.Slice(entry.Conditions, func(i, j int) bool {
			return canonicalJSON(entry.Conditions[i]) < canonicalJSON(entry.Conditions[j])
		})
		result = append(result, entry)
	}
	return result
}

type semanticSliceEndpoint struct {
	Addresses  []string                       `json:"addresses"`
	Conditions discoveryv1.EndpointConditions `json:"conditions"`
}

func relevantSliceEndpoints(endpoints []discoveryv1.Endpoint) []semanticSliceEndpoint {
	result := make([]semanticSliceEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		addresses := append([]string(nil), endpoint.Addresses...)
		sort.Strings(addresses)
		result = append(result, semanticSliceEndpoint{
			Addresses: addresses, Conditions: endpoint.Conditions,
		})
	}
	return result
}

func unexpectedSemanticType(kind platformqueue.EventKind, object any) error {
	return fmt.Errorf("unexpected %s event object %T", kind, object)
}
