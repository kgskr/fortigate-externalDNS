package source

import (
	"context"
	"errors"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	discoveryclient "k8s.io/client-go/kubernetes/typed/discovery/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

type KubernetesClients struct {
	Core                     kubernetes.Interface
	Dynamic                  dynamic.Interface
	EndpointSlices           discoveryclient.DiscoveryV1Interface
	EndpointSliceLister      discoverylisters.EndpointSliceLister
	EndpointSliceCacheSynced cache.InformerSynced
	Gateway                  gatewayclient.Interface
}

var errEndpointSliceCacheNotSynced = errors.New("EndpointSlice informer cache is not synchronized")

func Discover(ctx context.Context, clients KubernetesClients, opts Options) (Result, error) {
	var result Result
	for _, namespace := range namespacesForList(opts.Namespaces) {
		if opts.SourceEnabled(SourceService) {
			services, err := clients.Core.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return result, err
			}
			slicesByService := map[string][]*discoveryv1.EndpointSlice{}
			if endpointSlicesNeeded(services.Items, opts) {
				slices, sliceErr := listEndpointSlices(ctx, clients, namespace)
				if sliceErr != nil {
					result.MarkIncomplete(SourceService)
					ref := dns.SourceRef{Kind: "EndpointSlice", Namespace: namespace}
					if errors.Is(sliceErr, errEndpointSliceCacheNotSynced) {
						result.AddInfoEvent(ref, "", "EndpointSlice informer cache is not synchronized; headless Service discovery is incomplete")
					} else if endpointSliceAPIUnavailable(sliceErr) {
						result.AddInfoEvent(ref, "", "EndpointSlice resource is unavailable; headless Service discovery is incomplete")
					} else {
						result.AddEvent(ref, "", "EndpointSlice list failed; headless Service discovery is incomplete")
					}
				} else {
					slicesByService = indexEndpointSlices(slices)
				}
			}
			for i := range services.Items {
				service := &services.Items[i]
				result.Merge(EndpointsFromServiceWithEndpointSlices(service, slicesByService[service.Name], opts))
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

func listEndpointSlices(ctx context.Context, clients KubernetesClients, namespace string) ([]*discoveryv1.EndpointSlice, error) {
	if clients.EndpointSliceLister != nil {
		// Cache-backed discovery must positively prove synchronization. A lister
		// without a sync hook is not sufficient evidence for cleanup.
		if clients.EndpointSliceCacheSynced == nil || !clients.EndpointSliceCacheSynced() {
			return nil, errEndpointSliceCacheNotSynced
		}
		if namespace == corev1.NamespaceAll {
			return clients.EndpointSliceLister.List(labels.Everything())
		}
		return clients.EndpointSliceLister.EndpointSlices(namespace).List(labels.Everything())
	}

	sliceClient := clients.EndpointSlices
	if sliceClient == nil {
		sliceClient = clients.Core.DiscoveryV1()
	}
	list, err := sliceClient.EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName})
	if err != nil {
		return nil, err
	}
	slices := make([]*discoveryv1.EndpointSlice, 0, len(list.Items))
	for i := range list.Items {
		slices = append(slices, &list.Items[i])
	}
	return slices, nil
}

func endpointSlicesNeeded(services []corev1.Service, opts Options) bool {
	if !opts.PublishHeadlessServices {
		return false
	}
	for i := range services {
		service := &services[i]
		if !isHeadlessService(service) || len(HostnamesFromAnnotations(service.Annotations)) == 0 {
			continue
		}
		decision := servicePublicationDecision(service, opts, ServicePublicationHeadless)
		if decision == PublicationDeny {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(service.Annotations[AnnotationPublishHeadless]), "true") || decision == PublicationAllow {
			return true
		}
	}
	return false
}

func indexEndpointSlices(slices []*discoveryv1.EndpointSlice) map[string][]*discoveryv1.EndpointSlice {
	byService := map[string][]*discoveryv1.EndpointSlice{}
	for _, slice := range slices {
		if slice == nil {
			continue
		}
		serviceName := strings.TrimSpace(slice.Labels[discoveryv1.LabelServiceName])
		if serviceName == "" {
			continue
		}
		byService[serviceName] = append(byService[serviceName], slice)
	}
	for serviceName := range byService {
		sort.Slice(byService[serviceName], func(i, j int) bool {
			left, right := byService[serviceName][i], byService[serviceName][j]
			if left.Namespace != right.Namespace {
				return left.Namespace < right.Namespace
			}
			return left.Name < right.Name
		})
	}
	return byService
}

func discoverHTTPRoutes(ctx context.Context, clients KubernetesClients, opts Options) ([]*gatewayv1.HTTPRoute, bool, Result, error) {
	var result Result
	var routesOut []*gatewayv1.HTTPRoute
	for _, namespace := range namespacesForList(opts.Namespaces) {
		routes, err := clients.Gateway.GatewayV1().HTTPRoutes(namespace).List(ctx, metav1.ListOptions{})
		if gatewayAPIUnavailable(err) {
			// Gateway API CRDs not installed is an expected steady state on vanilla
			// clusters, not an actionable problem; log it at info so the default
			// (gateway-enabled) config does not warn on every reconcile. The source is
			// nevertheless incomplete, so the controller must suppress cleanup derived
			// from this partial discovery result.
			result.MarkIncomplete(SourceGateway)
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
			result.MarkIncomplete(SourceGateway)
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

func endpointSliceAPIUnavailable(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsGone(err)
}

func namespacesForList(namespaces []string) []string {
	if len(namespaces) == 0 {
		return []string{corev1.NamespaceAll}
	}
	return namespaces
}
