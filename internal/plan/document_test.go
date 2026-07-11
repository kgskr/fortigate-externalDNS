package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestDocumentCanonicalJSONGoldenAndOrderIndependence(t *testing.T) {
	document := canonicalTestDocument()
	canonical, err := document.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}

	const golden = `{"apiVersion":"fortigate-external-dns.kgskr.io/v1alpha1","kind":"ReconciliationPlan","target":{"namespace":"dns-system","name":"primary","uid":"target-uid","generation":7,"vdom":"root","zone":"example.com"},"preconditions":{"provider":{"revision":"provider-revision-9","stable":true,"complete":true},"discovery":{"generation":14,"complete":true,"sources":[{"kind":"Gateway","resourceVersion":"18","complete":true},{"kind":"Service","resourceVersion":"92","complete":true}]},"policy":{"generation":11,"complete":true,"resources":[{"namespace":"team-a","name":"default","uid":"policy-a","generation":3,"resourceVersion":"24"},{"namespace":"team-b","name":"restricted","uid":"policy-b","generation":4,"resourceVersion":"27"}]},"ownership":[{"namespace":"dns-system","name":"api-record","uid":"claim-a","resourceVersion":"30","fingerprint":"sha256:aaa","phase":"Reserved"},{"namespace":"dns-system","name":"old-record","uid":"claim-b","resourceVersion":"31","providerID":"42","fingerprint":"sha256:bbb","phase":"Confirmed"}]},"operations":[{"id":"019a7c347944545d87b09df5aff03caae2e5a2020f13d6dd88d9238ecd688c6c","type":"create","desired":{"zone":"example.com","dnsName":"api.example.com","recordType":"A","targets":["203.0.113.10","203.0.113.20"],"ttl":300,"disabled":false},"reason":"record-missing"},{"id":"6c4244009c98af65e012c7bee3327d7c9feb4fa24457dbe0295defd1524c0ef8","type":"delete","current":{"zone":"example.com","dnsName":"old.example.com","recordType":"A","targets":["198.51.100.9"],"ttl":60,"providerID":"42","disabled":false},"reason":"record-stale"}],"prerequisites":[{"operationID":"6c4244009c98af65e012c7bee3327d7c9feb4fa24457dbe0295defd1524c0ef8","requiresOperationID":"019a7c347944545d87b09df5aff03caae2e5a2020f13d6dd88d9238ecd688c6c"}],"safetyDecisions":[{"code":"discovery-complete","allowed":true,"operationIDs":["019a7c347944545d87b09df5aff03caae2e5a2020f13d6dd88d9238ecd688c6c","6c4244009c98af65e012c7bee3327d7c9feb4fa24457dbe0295defd1524c0ef8"]},{"code":"ownership-authorized","allowed":true,"operationIDs":["019a7c347944545d87b09df5aff03caae2e5a2020f13d6dd88d9238ecd688c6c","6c4244009c98af65e012c7bee3327d7c9feb4fa24457dbe0295defd1524c0ef8"]}]}`
	if string(canonical) != golden {
		t.Fatalf("canonical JSON changed\n got: %s\nwant: %s", canonical, golden)
	}

	planID, err := document.ID()
	if err != nil {
		t.Fatalf("plan id: %v", err)
	}
	const goldenID = "190d0527bab3a9e81fd8a165419c8692d9e6c2eb196ec841dd9b3229226fd08e"
	if planID != goldenID {
		t.Fatalf("plan ID changed: got %s want %s", planID, goldenID)
	}
	if planID != strings.ToLower(planID) || len(planID) != sha256.Size*2 {
		t.Fatalf("plan ID is not a lowercase SHA-256 digest: %q", planID)
	}

	for seed := int64(0); seed < 200; seed++ {
		candidate := cloneDocument(document)
		random := rand.New(rand.NewSource(seed))
		shuffleDocument(random, &candidate)

		got, err := candidate.CanonicalJSON()
		if err != nil {
			t.Fatalf("seed %d canonical json: %v", seed, err)
		}
		if !bytes.Equal(got, canonical) {
			t.Fatalf("seed %d changed canonical bytes\n got: %s\nwant: %s", seed, got, canonical)
		}
		gotID, err := candidate.ID()
		if err != nil {
			t.Fatalf("seed %d plan id: %v", seed, err)
		}
		if gotID != planID {
			t.Fatalf("seed %d changed plan ID: got %s want %s", seed, gotID, planID)
		}
	}
}

func TestDocumentSerializationExcludesSensitiveAndUnstableOperationData(t *testing.T) {
	secret := "sensitive-token-should-never-appear"
	operation := Operation{
		Type: OperationCreate,
		Desired: dns.Endpoint{
			Zone:       "example.com",
			DNSName:    "safe.example.com",
			RecordType: dns.RecordA,
			Targets:    []string{"203.0.113.10"},
			TTL:        300,
			OwnerID:    secret,
			Source: dns.SourceRef{
				Kind:      "Service",
				Namespace: secret,
				Name:      secret,
			},
		},
		Reason: "Authorization: Bearer " + secret + " raw response body",
	}
	document := NewDocument(
		TargetIdentity{Name: "default", Zone: "example.com"},
		Preconditions{},
		[]Operation{operation},
	)

	canonical, err := document.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if bytes.Contains(canonical, []byte(secret)) || bytes.Contains(bytes.ToLower(canonical), []byte("authorization")) || bytes.Contains(bytes.ToLower(canonical), []byte("response body")) {
		t.Fatalf("canonical document leaked sensitive operation metadata: %s", canonical)
	}
	if !bytes.Contains(canonical, []byte(`"reason":"unspecified"`)) {
		t.Fatalf("arbitrary operation reason was not reduced to a fixed code: %s", canonical)
	}
	for _, forbiddenField := range []string{"ownerID", "source", "timestamp", "createdAt", "updatedAt"} {
		if bytes.Contains(canonical, []byte(`"`+forbiddenField+`"`)) {
			t.Fatalf("canonical document contains forbidden field %q: %s", forbiddenField, canonical)
		}
	}
}

func TestDocumentCanonicalSchemaHasNoMapsTimestampsOrSecretFields(t *testing.T) {
	timeType := reflect.TypeOf(time.Time{})
	var inspect func(reflect.Type, string)
	inspect = func(valueType reflect.Type, path string) {
		for valueType.Kind() == reflect.Pointer || valueType.Kind() == reflect.Slice || valueType.Kind() == reflect.Array {
			valueType = valueType.Elem()
		}
		if valueType == timeType {
			t.Errorf("canonical schema contains timestamp at %s", path)
			return
		}
		if valueType.Kind() == reflect.Map {
			t.Errorf("canonical schema contains unstable map at %s", path)
			return
		}
		if valueType.Kind() != reflect.Struct {
			return
		}
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			fieldPath := path + "." + field.Name
			lowerName := strings.ToLower(field.Name)
			for _, forbidden := range []string{"secret", "token", "authorization", "header", "responsebody", "timestamp"} {
				if strings.Contains(lowerName, forbidden) {
					t.Errorf("canonical schema exposes forbidden field %s", fieldPath)
				}
			}
			inspect(field.Type, fieldPath)
		}
	}
	inspect(reflect.TypeOf(Document{}), "Document")
}

func TestDocumentRejectsInvalidPrerequisiteGraph(t *testing.T) {
	document := canonicalTestDocument()
	firstID := document.Operations[0].ID
	secondID := document.Operations[1].ID
	document.Prerequisites = []PrerequisiteEdge{
		{OperationID: firstID, RequiresOperationID: secondID},
		{OperationID: secondID, RequiresOperationID: firstID},
	}
	if _, err := document.CanonicalJSON(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected prerequisite cycle rejection, got %v", err)
	}

	document = canonicalTestDocument()
	document.Prerequisites = []PrerequisiteEdge{{OperationID: firstID, RequiresOperationID: "missing"}}
	if _, err := document.CanonicalJSON(); err == nil || !strings.Contains(err.Error(), "unknown requirement") {
		t.Fatalf("expected unknown prerequisite rejection, got %v", err)
	}
}

func TestCanonicalIDMatchesCanonicalBytesDigest(t *testing.T) {
	document := canonicalTestDocument()
	canonical, err := CanonicalJSON(document)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	digest := sha256.Sum256(canonical)
	want := hex.EncodeToString(digest[:])
	got, err := CanonicalID(document)
	if err != nil {
		t.Fatalf("canonical id: %v", err)
	}
	if got != want {
		t.Fatalf("ID does not hash canonical bytes: got %s want %s", got, want)
	}
}

func canonicalTestDocument() Document {
	create := SanitizeOperation(Operation{
		Type: OperationCreate,
		Desired: dns.Endpoint{
			Zone:       "Example.COM.",
			DNSName:    "Api.Example.COM.",
			RecordType: "a",
			Targets:    []string{"203.0.113.20", "203.0.113.10"},
			TTL:        300,
			OwnerID:    "excluded-owner",
		},
		Reason: "record is missing",
	})
	cleanup := SanitizeOperation(Operation{
		Type: OperationDelete,
		Current: dns.Endpoint{
			Zone:       "example.com",
			DNSName:    "old.example.com",
			RecordType: dns.RecordA,
			Targets:    []string{"198.51.100.9"},
			TTL:        60,
			ProviderID: "42",
		},
		Reason: "managed record is stale",
	})

	return Document{
		APIVersion: DocumentAPIVersion,
		Kind:       DocumentKind,
		Target: TargetIdentity{
			Namespace:  "dns-system",
			Name:       "primary",
			UID:        "target-uid",
			Generation: 7,
			VDOM:       "root",
			Zone:       "Example.COM.",
		},
		Preconditions: Preconditions{
			Provider: ProviderPrecondition{Revision: "provider-revision-9", Stable: true, Complete: true},
			Discovery: DiscoveryPrecondition{
				Generation: 14,
				Complete:   true,
				Sources: []DiscoverySourcePrecondition{
					{Kind: "Service", ResourceVersion: "92", Complete: true},
					{Kind: "Gateway", ResourceVersion: "18", Complete: true},
				},
			},
			Policy: PolicyPrecondition{
				Generation: 11,
				Complete:   true,
				Resources: []PolicyResourcePrecondition{
					{Namespace: "team-b", Name: "restricted", UID: "policy-b", Generation: 4, ResourceVersion: "27"},
					{Namespace: "team-a", Name: "default", UID: "policy-a", Generation: 3, ResourceVersion: "24"},
				},
			},
			Ownership: []OwnershipPrecondition{
				{Namespace: "dns-system", Name: "old-record", UID: "claim-b", ResourceVersion: "31", ProviderID: "42", Fingerprint: "sha256:bbb", Phase: "Confirmed"},
				{Namespace: "dns-system", Name: "api-record", UID: "claim-a", ResourceVersion: "30", Fingerprint: "sha256:aaa", Phase: "Reserved"},
			},
		},
		Operations: []SanitizedOperation{cleanup, create},
		Prerequisites: []PrerequisiteEdge{
			{OperationID: cleanup.ID, RequiresOperationID: create.ID},
		},
		SafetyDecisions: []SafetyDecision{
			{Code: SafetyDecisionOwnershipAuthorized, Allowed: true, OperationIDs: []string{cleanup.ID, create.ID}},
			{Code: SafetyDecisionDiscoveryComplete, Allowed: true, OperationIDs: []string{create.ID, cleanup.ID}},
		},
	}
}

func cloneDocument(document Document) Document {
	clone := document
	clone.Operations = append([]SanitizedOperation(nil), document.Operations...)
	for index := range clone.Operations {
		if clone.Operations[index].Desired != nil {
			record := *clone.Operations[index].Desired
			record.Targets = append([]string(nil), record.Targets...)
			clone.Operations[index].Desired = &record
		}
		if clone.Operations[index].Current != nil {
			record := *clone.Operations[index].Current
			record.Targets = append([]string(nil), record.Targets...)
			clone.Operations[index].Current = &record
		}
	}
	clone.Prerequisites = append([]PrerequisiteEdge(nil), document.Prerequisites...)
	clone.SafetyDecisions = append([]SafetyDecision(nil), document.SafetyDecisions...)
	for index := range clone.SafetyDecisions {
		clone.SafetyDecisions[index].OperationIDs = append([]string(nil), clone.SafetyDecisions[index].OperationIDs...)
	}
	clone.Preconditions.Discovery.Sources = append([]DiscoverySourcePrecondition(nil), document.Preconditions.Discovery.Sources...)
	clone.Preconditions.Policy.Resources = append([]PolicyResourcePrecondition(nil), document.Preconditions.Policy.Resources...)
	clone.Preconditions.Ownership = append([]OwnershipPrecondition(nil), document.Preconditions.Ownership...)
	return clone
}

func shuffleDocument(random *rand.Rand, document *Document) {
	random.Shuffle(len(document.Operations), func(i, j int) {
		document.Operations[i], document.Operations[j] = document.Operations[j], document.Operations[i]
	})
	for index := range document.Operations {
		if document.Operations[index].Desired != nil {
			random.Shuffle(len(document.Operations[index].Desired.Targets), func(i, j int) {
				document.Operations[index].Desired.Targets[i], document.Operations[index].Desired.Targets[j] = document.Operations[index].Desired.Targets[j], document.Operations[index].Desired.Targets[i]
			})
		}
		if document.Operations[index].Current != nil {
			random.Shuffle(len(document.Operations[index].Current.Targets), func(i, j int) {
				document.Operations[index].Current.Targets[i], document.Operations[index].Current.Targets[j] = document.Operations[index].Current.Targets[j], document.Operations[index].Current.Targets[i]
			})
		}
	}
	random.Shuffle(len(document.Prerequisites), func(i, j int) {
		document.Prerequisites[i], document.Prerequisites[j] = document.Prerequisites[j], document.Prerequisites[i]
	})
	random.Shuffle(len(document.SafetyDecisions), func(i, j int) {
		document.SafetyDecisions[i], document.SafetyDecisions[j] = document.SafetyDecisions[j], document.SafetyDecisions[i]
	})
	for index := range document.SafetyDecisions {
		random.Shuffle(len(document.SafetyDecisions[index].OperationIDs), func(i, j int) {
			document.SafetyDecisions[index].OperationIDs[i], document.SafetyDecisions[index].OperationIDs[j] = document.SafetyDecisions[index].OperationIDs[j], document.SafetyDecisions[index].OperationIDs[i]
		})
	}
	random.Shuffle(len(document.Preconditions.Discovery.Sources), func(i, j int) {
		document.Preconditions.Discovery.Sources[i], document.Preconditions.Discovery.Sources[j] = document.Preconditions.Discovery.Sources[j], document.Preconditions.Discovery.Sources[i]
	})
	random.Shuffle(len(document.Preconditions.Policy.Resources), func(i, j int) {
		document.Preconditions.Policy.Resources[i], document.Preconditions.Policy.Resources[j] = document.Preconditions.Policy.Resources[j], document.Preconditions.Policy.Resources[i]
	})
	random.Shuffle(len(document.Preconditions.Ownership), func(i, j int) {
		document.Preconditions.Ownership[i], document.Preconditions.Ownership[j] = document.Preconditions.Ownership[j], document.Preconditions.Ownership[i]
	})
}
