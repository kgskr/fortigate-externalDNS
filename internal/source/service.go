package source

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/gilsu/fortigate-external-dns/internal/dns"
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
		result.AddEvent("warning", ref, "", err.Error())
		ttl = opts.DefaultTTL
	}

	targets := serviceTargets(service)
	for _, hostname := range hostnames {
		appendEndpointForHost(&result, opts, ref, hostname, targets, ttl)
	}
	return result
}

func serviceTargets(service *corev1.Service) []string {
	var targets []string
	targets = append(targets, service.Spec.ExternalIPs...)
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				targets = append(targets, ingress.IP)
			}
			if ingress.Hostname != "" {
				targets = append(targets, ingress.Hostname)
			}
		}
	}
	return targets
}
