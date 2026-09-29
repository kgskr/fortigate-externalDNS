package source

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func routeAcrossGateways(hostnames []string, secondAddress string, secondType gatewayv1.AddressType) (*gatewayv1.HTTPRoute, map[string]*gatewayv1.Gateway) {
	first := gatewayv1.ParentReference{Name: "public"}
	second := gatewayv1.ParentReference{Name: "other"}
	route := routeWithStatus(1, hostnames, first, conds(metav1.ConditionTrue, metav1.ConditionTrue, 1))
	route.Spec.ParentRefs = append(route.Spec.ParentRefs, second)
	route.Status.Parents = append(route.Status.Parents, gatewayv1.RouteParentStatus{
		ParentRef: second, ControllerName: "example.com/controller", Conditions: conds(metav1.ConditionTrue, metav1.ConditionTrue, 1),
	})
	gateways := gatewayWith(listener("http", "a.example.com", 80))
	gateways[GatewayMapKey("apps", "other")] = &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "apps"},
		Spec:       gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{listener("http", "b.example.com", 80)}},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Type: &secondType, Value: secondAddress}}},
	}
	return route, gateways
}

func TestHTTPRouteKeepsParentAddressesWithMatchingHostnames(t *testing.T) {
	for _, hosts := range [][]string{{"*.example.com"}, {"a.example.com", "b.example.com"}} {
		for _, second := range []struct {
			address string
			kind    gatewayv1.AddressType
		}{
			{"203.0.113.50", gatewayv1.IPAddressType},
			{"lb.example.net", gatewayv1.HostnameAddressType},
		} {
			t.Run(hosts[0]+"/"+second.address, func(t *testing.T) {
				route, gateways := routeAcrossGateways(hosts, second.address, second.kind)
				result := EndpointsFromHTTPRoute(route, gateways, testOptions())
				got := map[string][]string{}
				for _, endpoint := range result.Endpoints {
					got[endpoint.DNSName] = append(got[endpoint.DNSName], endpoint.Targets...)
				}
				want := map[string][]string{"a.example.com": {"203.0.113.40"}, "b.example.com": {second.address}}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("hostnames must only use addresses of matching parents: got %v, want %v", got, want)
				}
				if len(result.Events) != 0 || !result.SourceComplete(SourceGateway) {
					t.Fatalf("fully matched route must not report skipped hostnames: %#v", result)
				}
			})
		}
	}
}

func TestHTTPRouteDeduplicatesRepeatedParentAttachments(t *testing.T) {
	route, gateways := routeAcrossGateways([]string{"*.example.com"}, "203.0.113.50", gatewayv1.IPAddressType)
	section := gatewayv1.SectionName("http")
	repeated := gatewayv1.ParentReference{Name: "public", SectionName: &section}
	route.Spec.ParentRefs = append(route.Spec.ParentRefs, repeated)
	route.Status.Parents = append(route.Status.Parents, gatewayv1.RouteParentStatus{
		ParentRef: repeated, ControllerName: "example.com/controller", Conditions: conds(metav1.ConditionTrue, metav1.ConditionTrue, 1),
	})
	opts := testOptions()
	opts.MaxEndpointsPerResource = 2
	result := EndpointsFromHTTPRoute(route, gateways, opts)
	if len(result.Endpoints) != 2 || !result.SourceComplete(SourceGateway) {
		t.Fatalf("duplicate attachments must not duplicate endpoints or consume extra budget: %#v", result)
	}
}

func TestHTTPRouteParentGroupsShareAtomicBudget(t *testing.T) {
	for _, exhausted := range []string{"resource", "discovery"} {
		t.Run(exhausted, func(t *testing.T) {
			route, gateways := routeAcrossGateways([]string{"*.example.com"}, "203.0.113.50", gatewayv1.IPAddressType)
			opts := testOptions()
			if exhausted == "resource" {
				opts.MaxEndpointsPerResource = 1
			} else {
				opts.MaxEndpointsPerDiscovery = 1
			}
			budget := newEndpointBudget(opts)
			before := budget.remaining
			result, err := endpointsFromHTTPRoute(context.Background(), route, gateways, opts, budget)
			if err != nil || len(result.Endpoints) != 0 || result.SourceComplete(SourceGateway) {
				t.Fatalf("oversized route must be rejected entirely: %#v, %v", result, err)
			}
			var rejections []string
			for _, event := range result.Events {
				if strings.Contains(event.Message, "exceeds discovery budget") {
					rejections = append(rejections, event.Message)
				}
			}
			if len(rejections) != 1 || !strings.Contains(rejections[0], "2 endpoints across 2 hostnames") {
				t.Fatalf("route rejection must be reported once with the route's full size: %q", rejections)
			}
			if budget.remaining != before {
				t.Fatalf("rejected route consumed discovery budget: %d -> %d", before, budget.remaining)
			}
			sibling := loadBalancerService("sibling", "apps", "sibling.example.com", "203.0.113.60")
			result, err = endpointsFromService(context.Background(), sibling, nil, opts, budget)
			if err != nil || len(result.Endpoints) != 1 {
				t.Fatalf("sibling should retain available budget: %#v, %v", result, err)
			}
		})
	}
}
