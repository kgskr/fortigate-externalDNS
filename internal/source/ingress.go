package source

import (
	"context"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func EndpointsFromIngress(ingress *networkingv1.Ingress, opts Options) Result {
	result, _ := endpointsFromIngress(context.Background(), ingress, opts, newEndpointBudget(opts))
	return result
}

func endpointsFromIngress(ctx context.Context, ingress *networkingv1.Ingress, opts Options, budget *endpointBudget) (result Result, err error) {
	if ingress == nil || !opts.SourceEnabled(SourceIngress) || !opts.NamespaceAllowed(ingress.Namespace) {
		return result, nil
	}

	ref := dns.SourceRef{APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: "Ingress", Namespace: ingress.Namespace, Name: ingress.Name, UID: string(ingress.UID)}
	defer func() {
		if len(result.Endpoints) > 0 {
			result.SetMetadata(ref, ingress.Labels, ingress.Annotations)
		}
	}()
	hostnames := HostnamesFromAnnotations(ingress.Annotations)
	for _, rule := range ingress.Spec.Rules {
		if rule.Host != "" {
			hostnames = append(hostnames, rule.Host)
		}
	}
	hostnames = uniqueSorted(hostnames)
	if len(hostnames) == 0 {
		return result, nil
	}

	ttl, err := TTLFromAnnotations(ingress.Annotations, opts.DefaultTTL)
	if err != nil {
		result.AddEvent(ref, "", err.Error())
		ttl = opts.DefaultTTL
	}

	targets := ingressTargets(ingress)
	return result, budget.appendSource(ctx, &result, opts, SourceIngress, ref, hostnames, targets, ttl)
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
