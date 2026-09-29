package source

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestHTTPRouteWildcardOnlyPublishesAllowedListeners(t *testing.T) {
	all := gatewayv1.NamespacesFromAll
	selector := gatewayv1.NamespacesFromSelector
	for _, tc := range []struct {
		name     string
		protocol gatewayv1.ProtocolType
		allowed  *gatewayv1.AllowedRoutes
		want     bool
	}{
		{"http-all", gatewayv1.HTTPProtocolType, &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &all}}, true},
		{"https-all", gatewayv1.HTTPSProtocolType, &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &all}}, true},
		{"tls", gatewayv1.TLSProtocolType, &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &all}}, false},
		{"same-default", gatewayv1.HTTPProtocolType, nil, false},
		{"grpc-only", gatewayv1.HTTPProtocolType, &gatewayv1.AllowedRoutes{Kinds: []gatewayv1.RouteGroupKind{{Kind: "GRPCRoute"}}, Namespaces: &gatewayv1.RouteNamespaces{From: &all}}, false},
		{"selector-denied", gatewayv1.HTTPProtocolType, &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &selector, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "other"}}}}, false},
		{"selector-allowed", gatewayv1.HTTPProtocolType, &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &selector, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "apps"}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := gatewayv1.Namespace("infra")
			parent := gatewayv1.ParentReference{Name: "public", Namespace: &ns}
			route := routeWithStatus(1, []string{"*.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1))
			good := listener("http", "web.example.com", 80)
			good.AllowedRoutes = &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &all}}
			other := listener("other", "other.example.com", 443)
			other.Protocol, other.AllowedRoutes = tc.protocol, tc.allowed
			if tc.protocol == gatewayv1.TLSProtocolType {
				mode := gatewayv1.TLSModePassthrough
				other.TLS = &gatewayv1.ListenerTLSConfig{Mode: &mode}
			}
			gw := gatewayWith(good, other)[GatewayMapKey("apps", "public")]
			gw.Namespace = "infra"
			opts := testOptions()
			opts.Namespaces, opts.GatewayTargetNamespaces = []string{"apps"}, []string{"infra"}
			opts.NamespaceLabels = func(context.Context, string) (map[string]string, error) {
				return map[string]string{"team": "apps"}, nil
			}
			if got := EndpointsFromGateway(gw, opts); len(got.Endpoints) != 0 {
				t.Fatal("lookup-only Gateway was published")
			}
			got := EndpointsFromHTTPRoute(route, map[string]*gatewayv1.Gateway{GatewayMapKey("infra", "public"): gw}, opts)
			want := 1
			if tc.want {
				want++
			}
			if len(got.Endpoints) != want || got.HasIncompleteSources() {
				t.Fatalf("unexpected listener publication: %#v", got)
			}
			for _, ep := range got.Endpoints {
				if !tc.want && ep.DNSName != "web.example.com" {
					t.Fatalf("disallowed listener published: %#v", ep)
				}
			}
		})
	}
}

func TestDiscoverGatewayNamespaceSelectorRefreshAndFailure(t *testing.T) {
	selector := gatewayv1.NamespacesFromSelector
	ns := gatewayv1.Namespace("infra")
	parent := gatewayv1.ParentReference{Name: "public", Namespace: &ns}
	route := routeWithStatus(1, []string{"*.example.com"}, parent, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1))
	a, b := listener("a", "a.example.com", 80), listener("b", "b.example.com", 81)
	allowed := &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &selector, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "apps"}}}}
	a.AllowedRoutes, b.AllowedRoutes = allowed, allowed
	gw := gatewayWith(a, b)[GatewayMapKey("apps", "public")]
	gw.Namespace = "infra"
	gw.TypeMeta = metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "Gateway"}
	route.TypeMeta = metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "HTTPRoute"}
	core := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"team": "apps"}}})
	clients := KubernetesClients{Core: core, Gateway: gatewayfake.NewSimpleClientset(route)}
	opts := testOptions()
	opts.Sources, opts.Namespaces, opts.GatewayTargetNamespaces = []string{SourceGateway}, []string{"apps"}, []string{"infra"}
	ctx := context.Background()
	if _, err := clients.Gateway.GatewayV1().Gateways("infra").Create(ctx, gw, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(ctx, clients, opts)
	if err != nil || len(got.Endpoints) != 2 || got.HasIncompleteSources() {
		t.Fatalf("allowed selector: result=%#v err=%v", got, err)
	}
	if len(core.Actions()) != 1 {
		t.Fatalf("namespace labels must be cached within one discovery: %v", core.Actions())
	}
	_, err = core.CoreV1().Namespaces().Update(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"team": "other"}}}, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = Discover(ctx, clients, opts)
	if err != nil || len(got.Endpoints) != 0 || got.HasIncompleteSources() {
		t.Fatalf("changed selector: result=%#v err=%v", got, err)
	}
	core.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	got, err = Discover(ctx, clients, opts)
	if err != nil || len(got.Endpoints) != 0 || !got.HasIncompleteSources() {
		t.Fatalf("failed lookup must suppress cleanup: result=%#v err=%v", got, err)
	}
}
