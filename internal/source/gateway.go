package source

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromGateway(gateway *gatewayv1.Gateway, opts Options) Result {
	result, _ := endpointsFromGateway(context.Background(), gateway, opts, newEndpointBudget(opts))
	return result
}

func endpointsFromGateway(ctx context.Context, gateway *gatewayv1.Gateway, opts Options, budget *endpointBudget) (result Result, err error) {
	if gateway == nil || !opts.SourceEnabled(SourceGateway) || !opts.NamespaceAllowed(gateway.Namespace) {
		return result, nil
	}

	ref := gatewaySourceRef(gateway)
	defer func() {
		if len(result.Endpoints) > 0 {
			result.SetMetadata(ref, gateway.Labels, gateway.Annotations)
		}
	}()
	var hostnames []string
	for _, listener := range gateway.Spec.Listeners {
		if listener.Hostname != nil {
			hostnames = append(hostnames, string(*listener.Hostname))
		}
	}
	hostnames = uniqueSorted(hostnames)
	if len(hostnames) == 0 {
		return result, nil
	}

	targets := collectGatewayTargets(gateway, &result).values()
	return result, budget.appendSource(ctx, &result, opts, SourceGateway, ref, hostnames, targets, opts.DefaultTTL)
}

func EndpointsFromHTTPRoute(route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, opts Options) Result {
	result, _ := endpointsFromHTTPRoute(context.Background(), route, gateways, opts, newEndpointBudget(opts))
	return result
}

func endpointsFromHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, opts Options, budget *endpointBudget) (result Result, err error) {
	if route == nil || !opts.SourceEnabled(SourceGateway) || !opts.NamespaceAllowed(route.Namespace) {
		return result, nil
	}

	ref := dns.SourceRef{APIVersion: gatewayv1.GroupVersion.String(), Kind: "HTTPRoute", Namespace: route.Namespace, Name: route.Name, UID: string(route.UID)}
	defer func() {
		if len(result.Endpoints) > 0 {
			result.SetMetadata(ref, route.Labels, route.Annotations)
		}
	}()
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
		return result, nil
	}

	acceptedParents := acceptedParentRefs(route)
	if len(acceptedParents) == 0 {
		result.AddEvent(ref, "", "HTTPRoute has no accepted parent with resolved references")
		return result, nil
	}

	targets := targetsForHTTPRoute(route, gateways, acceptedParents, &result)
	return result, budget.appendSource(ctx, &result, opts, SourceGateway, ref, hostnames, targets, opts.DefaultTTL)
}

func gatewaySourceRef(gateway *gatewayv1.Gateway) dns.SourceRef {
	return dns.SourceRef{APIVersion: gatewayv1.GroupVersion.String(), Kind: "Gateway", Namespace: gateway.Namespace, Name: gateway.Name, UID: string(gateway.UID)}
}

// acceptedParentRefs collects the parentRef keys a route currently reports as
// Accepted with ResolvedRefs. It keys by parentRef identity only, not RouteParentStatus
// ControllerName, which assumes a single Gateway controller writes status for a
// given parentRef — the common case. Multi-controller clusters that publish
// conflicting status for the same parentRef are not disambiguated here.
func acceptedParentRefs(route *gatewayv1.HTTPRoute) map[string]struct{} {
	accepted := map[string]struct{}{}
	for _, parent := range route.Status.Parents {
		if hasCurrentCondition(parent.Conditions, "Accepted", metav1.ConditionTrue, route.Generation) &&
			hasCurrentCondition(parent.Conditions, "ResolvedRefs", metav1.ConditionTrue, route.Generation) {
			accepted[parentRefKey(route.Namespace, parent.ParentRef)] = struct{}{}
		}
	}
	return accepted
}

func hasCurrentCondition(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, generation int64) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType && condition.Status == status && condition.ObservedGeneration == generation {
			return true
		}
	}
	return false
}

func GatewayMapKey(namespace, name string) string {
	return namespace + "/" + name
}

type gatewayAddressTargets struct {
	hostnames []string
	ips       []string
}

func (t gatewayAddressTargets) values() []string {
	return preferHostnames(t.hostnames, t.ips)
}

func collectGatewayTargets(gateway *gatewayv1.Gateway, result *Result) gatewayAddressTargets {
	var targets gatewayAddressTargets
	ref := gatewaySourceRef(gateway)
	for _, address := range gateway.Status.Addresses {
		value := strings.TrimSpace(address.Value)
		addressType := gatewayv1.IPAddressType
		if address.Type != nil {
			addressType = *address.Type
		}
		switch addressType {
		case gatewayv1.IPAddressType:
			if ip := net.ParseIP(value); ip != nil {
				targets.ips = append(targets.ips, ip.String())
			} else {
				result.AddEvent(ref, "", fmt.Sprintf("Gateway IPAddress value %q is not a valid IP address; skipping", value))
			}
		case gatewayv1.HostnameAddressType:
			if validGatewayHostname(value) && net.ParseIP(value) == nil {
				targets.hostnames = append(targets.hostnames, value)
			} else {
				result.AddEvent(ref, "", fmt.Sprintf("Gateway Hostname value %q is not a valid DNS hostname; skipping", value))
			}
		default:
			result.AddEvent(ref, "", fmt.Sprintf("Gateway address type %q is not supported for DNS publication; skipping", addressType))
		}
	}
	return targets
}

func validGatewayHostname(value string) bool {
	value = dns.NormalizeDNSName(value)
	if value == "" || len(value) > 253 || value == "*" || strings.HasPrefix(value, "*.") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || !asciiLetterOrDigit(label[0]) || !asciiLetterOrDigit(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			if !asciiLetterOrDigit(label[i]) && label[i] != '-' {
				return false
			}
		}
	}
	return true
}

func asciiLetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func targetsForHTTPRoute(route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, acceptedParents map[string]struct{}, result *Result) []string {
	var targets gatewayAddressTargets
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
		gatewayTargets := collectGatewayTargets(gateway, result)
		targets.hostnames = append(targets.hostnames, gatewayTargets.hostnames...)
		targets.ips = append(targets.ips, gatewayTargets.ips...)
	}
	return targets.values()
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
