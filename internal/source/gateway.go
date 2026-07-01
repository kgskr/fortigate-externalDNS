package source

import (
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromGateway(gateway *gatewayv1.Gateway, opts Options) Result {
	var result Result
	if gateway == nil || !opts.SourceEnabled(SourceGateway) || !opts.NamespaceAllowed(gateway.Namespace) {
		return result
	}

	ref := dns.SourceRef{Kind: "Gateway", Namespace: gateway.Namespace, Name: gateway.Name}
	var hostnames []string
	for _, listener := range gateway.Spec.Listeners {
		if listener.Hostname != nil {
			hostnames = append(hostnames, string(*listener.Hostname))
		}
	}
	hostnames = uniqueSorted(hostnames)
	if len(hostnames) == 0 {
		return result
	}

	targets := gatewayTargets(gateway)
	for _, hostname := range hostnames {
		appendEndpointForHost(&result, opts, ref, hostname, targets, opts.DefaultTTL)
	}
	return result
}

func EndpointsFromHTTPRoute(route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, opts Options) Result {
	var result Result
	if route == nil || !opts.SourceEnabled(SourceGateway) || !opts.NamespaceAllowed(route.Namespace) {
		return result
	}

	ref := dns.SourceRef{Kind: "HTTPRoute", Namespace: route.Namespace, Name: route.Name}
	var hostnames []string
	for _, hostname := range route.Spec.Hostnames {
		hostnames = append(hostnames, string(hostname))
	}
	hostnames = uniqueSorted(hostnames)
	if len(hostnames) == 0 {
		// A route with no hostnames matches all of its parent listeners' hostnames,
		// which are published separately by the Gateway source. This is a normal,
		// intentional configuration, so surface it at info level (not warning) so it
		// stays observable without being logged as a warning on every reconcile.
		result.AddInfoEvent(ref, "", "HTTPRoute declares no hostnames; parent Gateway listener hostnames are the source of truth")
		return result
	}

	acceptedParents := acceptedParentRefs(route)
	if len(acceptedParents) == 0 {
		result.AddEvent(ref, "", "HTTPRoute has no accepted parent with resolved references")
		return result
	}

	targets := targetsForHTTPRoute(route, gateways, acceptedParents)
	for _, hostname := range hostnames {
		appendEndpointForHost(&result, opts, ref, hostname, targets, opts.DefaultTTL)
	}
	return result
}

// acceptedParentRefs collects the parentRef keys a route reports as Accepted with
// ResolvedRefs. It keys by parentRef identity only, not RouteParentStatus
// ControllerName, which assumes a single Gateway controller writes status for a
// given parentRef — the common case. Multi-controller clusters that publish
// conflicting status for the same parentRef are not disambiguated here.
func acceptedParentRefs(route *gatewayv1.HTTPRoute) map[string]struct{} {
	accepted := map[string]struct{}{}
	for _, parent := range route.Status.Parents {
		if hasCondition(parent.Conditions, "Accepted", metav1.ConditionTrue) &&
			hasCondition(parent.Conditions, "ResolvedRefs", metav1.ConditionTrue) {
			accepted[parentRefKey(route.Namespace, parent.ParentRef)] = struct{}{}
		}
	}
	return accepted
}

func hasCondition(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType && condition.Status == status {
			return true
		}
	}
	return false
}

func GatewayMapKey(namespace, name string) string {
	return namespace + "/" + name
}

func gatewayTargets(gateway *gatewayv1.Gateway) []string {
	var targets []string
	for _, address := range gateway.Status.Addresses {
		if address.Value != "" {
			targets = append(targets, address.Value)
		}
	}
	return targets
}

func targetsForHTTPRoute(route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, acceptedParents map[string]struct{}) []string {
	var targets []string
	for _, parent := range route.Spec.ParentRefs {
		if !parentRefIsGateway(parent) {
			continue
		}
		if _, ok := acceptedParents[parentRefKey(route.Namespace, parent)]; !ok {
			continue
		}
		namespace := route.Namespace
		if parent.Namespace != nil {
			namespace = string(*parent.Namespace)
		}
		gateway, ok := gateways[GatewayMapKey(namespace, string(parent.Name))]
		if !ok {
			continue
		}
		targets = append(targets, gatewayTargets(gateway)...)
	}
	return targets
}

func parentRefIsGateway(ref gatewayv1.ParentReference) bool {
	group := "gateway.networking.k8s.io"
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	return group == "gateway.networking.k8s.io" && kind == "Gateway"
}

func parentRefKey(defaultNamespace string, ref gatewayv1.ParentReference) string {
	group := "gateway.networking.k8s.io"
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	namespace := defaultNamespace
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	sectionName := ""
	if ref.SectionName != nil {
		sectionName = string(*ref.SectionName)
	}
	port := ""
	if ref.Port != nil {
		port = strconv.Itoa(int(*ref.Port))
	}
	return group + "/" + kind + "/" + namespace + "/" + string(ref.Name) + "/" + sectionName + "/" + port
}
