package controller

import (
	"strings"
	"testing"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestSemanticAdaptersIgnoreNoiseAndTrackDiscoveryState(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", Generation: 3, Annotations: map[string]string{"external-dns.alpha.kubernetes.io/hostname": "api.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	base := mustSemanticFingerprint(t, platformqueue.EventService, service)
	noise := service.DeepCopy()
	noise.ResourceVersion = "99"
	noise.Status.Conditions = append(noise.Status.Conditions, metav1.Condition{
		Type: "Irrelevant", Status: metav1.ConditionTrue, Reason: "Noise", Message: "ignored", LastTransitionTime: metav1.Now(),
	})
	if got := mustSemanticFingerprint(t, platformqueue.EventService, noise); got != base {
		t.Fatalf("irrelevant Service status changed fingerprint: %s != %s", got, base)
	}
	relevant := service.DeepCopy()
	relevant.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
	if got := mustSemanticFingerprint(t, platformqueue.EventService, relevant); got == base {
		t.Fatal("publishable Service address did not change fingerprint")
	}

	parent := gatewayv1.RouteParentStatus{
		ParentRef: gatewayv1.ParentReference{Name: "edge"},
		Conditions: []metav1.Condition{{
			Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 4,
			Reason: "Accepted", Message: "message one", LastTransitionTime: metav1.NewTime(time.Unix(1, 0)),
		}},
	}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", Generation: 4}}
	route.Status.Parents = []gatewayv1.RouteParentStatus{parent}
	routeBase := mustSemanticFingerprint(t, platformqueue.EventHTTPRoute, route)
	routeNoise := route.DeepCopy()
	routeNoise.Status.Parents[0].Conditions[0].Message = "controller-specific detail"
	routeNoise.Status.Parents[0].Conditions[0].LastTransitionTime = metav1.NewTime(time.Unix(999, 0))
	if got := mustSemanticFingerprint(t, platformqueue.EventHTTPRoute, routeNoise); got != routeBase {
		t.Fatalf("HTTPRoute condition detail changed fingerprint: %s != %s", got, routeBase)
	}
	routeChanged := route.DeepCopy()
	routeChanged.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
	if got := mustSemanticFingerprint(t, platformqueue.EventHTTPRoute, routeChanged); got == routeBase {
		t.Fatal("HTTPRoute acceptance change did not change fingerprint")
	}
}

func TestEndpointSliceAndSecretSemanticBoundaries(t *testing.T) {
	ready := true
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "api-1", Namespace: "apps", Labels: map[string]string{discoveryv1.LabelServiceName: "api"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"203.0.113.10"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
	base := mustSemanticFingerprint(t, platformqueue.EventEndpointSlice, slice)
	noise := slice.DeepCopy()
	noise.Endpoints[0].NodeName = pointerTo("node-a")
	if got := mustSemanticFingerprint(t, platformqueue.EventEndpointSlice, noise); got != base {
		t.Fatalf("unused EndpointSlice node name changed fingerprint: %s != %s", got, base)
	}
	changed := slice.DeepCopy()
	changed.Endpoints[0].Addresses[0] = "203.0.113.11"
	if got := mustSemanticFingerprint(t, platformqueue.EventEndpointSlice, changed); got == base {
		t.Fatal("EndpointSlice address change did not change fingerprint")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "fortigate", Namespace: "dns-system", UID: "secret-uid", ResourceVersion: "1"},
		Data:       map[string][]byte{"token": []byte("super-secret-value")},
	}
	secretBase := mustSemanticFingerprint(t, platformqueue.EventSecret, secret)
	if strings.Contains(string(secretBase), "super-secret-value") {
		t.Fatal("Secret value became a semantic fingerprint")
	}
	dataOnly := secret.DeepCopy()
	dataOnly.Data["token"] = []byte("different-secret-value")
	if got := mustSemanticFingerprint(t, platformqueue.EventSecret, dataOnly); got != secretBase {
		t.Fatal("Secret data, rather than metadata version, affected the fingerprint")
	}
	rotated := secret.DeepCopy()
	rotated.Data["token"] = []byte("different-secret-value")
	rotated.ResourceVersion = "2"
	if got := mustSemanticFingerprint(t, platformqueue.EventSecret, rotated); got == secretBase {
		t.Fatal("Secret metadata rotation did not change fingerprint")
	}
}

func TestDynamicSemanticAdaptersIgnoreTargetStatusAndTrackOwnershipPhase(t *testing.T) {
	target := newAPITarget("dns-system", "edge", []string{"apps"})
	unstructuredTarget, err := api.ToUnstructured(target)
	if err != nil {
		t.Fatal(err)
	}
	base := mustSemanticFingerprint(t, platformqueue.EventTarget, unstructuredTarget)
	statusChanged := unstructuredTarget.DeepCopy()
	statusChanged.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if got := mustSemanticFingerprint(t, platformqueue.EventTarget, statusChanged); got != base {
		t.Fatalf("target status-only update changed fingerprint: %s != %s", got, base)
	}

	ownership := &api.FortiGateDNSRecordOwnership{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "FortiGateDNSRecordOwnership"},
		ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "dns-system", Generation: 1},
		Spec: api.FortiGateDNSRecordOwnershipSpec{
			TargetRef: corev1.LocalObjectReference{Name: "edge"}, Fingerprint: "sha256:abc", ControllerID: "controller-a",
		},
		Status: api.FortiGateDNSRecordOwnershipStatus{Phase: api.OwnershipPhaseReserved},
	}
	unstructuredOwnership, err := api.ToUnstructured(ownership)
	if err != nil {
		t.Fatal(err)
	}
	ownershipBase := mustSemanticFingerprint(t, platformqueue.EventOwnership, unstructuredOwnership)
	confirmed := unstructuredOwnership.DeepCopy()
	if err := unstructured.SetNestedField(confirmed.Object, string(api.OwnershipPhaseConfirmed), "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if got := mustSemanticFingerprint(t, platformqueue.EventOwnership, confirmed); got == ownershipBase {
		t.Fatal("ownership phase change did not change fingerprint")
	}
}

func mustSemanticFingerprint(t *testing.T, kind platformqueue.EventKind, object any) platformqueue.SemanticFingerprint {
	t.Helper()
	fingerprint, err := semanticObjectFingerprint(kind, object)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func pointerTo[T any](value T) *T {
	return &value
}
