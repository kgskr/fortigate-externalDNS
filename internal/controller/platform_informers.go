package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
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

func newInformerFactories(clients PlatformClients, namespace string, resync time.Duration) (*informerFactories, error) {
	if clients.Kubernetes == nil || clients.Gateway == nil || clients.Dynamic == nil {
		return nil, errors.New("Kubernetes, Gateway API, and dynamic clients are required")
	}
	if namespace == "" {
		return nil, errors.New("controller namespace is required")
	}

	sourceFactory := informers.NewSharedInformerFactory(clients.Kubernetes, resync)
	secretFactory := informers.NewSharedInformerFactoryWithOptions(clients.Kubernetes, resync, informers.WithNamespace(namespace))
	gatewayFactory := gatewayinformers.NewSharedInformerFactory(clients.Gateway, resync)
	platformFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clients.Dynamic, resync, namespace, nil)
	policyFactory := dynamicinformer.NewDynamicSharedInformerFactory(clients.Dynamic, resync)

	serviceInformer := sourceFactory.Core().V1().Services().Informer()
	ingressInformer := sourceFactory.Networking().V1().Ingresses().Informer()
	endpointSliceInformer := sourceFactory.Discovery().V1().EndpointSlices().Informer()
	secretInformer := secretFactory.Core().V1().Secrets().Informer()
	gatewayInformer := gatewayFactory.Gateway().V1().Gateways().Informer()
	httpRouteInformer := gatewayFactory.Gateway().V1().HTTPRoutes().Informer()
	targetInformer := platformFactory.ForResource(api.TargetGVR).Informer()
	policyInformer := policyFactory.ForResource(api.PolicyGVR).Informer()
	ownershipInformer := platformFactory.ForResource(api.OwnershipGVR).Informer()
	changePlanInformer := platformFactory.ForResource(api.ChangePlanGVR).Informer()

	return &informerFactories{
		start: []func(<-chan struct{}){
			sourceFactory.Start, secretFactory.Start, gatewayFactory.Start, platformFactory.Start, policyFactory.Start,
		},
		shutdown: []func(){
			sourceFactory.Shutdown, secretFactory.Shutdown, gatewayFactory.Shutdown, platformFactory.Shutdown, policyFactory.Shutdown,
		},
		bindings: []informerBinding{
			{kind: platformqueue.EventService, informer: serviceInformer},
			{kind: platformqueue.EventIngress, informer: ingressInformer},
			{kind: platformqueue.EventGateway, informer: gatewayInformer},
			{kind: platformqueue.EventHTTPRoute, informer: httpRouteInformer},
			{kind: platformqueue.EventEndpointSlice, informer: endpointSliceInformer},
			{kind: platformqueue.EventTarget, informer: targetInformer},
			{kind: platformqueue.EventPolicy, informer: policyInformer},
			{kind: platformqueue.EventOwnership, informer: ownershipInformer},
			{kind: platformqueue.EventChangePlan, informer: changePlanInformer},
			{kind: platformqueue.EventSecret, informer: secretInformer},
		},
		targets: targetInformer,
	}, nil
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
	requirements := make([]platformqueue.CacheRequirement, 0, len(f.bindings))
	for _, binding := range f.bindings {
		requirements = append(requirements, platformqueue.CacheRequirement{
			Kind: binding.kind, HasSynced: binding.informer.HasSynced,
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
