package target

import (
	"context"
	"strings"
	"testing"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolverReadsNamespacedTokenAndSecretCAInMemory(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "fortigate-token"}, Data: map[string][]byte{"api-token": []byte("wrong-namespace")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token", UID: "token-uid", ResourceVersion: "10"}, Data: map[string][]byte{"api-token": []byte("expected-token")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-ca", UID: "ca-uid", ResourceVersion: "11"}, Data: map[string][]byte{"ca.crt": []byte("test-ca")}},
	)
	resolver, err := NewResolver(client.CoreV1())
	if err != nil {
		t.Fatal(err)
	}
	definition := FromAPI(ptr(apiTarget("edge", "example.com", []string{"example.com"})))
	definition.CARef = &v1alpha1.LocalKeyReference{Kind: "Secret", Name: "fortigate-ca", Key: "ca.crt"}
	material, err := resolver.Resolve(context.Background(), definition)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := string(material.APIToken()); got != "expected-token" {
		t.Fatalf("token = %q, namespaced lookup used wrong value", got)
	}
	if got := string(material.CABundle()); got != "test-ca" {
		t.Fatalf("CA = %q", got)
	}
	if len(material.Fingerprint()) != 64 || strings.Contains(material.Fingerprint(), "expected-token") {
		t.Fatalf("unsafe rotation fingerprint %q", material.Fingerprint())
	}
	material.Clear()
	if len(material.APIToken()) != 0 || len(material.CABundle()) != 0 {
		t.Fatal("Clear() retained credential bytes")
	}
}

func TestResolverConfigMapCARotationChangesFingerprint(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token", UID: "token-uid", ResourceVersion: "1"}, Data: map[string][]byte{"api-token": []byte("token-value")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-ca", UID: "ca-uid", ResourceVersion: "1"}, Data: map[string]string{"ca.crt": "ca-one"}},
	)
	resolver, _ := NewResolver(client.CoreV1())
	definition := FromAPI(ptr(apiTarget("edge", "example.com", nil)))
	definition.CARef = &v1alpha1.LocalKeyReference{Kind: "ConfigMap", Name: "fortigate-ca", Key: "ca.crt"}
	first, err := resolver.Resolve(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}

	configMap, _ := client.CoreV1().ConfigMaps("dns-system").Get(context.Background(), "fortigate-ca", metav1.GetOptions{})
	configMap.Data["ca.crt"] = "ca-two"
	configMap.ResourceVersion = "2"
	if _, err := client.CoreV1().ConfigMaps("dns-system").Update(context.Background(), configMap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("CA rotation did not change credential fingerprint")
	}
}

func TestResolverTokenRotationChangesFingerprint(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token", UID: "token-uid", ResourceVersion: "1"},
		Data:       map[string][]byte{"api-token": []byte("token-one")},
	})
	resolver, _ := NewResolver(client.CoreV1())
	definition := FromAPI(ptr(apiTarget("edge", "example.com", nil)))
	first, _ := resolver.Resolve(context.Background(), definition)
	secret, _ := client.CoreV1().Secrets("dns-system").Get(context.Background(), "fortigate-token", metav1.GetOptions{})
	secret.Data["api-token"] = []byte("token-two")
	secret.ResourceVersion = "2"
	if _, err := client.CoreV1().Secrets("dns-system").Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	second, _ := resolver.Resolve(context.Background(), definition)
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("token rotation did not change credential fingerprint")
	}
}

func TestResolverFailuresAreFixedAndSanitized(t *testing.T) {
	secretValue := "super-sensitive-token"
	tests := []struct {
		name       string
		objects    []corev1.Secret
		mutate     func(*Definition)
		wantReason CredentialReason
	}{
		{name: "secret missing", wantReason: CredentialSecretUnavailable},
		{name: "key missing", objects: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token"}, Data: map[string][]byte{"other": []byte(secretValue)}}}, wantReason: CredentialTokenKeyMissing},
		{name: "empty token", objects: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token"}, Data: map[string][]byte{"api-token": nil}}}, wantReason: CredentialTokenEmpty},
		{name: "unsupported CA", objects: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "fortigate-token"}, Data: map[string][]byte{"api-token": []byte(secretValue)}}}, mutate: func(d *Definition) {
			d.CARef = &v1alpha1.LocalKeyReference{Kind: "File", Name: secretValue, Key: secretValue}
		}, wantReason: CredentialCAKindUnsupported},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			objects := make([]runtime.Object, 0, len(testCase.objects))
			for index := range testCase.objects {
				object := testCase.objects[index]
				objects = append(objects, &object)
			}
			client := fake.NewSimpleClientset(objects...)
			resolver, _ := NewResolver(client.CoreV1())
			definition := FromAPI(ptr(apiTarget("edge", "example.com", nil)))
			if testCase.mutate != nil {
				testCase.mutate(&definition)
			}
			_, err := resolver.Resolve(context.Background(), definition)
			if !IsCredentialError(err, testCase.wantReason) {
				t.Fatalf("Resolve() error = %v, want %q", err, testCase.wantReason)
			}
			for _, forbidden := range []string{secretValue, "fortigate-token", "dns-system"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("sanitized error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}
