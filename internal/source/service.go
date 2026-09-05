package source

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromService(service *corev1.Service, opts Options) Result {
	return EndpointsFromServiceWithEndpointSlices(service, nil, opts)
}

// EndpointsFromServiceWithEndpointSlices publishes a Service using only the
// source data appropriate for its mode. ExternalName never falls through to IP
// target handling, and headless Services obtain addresses only from matching
// EndpointSlices.
func EndpointsFromServiceWithEndpointSlices(service *corev1.Service, slices []*discoveryv1.EndpointSlice, opts Options) Result {
	result, _ := endpointsFromService(context.Background(), service, slices, opts, newEndpointBudget(opts))
	return result
}

func endpointsFromService(ctx context.Context, service *corev1.Service, slices []*discoveryv1.EndpointSlice, opts Options, budget *endpointBudget) (result Result, err error) {
	if service == nil || !opts.SourceEnabled(SourceService) || !opts.NamespaceAllowed(service.Namespace) {
		return result, nil
	}

	ref := dns.SourceRef{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service", Namespace: service.Namespace, Name: service.Name, UID: string(service.UID)}
	defer func() {
		if len(result.Endpoints) > 0 {
			result.SetMetadata(ref, service.Labels, service.Annotations)
		}
	}()
	hostnames := HostnamesFromAnnotations(service.Annotations)
	if len(hostnames) == 0 {
		return result, nil
	}

	if service.Spec.Type == corev1.ServiceTypeExternalName {
		discovered, err := endpointsFromExternalNameService(ctx, service, hostnames, opts, ref, budget)
		result.Merge(discovered)
		return result, err
	}
	if isHeadlessService(service) {
		discovered, err := endpointsFromHeadlessService(ctx, service, slices, hostnames, opts, ref, budget)
		result.Merge(discovered)
		return result, err
	}

	ttl, err := TTLFromAnnotations(service.Annotations, opts.DefaultTTL)
	if err != nil {
		result.AddEvent(ref, "", err.Error())
		ttl = opts.DefaultTTL
	}

	targets := serviceTargets(service)
	if len(targets) == 0 {
		// The Service carries a hostname but produced no publishable target. Be
		// explicit about why instead of silently ignoring it. Only LoadBalancer
		// status addresses and ExternalIPs are published.
		if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
			result.AddEvent(ref, "", "LoadBalancer service has no published status address yet")
		} else {
			result.AddEvent(ref, "", fmt.Sprintf("service type %q is not published; only LoadBalancer status addresses and ExternalIPs are supported", service.Spec.Type))
		}
		return result, nil
	}

	return result, budget.appendSource(ctx, &result, opts, SourceService, ref, hostnames, targets, ttl)
}

func endpointsFromExternalNameService(ctx context.Context, service *corev1.Service, hostnames []string, opts Options, ref dns.SourceRef, budget *endpointBudget) (Result, error) {
	var result Result
	if !opts.PublishExternalNameServices {
		result.AddEvent(ref, "", "ExternalName publication is disabled; skipping")
		return result, nil
	}
	if servicePublicationDecision(service, opts, ServicePublicationExternalName) == PublicationDeny {
		result.AddEvent(ref, "", "policy denied ExternalName publication")
		return result, nil
	}

	target := dns.NormalizeDNSName(service.Spec.ExternalName)
	if net.ParseIP(target) != nil {
		result.AddEvent(ref, "", "ExternalName target must be a DNS hostname, not an IP address; skipping")
		return result, nil
	}
	if target == "" || target == "*" || strings.HasPrefix(target, "*.") || len(validation.IsDNS1123Subdomain(target)) > 0 {
		result.AddEvent(ref, "", "ExternalName target is not a valid DNS hostname; skipping")
		return result, nil
	}

	ttl, err := TTLFromAnnotations(service.Annotations, opts.DefaultTTL)
	if err != nil {
		result.AddEvent(ref, "", err.Error())
		ttl = opts.DefaultTTL
	}
	return result, budget.appendSource(ctx, &result, opts, SourceService, ref, hostnames, []string{target}, ttl)
}

func endpointsFromHeadlessService(ctx context.Context, service *corev1.Service, slices []*discoveryv1.EndpointSlice, hostnames []string, opts Options, ref dns.SourceRef, budget *endpointBudget) (Result, error) {
	var result Result
	annotationValue, annotationPresent := service.Annotations[AnnotationPublishHeadless]
	annotated := strings.EqualFold(strings.TrimSpace(annotationValue), "true")
	decision := servicePublicationDecision(service, opts, ServicePublicationHeadless)

	if !opts.PublishHeadlessServices {
		if annotated || decision == PublicationAllow {
			result.AddEvent(ref, "", "headless Service publication is disabled; skipping")
		}
		return result, nil
	}
	if decision == PublicationDeny {
		result.AddEvent(ref, "", "policy denied headless Service publication")
		return result, nil
	}
	if annotationPresent && strings.TrimSpace(annotationValue) != "" && !annotated && !strings.EqualFold(strings.TrimSpace(annotationValue), "false") {
		result.AddEvent(ref, "", "headless publication annotation must be true or false; skipping")
		return result, nil
	}
	if !annotated && decision != PublicationAllow {
		return result, nil
	}

	ttl, err := TTLFromAnnotations(service.Annotations, opts.DefaultTTL)
	if err != nil {
		result.AddEvent(ref, "", err.Error())
		ttl = opts.DefaultTTL
	}
	targets, targetResult := headlessTargets(service, slices, ref)
	result.Merge(targetResult)
	if len(targets) == 0 {
		result.AddEvent(ref, "", "headless Service has no publishable EndpointSlice address")
		return result, nil
	}
	return result, budget.appendSource(ctx, &result, opts, SourceService, ref, hostnames, targets, ttl)
}

func servicePublicationDecision(service *corev1.Service, opts Options, mode ServicePublicationMode) PublicationDecision {
	if opts.ServicePublicationPolicy == nil {
		return PublicationUnspecified
	}
	return opts.ServicePublicationPolicy(ServicePublicationContext{
		Mode:        mode,
		Namespace:   service.Namespace,
		Name:        service.Name,
		Labels:      copyStringMap(service.Labels),
		Annotations: copyStringMap(service.Annotations),
	})
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func isHeadlessService(service *corev1.Service) bool {
	if service == nil {
		return false
	}
	if strings.EqualFold(service.Spec.ClusterIP, corev1.ClusterIPNone) {
		return true
	}
	for _, clusterIP := range service.Spec.ClusterIPs {
		if strings.EqualFold(clusterIP, corev1.ClusterIPNone) {
			return true
		}
	}
	return false
}

func headlessTargets(service *corev1.Service, slices []*discoveryv1.EndpointSlice, ref dns.SourceRef) ([]string, Result) {
	var result Result
	slices = append([]*discoveryv1.EndpointSlice(nil), slices...)
	sort.Slice(slices, func(i, j int) bool {
		if slices[i] == nil {
			return false
		}
		if slices[j] == nil {
			return true
		}
		if slices[i].Namespace != slices[j].Namespace {
			return slices[i].Namespace < slices[j].Namespace
		}
		return slices[i].Name < slices[j].Name
	})

	seen := map[string]struct{}{}
	var targets []string
	unsupportedTypeReported := false
	invalidFamilyReported := false
	for _, slice := range slices {
		if slice == nil || slice.Namespace != service.Namespace || slice.Labels[discoveryv1.LabelServiceName] != service.Name {
			continue
		}
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			if !unsupportedTypeReported {
				result.AddEvent(ref, "", "EndpointSlice address type is unsupported; only IPv4 and IPv6 are published")
				unsupportedTypeReported = true
			}
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if !endpointEligible(endpoint.Conditions, service.Spec.PublishNotReadyAddresses) {
				continue
			}
			for _, address := range endpoint.Addresses {
				ip := net.ParseIP(strings.TrimSpace(address))
				if ip == nil || (slice.AddressType == discoveryv1.AddressTypeIPv4 && ip.To4() == nil) || (slice.AddressType == discoveryv1.AddressTypeIPv6 && ip.To4() != nil) {
					if !invalidFamilyReported {
						result.AddEvent(ref, "", "EndpointSlice address does not match its declared IP address family; skipping")
						invalidFamilyReported = true
					}
					continue
				}
				address = ip.String()
				if _, ok := seen[address]; ok {
					continue
				}
				seen[address] = struct{}{}
				targets = append(targets, address)
			}
		}
	}
	sort.Strings(targets)
	return targets, result
}

func endpointEligible(conditions discoveryv1.EndpointConditions, publishNotReady bool) bool {
	if publishNotReady {
		return true
	}
	if conditions.Terminating != nil && *conditions.Terminating {
		return false
	}
	if conditions.Serving != nil && !*conditions.Serving {
		return false
	}
	// A nil Ready condition means readiness is unknown. It remains eligible as
	// long as the endpoint is not terminating and does not explicitly report
	// Serving=false.
	return conditions.Ready == nil || *conditions.Ready
}

func serviceTargets(service *corev1.Service) []string {
	var hostnames, ips []string
	ips = append(ips, service.Spec.ExternalIPs...)
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			if ingress.Hostname != "" {
				hostnames = append(hostnames, ingress.Hostname)
			} else if ingress.IP != "" {
				ips = append(ips, ingress.IP)
			}
		}
	}
	return preferHostnames(hostnames, ips)
}

// preferHostnames enforces that one DNS name never gets both a CNAME and an
// A/AAAA record: if any hostname target exists across the whole resource, only
// hostnames are published; otherwise the IP targets (ExternalIPs and/or load
// balancer IPs) are published.
func preferHostnames(hostnames, ips []string) []string {
	if len(hostnames) > 0 {
		return hostnames
	}
	return ips
}
