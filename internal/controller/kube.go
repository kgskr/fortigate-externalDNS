package controller

import (
	"errors"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/kgskr/fortigate-external-dns/internal/source"
)

func NewKubernetesClients(kubeconfig string) (source.KubernetesClients, error) {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return source.KubernetesClients{}, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return source.KubernetesClients{}, err
	}
	gateway, err := versioned.NewForConfig(cfg)
	if err != nil {
		return source.KubernetesClients{}, err
	}
	return source.KubernetesClients{Core: core, Gateway: gateway}, nil
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	// Only fall back to a local kubeconfig when we are genuinely not running in a
	// cluster. Any other in-cluster error (for example an unreadable service
	// account token) is a real failure and must not be masked by the fallback.
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, err
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
}
