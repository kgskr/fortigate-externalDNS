package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ToUnstructured converts a typed project API object for use with the dynamic
// client. Resource identity and optimistic-concurrency metadata are preserved.
func ToUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	if obj == nil {
		return nil, fmt.Errorf("cannot convert a nil API object")
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("convert %T to unstructured: %w", obj, err)
	}
	return &unstructured.Unstructured{Object: content}, nil
}

// FromUnstructured converts a dynamic-client object into an explicitly chosen
// typed destination. Callers retain control of which kind is accepted.
func FromUnstructured(in *unstructured.Unstructured, out runtime.Object) error {
	if in == nil || out == nil {
		return fmt.Errorf("input and output API objects are required")
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(in.Object, out); err != nil {
		return fmt.Errorf("convert %s to %T: %w", in.GroupVersionKind(), out, err)
	}
	return nil
}

// NewForGVK allocates a registered project API object for a dynamic result.
func NewForGVK(gvk schema.GroupVersionKind) (runtime.Object, error) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register project API types: %w", err)
	}
	obj, err := scheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("allocate %s: %w", gvk, err)
	}
	return obj, nil
}
