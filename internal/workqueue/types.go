// Package workqueue provides the target-scoped scheduling primitives used by
// informer adapters and reconciliation workers.
package workqueue

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TargetKey identifies one independently reconciled target.
type TargetKey struct {
	Namespace string
	Name      string
}

// NewTargetKey validates a target key before it enters the typed queue.
func NewTargetKey(namespace, name string) (TargetKey, error) {
	key := TargetKey{Namespace: strings.TrimSpace(namespace), Name: strings.TrimSpace(name)}
	if problems := validation.IsDNS1123Label(key.Namespace); len(problems) != 0 {
		return TargetKey{}, fmt.Errorf("invalid target namespace: %s", strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Subdomain(key.Name); len(problems) != 0 {
		return TargetKey{}, fmt.Errorf("invalid target name: %s", strings.Join(problems, "; "))
	}
	return key, nil
}

func (k TargetKey) String() string {
	return k.Namespace + "/" + k.Name
}

func (k TargetKey) valid() bool {
	_, err := NewTargetKey(k.Namespace, k.Name)
	return err == nil
}
