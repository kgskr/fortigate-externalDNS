package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

func TestInformerTargetMapperRoutesFixedEventKinds(t *testing.T) {
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc:  func(options metav1.ListOptions) (runtime.Object, error) { return &unstructured.UnstructuredList{}, nil },
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) { return watch.NewEmptyWatch(), nil },
		},
		&unstructured.Unstructured{}, 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	edge := newAPITarget("dns-system", "edge", []string{"apps"})
	edge.Spec.Sources = []string{"service", "gateway"}
	edge.Spec.HeadlessEnabled = true
	edge.Spec.GatewayTargetNamespaces = []string{"infra"}
	edge.Spec.CARef = &api.LocalKeyReference{Kind: "Secret", Name: "fortigate-ca", Key: "ca.crt"}
	other := newAPITarget("dns-system", "other", []string{"other"})
	for _, target := range []*api.FortiGateDNSTarget{edge, other} {
		object, err := api.ToUnstructured(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := informer.GetIndexer().Add(object); err != nil {
			t.Fatal(err)
		}
	}
	mapper := &informerTargetMapper{targets: informer}

	tests := []struct {
		name  string
		event platformqueue.Event
		want  []string
	}{
		{name: "service namespace", event: platformqueue.Event{Kind: platformqueue.EventService, Namespace: "apps"}, want: []string{"dns-system/edge"}},
		{name: "headless slice", event: platformqueue.Event{Kind: platformqueue.EventEndpointSlice, Namespace: "apps"}, want: []string{"dns-system/edge"}},
		{name: "gateway lookup scope", event: platformqueue.Event{Kind: platformqueue.EventGateway, Namespace: "infra"}, want: []string{"dns-system/edge"}},
		{name: "policy namespace", event: platformqueue.Event{Kind: platformqueue.EventPolicy, Namespace: "other"}, want: []string{"dns-system/other"}},
		{name: "token secret", event: platformqueue.Event{Kind: platformqueue.EventSecret, Namespace: "dns-system", Name: "fortigate-token"}, want: []string{"dns-system/edge", "dns-system/other"}},
		{name: "ca secret", event: platformqueue.Event{Kind: platformqueue.EventSecret, Namespace: "dns-system", Name: "fortigate-ca"}, want: []string{"dns-system/edge"}},
		{name: "ownership target ref", event: platformqueue.Event{Kind: platformqueue.EventOwnership, Namespace: "dns-system", RelatedTarget: "other"}, want: []string{"dns-system/other"}},
		{name: "target identity", event: platformqueue.Event{Kind: platformqueue.EventTarget, Namespace: "dns-system", Name: "edge"}, want: []string{"dns-system/edge"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keys, err := mapper.TargetsForEvent(context.Background(), test.event)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(keys))
			for _, key := range keys {
				got = append(got, key.String())
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mapped keys = %v, want %v", got, test.want)
			}
		})
	}
}
