package source

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	if !routeStatusCurrent(route) {
		// Even when one parent is accepted, another may still be reconciling.
		// Preserve its existing records until every relevant parent has reported
		// both conditions for this generation.
		result.MarkIncomplete(SourceGateway)
		result.AddEvent(ref, "", "HTTPRoute status is missing or stale for the current generation; suppressing cleanup until the Gateway controller reports status")
		return result, nil
	}
	if len(acceptedParents) == 0 {
		result.AddEvent(ref, "", "HTTPRoute has no accepted parent with resolved references")
		return result, nil
	}

	targetsByHost := targetsForHTTPRoute(ctx, route, gateways, acceptedParents, hostnames, opts, &result, ref)
	// All parent attachments belong to one source resource. Stage publication
	// against a shared local budget so an oversized route is rejected entirely
	// without consuming the discovery budget needed by its siblings.
	limit := min(budget.perResource, budget.remaining)
	stagedBudget := endpointBudget{perResource: limit, remaining: limit}
	hostnames = hostnames[:0]
	for host := range targetsByHost {
		hostnames = append(hostnames, host)
	}
	sort.Strings(hostnames)
	// Size the whole route first so the rejection reports the route's real
	// expansion rather than the single hostname that crossed the budget.
	total := 0
	for _, host := range hostnames {
		var scratch Result
		total += len(publishableHosts(&scratch, opts, ref, []string{host})) * len(uniqueSorted(targetsByHost[host].values()))
	}
	if total > limit {
		result.MarkIncomplete(SourceGateway)
		result.AddEvent(ref, "", fmt.Sprintf("HTTPRoute endpoint expansion (%d endpoints across %d hostnames and all parent attachments) exceeds discovery budget; rejecting the entire resource", total, len(hostnames)))
		return result, nil
	}
	for _, host := range hostnames {
		targets := targetsByHost[host].values()
		err := stagedBudget.appendSource(ctx, &result, opts, SourceGateway, ref, []string{host}, targets, opts.DefaultTTL)
		if err != nil || !result.SourceComplete(SourceGateway) {
			result.Endpoints = nil
			return result, err
		}
	}
	budget.remaining -= limit - stagedBudget.remaining
	return result, nil
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

// routeStatusCurrent requires a definitive Accepted and ResolvedRefs condition
// at the current generation for each Gateway parent still referenced by the
// spec. Removed or unrelated parent statuses cannot establish completeness.
func routeStatusCurrent(route *gatewayv1.HTTPRoute) bool {
	for _, ref := range route.Spec.ParentRefs {
		if !parentRefIsGateway(ref) {
			continue
		}
		current := false
		for _, parent := range route.Status.Parents {
			if parentRefKey(route.Namespace, parent.ParentRef) != parentRefKey(route.Namespace, ref) {
				continue
			}
			accepted, resolved := false, false
			for _, condition := range parent.Conditions {
				if condition.ObservedGeneration != route.Generation || (condition.Status != metav1.ConditionTrue && condition.Status != metav1.ConditionFalse) {
					continue
				}
				switch condition.Type {
				case "Accepted":
					accepted = true
				case "ResolvedRefs":
					resolved = true
				}
			}
			current = current || (accepted && resolved)
		}
		if !current {
			return false
		}
	}
	return true
}

// attachedListeners respects the sectionName and port of one parent reference.
func attachedListeners(gateway *gatewayv1.Gateway, parent gatewayv1.ParentReference) []gatewayv1.Listener {
	var listeners []gatewayv1.Listener
	for _, listener := range gateway.Spec.Listeners {
		if parent.SectionName != nil && listener.Name != *parent.SectionName {
			continue
		}
		if parent.Port != nil && listener.Port != *parent.Port {
			continue
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

// A whole-Gateway Accepted condition proves acceptance by at least one
// listener, not every listener whose hostname intersects the route.
func listenerAllowsHTTPRoute(ctx context.Context, listener gatewayv1.Listener, gatewayNamespace, routeNamespace string, opts Options) (bool, error) {
	if listener.Protocol != gatewayv1.HTTPProtocolType && listener.Protocol != gatewayv1.HTTPSProtocolType {
		return false, nil
	}
	allowed := listener.AllowedRoutes
	if allowed != nil && len(allowed.Kinds) > 0 {
		matches := false
		for _, kind := range allowed.Kinds {
			if kind.Kind == "HTTPRoute" && (kind.Group == nil || *kind.Group == gatewayv1.GroupName) {
				matches = true
			}
		}
		if !matches {
			return false, nil
		}
	}
	if allowed == nil || allowed.Namespaces == nil || allowed.Namespaces.From == nil || *allowed.Namespaces.From == gatewayv1.NamespacesFromSame {
		return gatewayNamespace == routeNamespace, nil
	}
	switch *allowed.Namespaces.From {
	case gatewayv1.NamespacesFromAll:
		return true, nil
	case gatewayv1.NamespacesFromSelector:
		if allowed.Namespaces.Selector == nil || opts.NamespaceLabels == nil {
			return false, fmt.Errorf("gateway listener namespace selector cannot be evaluated")
		}
		selector, err := metav1.LabelSelectorAsSelector(allowed.Namespaces.Selector)
		if err != nil {
			return false, err
		}
		namespaceLabels, err := opts.NamespaceLabels(ctx, routeNamespace)
		if err != nil {
			return false, err
		}
		return selector.Matches(labels.Set(namespaceLabels)), nil
	default:
		return false, fmt.Errorf("unknown Gateway listener namespace policy")
	}
}

// intersectHostname returns the intersection of a route hostname and a listener
// hostname (both normalized; empty listener hostname matches anything).
func intersectHostname(routeHost, listenerHost string) (string, bool) {
	if listenerHost == "" || listenerHost == routeHost {
		return routeHost, true
	}
	routeWild := strings.HasPrefix(routeHost, "*.")
	listenerWild := strings.HasPrefix(listenerHost, "*.")
	switch {
	case listenerWild && !routeWild:
		if strings.HasSuffix(routeHost, listenerHost[1:]) {
			return routeHost, true
		}
	case !listenerWild && routeWild:
		if strings.HasSuffix(listenerHost, routeHost[1:]) {
			return listenerHost, true
		}
	case listenerWild && routeWild:
		// Both wildcards: the more specific (longer) one wins if it lies under the other.
		switch {
		case strings.HasSuffix(routeHost, listenerHost[1:]):
			return routeHost, true
		case strings.HasSuffix(listenerHost, routeHost[1:]):
			return listenerHost, true
		}
	}
	return "", false
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

// targetsForHTTPRoute keeps each listener hostname paired with its Gateway's
// addresses. Hostname-address preference is applied only among parents that
// actually serve the same resulting DNS name.
func targetsForHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute, gateways map[string]*gatewayv1.Gateway, acceptedParents map[string]struct{}, hostnames []string, opts Options, result *Result, ref dns.SourceRef) map[string]gatewayAddressTargets {
	targetsByHost := map[string]gatewayAddressTargets{}
	matchedHosts := map[string]bool{}
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
		parentHosts := map[string]struct{}{}
		for _, listener := range attachedListeners(gateway, parent) {
			allowed, err := listenerAllowsHTTPRoute(ctx, listener, gateway.Namespace, route.Namespace, opts)
			if err != nil {
				result.MarkIncomplete(SourceGateway)
				result.AddEvent(ref, "", "cannot verify Gateway listener namespace selector; suppressing cleanup")
				continue
			}
			if !allowed {
				continue
			}
			listenerHost := ""
			if listener.Hostname != nil {
				listenerHost = dns.NormalizeDNSName(string(*listener.Hostname))
			}
			for _, host := range hostnames {
				if merged, ok := intersectHostname(host, listenerHost); ok {
					parentHosts[merged] = struct{}{}
					matchedHosts[host] = true
				}
			}
		}
		for host := range parentHosts {
			targets := targetsByHost[host]
			targets.hostnames = append(targets.hostnames, gatewayTargets.hostnames...)
			targets.ips = append(targets.ips, gatewayTargets.ips...)
			targetsByHost[host] = targets
		}
	}
	for _, host := range hostnames {
		if !matchedHosts[host] {
			result.AddEvent(ref, host, "HTTPRoute hostname does not intersect any attached Gateway listener hostname; skipping")
		}
	}
	return targetsByHost
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
