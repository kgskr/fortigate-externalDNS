package controller

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/gilsu/fortigate-external-dns/internal/source"
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
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
}
