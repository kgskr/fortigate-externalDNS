package source

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

type KubernetesClients struct {
	Core    kubernetes.Interface
	Gateway gatewayclient.Interface
}

func Discover(ctx context.Context, clients KubernetesClients, opts Options) (Result, error) {
	var result Result
	for _, namespace := range namespacesForList(opts.Namespaces) {
		if opts.SourceEnabled(SourceService) {
			services, err := clients.Core.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return result, err
			}
			for i := range services.Items {
				result.Merge(EndpointsFromService(&services.Items[i], opts))
			}
		}

		if opts.SourceEnabled(SourceIngress) {
			ingresses, err := clients.Core.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return result, err
			}
			for i := range ingresses.Items {
				result.Merge(EndpointsFromIngress(&ingresses.Items[i], opts))
			}
		}

	}

	if opts.SourceEnabled(SourceGateway) {
		routes, routesAvailable, routeResult, err := discoverHTTPRoutes(ctx, clients, opts)
		result.Merge(routeResult)
		if err != nil {
			return result, err
		}
		if !routesAvailable {
			return result, nil
		}

		gatewayMap, gatewayResult, err := discoverGateways(ctx, clients, opts, routes)
		result.Merge(gatewayResult)
		if err != nil {
			return result, err
		}
		if gatewayMap == nil {
			return result, nil
		}
		for _, route := range routes {
			result.Merge(EndpointsFromHTTPRoute(route, gatewayMap, opts))
		}
	}
	return result, nil
}

func discoverHTTPRoutes(ctx context.Context, clients KubernetesClients, opts Options) ([]*gatewayv1.HTTPRoute, bool, Result, error) {
	var result Result
	var routesOut []*gatewayv1.HTTPRoute
	for _, namespace := range namespacesForList(opts.Namespaces) {
		routes, err := clients.Gateway.GatewayV1().HTTPRoutes(namespace).List(ctx, metav1.ListOptions{})
		if gatewayAPIUnavailable(err) {
			// Gateway API CRDs not installed is an expected steady state on vanilla
			// clusters, not an actionable problem; log it at info so the default
			// (gateway-enabled) config does not warn on every reconcile.
			result.AddInfoEvent(dns.SourceRef{Kind: "HTTPRoute"}, "", "Gateway API HTTPRoute resource is unavailable; skipping gateway source")
			return nil, false, result, nil
		}
		if err != nil {
			return nil, false, result, err
		}
		for i := range routes.Items {
			routesOut = append(routesOut, &routes.Items[i])
		}
	}
	return routesOut, true, result, nil
}

func discoverGateways(ctx context.Context, clients KubernetesClients, opts Options, routes []*gatewayv1.HTTPRoute) (map[string]*gatewayv1.Gateway, Result, error) {
	var result Result
	gatewayMap := map[string]*gatewayv1.Gateway{}
	for _, namespace := range gatewayNamespacesForList(opts, routes) {
		gateways, err := clients.Gateway.GatewayV1().Gateways(namespace).List(ctx, metav1.ListOptions{})
		if gatewayAPIUnavailable(err) {
			result.AddInfoEvent(dns.SourceRef{Kind: "Gateway"}, "", "Gateway API Gateway resource is unavailable; skipping gateway source")
			return nil, result, nil
		}
		if err != nil {
			return nil, result, err
		}
		for i := range gateways.Items {
			gateway := &gateways.Items[i]
			gatewayMap[GatewayMapKey(gateway.Namespace, gateway.Name)] = gateway
			result.Merge(EndpointsFromGateway(gateway, opts))
		}
	}
	return gatewayMap, result, nil
}

func gatewayNamespacesForList(opts Options, routes []*gatewayv1.HTTPRoute) []string {
	if len(opts.Namespaces) == 0 {
		return []string{corev1.NamespaceAll}
	}
	seen := map[string]struct{}{}
	var namespaces []string
	add := func(namespace string) {
		if namespace == "" {
			return
		}
		if _, ok := seen[namespace]; ok {
			return
		}
		seen[namespace] = struct{}{}
		namespaces = append(namespaces, namespace)
	}
	// Source namespaces and explicitly configured Gateway target namespaces are
	// always listed. Target namespaces let an HTTPRoute in an app namespace
	// resolve a parent Gateway in a shared infrastructure namespace; they do not
	// grant ownership or cleanup over records in those namespaces.
	for _, namespace := range opts.Namespaces {
		add(namespace)
	}
	for _, namespace := range opts.GatewayTargetNamespaces {
		add(namespace)
	}
	for _, route := range routes {
		for _, parent := range route.Spec.ParentRefs {
			namespace := route.Namespace
			if parent.Namespace != nil {
				namespace = string(*parent.Namespace)
			}
			if opts.NamespaceAllowed(namespace) || opts.GatewayTargetNamespaceAllowed(namespace) {
				add(namespace)
			}
		}
	}
	if len(namespaces) == 0 {
		return nil
	}
	sort.Strings(namespaces)
	return namespaces
}

func gatewayAPIUnavailable(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsGone(err)
}

func namespacesForList(namespaces []string) []string {
	if len(namespaces) == 0 {
		return []string{corev1.NamespaceAll}
	}
	return namespaces
}
