package source

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestDiscoverHeadlessUsesStandardServiceNameLabelAndDualStackSlices(t *testing.T) {
	service := headlessService("db", true)
	v4 := endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.10", "192.0.2.10"}, boolPtr(true), nil, nil))
	v6 := endpointSlice("db-v6", "db", discoveryv1.AddressTypeIPv6,
		endpoint([]string{"2001:db8::10"}, boolPtr(true), nil, nil))
	wrongLabel := endpointSlice("other", "other", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.99"}, boolPtr(true), nil, nil))
	missingLabel := endpointSlice("unowned", "", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.98"}, boolPtr(true), nil, nil))
	delete(missingLabel.Labels, discoveryv1.LabelServiceName)

	result, err := Discover(context.Background(), KubernetesClients{
		Core:    fake.NewSimpleClientset(service, v4, v6, wrongLabel, missingLabel),
		Gateway: gatewayfake.NewSimpleClientset(),
	}, sourceExpansionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := endpointTargets(result); len(got) != 2 || got[0] != "192.0.2.10" || got[1] != "2001:db8::10" {
		t.Fatalf("expected only standard-label dual-stack endpoints, got %v events=%#v", got, result.Events)
	}
}

func TestDiscoverHeadlessSliceDeletionProducesCompleteEmptyDesiredState(t *testing.T) {
	service := headlessService("db", true)
	slice := endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.10"}, boolPtr(true), nil, nil))
	client := fake.NewSimpleClientset(service, slice)
	opts := sourceExpansionOptions()

	first, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, opts)
	if err != nil || len(first.Endpoints) != 1 {
		t.Fatalf("initial EndpointSlice discovery failed: result=%#v err=%v", first, err)
	}
	if err := client.DiscoveryV1().EndpointSlices("apps").Delete(context.Background(), slice.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Endpoints) != 0 {
		t.Fatalf("deleted EndpointSlice must remove desired endpoints, got %#v", second.Endpoints)
	}
	if second.HasIncompleteSources() {
		t.Fatalf("successful empty EndpointSlice list must be complete so cleanup can be planned, got %#v", second.IncompleteSources)
	}
}

func TestDiscoverEndpointSliceUnavailableMarksServiceIncompleteAndKeepsSafeCreates(t *testing.T) {
	headless := headlessService("db", true)
	loadBalancer := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Annotations: map[string]string{AnnotationHostname: "web.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.20"}}}},
	}
	client := fake.NewSimpleClientset(headless, loadBalancer)
	client.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: discoveryv1.GroupName, Resource: "endpointslices"}, "")
	})

	result, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, sourceExpansionOptions())
	if err != nil {
		t.Fatalf("missing optional EndpointSlice API should return partial discovery, got %v", err)
	}
	if result.SourceComplete(SourceService) || !result.HasIncompleteSources() {
		t.Fatalf("EndpointSlice API absence must suppress cleanup, got %#v", result.IncompleteSources)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].DNSName != "web.example.com" {
		t.Fatalf("independently proven non-destructive Service endpoint must remain available, got %#v", result.Endpoints)
	}
	if !hasEventContaining(result, "EndpointSlice resource is unavailable") {
		t.Fatalf("expected unavailable diagnostic, got %#v", result.Events)
	}
}

func TestDiscoverEndpointSliceListErrorMarksServiceIncomplete(t *testing.T) {
	service := headlessService("db", true)
	client := fake.NewSimpleClientset(service)
	client.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary API failure")
	})

	result, err := Discover(context.Background(), KubernetesClients{Core: client, Gateway: gatewayfake.NewSimpleClientset()}, sourceExpansionOptions())
	if err != nil {
		t.Fatalf("EndpointSlice list failure should preserve a partial result, got %v", err)
	}
	if result.SourceComplete(SourceService) || !hasEventContaining(result, "list failed") {
		t.Fatalf("EndpointSlice list failure must be incomplete and observable, got %#v", result)
	}
}

func TestDiscoverUnsynchronizedEndpointSliceInformerMarksServiceIncomplete(t *testing.T) {
	service := headlessService("db", true)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := indexer.Add(endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.10"}, boolPtr(true), nil, nil))); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(context.Background(), KubernetesClients{
		Core:                     fake.NewSimpleClientset(service),
		EndpointSliceLister:      discoverylisters.NewEndpointSliceLister(indexer),
		EndpointSliceCacheSynced: func() bool { return false },
		Gateway:                  gatewayfake.NewSimpleClientset(),
	}, sourceExpansionOptions())
	if err != nil {
		t.Fatalf("unsynchronized optional cache should return a partial result, got %v", err)
	}
	if result.SourceComplete(SourceService) || !hasEventContaining(result, "cache is not synchronized") {
		t.Fatalf("unsynchronized EndpointSlice cache must suppress cleanup, got %#v", result)
	}
}

func TestDiscoverReadsSynchronizedEndpointSliceInformerData(t *testing.T) {
	service := headlessService("db", true)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := indexer.Add(endpointSlice("db-v4", "db", discoveryv1.AddressTypeIPv4,
		endpoint([]string{"192.0.2.10"}, boolPtr(true), nil, nil))); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(context.Background(), KubernetesClients{
		Core:                     fake.NewSimpleClientset(service),
		EndpointSliceLister:      discoverylisters.NewEndpointSliceLister(indexer),
		EndpointSliceCacheSynced: func() bool { return true },
		Gateway:                  gatewayfake.NewSimpleClientset(),
	}, sourceExpansionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.HasIncompleteSources() || len(result.Endpoints) != 1 || result.Endpoints[0].Targets[0] != "192.0.2.10" {
		t.Fatalf("synchronized informer data should publish normally, got %#v", result)
	}
}

func sourceExpansionOptions() Options {
	opts := testOptions()
	opts.Sources = []string{SourceService}
	opts.PublishHeadlessServices = true
	return opts
}

func TestDiscoverPublishesGatewayWhenHTTPRoutesEmpty(t *testing.T) {
	hostname := gatewayv1.Hostname("gateway.example.com")
	gateway := &gatewayv1.Gateway{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gateway.networking.k8s.io/v1", Kind: "Gateway"},
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"},
		Spec:       gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{{Name: "http", Hostname: &hostname}}},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.20"}}},
	}
	ctx := context.Background()
	gatewayClient := gatewayfake.NewSimpleClientset()
	if _, err := gatewayClient.GatewayV1().Gateways("apps").Create(ctx, gateway, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(ctx, KubernetesClients{
		Core:    fake.NewSimpleClientset(),
		Gateway: gatewayClient,
	}, Options{
		Sources:       []string{SourceGateway},
		Namespaces:    []string{"apps"},
		DomainFilters: []string{"example.com"},
		DefaultTTL:    300,
		Zone:          "example.com",
		OwnerID:       "cluster-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected Gateway endpoint with zero HTTPRoutes, got endpoints=%#v events=%#v", result.Endpoints, result.Events)
	}
	if got := result.Endpoints[0]; got.DNSName != "gateway.example.com" || got.Targets[0] != "203.0.113.20" {
		t.Fatalf("unexpected endpoint: %#v", got)
	}
}

func TestDiscoverResolvesHTTPRouteParentGatewayAcrossFilteredNamespaces(t *testing.T) {
	parentNamespace := gatewayv1.Namespace("infra")
	routeHostname := gatewayv1.Hostname("route.example.com")
	gateway := &gatewayv1.Gateway{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gateway.networking.k8s.io/v1", Kind: "Gateway"},
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "infra"},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "203.0.113.30"}}},
	}
	route := &gatewayv1.HTTPRoute{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gateway.networking.k8s.io/v1", Kind: "HTTPRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "apps"},
		Spec: gatewayv1.HTTPRouteSpec{
			Hostnames: []gatewayv1.Hostname{routeHostname},
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
				Name:      "public",
				Namespace: &parentNamespace,
			}}},
		},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef: gatewayv1.ParentReference{
				Name:      "public",
				Namespace: &parentNamespace,
			},
			ControllerName: gatewayv1.GatewayController("example.com/controller"),
			Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue},
				{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
			},
		}}}},
	}
	direct := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{GatewayMapKey("infra", "public"): gateway}, Options{
		Sources:       []string{SourceGateway},
		Namespaces:    []string{"apps", "infra"},
		DomainFilters: []string{"example.com"},
		DefaultTTL:    300,
		Zone:          "example.com",
		OwnerID:       "cluster-a",
	})
	if len(direct.Endpoints) != 1 {
		t.Fatalf("direct route extraction failed: %#v", direct)
	}

	ctx := context.Background()
	gatewayClient := gatewayfake.NewSimpleClientset(route)
	if _, err := gatewayClient.GatewayV1().Gateways("infra").Create(ctx, gateway, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(ctx, KubernetesClients{
		Core:    fake.NewSimpleClientset(),
		Gateway: gatewayClient,
	}, Options{
		Sources:       []string{SourceGateway},
		Namespaces:    []string{"apps", "infra"},
		DomainFilters: []string{"example.com"},
		DefaultTTL:    300,
		Zone:          "example.com",
		OwnerID:       "cluster-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected one HTTPRoute endpoint, got %#v events=%#v", result.Endpoints, result.Events)
	}
	if got := result.Endpoints[0]; got.DNSName != "route.example.com" || got.Targets[0] != "203.0.113.30" {
		t.Fatalf("unexpected endpoint: %#v", got)
	}
}

func TestDiscoverMarksGatewayIncompleteWhenHTTPRouteUnavailable(t *testing.T) {
	gatewayClient := gatewayfake.NewSimpleClientset()
	gatewayClient.PrependReactor("list", "httproutes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: gatewayv1.GroupName, Resource: "httproutes"}, "")
	})

	result, err := Discover(context.Background(), KubernetesClients{
		Core:    fake.NewSimpleClientset(),
		Gateway: gatewayClient,
	}, Options{Sources: []string{SourceGateway}})
	if err != nil {
		t.Fatalf("an unavailable optional Gateway API should return a partial result, got %v", err)
	}
	if !result.HasIncompleteSources() || result.SourceComplete(SourceGateway) {
		t.Fatalf("HTTPRoute unavailability must mark gateway discovery incomplete, got %#v", result.IncompleteSources)
	}
	if !hasEventContaining(result, "HTTPRoute resource is unavailable") {
		t.Fatalf("expected an informational unavailability event, got %#v", result.Events)
	}
}

func TestDiscoverMarksGatewayIncompleteWhenGatewayUnavailable(t *testing.T) {
	gatewayClient := gatewayfake.NewSimpleClientset()
	gatewayClient.PrependReactor("list", "gateways", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewGone("Gateway resource is unavailable")
	})

	result, err := Discover(context.Background(), KubernetesClients{
		Core:    fake.NewSimpleClientset(),
		Gateway: gatewayClient,
	}, Options{Sources: []string{SourceGateway}})
	if err != nil {
		t.Fatalf("an unavailable optional Gateway API should return a partial result, got %v", err)
	}
	if !result.HasIncompleteSources() || result.SourceComplete(SourceGateway) {
		t.Fatalf("Gateway unavailability must mark gateway discovery incomplete, got %#v", result.IncompleteSources)
	}
	if !hasEventContaining(result, "Gateway resource is unavailable") {
		t.Fatalf("expected an informational unavailability event, got %#v", result.Events)
	}
}
