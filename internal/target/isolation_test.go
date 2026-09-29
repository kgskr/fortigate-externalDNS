package target

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
)

func TestIsolateDefinitionsExcludesOnlyInvalidTarget(t *testing.T) {
	good := FromAPI(ptr(apiTarget("good", "example.com", []string{"example.com"})))
	badURL := FromAPI(ptr(apiTarget("bad", "other.org", []string{"other.org"})))
	badURL.URL = "http://cleartext.example.com"

	set := IsolateDefinitions([]Definition{good, badURL})
	if len(set.Valid) != 1 || set.Valid[0].Name != "good" {
		t.Fatalf("valid = %#v", set.Valid)
	}
	invalid, ok := set.Invalid["dns-system/bad"]
	if !ok || invalid.Reason != FailureInvalid || invalid.Err == nil || len(set.Invalid) != 1 {
		t.Fatalf("invalid = %#v", set.Invalid)
	}
}

func TestIsolateDefinitionsExcludesBothSidesOfOverlapButKeepsSiblings(t *testing.T) {
	left := FromAPI(ptr(apiTarget("left", "example.com", []string{"example.com"})))
	right := FromAPI(ptr(apiTarget("right", "example.com", []string{"apps.example.com"})))
	other := FromAPI(ptr(apiTarget("other", "example.net", []string{"example.net"})))

	set := IsolateDefinitions([]Definition{left, right, other})
	if len(set.Valid) != 1 || set.Valid[0].Name != "other" {
		t.Fatalf("valid = %#v", set.Valid)
	}
	for _, key := range []string{"dns-system/left", "dns-system/right"} {
		if set.Invalid[key].Reason != FailureConflict {
			t.Fatalf("%s = %#v, want scope-conflict", key, set.Invalid[key])
		}
	}
}

func TestIsolateDefinitionsInvalidTargetCannotConflictWithHealthySibling(t *testing.T) {
	healthy := FromAPI(ptr(apiTarget("healthy", "example.com", []string{"example.com"})))
	broken := FromAPI(ptr(apiTarget("broken", "example.com", []string{"example.com"})))
	broken.ControllerID = ""

	set := IsolateDefinitions([]Definition{healthy, broken})
	if len(set.Valid) != 1 || set.Valid[0].Name != "healthy" || set.Invalid["dns-system/broken"].Reason != FailureInvalid {
		t.Fatalf("set = %#v", set)
	}
}

func TestBuildIsolatedDefinitionsStillRejectsDirectConfiguration(t *testing.T) {
	object := apiTarget("edge", "example.com", nil)
	if _, err := BuildIsolatedDefinitions(legacyConfig(), []v1alpha1.FortiGateDNSTarget{object}); err == nil {
		t.Fatal("direct settings alongside target mode must remain a global error")
	}
	set, err := BuildIsolatedDefinitions(config.Config{}, []v1alpha1.FortiGateDNSTarget{object})
	if err != nil || len(set.Valid) != 1 {
		t.Fatalf("set=%#v err=%v", set, err)
	}
}

func TestTargetDomainFilterMustBeInsideZone(t *testing.T) {
	for _, filter := range []string{"other.org", "notexample.com"} {
		definition := FromAPI(ptr(apiTarget("edge", "example.com", []string{filter})))
		if err := ValidateAll([]Definition{definition}); err == nil || !strings.Contains(err.Error(), "outside zone") {
			t.Fatalf("filter %q error = %v, want outside zone", filter, err)
		}
	}
	for _, filter := range []string{"example.com", "apps.example.com"} {
		definition := FromAPI(ptr(apiTarget("edge", "example.com", []string{filter})))
		if err := ValidateAll([]Definition{definition}); err != nil {
			t.Fatalf("filter %q inside the zone rejected: %v", filter, err)
		}
	}
	for _, filter := range []string{".example.com", "*.example.com"} {
		definition := FromAPI(ptr(apiTarget("edge", "example.com", []string{filter})))
		if err := ValidateAll([]Definition{definition}); err == nil {
			t.Fatalf("filter %q must be rejected", filter)
		}
	}
}

func TestSyncKeepsCredentialSubReasonAndLogsClientError(t *testing.T) {
	definitions := independentDefinitions()
	// left: Secret without the token key; right: client construction fails.
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: definitions[0].Namespace, Name: definitions[0].APITokenSecretRef.Name, UID: "l", ResourceVersion: "1"}, Data: map[string][]byte{"other": []byte("x")}},
		secretForDefinition(definitions[1], "super-secret-token", "1"),
	)
	resolver, _ := NewResolver(client.CoreV1())
	clients := newRecordingClientFactory()
	clients.fail[definitions[1].Key()] = true
	manager, _ := NewRuntimeManager(resolver, clients, newRecordingResourceFactory(), nil, nil)
	var logs bytes.Buffer
	manager.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))

	result, err := manager.Sync(context.Background(), definitions)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failures[definitions[0].Key()] != FailureCredentials || result.CredentialReasons[definitions[0].Key()] != CredentialTokenKeyMissing {
		t.Fatalf("credential failure = %#v / %#v", result.Failures, result.CredentialReasons)
	}
	if result.Failures[definitions[1].Key()] != FailureClient {
		t.Fatalf("client failure = %#v", result.Failures)
	}
	if !strings.Contains(logs.String(), "target client construction failed") || !strings.Contains(logs.String(), "factory raw detail") {
		t.Fatalf("client error was not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "super-secret-token") {
		t.Fatalf("log leaked the API token: %s", logs.String())
	}
}
