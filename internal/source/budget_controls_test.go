package source

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExpandedServiceModesShareResourceAndDiscoveryBudgets(t *testing.T) {
	opts := testOptions()
	opts.Sources = []string{SourceService}
	opts.PublishExternalNameServices = true
	opts.PublishHeadlessServices = true
	opts.MaxEndpointsPerResource = 2
	opts.MaxEndpointsPerDiscovery = 2
	external := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "a-external", Namespace: "apps", UID: "external-uid", Annotations: map[string]string{AnnotationHostname: "a.example.com"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "upstream.example.net"},
	}
	headless := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "b-headless", Namespace: "apps", UID: "headless-uid", Annotations: map[string]string{AnnotationHostname: "b.example.com", AnnotationPublishHeadless: "true"}},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}
	ready := true
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "headless-slice", Namespace: "apps", Labels: map[string]string{discoveryv1.LabelServiceName: headless.Name}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.1", "192.0.2.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
	ordinary := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "c-ordinary", Namespace: "apps", UID: "ordinary-uid", Labels: map[string]string{"publication": "denied"}, Annotations: map[string]string{AnnotationHostname: "c.example.com"}},
		Spec:       corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.3"}},
	}
	result, err := Discover(context.Background(), KubernetesClients{Core: fake.NewSimpleClientset(external, headless, ordinary, slice)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceComplete(SourceService) || len(result.Endpoints) != 2 {
		t.Fatalf("headless must share remaining discovery budget: %#v", result)
	}
	for _, endpoint := range result.Endpoints {
		if endpoint.Source.Name == headless.Name {
			t.Fatal("headless resource must be rejected entirely, not truncated")
		}
		if endpoint.Source.Name == ordinary.Name && result.MetadataFor(endpoint.Source).Labels["publication"] != "denied" {
			t.Fatal("publication must preserve the policy selector metadata")
		}
	}
	external.Annotations[AnnotationHostname] = "a.example.com,b.example.com,c.example.com"
	if got := EndpointsFromService(external, opts); len(got.Endpoints) != 0 || got.SourceComplete(SourceService) {
		t.Fatalf("ExternalName bypassed resource budget: %#v", got)
	}
	headless.Annotations[AnnotationHostname] = "b.example.com,c.example.com"
	if got := EndpointsFromServiceWithEndpointSlices(headless, []*discoveryv1.EndpointSlice{slice}, opts); len(got.Endpoints) != 0 || got.SourceComplete(SourceService) {
		t.Fatalf("headless bypassed resource budget: %#v", got)
	}
}

func TestNonPublishingObjectsDoNotRetainPolicyMetadata(t *testing.T) {
	ignored := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "ignored", Namespace: "apps", Labels: map[string]string{"publication": "denied"}}}
	result := EndpointsFromService(ignored, testOptions())
	if len(result.Metadata) != 0 {
		t.Fatalf("nonpublishing objects need no retained metadata: %#v", result.Metadata)
	}
}
