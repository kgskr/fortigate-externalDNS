package target

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

const DefaultName = "default"

type Definition struct {
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	Generation      int64
	Legacy          bool

	URL                string
	VDOM               string
	Zone               string
	APIToken           string
	APITokenSecretRef  *corev1.SecretKeySelector
	CAFile             string
	CARef              *v1alpha1.LocalKeyReference
	InsecureSkipVerify bool

	OwnershipMode              v1alpha1.OwnershipMode
	ControllerID               string
	Sources                    []string
	Namespaces                 []string
	GatewayTargetNamespaces    []string
	DomainFilters              []string
	CleanupPolicy              v1alpha1.CleanupPolicy
	DryRun                     bool
	ApprovalMode               v1alpha1.ApprovalMode
	AllowNonDestructiveOverlap bool
	ExternalNameEnabled        bool
	HeadlessEnabled            bool
	Interval                   time.Duration
	Debounce                   time.Duration
	Timeout                    time.Duration
	Retries                    int
	DefaultTTL                 int64
}

// BuildDefinitions selects exactly one configuration authority. targetMode is
// supplied explicitly by the future runtime flag so merely installing CRDs can
// never switch an existing deployment away from its legacy target.
func BuildDefinitions(cfg config.Config, targetMode bool, objects []v1alpha1.FortiGateDNSTarget) ([]Definition, error) {
	if !targetMode {
		if len(objects) > 0 {
			return nil, fmt.Errorf("CRD targets cannot be supplied while target mode is disabled")
		}
		definitions := []Definition{FromLegacy(cfg)}
		if err := ValidateAll(definitions); err != nil {
			return nil, err
		}
		return definitions, nil
	}
	if hasDirectFortiGateConfiguration(cfg.FortiGate) {
		return nil, fmt.Errorf("direct FortiGate credentials and connection settings are mutually exclusive with CRD target mode")
	}
	definitions := make([]Definition, 0, len(objects))
	for i := range objects {
		definitions = append(definitions, FromAPI(&objects[i]))
	}
	if err := ValidateAll(definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func FromLegacy(cfg config.Config) Definition {
	mode := v1alpha1.OwnershipModeExclusive
	return Definition{
		Name:                    DefaultName,
		Legacy:                  true,
		URL:                     cfg.FortiGate.BaseURL,
		VDOM:                    cfg.FortiGate.VDOM,
		Zone:                    cfg.FortiGate.Zone,
		APIToken:                cfg.FortiGate.APIToken,
		CAFile:                  cfg.FortiGate.CAFile,
		InsecureSkipVerify:      cfg.FortiGate.InsecureSkipVerify,
		OwnershipMode:           mode,
		ControllerID:            cfg.OwnerID,
		Sources:                 copyStrings(cfg.Sources),
		Namespaces:              copyStrings(cfg.Namespaces),
		GatewayTargetNamespaces: copyStrings(cfg.GatewayTargetNamespaces),
		DomainFilters:           copyStrings(cfg.DomainFilters),
		CleanupPolicy:           v1alpha1.CleanupPolicy(cfg.CleanupPolicy),
		DryRun:                  cfg.DryRun,
		ApprovalMode:            v1alpha1.ApprovalModeDisabled,
		ExternalNameEnabled:     false,
		HeadlessEnabled:         false,
		Interval:                cfg.Interval,
		Timeout:                 cfg.FortiGate.Timeout,
		Retries:                 cfg.FortiGate.Retries,
		DefaultTTL:              cfg.DefaultTTL,
	}
}

func FromAPI(target *v1alpha1.FortiGateDNSTarget) Definition {
	if target == nil {
		return Definition{}
	}
	secretRef := target.Spec.APITokenSecretRef
	if target.Spec.APITokenSecretRef.Optional != nil {
		optional := *target.Spec.APITokenSecretRef.Optional
		secretRef.Optional = &optional
	}
	var caRef *v1alpha1.LocalKeyReference
	if target.Spec.CARef != nil {
		copy := *target.Spec.CARef
		caRef = &copy
	}
	return Definition{
		Namespace:                  target.Namespace,
		Name:                       target.Name,
		UID:                        string(target.UID),
		ResourceVersion:            target.ResourceVersion,
		Generation:                 target.Generation,
		URL:                        target.Spec.URL,
		VDOM:                       target.Spec.VDOM,
		Zone:                       target.Spec.Zone,
		APITokenSecretRef:          &secretRef,
		CARef:                      caRef,
		InsecureSkipVerify:         target.Spec.InsecureSkipVerify,
		OwnershipMode:              target.Spec.OwnershipMode,
		ControllerID:               target.Spec.ControllerID,
		Sources:                    copyStrings(target.Spec.Sources),
		Namespaces:                 copyStrings(target.Spec.Namespaces),
		GatewayTargetNamespaces:    copyStrings(target.Spec.GatewayTargetNamespaces),
		DomainFilters:              copyStrings(target.Spec.DomainFilters),
		CleanupPolicy:              target.Spec.CleanupPolicy,
		DryRun:                     target.Spec.DryRun,
		ApprovalMode:               target.Spec.ApprovalMode,
		AllowNonDestructiveOverlap: target.Spec.AllowNonDestructiveOverlap,
		ExternalNameEnabled:        target.Spec.ExternalNameEnabled,
		HeadlessEnabled:            target.Spec.HeadlessEnabled,
		Interval:                   target.Spec.Interval.Duration,
		Debounce:                   target.Spec.Debounce.Duration,
		Timeout:                    target.Spec.Timeout.Duration,
		Retries:                    int(target.Spec.Retries),
		DefaultTTL:                 target.Spec.DefaultTTL,
	}
}

func ValidateAll(definitions []Definition) error {
	seen := map[string]struct{}{}
	for i := range definitions {
		if err := validateDefinition(&definitions[i]); err != nil {
			return fmt.Errorf("target %q: %w", definitions[i].Name, err)
		}
		key := definitions[i].Namespace + "/" + definitions[i].Name
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate target %q", key)
		}
		seen[key] = struct{}{}
	}
	for i := range definitions {
		for j := i + 1; j < len(definitions); j++ {
			if !writeScopesOverlap(definitions[i], definitions[j]) {
				continue
			}
			if nonDestructiveOverlapAllowed(definitions[i], definitions[j]) {
				continue
			}
			return fmt.Errorf("write-enabled targets %q and %q have overlapping domain scopes", definitions[i].Name, definitions[j].Name)
		}
	}
	return nil
}

func validateDefinition(definition *Definition) error {
	definition.Name = strings.ToLower(strings.TrimSpace(definition.Name))
	definition.Namespace = strings.ToLower(strings.TrimSpace(definition.Namespace))
	definition.Zone = dns.NormalizeDNSName(definition.Zone)
	definition.DomainFilters = normalizeSuffixes(definition.DomainFilters)
	definition.Sources = normalizeValues(definition.Sources)
	definition.Namespaces = normalizeValues(definition.Namespaces)
	definition.GatewayTargetNamespaces = normalizeValues(definition.GatewayTargetNamespaces)
	if definition.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateURL(definition.URL); err != nil {
		return err
	}
	if definition.Zone == "" {
		return fmt.Errorf("zone is required")
	}
	if strings.TrimSpace(definition.ControllerID) == "" {
		return fmt.Errorf("controller ID is required")
	}
	if definition.Legacy {
		if strings.TrimSpace(definition.APIToken) == "" {
			return fmt.Errorf("legacy API token is required")
		}
		if definition.APITokenSecretRef != nil {
			return fmt.Errorf("legacy target cannot use a Secret reference")
		}
	} else {
		if definition.APIToken != "" {
			return fmt.Errorf("CRD target cannot contain an inline API token")
		}
		if definition.APITokenSecretRef == nil || definition.APITokenSecretRef.Name == "" || definition.APITokenSecretRef.Key == "" {
			return fmt.Errorf("API token Secret name and key are required")
		}
	}
	if definition.CAFile != "" && definition.CARef != nil {
		return fmt.Errorf("CA file and CA reference are mutually exclusive")
	}
	if definition.InsecureSkipVerify && (definition.CAFile != "" || definition.CARef != nil) {
		return fmt.Errorf("CA trust and insecure skip verification are mutually exclusive")
	}
	switch definition.OwnershipMode {
	case v1alpha1.OwnershipModeExclusive, v1alpha1.OwnershipModeShared:
	default:
		return fmt.Errorf("unsupported ownership mode %q", definition.OwnershipMode)
	}
	if definition.CleanupPolicy == "" {
		definition.CleanupPolicy = v1alpha1.CleanupPolicyDelete
	}
	switch definition.CleanupPolicy {
	case v1alpha1.CleanupPolicyDelete, v1alpha1.CleanupPolicyDeactivate, v1alpha1.CleanupPolicyKeep:
	default:
		return fmt.Errorf("unsupported cleanup policy %q", definition.CleanupPolicy)
	}
	if definition.ApprovalMode == "" {
		definition.ApprovalMode = v1alpha1.ApprovalModeDisabled
	}
	switch definition.ApprovalMode {
	case v1alpha1.ApprovalModeDisabled, v1alpha1.ApprovalModeRequired:
	default:
		return fmt.Errorf("unsupported approval mode %q", definition.ApprovalMode)
	}
	if definition.OwnershipMode == v1alpha1.OwnershipModeShared && len(definition.Namespaces) > 0 && definition.CleanupPolicy != v1alpha1.CleanupPolicyKeep {
		return fmt.Errorf("namespace-restricted shared target requires cleanup policy keep")
	}
	if definition.Interval < 0 || definition.Debounce < 0 || definition.Timeout < 0 {
		return fmt.Errorf("reconcile durations cannot be negative")
	}
	if definition.Retries < 0 || definition.Retries > config.MaxRetries {
		return fmt.Errorf("retries must be between 0 and %d", config.MaxRetries)
	}
	if definition.DefaultTTL < 0 || definition.DefaultTTL > config.MaxDefaultTTL {
		return fmt.Errorf("default TTL must be between 0 and %d", config.MaxDefaultTTL)
	}
	return nil
}

func validateURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("FortiGate URL is invalid")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("FortiGate URL scheme must be https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("FortiGate URL must not contain user information, query, or fragment")
	}
	return nil
}

func writeScopesOverlap(left, right Definition) bool {
	if left.DryRun || right.DryRun {
		return false
	}
	for _, leftScope := range domainScopes(left) {
		for _, rightScope := range domainScopes(right) {
			if suffixContains(leftScope, rightScope) || suffixContains(rightScope, leftScope) {
				return true
			}
		}
	}
	return false
}

func nonDestructiveOverlapAllowed(left, right Definition) bool {
	return left.CleanupPolicy == v1alpha1.CleanupPolicyKeep && right.CleanupPolicy == v1alpha1.CleanupPolicyKeep &&
		left.AllowNonDestructiveOverlap && right.AllowNonDestructiveOverlap
}

func domainScopes(definition Definition) []string {
	if len(definition.DomainFilters) > 0 {
		return definition.DomainFilters
	}
	return []string{definition.Zone}
}

func suffixContains(parent, child string) bool {
	parent = dns.NormalizeDNSName(parent)
	child = dns.NormalizeDNSName(child)
	return parent == child || strings.HasSuffix(child, "."+parent)
}

func hasDirectFortiGateConfiguration(cfg config.FortiGateConfig) bool {
	return strings.TrimSpace(cfg.BaseURL) != "" || strings.TrimSpace(cfg.APIToken) != "" || strings.TrimSpace(cfg.Zone) != "" ||
		strings.TrimSpace(cfg.CAFile) != "" || cfg.InsecureSkipVerify || cfg.ExclusiveZoneOwnership
}

func normalizeSuffixes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = dns.NormalizeDNSName(value); value != "" {
			result = append(result, value)
		}
	}
	return uniqueSorted(result)
}

func normalizeValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result = append(result, value)
		}
	}
	return uniqueSorted(result)
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func copyStrings(values []string) []string {
	return append([]string(nil), values...)
}
