package target

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
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

func TestIsolateDefinitionsPreservesUnchangedActiveWriterAgainstNewOverlap(t *testing.T) {
	incumbent := FromAPI(ptr(apiTarget("incumbent", "example.com", []string{"example.com"})))
	incumbent.UID, incumbent.Generation = "incumbent-uid", 3
	newcomer := FromAPI(ptr(apiTarget("newcomer", "example.com", []string{"app.example.com"})))
	newcomer.UID, newcomer.Generation = "newcomer-uid", 1
	active := map[string]Definition{incumbent.Key(): incumbent}
	for _, definitions := range [][]Definition{{incumbent, newcomer}, {newcomer, incumbent}} {
		set := IsolateDefinitionsWithActive(definitions, active)
		if len(set.Valid) != 1 || set.Valid[0].Key() != incumbent.Key() || set.Invalid[newcomer.Key()].Reason != FailureConflict {
			t.Fatalf("new overlap displaced active target: %#v", set)
		}
	}
	changed := incumbent
	changed.Generation++
	set := IsolateDefinitionsWithActive([]Definition{changed, newcomer}, active)
	if len(set.Valid) != 0 || len(set.Invalid) != 2 {
		t.Fatalf("changed incumbent inherited old authority: %#v", set)
	}
}

func TestIsolateDefinitionsConflictGraphIsIndependentOfInputOrder(t *testing.T) {
	makeDefinition := func(name, scope string) Definition {
		definition := FromAPI(ptr(apiTarget(name, "example.com", []string{scope})))
		definition.UID, definition.Generation = name+"-uid", 1
		return definition
	}
	a := makeDefinition("a", "example.com")
	b := makeDefinition("b", "example.com")
	c := makeDefinition("c", "example.com")
	left := makeDefinition("left", "left.example.com")
	right := makeDefinition("right", "right.example.com")
	parent := makeDefinition("parent", "example.com")
	changed := left
	changed.Generation++
	recreated := left
	recreated.UID = "replacement-uid"
	dryRun := c
	dryRun.DryRun = true
	keepA, keepB, keepC := a, b, c
	for _, definition := range []*Definition{&keepA, &keepB, &keepC} {
		definition.CleanupPolicy = v1alpha1.CleanupPolicyKeep
		definition.AllowNonDestructiveOverlap = true
	}
	tests := []struct {
		name        string
		definitions []Definition
		active      map[string]Definition
		wantValid   []string
	}{
		{name: "three identical scopes at startup", definitions: []Definition{a, b, c}},
		{name: "parent conflicts with both children", definitions: []Definition{left, parent, right}},
		{name: "active writer rejects all newcomers", definitions: []Definition{a, b, c}, active: map[string]Definition{b.Key(): b}, wantValid: []string{"b"}},
		{name: "rejected newcomer cannot block independent sibling", definitions: []Definition{parent, right, left}, active: map[string]Definition{left.Key(): left}, wantValid: []string{"left", "right"}},
		{name: "both active siblings survive a new parent", definitions: []Definition{parent, right, left}, active: map[string]Definition{left.Key(): left, right.Key(): right}, wantValid: []string{"left", "right"}},
		{name: "changed writer loses precedence", definitions: []Definition{changed, parent, right}, active: map[string]Definition{left.Key(): left}},
		{name: "recreated writer loses precedence", definitions: []Definition{recreated, parent, right}, active: map[string]Definition{left.Key(): left}},
		{name: "conflicting active writers fail closed", definitions: []Definition{a, b, c}, active: map[string]Definition{a.Key(): a, b.Key(): b}},
		{name: "dry run remains isolated from writers", definitions: []Definition{a, b, dryRun}, wantValid: []string{"c"}},
		{name: "explicit non destructive overlap remains allowed", definitions: []Definition{keepA, keepB, keepC}, wantValid: []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Exercise every LIST ordering, including a rejected parent appearing
			// before or after the incumbent and its unrelated sibling.
			for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				definitions := []Definition{tt.definitions[order[0]], tt.definitions[order[1]], tt.definitions[order[2]]}
				set := IsolateDefinitionsWithActive(definitions, tt.active)
				var got []string
				for _, definition := range set.Valid {
					got = append(got, definition.Name)
				}
				slices.Sort(got)
				if !slices.Equal(got, tt.wantValid) {
					t.Fatalf("order %v: valid = %v, want %v", order, got, tt.wantValid)
				}
				for _, definition := range definitions {
					if !slices.Contains(tt.wantValid, definition.Name) && set.Invalid[definition.Key()].Reason != FailureConflict {
						t.Fatalf("order %v: %s must report scope-conflict: %#v", order, definition.Key(), set.Invalid)
					}
				}
				if err := ValidateAll(set.Valid); err != nil {
					t.Fatalf("order %v: selected targets cannot run together: %v", order, err)
				}
			}
		})
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
	for _, filter := range []string{".example.com", "*.example.com", "foo..example.com", "_svc.example.com", "bücher.example.com", "-svc.example.com", "example.com..", strings.Repeat("a", 64) + ".example.com"} {
		definition := FromAPI(ptr(apiTarget("edge", "example.com", []string{filter})))
		if err := ValidateAll([]Definition{definition}); err == nil {
			t.Fatalf("filter %q must be rejected", filter)
		}
		healthy := FromAPI(ptr(apiTarget("healthy", "example.com", []string{"example.com"})))
		set := IsolateDefinitions([]Definition{definition, healthy})
		if len(set.Valid) != 1 || set.Valid[0].Name != "healthy" || set.Invalid[definition.Key()].Reason != FailureInvalid {
			t.Fatalf("invalid filter %q must isolate only its own target: %#v", filter, set)
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
