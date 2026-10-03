package target

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolverTrimsTokenWhitespaceAndRejectsBlankToken(t *testing.T) {
	newResolver := func(token string) *Resolver {
		client := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token", UID: "u", ResourceVersion: "1", Annotations: bindingAnnotations(FromAPI(ptr(apiTarget("edge", "example.com", nil))))},
			Data:       map[string][]byte{"api-token": []byte(token)},
		})
		resolver, err := NewResolver(client.CoreV1())
		if err != nil {
			t.Fatal(err)
		}
		return resolver
	}
	definition := FromAPI(ptr(apiTarget("edge", "example.com", nil)))

	material, err := newResolver("  expected-token\r\n").Resolve(context.Background(), definition)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := string(material.APIToken()); got != "expected-token" {
		t.Fatalf("token = %q, want surrounding whitespace trimmed", got)
	}

	if _, err := newResolver(" \n\t").Resolve(context.Background(), definition); !IsCredentialError(err, CredentialTokenEmpty) {
		t.Fatalf("all-whitespace token error = %v, want token-empty", err)
	}
}
