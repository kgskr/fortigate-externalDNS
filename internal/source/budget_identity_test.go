package source

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestServiceExpansionRejectsWholeResourceAndKeepsSibling(t *testing.T) {
	opts := testOptions()
	opts.MaxEndpointsPerResource = 100
	large := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "large", Namespace: "apps", UID: types.UID("large-uid"), Annotations: map[string]string{AnnotationHostname: numberedHostnames(64)}}, Spec: corev1.ServiceSpec{ExternalIPs: numberedIPs(64)}}
	small := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "small", Namespace: "apps", UID: types.UID("small-uid"), Annotations: map[string]string{AnnotationHostname: "small.example.com"}}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}

	client := fake.NewSimpleClientset(large, small)
	result, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].DNSName != "small.example.com" {
		t.Fatalf("oversized resource must publish nothing while sibling remains: %#v", result.Endpoints)
	}
	if result.SourceComplete(SourceService) || !hasEventContaining(result, "64 hostnames x 64 targets") {
		t.Fatalf("oversized Service must mark cleanup unsafe with bounded evidence: %#v", result)
	}
}

func TestSiblingSourceFormsUseSameExpansionBudget(t *testing.T) {
	opts := testOptions()
	opts.MaxEndpointsPerResource = 1
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: types.UID("ingress-uid")}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "a.example.com"}, {Host: "b.example.com"}}}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}}}}}
	if result := EndpointsFromIngress(ingress, opts); len(result.Endpoints) != 0 || result.SourceComplete(SourceIngress) {
		t.Fatalf("Ingress must reject the whole expansion: %#v", result)
	}

	hostA, hostB := gatewayv1.Hostname("a.example.com"), gatewayv1.Hostname("b.example.com")
	gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "apps", UID: types.UID("gateway-uid")}, Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{{Name: "a", Hostname: &hostA}, {Name: "b", Hostname: &hostB}}}, Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.1"}}}}
	if result := EndpointsFromGateway(gateway, opts); len(result.Endpoints) != 0 || result.SourceComplete(SourceGateway) {
		t.Fatalf("Gateway must reject the whole expansion: %#v", result)
	}
}

func TestExcludedHostsDoNotConsumeExpansionBudget(t *testing.T) {
	opts := testOptions()
	opts.MaxEndpointsPerResource = 1
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: types.UID("service-uid"), Annotations: map[string]string{AnnotationHostname: "outside.invalid,web.example.com"}}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}
	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 1 || result.Endpoints[0].DNSName != "web.example.com" || !result.SourceComplete(SourceService) {
		t.Fatalf("excluded hostname must not consume the source budget: %#v", result)
	}
}

func TestMetadataIsKeptForEveryPublishedSourceAndSkippedForNonPublishingSources(t *testing.T) {
	opts := testOptions()
	opts.MaxEndpointsPerDiscovery = 2
	published := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "published", Namespace: "apps", UID: types.UID("published-uid"), Labels: map[string]string{"policy": "deny"}, Annotations: map[string]string{AnnotationHostname: "published.example.com"}}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}
	nonPublishing := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "apps", UID: types.UID("idle-uid"), Labels: map[string]string{"noise": "true"}}}
	client := fake.NewSimpleClientset(nonPublishing, published)
	result, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	metadata := result.MetadataFor(result.Endpoints[0].Source)
	if metadata.Labels["policy"] != "deny" || len(result.Metadata) != 1 {
		t.Fatalf("published source metadata must be complete without retaining idle-object metadata: %#v", result.Metadata)
	}
}

func TestDiscoverCancellationStopsBeforePublication(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: types.UID("service-uid"), Annotations: map[string]string{AnnotationHostname: "web.example.com"}}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Discover(ctx, KubernetesClients{Core: fake.NewSimpleClientset(service), Gateway: gatewayfake.NewSimpleClientset()}, testOptions())
	if err == nil || len(result.Endpoints) != 0 {
		t.Fatalf("canceled discovery must stop without publication: result=%#v err=%v", result, err)
	}
}

func TestTypedSourcesPreserveCanonicalIdentity(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: types.UID("service-uid"), Annotations: map[string]string{AnnotationHostname: "web.example.com"}}, Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}
	result := EndpointsFromService(service, testOptions())
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected endpoint: %#v", result)
	}
	ref := result.Endpoints[0].Source
	if ref.APIVersion != "v1" || ref.Kind != "Service" || ref.Namespace != "apps" || ref.Name != "web" || ref.UID != "service-uid" || ref.String() != "Service/apps/web" {
		t.Fatalf("unexpected canonical source identity: %#v", ref)
	}
}

func numberedHostnames(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprintf("h-%d.example.com", i)
	}
	return strings.Join(values, ",")
}

func numberedIPs(count int) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprintf("192.0.2.%d", i+1)
	}
	return values
}
