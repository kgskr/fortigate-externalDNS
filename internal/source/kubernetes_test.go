package source

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

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
