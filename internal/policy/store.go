package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
)

type Provider interface {
	Evaluator(context.Context, []string, Bounds) (*Evaluator, error)
}

type DynamicProvider struct {
	client dynamic.Interface
}

func NewDynamicProvider(client dynamic.Interface) (*DynamicProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("dynamic policy client is required")
	}
	return &DynamicProvider{client: client}, nil
}

// Evaluator lists a complete deterministic policy snapshot for the configured
// namespaces. An empty namespace set intentionally lists across all namespaces.
func (p *DynamicProvider) Evaluator(ctx context.Context, namespaces []string, bounds Bounds) (*Evaluator, error) {
	policies, err := p.list(ctx, namespaces)
	if err != nil {
		return nil, err
	}
	return NewEvaluator(bounds, policies)
}

func (p *DynamicProvider) list(ctx context.Context, namespaces []string) ([]NamedPolicy, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("dynamic policy client is required")
	}
	namespaces = uniqueNamespaces(namespaces)
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	seen := map[string]struct{}{}
	var policies []NamedPolicy
	for _, namespace := range namespaces {
		list, err := p.client.Resource(v1alpha1.PolicyGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list DNS policies: %w", err)
		}
		for i := range list.Items {
			var object v1alpha1.FortiGateDNSPolicy
			if err := v1alpha1.FromUnstructured(&list.Items[i], &object); err != nil {
				return nil, err
			}
			key := object.Namespace + "/" + object.Name
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("duplicate DNS policy %q", key)
			}
			seen[key] = struct{}{}
			policies = append(policies, NamedPolicy{Namespace: object.Namespace, Name: object.Name, Spec: object.Spec})
		}
	}
	sort.Slice(policies, func(i, j int) bool {
		if policies[i].Namespace != policies[j].Namespace {
			return policies[i].Namespace < policies[j].Namespace
		}
		return policies[i].Name < policies[j].Name
	})
	return policies, nil
}

func uniqueNamespaces(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
