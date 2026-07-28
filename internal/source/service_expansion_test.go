package source

import (
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExternalNamePublicationIsDisabledByDefault(t *testing.T) {
	service := externalNameService("alias.example.com", "Backend.Example.NET.")
	result := EndpointsFromService(service, testOptions())
	if len(result.Endpoints) != 0 {
		t.Fatalf("ExternalName must remain disabled by default, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "disabled") {
		t.Fatalf("expected disabled diagnostic, got %#v", result.Events)
	}
}

func TestExternalNamePublishesNormalizedCNAMEWithTTL(t *testing.T) {
	opts := testOptions()
	opts.PublishExternalNameServices = true
	service := externalNameService("Alias.Example.COM.", "Backend.Example.NET.")
	service.Annotations[AnnotationTTL] = "120"
	// A malformed object or permissive API server must not let ExternalIPs mix
	// A/AAAA targets into the ExternalName CNAME publication path.
	service.Spec.ExternalIPs = []string{"192.0.2.99"}

	result := EndpointsFromService(service, opts)
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected one ExternalName endpoint, got endpoints=%#v events=%#v", result.Endpoints, result.Events)
	}
	got := result.Endpoints[0]
	if got.DNSName != "alias.example.com" || got.RecordType != "CNAME" || got.Targets[0] != "backend.example.net" || got.TTL != 120 {
		t.Fatalf("unexpected ExternalName endpoint: %#v", got)
	}
}

func TestExternalNameRejectsIPMalformedAndPolicyDeniedTargets(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		policy  ServicePublicationPolicy
		message string
	}{
		{name: "IPv4", target: "192.0.2.10", message: "not an IP address"},
		{name: "IPv6", target: "2001:db8::10", message: "not an IP address"},
		{name: "malformed", target: "bad_target.example.net", message: "not a valid DNS hostname"},
		{
			name:    "policy deny",
			target:  "backend.example.net",
			policy:  func(ServicePublicationContext) PublicationDecision { return PublicationDeny },
			message: "policy denied",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions()
			opts.PublishExternalNameServices = true
			opts.ServicePublicationPolicy = tc.policy
			result := EndpointsFromService(externalNameService("alias.example.com", tc.target), opts)
			if len(result.Endpoints) != 0 {
				t.Fatalf("invalid/denied ExternalName must not publish, got %#v", result.Endpoints)
			}
			if !hasEventContaining(result, tc.message) {
				t.Fatalf("expected %q diagnostic, got %#v", tc.message, result.Events)
			}
		})
	}
}

func TestHeadlessPublicationRequiresFeatureAndAnnotationOrPolicy(t *testing.T) {
	service := headlessService("db", true)
	slice := endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.10"}, boolPtr(true), nil, nil))

	result := EndpointsFromServiceWithEndpointSlices(service, []*discoveryv1.EndpointSlice{slice}, testOptions())
	if len(result.Endpoints) != 0 || !hasEventContaining(result, "disabled") {
		t.Fatalf("headless feature must be disabled by default, got %#v", result)
	}

	opts := testOptions()
	opts.PublishHeadlessServices = true
	delete(service.Annotations, AnnotationPublishHeadless)
	result = EndpointsFromServiceWithEndpointSlices(service, []*discoveryv1.EndpointSlice{slice}, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("enabled headless feature still requires an annotation or policy grant, got %#v", result.Endpoints)
	}

	opts.ServicePublicationPolicy = func(context ServicePublicationContext) PublicationDecision {
		if context.Mode == ServicePublicationHeadless && context.Name == "db" {
			return PublicationAllow
		}
		return PublicationUnspecified
	}
	result = EndpointsFromServiceWithEndpointSlices(service, []*discoveryv1.EndpointSlice{slice}, opts)
	if len(result.Endpoints) != 1 || result.Endpoints[0].Targets[0] != "192.0.2.10" {
		t.Fatalf("policy grant should publish headless endpoints, got %#v", result)
	}

	service.Annotations[AnnotationPublishHeadless] = "true"
	opts.ServicePublicationPolicy = func(ServicePublicationContext) PublicationDecision { return PublicationDeny }
	result = EndpointsFromServiceWithEndpointSlices(service, []*discoveryv1.EndpointSlice{slice}, opts)
	if len(result.Endpoints) != 0 || !hasEventContaining(result, "policy denied") {
		t.Fatalf("policy deny must override annotation grant, got %#v", result)
	}
}

func TestMalformedHeadlessOptInFailsClosed(t *testing.T) {
	opts := testOptions()
	opts.PublishHeadlessServices = true
	service := headlessService("db", false)
	service.Annotations[AnnotationPublishHeadless] = "sometimes"

	result := EndpointsFromServiceWithEndpointSlices(service, nil, opts)
	if len(result.Endpoints) != 0 || !result.SourceComplete(SourceService) || !hasEventContaining(result, "must be true or false") {
		t.Fatalf("malformed headless opt-in must reject only the observed Service, got %#v", result)
	}
}

func TestHeadlessDualStackReadinessAndDeterministicDeduplication(t *testing.T) {
	opts := testOptions()
	opts.PublishHeadlessServices = true
	service := headlessService("db", true)

	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("z-v6", "db", discoveryv1.AddressTypeIPv6,
			endpoint([]string{"2001:db8::20", "2001:0db8::20"}, boolPtr(true), nil, nil)),
		endpointSlice("a-v4", "db", discoveryv1.AddressTypeIPv4,
			endpoint([]string{"192.0.2.20", "192.0.2.10", "192.0.2.10"}, boolPtr(true), nil, nil),
			endpoint([]string{"192.0.2.30"}, boolPtr(false), boolPtr(true), nil),
			endpoint([]string{"192.0.2.40"}, nil, boolPtr(true), nil),
			endpoint([]string{"192.0.2.50"}, nil, boolPtr(false), nil),
			endpoint([]string{"192.0.2.60"}, nil, boolPtr(true), boolPtr(true))),
	}

	result := EndpointsFromServiceWithEndpointSlices(service, slices, opts)
	got := endpointTargets(result)
	want := []string{"192.0.2.10", "192.0.2.20", "192.0.2.40", "2001:db8::20"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected ready/unknown dual-stack targets: got %v want %v; events=%#v", got, want, result.Events)
	}
	if result.Endpoints[0].Key() > result.Endpoints[len(result.Endpoints)-1].Key() {
		t.Fatalf("headless endpoints must be emitted deterministically: %#v", result.Endpoints)
	}
}

func TestHeadlessPublishNotReadyIncludesUnreadyAndTerminatingEndpoints(t *testing.T) {
	opts := testOptions()
	opts.PublishHeadlessServices = true
	service := headlessService("db", true)
	service.Spec.PublishNotReadyAddresses = true
	slice := endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.30"}, boolPtr(false), boolPtr(false), nil),
		endpoint([]string{"192.0.2.60"}, boolPtr(false), boolPtr(true), boolPtr(true)))

	result := EndpointsFromServiceWithEndpointSlices(service, []*discoveryv1.EndpointSlice{slice}, opts)
	if got := strings.Join(endpointTargets(result), ","); got != "192.0.2.30,192.0.2.60" {
		t.Fatalf("publishNotReadyAddresses must include valid unready/terminating addresses, got %s", got)
	}
}

func TestHeadlessRejectsWrongAddressFamilyAndDoesNotDeriveSRV(t *testing.T) {
	opts := testOptions()
	opts.PublishHeadlessServices = true
	service := headlessService("db", true)
	hostname := "pod-0"
	portName := "database"
	port := int32(5432)
	protocol := corev1.ProtocolTCP
	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("bad-v4", "db", discoveryv1.AddressTypeIPv4,
			endpoint([]string{"2001:db8::1"}, boolPtr(true), nil, nil)),
		{
			ObjectMeta:  metav1.ObjectMeta{Name: "fqdn", Namespace: "apps", Labels: map[string]string{discoveryv1.LabelServiceName: "db"}},
			AddressType: discoveryv1.AddressTypeFQDN,
			Ports:       []discoveryv1.EndpointPort{{Name: &portName, Port: &port, Protocol: &protocol}},
			Endpoints:   []discoveryv1.Endpoint{{Hostname: &hostname, Addresses: []string{"pod-0.example.net"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}}},
		},
	}

	result := EndpointsFromServiceWithEndpointSlices(service, slices, opts)
	if len(result.Endpoints) != 0 {
		t.Fatalf("invalid family and FQDN/SRV data must not produce records, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "declared IP address family") || !hasEventContaining(result, "only IPv4 and IPv6") {
		t.Fatalf("expected bounded address-family diagnostics, got %#v", result.Events)
	}
}

func TestUnsupportedNodePortAndOrdinaryClusterIPStayExplicit(t *testing.T) {
	for _, serviceType := range []corev1.ServiceType{corev1.ServiceTypeNodePort, corev1.ServiceTypeClusterIP} {
		t.Run(string(serviceType), func(t *testing.T) {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "unsupported", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: "unsupported.example.com"}},
				Spec:       corev1.ServiceSpec{Type: serviceType, ClusterIP: "10.96.0.10"},
			}
			result := EndpointsFromService(service, testOptions())
			if len(result.Endpoints) != 0 || !hasEventContaining(result, string(serviceType)) {
				t.Fatalf("unsupported %s mode must remain explicit, got %#v", serviceType, result)
			}
		})
	}
}

func externalNameService(hostname, target string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "alias", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: hostname}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: target},
	}
}

func headlessService(name string, annotated bool) *corev1.Service {
	annotations := map[string]string{AnnotationHostname: name + ".example.com"}
	if annotated {
		annotations[AnnotationPublishHeadless] = "true"
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", Annotations: annotations},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: corev1.ClusterIPNone, ClusterIPs: []string{corev1.ClusterIPNone}},
	}
}

func endpointSlice(name, serviceName string, addressType discoveryv1.AddressType, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Namespace: "apps", Labels: map[string]string{discoveryv1.LabelServiceName: serviceName}},
		AddressType: addressType,
		Endpoints:   endpoints,
	}
}

func endpoint(addresses []string, ready, serving, terminating *bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses: addresses,
		Conditions: discoveryv1.EndpointConditions{
			Ready:       ready,
			Serving:     serving,
			Terminating: terminating,
		},
	}
}

func boolPtr(value bool) *bool { return &value }

func endpointTargets(result Result) []string {
	var targets []string
	for _, endpoint := range result.Endpoints {
		if len(endpoint.Targets) > 0 {
			targets = append(targets, endpoint.Targets[0])
		}
	}
	sort.Strings(targets)
	return targets
}
