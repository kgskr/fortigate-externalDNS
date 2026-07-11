package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"strings"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

type CredentialReason string

const (
	CredentialSecretUnavailable CredentialReason = "secret-unavailable"
	CredentialTokenKeyMissing   CredentialReason = "token-key-missing"
	CredentialTokenEmpty        CredentialReason = "token-empty"
	CredentialCAUnavailable     CredentialReason = "ca-unavailable"
	CredentialCAKeyMissing      CredentialReason = "ca-key-missing"
	CredentialCAEmpty           CredentialReason = "ca-empty"
	CredentialCAKindUnsupported CredentialReason = "ca-kind-unsupported"
)

// CredentialError deliberately contains only a fixed reason. Kubernetes API
// errors, Secret names, keys, and values are never copied into status-safe
// error text.
type CredentialError struct {
	Reason CredentialReason
}

func (e *CredentialError) Error() string {
	return "target credentials are unavailable: " + string(e.Reason)
}

func IsCredentialError(err error, reason CredentialReason) bool {
	var credentialErr *CredentialError
	return errors.As(err, &credentialErr) && credentialErr.Reason == reason
}

// CoreReader is intentionally narrower than kubernetes.Interface. Resolver can
// perform only namespace-scoped Secret and ConfigMap reads.
type CoreReader interface {
	Secrets(namespace string) typedcorev1.SecretInterface
	ConfigMaps(namespace string) typedcorev1.ConfigMapInterface
}

type Resolver struct {
	core CoreReader
}

func NewResolver(core CoreReader) (*Resolver, error) {
	if core == nil {
		return nil, errors.New("credential core reader is required")
	}
	return &Resolver{core: core}, nil
}

// CredentialMaterial is short-lived in-memory material. RuntimeManager clears
// it immediately after ClientFactory returns and stores only Fingerprint.
type CredentialMaterial struct {
	apiToken    []byte
	caBundle    []byte
	fingerprint string
}

func (*CredentialMaterial) String() string   { return "<redacted-credential-material>" }
func (*CredentialMaterial) GoString() string { return "target.CredentialMaterial{<redacted>}" }

func (m *CredentialMaterial) APIToken() []byte {
	if m == nil {
		return nil
	}
	return append([]byte(nil), m.apiToken...)
}

func (m *CredentialMaterial) CABundle() []byte {
	if m == nil {
		return nil
	}
	return append([]byte(nil), m.caBundle...)
}

func (m *CredentialMaterial) Fingerprint() string {
	if m == nil {
		return ""
	}
	return m.fingerprint
}

func (m *CredentialMaterial) Clear() {
	if m == nil {
		return
	}
	clear(m.apiToken)
	clear(m.caBundle)
	m.apiToken = nil
	m.caBundle = nil
}

func (r *Resolver) Resolve(ctx context.Context, definition Definition) (*CredentialMaterial, error) {
	if definition.Legacy {
		token := []byte(definition.APIToken)
		if len(token) == 0 {
			return nil, &CredentialError{Reason: CredentialTokenEmpty}
		}
		return newCredentialMaterial(token, nil, "legacy", "", definition.CAFile), nil
	}
	if r == nil || r.core == nil || definition.APITokenSecretRef == nil {
		return nil, &CredentialError{Reason: CredentialSecretUnavailable}
	}

	secret, err := r.core.Secrets(definition.Namespace).Get(ctx, definition.APITokenSecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, &CredentialError{Reason: CredentialSecretUnavailable}
	}
	token, exists := secret.Data[definition.APITokenSecretRef.Key]
	if !exists {
		return nil, &CredentialError{Reason: CredentialTokenKeyMissing}
	}
	if len(token) == 0 {
		return nil, &CredentialError{Reason: CredentialTokenEmpty}
	}

	var caBundle []byte
	caVersion := ""
	if definition.CARef != nil {
		caBundle, caVersion, err = r.resolveCA(ctx, definition.Namespace, *definition.CARef)
		if err != nil {
			return nil, err
		}
	}
	return newCredentialMaterial(
		token,
		caBundle,
		string(secret.UID),
		secret.ResourceVersion,
		caVersion,
	), nil
}

func (r *Resolver) resolveCA(ctx context.Context, namespace string, reference v1alpha1.LocalKeyReference) ([]byte, string, error) {
	switch strings.ToLower(strings.TrimSpace(reference.Kind)) {
	case "secret":
		secret, err := r.core.Secrets(namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err != nil {
			return nil, "", &CredentialError{Reason: CredentialCAUnavailable}
		}
		value, exists := secret.Data[reference.Key]
		if !exists {
			return nil, "", &CredentialError{Reason: CredentialCAKeyMissing}
		}
		if len(value) == 0 {
			return nil, "", &CredentialError{Reason: CredentialCAEmpty}
		}
		return append([]byte(nil), value...), string(secret.UID) + "/" + secret.ResourceVersion, nil
	case "configmap":
		configMap, err := r.core.ConfigMaps(namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err != nil {
			return nil, "", &CredentialError{Reason: CredentialCAUnavailable}
		}
		if value, exists := configMap.BinaryData[reference.Key]; exists {
			if len(value) == 0 {
				return nil, "", &CredentialError{Reason: CredentialCAEmpty}
			}
			return append([]byte(nil), value...), string(configMap.UID) + "/" + configMap.ResourceVersion, nil
		}
		value, exists := configMap.Data[reference.Key]
		if !exists {
			return nil, "", &CredentialError{Reason: CredentialCAKeyMissing}
		}
		if value == "" {
			return nil, "", &CredentialError{Reason: CredentialCAEmpty}
		}
		return []byte(value), string(configMap.UID) + "/" + configMap.ResourceVersion, nil
	default:
		return nil, "", &CredentialError{Reason: CredentialCAKindUnsupported}
	}
}

func newCredentialMaterial(token, ca []byte, versions ...string) *CredentialMaterial {
	digest := sha256.New()
	hashPart(digest, token)
	hashPart(digest, ca)
	for _, version := range versions {
		hashPart(digest, []byte(version))
	}
	return &CredentialMaterial{
		apiToken:    append([]byte(nil), token...),
		caBundle:    append([]byte(nil), ca...),
		fingerprint: hex.EncodeToString(digest.Sum(nil)),
	}
}

func hashPart(digest hash.Hash, value []byte) {
	var length [8]byte
	for index := range length {
		length[len(length)-1-index] = byte(uint64(len(value)) >> (index * 8))
	}
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func credentialReferenceKey(kind, namespace, name string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + "|" + strings.ToLower(strings.TrimSpace(namespace)) + "|" + strings.ToLower(strings.TrimSpace(name))
}

func credentialReferences(definition Definition) []string {
	if definition.Legacy {
		return nil
	}
	result := []string{credentialReferenceKey("secret", definition.Namespace, definition.APITokenSecretRef.Name)}
	if definition.CARef != nil {
		result = append(result, credentialReferenceKey(definition.CARef.Kind, definition.Namespace, definition.CARef.Name))
	}
	return uniqueSorted(result)
}
