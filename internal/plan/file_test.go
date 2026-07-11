package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

func TestWriteCanonicalFileIsAtomicAndRequiresExplicitOverwrite(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "plan.json")
	document := fileTestDocument("api.example.com")
	if err := WriteCanonicalFile(path, document, false); err != nil {
		t.Fatal(err)
	}
	want, err := document.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("file differs from canonical bytes:\n%s\n%s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plan mode = %o", info.Mode().Perm())
	}

	replacement := fileTestDocument("new.example.com")
	if err := WriteCanonicalFile(path, replacement, false); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("existing plan was not protected: %v", err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(want) {
		t.Fatal("failed non-overwrite changed the existing file")
	}
	if err := WriteCanonicalFile(path, replacement, true); err != nil {
		t.Fatal(err)
	}
	replacementBytes, _ := replacement.CanonicalJSON()
	got, _ = os.ReadFile(path)
	if string(got) != string(replacementBytes) {
		t.Fatal("explicit overwrite did not install replacement bytes")
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".fortigate-external-dns-plan-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v %v", matches, err)
	}
}

func TestVerifyApprovedHashRequiresExactCurrentID(t *testing.T) {
	document := fileTestDocument("api.example.com")
	planID, err := document.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyApprovedHash(document, planID); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", strings.Repeat("0", 64), strings.ToUpper(planID)} {
		if err := VerifyApprovedHash(document, value); err == nil {
			t.Fatalf("approval %q unexpectedly accepted", value)
		}
	}
}

func TestWriteCanonicalFileRejectsMissingParentAndEmptyPath(t *testing.T) {
	document := fileTestDocument("api.example.com")
	if err := WriteCanonicalFile("", document, false); err == nil {
		t.Fatal("empty path unexpectedly accepted")
	}
	if err := WriteCanonicalFile(filepath.Join(t.TempDir(), "missing", "plan.json"), document, false); err == nil {
		t.Fatal("missing parent directory unexpectedly created")
	}
}

func fileTestDocument(hostname string) Document {
	return NewDocument(
		TargetIdentity{Name: "default", Zone: "example.com"},
		Preconditions{Provider: ProviderPrecondition{Revision: "rev-1", Stable: true, Complete: true}},
		[]Operation{{Type: OperationCreate, Desired: dns.Endpoint{Zone: "example.com", DNSName: hostname, RecordType: dns.RecordA, Targets: []string{"203.0.113.10"}, TTL: 300}, Reason: "record is missing"}},
	)
}
