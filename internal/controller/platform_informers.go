package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
)

type PlatformClients struct {
	Kubernetes kubernetes.Interface
	Gateway    gatewayclient.Interface
	Dynamic    dynamic.Interface
}

type informerBinding struct {
	kind     platformqueue.EventKind
	informer cache.SharedIndexInformer
}

type informerFactories struct {
	start    []func(<-chan struct{})
	shutdown []func()
	bindings []informerBinding
	targets  cache.SharedIndexInformer
}

func newInformerFactories(clients PlatformClients, config PlatformRuntimeConfig, resync time.Duration) (*informerFactories, error) {
	if clients.Dynamic == nil {
		return nil, fmt.Errorf("dynamic client is required")
	}
	if config.Namespace == "" {
		return nil, fmt.Errorf("controller namespace is required")
	}
	enabled := normalizedSet(config.Sources)
	if (enabled["service"] || enabled["ingress"]) && clients.Kubernetes == nil {
		return nil, fmt.Errorf("Kubernetes client is required for enabled sources")
	}
	if enabled["gateway"] && clients.Gateway == nil {
		return nil, fmt.Errorf("Gateway API client is required for gateway source")
	}

	result := &informerFactories{}
	addBinding := func(kind platformqueue.EventKind, informer cache.SharedIndexInformer) {
		result.bindings = append(result.bindings, informerBinding{kind: kind, informer: informer})
	}
	for _, namespace := range informerNamespaces(config.SourceNamespaces) {
		if enabled["service"] || enabled["ingress"] {
			factory := informers.NewSharedInformerFactoryWithOptions(clients.Kubernetes, resync, informers.WithNamespace(namespace))
			result.start = append(result.start, factory.Start)
			result.shutdown = append(result.shutdown, factory.Shutdown)
			if enabled["service"] {
				addBinding(platformqueue.EventService, factory.Core().V1().Services().Informer())
				if config.Headless {
					addBinding(platformqueue.EventEndpointSlice, factory.Discovery().V1().EndpointSlices().Informer())
				}
			}
			if enabled["ingress"] {
				addBinding(platformqueue.EventIngress, factory.Networking().V1().Ingresses().Informer())
			}
		}
	}
	if enabled["gateway"] {
		var gatewayNamespaces []string
		if len(informerNamespaces(config.SourceNamespaces)) == 1 && informerNamespaces(config.SourceNamespaces)[0] == metav1.NamespaceAll {
			gatewayNamespaces = nil
		} else {
			gatewayNamespaces = append(append([]string(nil), config.SourceNamespaces...), config.GatewayNamespaces...)
		}
		for _, namespace := range informerNamespaces(gatewayNamespaces) {
			factory := gatewayinformers.NewSharedInformerFactoryWithOptions(clients.Gateway, resync, gatewayinformers.WithNamespace(namespace))
			result.start = append(result.start, factory.Start)
			result.shutdown = append(result.shutdown, factory.Shutdown)
			addBinding(platformqueue.EventGateway, factory.Gateway().V1().Gateways().Informer())
			if informerNamespaceSelected(config.SourceNamespaces, namespace) {
				addBinding(platformqueue.EventHTTPRoute, factory.Gateway().V1().HTTPRoutes().Informer())
			}
		}
	}
	platformFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clients.Dynamic, resync, config.Namespace, nil)
	targetInformer := platformFactory.ForResource(api.TargetGVR).Informer()
	result.start = append(result.start, platformFactory.Start)
	result.shutdown = append(result.shutdown, platformFactory.Shutdown)
	addBinding(platformqueue.EventTarget, targetInformer)
	if config.Ownership {
		addBinding(platformqueue.EventOwnership, platformFactory.ForResource(api.OwnershipGVR).Informer())
	}
	if config.PlanApproval {
		addBinding(platformqueue.EventChangePlan, platformFactory.ForResource(api.ChangePlanGVR).Informer())
	}
	if config.Policy {
		for _, namespace := range informerNamespaces(config.SourceNamespaces) {
			factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clients.Dynamic, resync, namespace, nil)
			result.start = append(result.start, factory.Start)
			result.shutdown = append(result.shutdown, factory.Shutdown)
			addBinding(platformqueue.EventPolicy, factory.ForResource(api.PolicyGVR).Informer())
		}
	}
	result.targets = targetInformer
	return result, nil
}

func normalizedSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[strings.ToLower(strings.TrimSpace(value))] = true
	}
	return result
}

func informerNamespaces(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	if len(set) == 0 {
		return []string{metav1.NamespaceAll}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func informerNamespaceSelected(sourceNamespaces []string, namespace string) bool {
	if len(informerNamespaces(sourceNamespaces)) == 1 && informerNamespaces(sourceNamespaces)[0] == metav1.NamespaceAll {
		return true
	}
	for _, candidate := range sourceNamespaces {
		if strings.TrimSpace(candidate) == namespace {
			return true
		}
	}
	return false
}

func (f *informerFactories) Start(stop <-chan struct{}) {
	for _, start := range f.start {
		start(stop)
	}
}

func (f *informerFactories) Shutdown() {
	for _, shutdown := range f.shutdown {
		shutdown()
	}
}

func (f *informerFactories) SyncGate() (*platformqueue.CacheSyncGate, error) {
	// CacheSyncGate reports fixed resource kinds. One kind can now have a
	// separate informer for each source namespace, and all must be synchronized.
	byKind := map[platformqueue.EventKind][]cache.InformerSynced{}
	for _, binding := range f.bindings {
		byKind[binding.kind] = append(byKind[binding.kind], binding.informer.HasSynced)
	}
	requirements := make([]platformqueue.CacheRequirement, 0, len(byKind))
	for kind, checks := range byKind {
		requirements = append(requirements, platformqueue.CacheRequirement{
			Kind: kind, HasSynced: func() bool {
				for _, hasSynced := range checks {
					if !hasSynced() {
						return false
					}
				}
				return true
			},
		})
	}
	return platformqueue.NewCacheSyncGate(requirements...)
}

func (f *informerFactories) WaitForSync(ctx context.Context) bool {
	functions := make([]cache.InformerSynced, 0, len(f.bindings))
	for _, binding := range f.bindings {
		functions = append(functions, binding.informer.HasSynced)
	}
	return cache.WaitForCacheSync(ctx.Done(), functions...)
}

func (f *informerFactories) RegisterHandlers(dispatcher platformqueue.Dispatcher, report func(error)) error {
	if report == nil {
		report = func(error) {}
	}
	for _, binding := range f.bindings {
		binding := binding
		_, err := binding.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(object any) {
				dispatchInformerEvent(dispatcher, binding.kind, platformqueue.EventAdd, nil, object, report)
			},
			UpdateFunc: func(oldObject, newObject any) {
				dispatchInformerEvent(dispatcher, binding.kind, platformqueue.EventUpdate, oldObject, newObject, report)
			},
			DeleteFunc: func(object any) {
				dispatchInformerEvent(dispatcher, binding.kind, platformqueue.EventDelete, object, nil, report)
			},
		})
		if err != nil {
			return fmt.Errorf("register %s informer handler: %w", binding.kind, err)
		}
	}
	return nil
}

func dispatchInformerEvent(dispatcher platformqueue.Dispatcher, kind platformqueue.EventKind, action platformqueue.EventAction, oldObject, newObject any, report func(error)) {
	event, err := semanticEvent(kind, action, oldObject, newObject)
	if err != nil {
		report(err)
		return
	}
	if _, err := dispatcher.Handle(context.Background(), event); err != nil {
		report(err)
	}
}
