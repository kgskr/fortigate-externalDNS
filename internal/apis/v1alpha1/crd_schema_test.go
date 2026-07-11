package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func loadCRDs(t *testing.T) map[string]map[string]any {
	t.Helper()
	path := filepath.Join("..", "..", "..", "charts", "fortigate-external-dns", "crds", "fortigate-external-dns.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	result := map[string]map[string]any{}
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if len(raw) == 0 {
			continue
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		name := nestedString(t, document, "metadata", "name")
		result[name] = document
	}
	return result
}

func TestCRDsAreNamespacedStructuralSingleStorageVersion(t *testing.T) {
	crds := loadCRDs(t)
	wantNames := []string{
		"fortigatednstargets." + GroupName,
		"fortigatednsrecordownerships." + GroupName,
		"fortigatednspolicies." + GroupName,
		"fortigatednschangeplans." + GroupName,
		"fortigatednsstatuses." + GroupName,
	}
	if len(crds) != len(wantNames) {
		t.Fatalf("loaded %d CRDs, want %d", len(crds), len(wantNames))
	}
	for _, name := range wantNames {
		crd, ok := crds[name]
		if !ok {
			t.Errorf("missing CRD %s", name)
			continue
		}
		if got := nestedString(t, crd, "spec", "group"); got != GroupName {
			t.Errorf("%s group = %q", name, got)
		}
		if got := nestedString(t, crd, "spec", "scope"); got != "Namespaced" {
			t.Errorf("%s scope = %q", name, got)
		}
		versions := nestedSlice(t, crd, "spec", "versions")
		storage := 0
		for _, value := range versions {
			version := value.(map[string]any)
			if version["storage"] == true {
				storage++
			}
			schema := nestedMap(t, version, "schema", "openAPIV3Schema")
			assertStructuralSchema(t, name+".schema", schema)
		}
		if storage != 1 {
			t.Errorf("%s has %d storage versions", name, storage)
		}
	}
}

func TestCRDSchemasRejectUnsafeModesAndUnboundedFields(t *testing.T) {
	crds := loadCRDs(t)
	target := versionSchema(t, crds["fortigatednstargets."+GroupName])
	assertEnum(t, property(t, target, "spec", "ownershipMode"), []any{"exclusive", "shared"})
	assertEnum(t, property(t, target, "spec", "approvalMode"), []any{"disabled", "required"})
	assertEnum(t, property(t, target, "spec", "cleanupPolicy"), []any{"delete", "deactivate", "keep"})
	secretRef := property(t, target, "spec", "apiTokenSecretRef")
	assertContainsAll(t, nestedSlice(t, secretRef, "required"), []any{"name", "key"})
	if property(t, target, "spec", "retries")["maximum"] != float64(10) {
		t.Fatal("target retries must be bounded at 10")
	}

	ownership := versionSchema(t, crds["fortigatednsrecordownerships."+GroupName])
	assertEnum(t, property(t, ownership, "status", "phase"), []any{"Reserved", "Confirmed", "Orphaned", "Conflict"})
	if got := property(t, ownership, "spec", "fingerprint")["pattern"]; got != "^[a-f0-9]{64}$" {
		t.Fatalf("ownership fingerprint pattern = %#v", got)
	}

	policy := versionSchema(t, crds["fortigatednspolicies."+GroupName])
	if property(t, policy, "spec", "maxRecordsPerTarget")["maximum"] == nil {
		t.Fatal("target record quota must have a maximum")
	}

	plan := versionSchema(t, crds["fortigatednschangeplans."+GroupName])
	if property(t, plan, "spec", "operations")["maxItems"] == nil {
		t.Fatal("plan operations must have maxItems")
	}
	if property(t, plan, "status", "outcomes")["maxItems"] == nil {
		t.Fatal("plan outcomes must have maxItems")
	}

	status := versionSchema(t, crds["fortigatednsstatuses."+GroupName])
	retention := property(t, status, "spec", "retention")
	if retention["maximum"] != float64(100) {
		t.Fatalf("status retention maximum = %#v", retention["maximum"])
	}
	history := property(t, status, "status", "history")
	if history["maxItems"] != float64(100) {
		t.Fatalf("status history maxItems = %#v", history["maxItems"])
	}
}

func TestCRDSchemasDoNotPersistSecretValuesOrRawProviderData(t *testing.T) {
	for name, crd := range loadCRDs(t) {
		forbidden := map[string]bool{
			"apitoken": true, "token": true, "secretvalue": true,
			"authorization": true, "authorizationheader": true,
			"cabundle": true, "responsebody": true, "providerbody": true,
			"records": true,
		}
		walkProperties(versionSchema(t, crd), func(path, propertyName string) {
			if forbidden[strings.ToLower(propertyName)] {
				t.Errorf("%s exposes forbidden property %s.%s", name, path, propertyName)
			}
		})
	}
}

func versionSchema(t *testing.T, crd map[string]any) map[string]any {
	t.Helper()
	versions := nestedSlice(t, crd, "spec", "versions")
	if len(versions) != 1 {
		t.Fatalf("expected one CRD version, got %d", len(versions))
	}
	return nestedMap(t, versions[0].(map[string]any), "schema", "openAPIV3Schema")
}

func property(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	current := root
	for _, name := range path {
		properties := nestedMap(t, current, "properties")
		next, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("property %q missing in path %v", name, path)
		}
		current = next
	}
	return current
}

func assertStructuralSchema(t *testing.T, path string, node map[string]any) {
	t.Helper()
	if node["x-kubernetes-preserve-unknown-fields"] == true {
		t.Errorf("%s preserves unknown fields", path)
	}
	if _, hasProperties := node["properties"]; hasProperties && node["type"] != "object" {
		t.Errorf("%s has properties without object type", path)
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, child := range properties {
			assertStructuralSchema(t, path+"."+name, child.(map[string]any))
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		assertStructuralSchema(t, path+"[]", items)
	}
}

func walkProperties(root map[string]any, visit func(path, propertyName string)) {
	var walk func(string, map[string]any)
	walk = func(path string, node map[string]any) {
		if properties, ok := node["properties"].(map[string]any); ok {
			for name, value := range properties {
				visit(path, name)
				walk(path+"."+name, value.(map[string]any))
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(path+"[]", items)
		}
	}
	walk("schema", root)
}

func nestedMap(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	current := root
	for _, name := range path {
		next, ok := current[name].(map[string]any)
		if !ok {
			t.Fatalf("%v is not a map at %q: %#v", path, name, current[name])
		}
		current = next
	}
	return current
}

func nestedSlice(t *testing.T, root map[string]any, path ...string) []any {
	t.Helper()
	current := root
	for _, name := range path[:len(path)-1] {
		current = nestedMap(t, current, name)
	}
	value, ok := current[path[len(path)-1]].([]any)
	if !ok {
		t.Fatalf("%v is not a slice: %#v", path, current[path[len(path)-1]])
	}
	return value
}

func nestedString(t *testing.T, root map[string]any, path ...string) string {
	t.Helper()
	current := root
	for _, name := range path[:len(path)-1] {
		current = nestedMap(t, current, name)
	}
	value, ok := current[path[len(path)-1]].(string)
	if !ok {
		t.Fatalf("%v is not a string: %#v", path, current[path[len(path)-1]])
	}
	return value
}

func assertEnum(t *testing.T, schema map[string]any, want []any) {
	t.Helper()
	got, ok := schema["enum"].([]any)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("enum = %#v, want %#v", schema["enum"], want)
	}
}

func assertContainsAll(t *testing.T, got, want []any) {
	t.Helper()
	seen := map[any]bool{}
	for _, value := range got {
		seen[value] = true
	}
	for _, value := range want {
		if !seen[value] {
			t.Errorf("%#v does not contain %#v", got, value)
		}
	}
}
