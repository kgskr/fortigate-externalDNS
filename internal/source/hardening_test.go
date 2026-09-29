package source

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func routeWithStatus(generation int64, hostnames []string, parent gatewayv1.ParentReference, conditions []metav1.Condition) *gatewayv1.HTTPRoute {
	var hosts []gatewayv1.Hostname
	for _, h := range hostnames {
		hosts = append(hosts, gatewayv1.Hostname(h))
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps", Generation: generation},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames:       hosts,
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
		},
	}
	if conditions != nil {
		route.Status.Parents = []gatewayv1.RouteParentStatus{{ParentRef: parent, ControllerName: "example.com/controller", Conditions: conditions}}
	}
	return route
}

func conds(accepted, resolved metav1.ConditionStatus, generation int64) []metav1.Condition {
	return []metav1.Condition{
		{Type: "Accepted", Status: accepted, ObservedGeneration: generation},
		{Type: "ResolvedRefs", Status: resolved, ObservedGeneration: generation},
	}
}

func gatewayWith(listeners ...gatewayv1.Listener) map[string]*gatewayv1.Gateway {
	return map[string]*gatewayv1.Gateway{GatewayMapKey("apps", "public"): {
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"},
		Spec:       gatewayv1.GatewaySpec{Listeners: listeners},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.40"}}},
	}}
}

func listener(name, hostname string, port int32) gatewayv1.Listener {
	l := gatewayv1.Listener{Name: gatewayv1.SectionName(name), Port: gatewayv1.PortNumber(port), Protocol: gatewayv1.HTTPProtocolType}
	if hostname != "" {
		h := gatewayv1.Hostname(hostname)
		l.Hostname = &h
	}
	return l
}

func TestHTTPRouteStatusFreshness(t *testing.T) {
	parent := gatewayv1.ParentReference{Name: "public"}
	gws := gatewayWith(listener("any", "", 80))
	tests := []struct {
		name       string
		route      *gatewayv1.HTTPRoute
		wantHosts  int
		incomplete bool
	}{
		{"missing status", routeWithStatus(2, []string{"a.example.com"}, parent, nil), 0, true},
		{"stale status", routeWithStatus(2, []string{"a.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1)), 0, true},
		{"current accepted=false", routeWithStatus(2, []string{"a.example.com"}, parent, conds(metav1.ConditionFalse, metav1.ConditionTrue, 2)), 0, false},
		{"current resolvedrefs=false", routeWithStatus(2, []string{"a.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionFalse, 2)), 0, false},
		{"current accepted", routeWithStatus(2, []string{"a.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 2)), 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EndpointsFromHTTPRoute(tc.route, gws, testOptions())
			if len(result.Endpoints) != tc.wantHosts {
				t.Fatalf("endpoints = %#v", result.Endpoints)
			}
			if result.SourceComplete(SourceGateway) == tc.incomplete {
				t.Fatalf("incomplete = %v, want %v", !result.SourceComplete(SourceGateway), tc.incomplete)
			}
			if len(result.Events) == 0 && tc.wantHosts == 0 {
				t.Fatal("expected an event")
			}
		})
	}
}

func TestHTTPRouteHostnameIntersection(t *testing.T) {
	parent := gatewayv1.ParentReference{Name: "public"}
	sectionHTTPS := gatewayv1.SectionName("https")
	port8080 := gatewayv1.PortNumber(8080)
	tests := []struct {
		name      string
		hostnames []string
		parent    gatewayv1.ParentReference
		listeners []gatewayv1.Listener
		want      []string
	}{
		{"takeover dropped", []string{"a.example.com", "login.example.com"}, parent, []gatewayv1.Listener{listener("l", "a.example.com", 80)}, []string{"a.example.com"}},
		{"nil listener hostname matches all", []string{"a.example.com", "b.example.com"}, parent, []gatewayv1.Listener{listener("l", "", 80)}, []string{"a.example.com", "b.example.com"}},
		{"wildcard listener matches subdomain and nested", []string{"foo.example.com", "a.b.example.com"}, parent, []gatewayv1.Listener{listener("l", "*.example.com", 80)}, []string{"a.b.example.com", "foo.example.com"}},
		{"wildcard listener not apex", []string{"example.com"}, parent, []gatewayv1.Listener{listener("l", "*.example.com", 80)}, nil},
		{"wildcard listener not similar suffix", []string{"fooexample.com"}, parent, []gatewayv1.Listener{listener("l", "*.example.com", 80)}, nil},
		{"section name respected", []string{"a.example.com", "b.example.com"}, gatewayv1.ParentReference{Name: "public", SectionName: &sectionHTTPS},
			[]gatewayv1.Listener{listener("http", "a.example.com", 80), listener("https", "b.example.com", 443)}, []string{"b.example.com"}},
		{"port respected", []string{"a.example.com", "b.example.com"}, gatewayv1.ParentReference{Name: "public", Port: &port8080},
			[]gatewayv1.Listener{listener("x", "a.example.com", 80), listener("y", "b.example.com", 8080)}, []string{"b.example.com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route := routeWithStatus(1, tc.hostnames, tc.parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1))
			result := EndpointsFromHTTPRoute(route, gatewayWith(tc.listeners...), testOptions())
			var got []string
			for _, e := range result.Endpoints {
				got = append(got, e.DNSName)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hosts = %v, want %v (events %#v)", got, tc.want, result.Events)
			}
			if !result.SourceComplete(SourceGateway) {
				t.Fatal("dropped hostnames must not mark the source incomplete")
			}
		})
	}
}

func TestIntersectHostname(t *testing.T) {
	tests := []struct {
		route, listener, want string
		ok                    bool
	}{
		{"*.example.com", "a.example.com", "a.example.com", true},
		{"*.example.com", "a.b.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", "", false},
		{"*.example.com", "*.example.com", "*.example.com", true},
		{"*.example.com", "*.a.example.com", "*.a.example.com", true},
		{"*.a.example.com", "*.example.com", "*.a.example.com", true},
		{"*.example.com", "*.example.org", "", false},
		{"a.example.com", "b.example.com", "", false},
	}
	for _, tc := range tests {
		got, ok := intersectHostname(tc.route, tc.listener)
		if got != tc.want || ok != tc.ok {
			t.Errorf("intersectHostname(%q,%q) = %q,%v want %q,%v", tc.route, tc.listener, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHostnameValidation(t *testing.T) {
	svc := func(hostname string) *corev1.Service {
		return loadBalancerService("web", "apps", hostname, "203.0.113.5")
	}
	tests := []struct {
		name, annotation string
		want             []string
	}{
		{"space separated", "a.example.com b.example.com", nil},
		{"underscore", "_x.example.com", nil},
		{"empty label", "a..example.com", nil},
		{"too long", strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + "." + strings.Repeat("e", 20) + ".example.com", nil},
		{"unicode converted", "bücher.example.com", []string{"xn--bcher-kva.example.com"}},
		{"valid", "Good.Example.com.", []string{"good.example.com"}},
		{"apex skipped", "example.com", nil},
		{"mixed", "ok.example.com,bad host.example.com", []string{"ok.example.com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EndpointsFromService(svc(tc.annotation), testOptions())
			var got []string
			for _, e := range result.Endpoints {
				got = append(got, e.DNSName)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hosts = %v, want %v", got, tc.want)
			}
			if !result.SourceComplete(SourceService) {
				t.Fatal("invalid names must not mark the source incomplete")
			}
			if len(tc.want) < 1 && len(result.Events) == 0 {
				t.Fatal("expected a warning event")
			}
		})
	}
}

func TestValidHostname(t *testing.T) {
	for _, ok := range []string{"a.example.com", "*.example.com", "xn--bcher-kva.example.com"} {
		if !validHostname(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "*", "*.", "**.example.com", "a.*.example.com", "-a.example.com", "a b.example.com"} {
		if validHostname(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestLoadBalancerHostnameTargetValidation(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: "web.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{Hostname: "bad_host.example.net"}, {Hostname: "LB.Example.net."},
		}}},
	}
	result := EndpointsFromService(svc, testOptions())
	if len(result.Endpoints) != 1 || result.Endpoints[0].Targets[0] != "lb.example.net" {
		t.Fatalf("endpoints = %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "not a valid DNS hostname") {
		t.Fatalf("events = %#v", result.Events)
	}
}

func TestTTLAnnotationDurations(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{"300", 300, true}, {"5m", 300, true}, {"1h30m", 5400, true}, {"90s", 90, true},
		{"1", 1, true}, {"604800", 604800, true}, {"168h", 604800, true},
		{"500ms", 0, false}, {"1.5s", 0, false}, {"1500ms", 0, false}, {"0", 0, false}, {"0s", 0, false},
		{"-5m", 0, false}, {"169h", 0, false}, {"604801", 0, false}, {"abc", 0, false},
	}
	for _, tc := range tests {
		got, err := TTLFromAnnotations(map[string]string{AnnotationTTL: tc.value}, 60)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("TTL %q = %d, %v; want %d ok=%v", tc.value, got, err, tc.want, tc.ok)
		}
	}
}
