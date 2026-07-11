package policy

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
)

type Reason string

const (
	ReasonDenied                 Reason = "Denied"
	ReasonHostnameNotAllowed     Reason = "HostnameNotAllowed"
	ReasonTTLNotAllowed          Reason = "TTLNotAllowed"
	ReasonTargetNotAllowed       Reason = "TargetNotAllowed"
	ReasonOptInRequired          Reason = "OptInRequired"
	ReasonNamespaceQuotaExceeded Reason = "NamespaceQuotaExceeded"
	ReasonTargetQuotaExceeded    Reason = "TargetQuotaExceeded"
)

type NamedPolicy struct {
	Namespace string
	Name      string
	Spec      v1alpha1.FortiGateDNSPolicySpec
}

type Candidate struct {
	Endpoint    dns.Endpoint
	TargetName  string
	Labels      map[string]string
	Annotations map[string]string
}

type Bounds struct {
	SourceKinds            []string
	HostnameSuffixes       []string
	TTL                    *v1alpha1.TTLRange
	TargetCIDRs            []string
	TargetHostnameSuffixes []string
}

type Rejection struct {
	Candidate Candidate
	Reason    Reason
}

type Result struct {
	Allowed  []Candidate
	Rejected []Rejection
}

type compiledConstraint struct {
	name                   string
	selector               labels.Selector
	sourceKinds            map[string]struct{}
	hostnameSuffixes       []string
	ttl                    *v1alpha1.TTLRange
	targetCIDRs            []netip.Prefix
	targetHostnameSuffixes []string
	requireOptIn           *v1alpha1.OptInRequirement
	maxRecordsPerNamespace int
	maxRecordsPerTarget    int
	deny                   bool
}

type Evaluator struct {
	outer    compiledConstraint
	policies map[string][]compiledConstraint
}

func NewEvaluator(bounds Bounds, policies []NamedPolicy) (*Evaluator, error) {
	outerSpec := v1alpha1.FortiGateDNSPolicySpec{
		SourceKinds:             bounds.SourceKinds,
		AllowedHostnameSuffixes: bounds.HostnameSuffixes,
		TTL:                     bounds.TTL,
		AllowedTargetCIDRs:      bounds.TargetCIDRs,
		AllowedTargetSuffixes:   bounds.TargetHostnameSuffixes,
	}
	outer, err := compile("global bounds", outerSpec)
	if err != nil {
		return nil, err
	}
	evaluator := &Evaluator{outer: outer, policies: map[string][]compiledConstraint{}}
	for _, policy := range policies {
		compiled, err := compile(policy.Namespace+"/"+policy.Name, policy.Spec)
		if err != nil {
			return nil, err
		}
		evaluator.policies[policy.Namespace] = append(evaluator.policies[policy.Namespace], compiled)
	}
	for namespace := range evaluator.policies {
		sort.Slice(evaluator.policies[namespace], func(i, j int) bool {
			return evaluator.policies[namespace][i].name < evaluator.policies[namespace][j].name
		})
	}
	return evaluator, nil
}

func compile(name string, spec v1alpha1.FortiGateDNSPolicySpec) (compiledConstraint, error) {
	selector := labels.Everything()
	if spec.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(spec.Selector)
		if err != nil {
			return compiledConstraint{}, fmt.Errorf("policy %s selector: %w", name, err)
		}
	}
	constraint := compiledConstraint{
		name:                   name,
		selector:               selector,
		sourceKinds:            stringSet(spec.SourceKinds),
		hostnameSuffixes:       normalizeSuffixes(spec.AllowedHostnameSuffixes),
		targetHostnameSuffixes: normalizeSuffixes(spec.AllowedTargetSuffixes),
		ttl:                    copyTTL(spec.TTL),
		requireOptIn:           copyOptIn(spec.RequireOptIn),
		maxRecordsPerNamespace: int(spec.MaxRecordsPerNamespace),
		maxRecordsPerTarget:    int(spec.MaxRecordsPerTarget),
		deny:                   spec.Deny,
	}
	if constraint.ttl != nil && constraint.ttl.Minimum > 0 && constraint.ttl.Maximum > 0 && constraint.ttl.Minimum > constraint.ttl.Maximum {
		return compiledConstraint{}, fmt.Errorf("policy %s minimum TTL exceeds maximum TTL", name)
	}
	for _, raw := range spec.AllowedTargetCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return compiledConstraint{}, fmt.Errorf("policy %s target CIDR %q: %w", name, raw, err)
		}
		constraint.targetCIDRs = append(constraint.targetCIDRs, prefix.Masked())
	}
	sort.Slice(constraint.targetCIDRs, func(i, j int) bool {
		return constraint.targetCIDRs[i].String() < constraint.targetCIDRs[j].String()
	})
	return constraint, nil
}

func (e *Evaluator) Evaluate(candidates []Candidate) Result {
	if e == nil {
		return Result{Allowed: append([]Candidate(nil), candidates...)}
	}
	sorted := append([]Candidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool { return candidateKey(sorted[i]) < candidateKey(sorted[j]) })

	accepted := make([]evaluatedCandidate, 0, len(sorted))
	var rejected []Rejection
	for _, candidate := range sorted {
		if len(e.outer.sourceKinds) > 0 && !containsFold(e.outer.sourceKinds, candidate.Endpoint.Source.Kind) {
			rejected = append(rejected, Rejection{Candidate: candidate, Reason: ReasonDenied})
			continue
		}
		constraints := []compiledConstraint{e.outer}
		for _, policy := range e.policies[candidate.Endpoint.Source.Namespace] {
			if policy.matches(candidate) {
				constraints = append(constraints, policy)
			}
		}
		reason := evaluateConstraints(candidate, constraints)
		if reason != "" {
			rejected = append(rejected, Rejection{Candidate: candidate, Reason: reason})
			continue
		}
		accepted = append(accepted, evaluatedCandidate{
			Candidate:      candidate,
			namespaceLimit: minimumPositive(constraints, func(c compiledConstraint) int { return c.maxRecordsPerNamespace }),
			targetLimit:    minimumPositive(constraints, func(c compiledConstraint) int { return c.maxRecordsPerTarget }),
		})
	}

	namespaceCount := map[string]int{}
	targetCount := map[string]int{}
	allowed := make([]Candidate, 0, len(accepted))
	for _, candidate := range accepted {
		namespace := candidate.Endpoint.Source.Namespace
		if candidate.namespaceLimit > 0 && namespaceCount[namespace] >= candidate.namespaceLimit {
			rejected = append(rejected, Rejection{Candidate: candidate.Candidate, Reason: ReasonNamespaceQuotaExceeded})
			continue
		}
		if candidate.targetLimit > 0 && targetCount[candidate.TargetName] >= candidate.targetLimit {
			rejected = append(rejected, Rejection{Candidate: candidate.Candidate, Reason: ReasonTargetQuotaExceeded})
			continue
		}
		namespaceCount[namespace]++
		targetCount[candidate.TargetName]++
		allowed = append(allowed, candidate.Candidate)
	}
	return Result{Allowed: allowed, Rejected: rejected}
}

type evaluatedCandidate struct {
	Candidate
	namespaceLimit int
	targetLimit    int
}

func (c compiledConstraint) matches(candidate Candidate) bool {
	if !c.selector.Matches(labels.Set(candidate.Labels)) {
		return false
	}
	return len(c.sourceKinds) == 0 || containsFold(c.sourceKinds, candidate.Endpoint.Source.Kind)
}

func evaluateConstraints(candidate Candidate, constraints []compiledConstraint) Reason {
	endpoint := candidate.Endpoint.Normalize()
	for _, constraint := range constraints {
		if constraint.deny {
			return ReasonDenied
		}
		if len(constraint.hostnameSuffixes) > 0 && !matchesSuffix(endpoint.DNSName, constraint.hostnameSuffixes) {
			return ReasonHostnameNotAllowed
		}
		if constraint.ttl != nil {
			if constraint.ttl.Minimum > 0 && endpoint.TTL < constraint.ttl.Minimum {
				return ReasonTTLNotAllowed
			}
			if constraint.ttl.Maximum > 0 && endpoint.TTL > constraint.ttl.Maximum {
				return ReasonTTLNotAllowed
			}
		}
		if constraint.requireOptIn != nil && candidate.Annotations[constraint.requireOptIn.Annotation] != constraint.requireOptIn.Value {
			return ReasonOptInRequired
		}
		for _, target := range endpoint.Targets {
			address, parseErr := netip.ParseAddr(target)
			if parseErr == nil {
				if len(constraint.targetCIDRs) > 0 && !containsAddress(constraint.targetCIDRs, address) {
					return ReasonTargetNotAllowed
				}
				continue
			}
			if len(constraint.targetHostnameSuffixes) > 0 && !matchesSuffix(target, constraint.targetHostnameSuffixes) {
				return ReasonTargetNotAllowed
			}
		}
	}
	return ""
}

func minimumPositive(constraints []compiledConstraint, value func(compiledConstraint) int) int {
	minimum := 0
	for _, constraint := range constraints {
		candidate := value(constraint)
		if candidate > 0 && (minimum == 0 || candidate < minimum) {
			minimum = candidate
		}
	}
	return minimum
}

func candidateKey(candidate Candidate) string {
	return strings.Join([]string{candidate.TargetName, candidate.Endpoint.Source.Namespace, candidate.Endpoint.Source.Kind, candidate.Endpoint.Source.Name, candidate.Endpoint.Key()}, "|")
}

func stringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func containsFold(values map[string]struct{}, value string) bool {
	_, ok := values[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

func normalizeSuffixes(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = dns.NormalizeDNSName(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func matchesSuffix(value string, suffixes []string) bool {
	value = dns.NormalizeDNSName(value)
	for _, suffix := range suffixes {
		if value == suffix || strings.HasSuffix(value, "."+suffix) {
			return true
		}
	}
	return false
}

func containsAddress(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func copyTTL(value *v1alpha1.TTLRange) *v1alpha1.TTLRange {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyOptIn(value *v1alpha1.OptInRequirement) *v1alpha1.OptInRequirement {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
