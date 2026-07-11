package ownership

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

const claimNamePrefix = "record-"

type Identity struct {
	TargetName string `json:"targetName"`
	Zone       string `json:"zone"`
	DNSName    string `json:"dnsName"`
	RecordType string `json:"recordType"`
	Target     string `json:"target"`
}

type FingerprintInput struct {
	Identity Identity `json:"identity"`
	TTL      int64    `json:"ttl"`
	Status   string   `json:"status"`
}

func IdentityFor(targetName string, endpoint dns.Endpoint) (Identity, error) {
	endpoint = endpoint.Normalize()
	if len(endpoint.Targets) != 1 || endpoint.Targets[0] == "" {
		return Identity{}, fmt.Errorf("ownership identity requires exactly one non-empty record target")
	}
	targetName = strings.ToLower(strings.TrimSpace(targetName))
	if targetName == "" {
		return Identity{}, fmt.Errorf("ownership identity requires a target name")
	}
	if endpoint.Zone == "" || endpoint.DNSName == "" {
		return Identity{}, fmt.Errorf("ownership identity requires a zone and DNS name")
	}
	switch endpoint.RecordType {
	case dns.RecordA, dns.RecordAAAA, dns.RecordCNAME:
	default:
		return Identity{}, fmt.Errorf("ownership identity does not support record type %q", endpoint.RecordType)
	}
	return Identity{
		TargetName: targetName,
		Zone:       endpoint.Zone,
		DNSName:    endpoint.DNSName,
		RecordType: endpoint.RecordType,
		Target:     endpoint.Targets[0],
	}, nil
}

func ClaimName(identity Identity) string {
	digest := sha256.Sum256(mustCanonical(identity))
	// A DNS label is at most 63 characters. "record-" plus 56 lowercase hex
	// characters is stable and leaves the complete identity in the claim spec.
	return claimNamePrefix + hex.EncodeToString(digest[:])[:56]
}

func Fingerprint(targetName string, endpoint dns.Endpoint) (string, error) {
	identity, err := IdentityFor(targetName, endpoint)
	if err != nil {
		return "", err
	}
	status := "enable"
	if endpoint.Disabled {
		status = "disable"
	}
	payload := FingerprintInput{Identity: identity, TTL: endpoint.TTL, Status: status}
	digest := sha256.Sum256(mustCanonical(payload))
	return hex.EncodeToString(digest[:]), nil
}

func RecordKey(identity Identity) v1alpha1.DNSRecordKey {
	return v1alpha1.DNSRecordKey{
		Name:   identity.DNSName,
		Type:   identity.RecordType,
		Target: identity.Target,
	}
}

func ClaimMatches(claim *v1alpha1.FortiGateDNSRecordOwnership, targetName string, endpoint dns.Endpoint) bool {
	if claim == nil {
		return false
	}
	identity, err := IdentityFor(targetName, endpoint)
	if err != nil {
		return false
	}
	fingerprint, err := Fingerprint(targetName, endpoint)
	if err != nil {
		return false
	}
	return claim.Name == ClaimName(identity) &&
		claim.Spec.TargetRef.Name == identity.TargetName &&
		claim.Spec.Record == RecordKey(identity) &&
		claim.Spec.Fingerprint == fingerprint
}

func mustCanonical(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("canonical ownership value %T is not JSON serializable: %v", value, err))
	}
	return data
}
