package source

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromService(service *corev1.Service, opts Options) Result {
	var result Result
	if service == nil || !opts.SourceEnabled(SourceService) || !opts.NamespaceAllowed(service.Namespace) {
		return result
	}

	ref := dns.SourceRef{Kind: "Service", Namespace: service.Namespace, Name: service.Name}
	hostnames := HostnamesFromAnnotations(service.Annotations)
	if len(hostnames) == 0 {
		return result
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
		return result
	}

	for _, hostname := range hostnames {
		appendEndpointForHost(&result, opts, ref, hostname, targets, ttl)
	}
	return result
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
