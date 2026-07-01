package source

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestServiceEndpointExtraction(t *testing.T) {
	opts := testOptions()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "apps",
			Annotations: map[string]string{
				AnnotationHostname: "web.example.com.",
				AnnotationTTL:      "120",
			},
		},
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}}},
	}

	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Endpoints))
	}
	got := result.Endpoints[0]
	if got.DNSName != "web.example.com" || got.RecordType != "A" || got.TTL != 120 || got.Targets[0] != "203.0.113.10" {
		t.Fatalf("unexpected endpoint: %#v", got)
	}
}

func TestIngressEndpointExtractionUsesRulesAndAnnotations(t *testing.T) {
	opts := testOptions()
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ing",
			Namespace: "apps",
			Annotations: map[string]string{
				AnnotationHostnameAlpha: "annotated.example.com",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{Host: "rule.example.com"}},
		},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.net"}}}},
	}

	result := EndpointsFromIngress(ingress, opts)
	if len(result.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(result.Endpoints))
	}
	for _, endpoint := range result.Endpoints {
		if endpoint.RecordType != "CNAME" || endpoint.Targets[0] != "lb.example.net" {
			t.Fatalf("unexpected endpoint: %#v", endpoint)
		}
	}
}

func TestGatewayAndHTTPRouteExtraction(t *testing.T) {
	opts := testOptions()
	hostname := gatewayv1.Hostname("gateway.example.com")
	routeHostname := gatewayv1.Hostname("route.example.com")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{{Name: "http", Hostname: &hostname}},
		},
		Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.20"}}},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames:       []gatewayv1.Hostname{routeHostname},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "public"}}},
		},
		Status: acceptedHTTPRouteStatus(),
	}

	gatewayResult := EndpointsFromGateway(gateway, opts)
	if len(gatewayResult.Endpoints) != 1 || gatewayResult.Endpoints[0].DNSName != "gateway.example.com" {
		t.Fatalf("unexpected gateway result: %#v", gatewayResult)
	}

	routeResult := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{GatewayMapKey("apps", "public"): gateway}, opts)
	if len(routeResult.Endpoints) != 1 || routeResult.Endpoints[0].DNSName != "route.example.com" {
		t.Fatalf("unexpected route result: %#v", routeResult)
	}
}

func TestHTTPRouteWithoutAcceptedParentDoesNotPublish(t *testing.T) {
	opts := testOptions()
	routeHostname := gatewayv1.Hostname("route.example.com")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames:       []gatewayv1.Hostname{routeHostname},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "public"}}},
		},
	}

	result := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{}, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("expected no endpoints for unaccepted route, got %#v", result.Endpoints)
	}
	if len(result.Events) != 1 {
		t.Fatalf("expected route status event, got %#v", result.Events)
	}
}

func TestHTTPRouteUsesOnlyAcceptedParentTargets(t *testing.T) {
	opts := testOptions()
	routeHostname := gatewayv1.Hostname("route.example.com")
	acceptedGateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "accepted", Namespace: "apps"},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.40"}}},
	}
	rejectedGateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "rejected", Namespace: "apps"},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.99"}}},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{routeHostname},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{
				{Name: "accepted"},
				{Name: "rejected"},
			}},
		},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef:      gatewayv1.ParentReference{Name: "accepted"},
			ControllerName: gatewayv1.GatewayController("example.com/controller"),
			Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue},
				{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
			},
		}, {
			ParentRef:      gatewayv1.ParentReference{Name: "rejected"},
			ControllerName: gatewayv1.GatewayController("example.com/controller"),
			Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionFalse},
				{Type: "ResolvedRefs", Status: metav1.ConditionFalse},
			},
		}}}},
	}

	result := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{
		GatewayMapKey("apps", "accepted"): acceptedGateway,
		GatewayMapKey("apps", "rejected"): rejectedGateway,
	}, opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected one endpoint from accepted parent, got %#v", result.Endpoints)
	}
	if got := result.Endpoints[0].Targets[0]; got != "203.0.113.40" {
		t.Fatalf("unexpected target from rejected parent: %s", got)
	}
}

func TestHTTPRouteAcceptedParentKeyIncludesKindGroupAndPort(t *testing.T) {
	opts := testOptions()
	routeHostname := gatewayv1.Hostname("route.example.com")
	serviceKind := gatewayv1.Kind("Service")
	coreGroup := gatewayv1.Group("")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "apps"},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.40"}}},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{routeHostname},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{
				{Name: "shared"},
				{Name: "shared", Kind: &serviceKind, Group: &coreGroup},
			}},
		},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef:      gatewayv1.ParentReference{Name: "shared", Kind: &serviceKind, Group: &coreGroup},
			ControllerName: gatewayv1.GatewayController("example.com/controller"),
			Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue},
				{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
			},
		}}}},
	}

	result := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{
		GatewayMapKey("apps", "shared"): gateway,
	}, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("accepted Service parent must not authorize Gateway target: %#v", result.Endpoints)
	}
	if len(result.Events) != 1 {
		t.Fatalf("expected missing target event after skipping unaccepted Gateway parent, got %#v", result.Events)
	}
}

func TestFiltersAndMissingTargets(t *testing.T) {
	opts := testOptions()
	opts.DomainFilters = []string{"allowed.example.com"}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "apps",
			Annotations: map[string]string{
				AnnotationHostname: "skip.example.net,keep.allowed.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}

	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("expected no endpoints without target, got %#v", result.Endpoints)
	}
	if len(result.Events) != 1 || !strings.Contains(result.Events[0].Message, "status address") {
		t.Fatalf("expected one LoadBalancer-pending warning, got %#v", result.Events)
	}
}

func TestUnsupportedServiceTypeReported(t *testing.T) {
	opts := testOptions()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal",
			Namespace: "apps",
			Annotations: map[string]string{
				AnnotationHostname: "internal.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
	}

	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("ClusterIP service must not publish a record, got %#v", result.Endpoints)
	}
	if len(result.Events) != 1 || !strings.Contains(result.Events[0].Message, "ClusterIP") {
		t.Fatalf("expected a warning naming the unsupported ClusterIP type, got %#v", result.Events)
	}
}

func TestMultipleServiceTargetsProduceSeparateEndpoints(t *testing.T) {
	opts := testOptions()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "apps",
			Annotations: map[string]string{
				AnnotationHostname: "web.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{IP: "203.0.113.10"},
			{IP: "203.0.113.11"},
		}}},
	}

	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 2 {
		t.Fatalf("expected one endpoint per FortiGate record target, got %#v", result.Endpoints)
	}
}

func TestUnsupportedSourcesAreNotConfigured(t *testing.T) {
	opts := testOptions()
	for _, unsupported := range []string{"istio", "linkerd", "consul", "kuma", "crd"} {
		if opts.SourceEnabled(unsupported) {
			t.Fatalf("unsupported source %q unexpectedly enabled", unsupported)
		}
	}
}

func acceptedHTTPRouteStatus() gatewayv1.HTTPRouteStatus {
	return gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
		ParentRef:      gatewayv1.ParentReference{Name: "public"},
		ControllerName: gatewayv1.GatewayController("example.com/controller"),
		Conditions: []metav1.Condition{
			{Type: "Accepted", Status: metav1.ConditionTrue},
			{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
		},
	}}}}
}

func TestGatewayTargetNamespaceLookupScopeIsSeparateFromCleanup(t *testing.T) {
	opts := Options{Namespaces: []string{"apps"}, GatewayTargetNamespaces: []string{"infra"}}
	infra := gatewayv1.Namespace("infra")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "shared", Namespace: &infra}}},
		},
	}

	namespaces := gatewayNamespacesForList(opts, []*gatewayv1.HTTPRoute{route})
	if strings.Join(namespaces, ",") != "apps,infra" {
		t.Fatalf("expected gateway lookup over apps and infra, got %v", namespaces)
	}

	// The infra namespace is a target-lookup scope only; it must never be treated
	// as an ownership/cleanup namespace.
	if opts.NamespaceAllowed("infra") {
		t.Fatal("target lookup namespace must not enter ownership/cleanup scope")
	}
	if !opts.GatewayTargetNamespaceAllowed("infra") {
		t.Fatal("infra should be recognized as a target lookup namespace")
	}
}

func TestOutOfZoneHostnameIsNotPublished(t *testing.T) {
	opts := testOptions()
	opts.DomainFilters = nil // isolate the zone check as the only gate
	result := EndpointsFromService(loadBalancerService("web", "apps", "web.other.com", "203.0.113.10"), opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("out-of-zone hostname must not be published, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "outside the configured FortiGate zone") {
		t.Fatalf("expected an out-of-zone warning, got %#v", result.Events)
	}
}

func TestInZoneHostnameIsPublished(t *testing.T) {
	opts := testOptions()
	opts.DomainFilters = nil
	result := EndpointsFromService(loadBalancerService("web", "apps", "web.example.com", "203.0.113.10"), opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("in-zone hostname should publish, got %#v", result.Endpoints)
	}
}

func TestWildcardHostnameIsNotPublished(t *testing.T) {
	opts := testOptions()
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing", Namespace: "apps"},
		Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "*.example.com"}}},
		Status:     networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.10"}}}},
	}
	result := EndpointsFromIngress(ingress, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("wildcard hostname must not be published, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "wildcard") {
		t.Fatalf("expected a wildcard warning, got %#v", result.Events)
	}
}

func TestLoadBalancerStatusPrefersHostnameOverIP(t *testing.T) {
	opts := testOptions()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: "web.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10", Hostname: "lb.example.net"}}}},
	}
	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("a status entry with both IP and hostname must yield one target, got %#v", result.Endpoints)
	}
	if got := result.Endpoints[0]; got.RecordType != "CNAME" || got.Targets[0] != "lb.example.net" {
		t.Fatalf("hostname should be preferred over IP, got %#v", got)
	}
}

func TestHostnamelessHTTPRouteEmitsEvent(t *testing.T) {
	opts := testOptions()
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec:       gatewayv1.HTTPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "public"}}}},
	}
	result := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{}, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("a hostname-less route publishes nothing, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "no hostnames") {
		t.Fatalf("expected a hostname-less diagnostic event, got %#v", result.Events)
	}
}

func TestMixedIPAndHostnameStatusPublishesHostnamesOnly(t *testing.T) {
	opts := testOptions()
	// One status entry carries only an IP, another only a hostname, and an
	// ExternalIP is also set: a single name must not get both A and CNAME.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: "web.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ExternalIPs: []string{"203.0.113.99"}},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{IP: "203.0.113.10"},
			{Hostname: "lb.example.net"},
		}}},
	}
	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("mixed IP/hostname status must publish hostnames only (one target), got %#v", result.Endpoints)
	}
	if got := result.Endpoints[0]; got.RecordType != "CNAME" || got.Targets[0] != "lb.example.net" {
		t.Fatalf("expected a single CNAME to the hostname, got %#v", got)
	}
}

func loadBalancerService(name, namespace, hostname, ip string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{AnnotationHostname: hostname}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: ip}}}},
	}
}

func hasEventContaining(result Result, substr string) bool {
	for _, event := range result.Events {
		if strings.Contains(event.Message, substr) {
			return true
		}
	}
	return false
}

func testOptions() Options {
	return Options{
		Sources:       []string{SourceService, SourceIngress, SourceGateway},
		Namespaces:    []string{"apps"},
		DomainFilters: []string{"example.com"},
		DefaultTTL:    300,
		Zone:          "example.com",
		OwnerID:       "cluster-a",
	}
}
