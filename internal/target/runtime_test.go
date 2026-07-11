package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRuntimeManagerConstructsIndependentTargetState(t *testing.T) {
	definitions := independentDefinitions()
	resolver := resolverForDefinitions(t, definitions)
	clients := newRecordingClientFactory()
	resources := newRecordingResourceFactory()
	manager, err := NewRuntimeManager(resolver, clients, resources, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Sync(context.Background(), definitions)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(result.Ready) != 2 || len(result.Failures) != 0 {
		t.Fatalf("Sync() result = %#v", result)
	}

	left, _ := manager.Runtime(definitions[0].Key())
	right, _ := manager.Runtime(definitions[1].Key())
	if left == nil || right == nil || left == right || left.ProviderClient() == right.ProviderClient() || left.Queue == right.Queue || left.Retry == right.Retry {
		t.Fatalf("target runtimes share mutable state: left=%p right=%p", left, right)
	}
	if left.Stores.PlanStore == right.Stores.PlanStore || left.Stores.OwnershipStore == right.Stores.OwnershipStore || left.Stores.StatusStore == right.Stores.StatusStore {
		t.Fatal("target runtimes share plan, ownership, or status handles")
	}
	if left.Definition.Zone == right.Definition.Zone || left.Definition.VDOM == right.Definition.VDOM {
		t.Fatalf("independent zone/VDOM configuration was lost: %#v %#v", left.Definition, right.Definition)
	}
	if left.Definition.APIToken != "" || right.Definition.APIToken != "" {
		t.Fatal("runtime retained an inline API token")
	}
	for _, material := range clients.materials {
		if len(material.APIToken()) != 0 || len(material.CABundle()) != 0 {
			t.Fatal("manager retained client-factory credential material")
		}
	}
}

func TestLegacyRuntimeKeepsInlineCredentialOutOfStoredDefinition(t *testing.T) {
	definitions, err := BuildDefinitions(legacyConfig(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := newRecordingClientFactory()
	manager, err := NewRuntimeManager(nil, clients, newRecordingResourceFactory(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Sync(context.Background(), definitions)
	if err != nil || len(result.Ready) != 1 {
		t.Fatalf("Sync() result=%#v err=%v", result, err)
	}
	runtime, exists := manager.Runtime(DefaultName)
	if !exists || runtime.Definition.APIToken != "" || clients.callCount(DefaultName) != 1 {
		t.Fatalf("legacy runtime retained credential or failed to build: %#v", runtime)
	}
	if got := manager.Definitions(); len(got) != 1 || got[0].APIToken != "" {
		t.Fatalf("manager definitions retained legacy token: %#v", got)
	}
}

func TestMissingSecretDoesNotConstructClientOrBlockHealthyTarget(t *testing.T) {
	definitions := independentDefinitions()
	clientset := fake.NewSimpleClientset(secretForDefinition(definitions[1], "healthy-token", "1"))
	resolver, _ := NewResolver(clientset.CoreV1())
	clients := newRecordingClientFactory()
	manager, _ := NewRuntimeManager(resolver, clients, newRecordingResourceFactory(), nil, nil)
	result, err := manager.Sync(context.Background(), definitions)
	if err != nil {
		t.Fatalf("Sync() global error = %v", err)
	}
	if result.Failures[definitions[0].Key()] != FailureCredentials {
		t.Fatalf("missing target failure = %#v", result.Failures)
	}
	if _, ready := manager.Runtime(definitions[1].Key()); !ready {
		t.Fatal("healthy target was blocked by sibling credential failure")
	}
	if _, failed := manager.Runtime(definitions[0].Key()); failed {
		t.Fatal("credential failure retained a runnable client")
	}
	if clients.callCount(definitions[0].Key()) != 0 || clients.callCount(definitions[1].Key()) != 1 {
		t.Fatalf("client calls = %#v", clients.calls)
	}
}

func TestSecretAndCARotationRebuildOnlyAffectedClientAndEnqueueTarget(t *testing.T) {
	definition := independentDefinitions()[0]
	definition.CARef = &localCAReference
	clientset := fake.NewSimpleClientset(
		secretForDefinition(definition, "token-one", "1"),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: definition.Namespace, Name: localCAReference.Name, UID: "ca-uid", ResourceVersion: "1"}, Data: map[string]string{localCAReference.Key: "ca-one"}},
	)
	resolver, _ := NewResolver(clientset.CoreV1())
	clients := newRecordingClientFactory()
	var enqueueMu sync.Mutex
	var enqueued []string
	manager, _ := NewRuntimeManager(resolver, clients, newRecordingResourceFactory(), nil, func(key string) {
		enqueueMu.Lock()
		enqueued = append(enqueued, key)
		enqueueMu.Unlock()
	})
	if _, err := manager.Sync(context.Background(), []Definition{definition}); err != nil {
		t.Fatal(err)
	}
	first, _ := manager.Runtime(definition.Key())
	firstFingerprint := first.CredentialFingerprint()

	secret, _ := clientset.CoreV1().Secrets(definition.Namespace).Get(context.Background(), definition.APITokenSecretRef.Name, metav1.GetOptions{})
	secret.Data[definition.APITokenSecretRef.Key] = []byte("token-two")
	secret.ResourceVersion = "2"
	if _, err := clientset.CoreV1().Secrets(definition.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background(), []Definition{definition}); err != nil {
		t.Fatal(err)
	}
	second, _ := manager.Runtime(definition.Key())
	if second == first || second.CredentialFingerprint() == firstFingerprint || clients.callCount(definition.Key()) != 2 {
		t.Fatal("Secret rotation did not rebuild exactly the affected target client")
	}

	configMap, _ := clientset.CoreV1().ConfigMaps(definition.Namespace).Get(context.Background(), localCAReference.Name, metav1.GetOptions{})
	configMap.Data[localCAReference.Key] = "ca-two"
	configMap.ResourceVersion = "2"
	if _, err := clientset.CoreV1().ConfigMaps(definition.Namespace).Update(context.Background(), configMap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background(), []Definition{definition}); err != nil {
		t.Fatal(err)
	}
	third, _ := manager.Runtime(definition.Key())
	if third == second || clients.callCount(definition.Key()) != 3 {
		t.Fatal("CA rotation did not rebuild the target client")
	}

	targets := manager.CredentialEvent("ConfigMap", definition.Namespace, localCAReference.Name)
	if len(targets) != 1 || targets[0] != definition.Key() {
		t.Fatalf("CredentialEvent() = %v", targets)
	}
	enqueueMu.Lock()
	defer enqueueMu.Unlock()
	if len(enqueued) < 4 {
		t.Fatalf("rotation and reference events were not enqueued: %v", enqueued)
	}
}

func TestClientFactoryFailureIsSanitizedAndIsolated(t *testing.T) {
	definitions := independentDefinitions()
	resolver := resolverForDefinitions(t, definitions)
	clients := newRecordingClientFactory()
	clients.fail[definitions[0].Key()] = true
	manager, _ := NewRuntimeManager(resolver, clients, newRecordingResourceFactory(), nil, nil)
	result, err := manager.Sync(context.Background(), definitions)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failures[definitions[0].Key()] != FailureClient {
		t.Fatalf("factory failure = %#v", result.Failures)
	}
	if _, exists := manager.Runtime(definitions[1].Key()); !exists {
		t.Fatal("healthy target was blocked by client factory failure")
	}
	if got := fmt.Sprintf("%#v", result); containsAny(got, "token-left", "token-right", "factory raw") {
		t.Fatalf("Sync result leaked factory detail or credentials: %s", got)
	}
}

func TestRunAllFailureIsolationAndIndependentRetryState(t *testing.T) {
	definitions := independentDefinitions()
	manager := readyManager(t, definitions)
	healthyDone := make(chan struct{})
	releaseFailure := make(chan struct{})
	resultDone := make(chan map[string]RunResult, 1)
	failingKey := definitions[0].Key()
	healthyKey := definitions[1].Key()
	go func() {
		resultDone <- manager.RunAll(context.Background(), func(_ context.Context, runtime *Runtime) error {
			if runtime.Definition.Key() == failingKey {
				<-releaseFailure
				return Fail(FailurePolicy)
			}
			close(healthyDone)
			return nil
		})
	}()
	select {
	case <-healthyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy target waited for failing sibling")
	}
	close(releaseFailure)
	results := <-resultDone
	if !results[healthyKey].Succeeded || results[failingKey].Reason != FailurePolicy {
		t.Fatalf("RunAll() = %#v", results)
	}
	failing, _ := manager.Runtime(failingKey)
	healthy, _ := manager.Runtime(healthyKey)
	if failing.Retry.Snapshot().Attempts != 1 || healthy.Retry.Snapshot().Attempts != 0 {
		t.Fatalf("retry state crossed targets: failing=%#v healthy=%#v", failing.Retry.Snapshot(), healthy.Retry.Snapshot())
	}
}

func TestRunAllKeepsProviderPolicyOwnershipApprovalAndRetryFailuresTargetScoped(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		error  error
		reason FailureReason
	}{
		{name: "provider", error: errors.New("raw provider body with token-left"), reason: FailureProvider},
		{name: "policy", error: Fail(FailurePolicy), reason: FailurePolicy},
		{name: "ownership", error: Fail(FailureOwnership), reason: FailureOwnership},
		{name: "approval", error: Fail(FailureApproval), reason: FailureApproval},
		{name: "retry", error: Fail(FailureRetry), reason: FailureRetry},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			definitions := independentDefinitions()
			manager := readyManager(t, definitions)
			failingKey := definitions[0].Key()
			healthyKey := definitions[1].Key()
			results := manager.RunAll(context.Background(), func(_ context.Context, runtime *Runtime) error {
				if runtime.Definition.Key() == failingKey {
					return testCase.error
				}
				return nil
			})
			if results[failingKey].Reason != testCase.reason || !results[healthyKey].Succeeded {
				t.Fatalf("RunAll() = %#v", results)
			}
			if strings.Contains(fmt.Sprintf("%#v", results), "token-left") {
				t.Fatalf("run results leaked provider detail: %#v", results)
			}
		})
	}
}

func TestTargetDeletionCancelsWorkAndClosesOnlyDeletedResources(t *testing.T) {
	definitions := independentDefinitions()
	resources := newRecordingResourceFactory()
	manager := readyManagerWithFactories(t, definitions, newRecordingClientFactory(), resources)
	deletedKey := definitions[0].Key()
	started := make(chan struct{})
	resultsDone := make(chan map[string]RunResult, 1)
	go func() {
		resultsDone <- manager.RunAll(context.Background(), func(ctx context.Context, runtime *Runtime) error {
			if runtime.Definition.Key() != deletedKey {
				return nil
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	result, err := manager.Sync(context.Background(), definitions[1:])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != deletedKey {
		t.Fatalf("removed = %v", result.Removed)
	}
	runResults := <-resultsDone
	if runResults[deletedKey].Reason != FailureCancelled || !runResults[definitions[1].Key()].Succeeded {
		t.Fatalf("deletion run results = %#v", runResults)
	}
	if resources.closeCount(deletedKey) != 1 || resources.closeCount(definitions[1].Key()) != 0 {
		t.Fatalf("resource closes = %#v", resources.closes)
	}
}

func independentDefinitions() []Definition {
	leftObject := apiTarget("left", "left.example.com", []string{"left.example.com"})
	leftObject.Spec.VDOM = "left-vdom"
	leftObject.Spec.APITokenSecretRef.Name = "left-token"
	rightObject := apiTarget("right", "right.example.net", []string{"right.example.net"})
	rightObject.Spec.VDOM = "right-vdom"
	rightObject.Spec.APITokenSecretRef.Name = "right-token"
	return []Definition{FromAPI(&leftObject), FromAPI(&rightObject)}
}

func resolverForDefinitions(t *testing.T, definitions []Definition) *Resolver {
	t.Helper()
	objects := make([]runtime.Object, 0, len(definitions))
	for index, definition := range definitions {
		objects = append(objects, secretForDefinition(definition, fmt.Sprintf("token-%d", index), "1"))
	}
	client := fake.NewSimpleClientset(objects...)
	resolver, err := NewResolver(client.CoreV1())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func secretForDefinition(definition Definition, token, resourceVersion string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: definition.Namespace, Name: definition.APITokenSecretRef.Name, UID: types.UID("uid-" + definition.Name), ResourceVersion: resourceVersion},
		Data:       map[string][]byte{definition.APITokenSecretRef.Key: []byte(token)},
	}
}

func readyManager(t *testing.T, definitions []Definition) *RuntimeManager {
	t.Helper()
	return readyManagerWithFactories(t, definitions, newRecordingClientFactory(), newRecordingResourceFactory())
}

func readyManagerWithFactories(t *testing.T, definitions []Definition, clients *recordingClientFactory, resources *recordingResourceFactory) *RuntimeManager {
	t.Helper()
	manager, err := NewRuntimeManager(resolverForDefinitions(t, definitions), clients, resources, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Sync(context.Background(), definitions); err != nil || len(result.Failures) != 0 {
		t.Fatalf("Sync() result=%#v err=%v", result, err)
	}
	return manager
}

type fakeTargetClient struct {
	key    string
	closed bool
	mu     sync.Mutex
}

func (*fakeTargetClient) ListRecords(context.Context) ([]dns.Endpoint, error) { return nil, nil }
func (*fakeTargetClient) Apply(context.Context, []plan.Operation, bool) error { return nil }
func (c *fakeTargetClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

type recordingClientFactory struct {
	mu        sync.Mutex
	calls     map[string]int
	fail      map[string]bool
	materials []*CredentialMaterial
	digests   map[string]string
}

func newRecordingClientFactory() *recordingClientFactory {
	return &recordingClientFactory{calls: map[string]int{}, fail: map[string]bool{}, digests: map[string]string{}}
}

func (f *recordingClientFactory) NewClient(_ context.Context, definition Definition, material *CredentialMaterial) (ProviderClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := definition.Key()
	f.calls[key]++
	f.materials = append(f.materials, material)
	token := material.APIToken()
	digest := sha256.Sum256(token)
	f.digests[key] = hex.EncodeToString(digest[:])
	if f.fail[key] {
		return nil, errors.New("factory raw detail: " + string(token))
	}
	return &fakeTargetClient{key: key}, nil
}

func (f *recordingClientFactory) callCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

type resourceMarker struct{ key, kind string }

type recordingResourceFactory struct {
	mu     sync.Mutex
	closes map[string]int
}

func newRecordingResourceFactory() *recordingResourceFactory {
	return &recordingResourceFactory{closes: map[string]int{}}
}

func (f *recordingResourceFactory) NewResources(_ context.Context, definition Definition) (StoreHandles, error) {
	key := definition.Key()
	return StoreHandles{
		PlanStore:      &resourceMarker{key: key, kind: "plan"},
		OwnershipStore: &resourceMarker{key: key, kind: "ownership"},
		StatusStore:    &resourceMarker{key: key, kind: "status"},
		Close: func() error {
			f.mu.Lock()
			f.closes[key]++
			f.mu.Unlock()
			return nil
		},
	}, nil
}

func (f *recordingResourceFactory) closeCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes[key]
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

var localCAReference = v1alpha1.LocalKeyReference{Kind: "ConfigMap", Name: "fortigate-ca", Key: "ca.crt"}
