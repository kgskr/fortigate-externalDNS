package source

import (
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromIngress(ingress *networkingv1.Ingress, opts Options) Result {
	var result Result
	if ingress == nil || !opts.SourceEnabled(SourceIngress) || !opts.NamespaceAllowed(ingress.Namespace) {
		return result
	}

	ref := dns.SourceRef{Kind: "Ingress", Namespace: ingress.Namespace, Name: ingress.Name}
	hostnames := HostnamesFromAnnotations(ingress.Annotations)
	for _, rule := range ingress.Spec.Rules {
		if rule.Host != "" {
			hostnames = append(hostnames, rule.Host)
		}
	}
	hostnames = uniqueSorted(hostnames)
	if len(hostnames) == 0 {
		return result
	}

	ttl, err := TTLFromAnnotations(ingress.Annotations, opts.DefaultTTL)
	if err != nil {
		result.AddEvent(ref, "", err.Error())
		ttl = opts.DefaultTTL
	}

	targets := ingressTargets(ingress)
	for _, hostname := range hostnames {
		appendEndpointForHost(&result, opts, ref, hostname, targets, ttl)
	}
	return result
}

func ingressTargets(ingress *networkingv1.Ingress) []string {
	var hostnames, ips []string
	for _, item := range ingress.Status.LoadBalancer.Ingress {
		if item.Hostname != "" {
			hostnames = append(hostnames, item.Hostname)
		} else if item.IP != "" {
			ips = append(ips, item.IP)
		}
	}
	return preferHostnames(hostnames, ips)
}
