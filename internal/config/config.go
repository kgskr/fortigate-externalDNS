package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultProvider         = "fortigate"
	DefaultInterval         = time.Minute
	DefaultTimeout          = 15 * time.Second
	DefaultOwnerID          = "fortigate-external-dns"
	DefaultCleanupPolicy    = "delete"
	DefaultReconcileTimeout = 2 * time.Minute
	DefaultMetricsAddr      = ":8080"
	DefaultLeaderElectionID = "fortigate-external-dns"
)

type Config struct {
	Provider                string
	Kubeconfig              string
	Once                    bool
	DryRun                  bool
	Interval                time.Duration
	ReconcileTimeout        time.Duration
	Sources                 []string
	Namespaces              []string
	GatewayTargetNamespaces []string
	DomainFilters           []string
	DefaultTTL              int64
	OwnerID                 string
	CleanupPolicy           string
	MetricsAddr             string
	LeaderElection          bool
	LeaderElectionID        string
	LeaderElectionNamespace string
	FortiGate               FortiGateConfig
}

type FortiGateConfig struct {
	BaseURL            string
	APIToken           string
	VDOM               string
	Zone               string
	InsecureSkipVerify bool
	Timeout            time.Duration
	Retries            int
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(value string) error {
	for _, item := range splitCSV(value) {
		*s = append(*s, item)
	}
	return nil
}

func Load(args []string) (Config, error) {
	var parseErrs []error
	boolEnv := func(name string, fallback bool) bool {
		value, err := envBool(name, fallback)
		if err != nil {
			parseErrs = append(parseErrs, err)
		}
		return value
	}
	durationEnv := func(name string, fallback time.Duration) time.Duration {
		value, err := envDuration(name, fallback)
		if err != nil {
			parseErrs = append(parseErrs, err)
		}
		return value
	}
	int64Env := func(name string, fallback int64) int64 {
		value, err := envInt64(name, fallback)
		if err != nil {
			parseErrs = append(parseErrs, err)
		}
		return value
	}

	cfg := Config{
		Provider:                envString("PROVIDER", DefaultProvider),
		Kubeconfig:              envString("KUBECONFIG", ""),
		Once:                    boolEnv("ONCE", false),
		DryRun:                  boolEnv("DRY_RUN", false),
		Interval:                durationEnv("INTERVAL", DefaultInterval),
		ReconcileTimeout:        durationEnv("RECONCILE_TIMEOUT", DefaultReconcileTimeout),
		Sources:                 splitCSV(envString("SOURCES", "service,ingress,gateway")),
		Namespaces:              splitCSV(envString("NAMESPACES", "")),
		GatewayTargetNamespaces: splitCSV(envString("GATEWAY_TARGET_NAMESPACES", "")),
		DomainFilters:           splitCSV(envString("DOMAIN_FILTERS", "")),
		DefaultTTL:              int64Env("DEFAULT_TTL", 300),
		OwnerID:                 envString("OWNER_ID", DefaultOwnerID),
		CleanupPolicy:           envString("CLEANUP_POLICY", DefaultCleanupPolicy),
		MetricsAddr:             envString("METRICS_ADDR", DefaultMetricsAddr),
		LeaderElection:          boolEnv("LEADER_ELECTION", true),
		LeaderElectionID:        envString("LEADER_ELECTION_ID", DefaultLeaderElectionID),
		LeaderElectionNamespace: envString("LEADER_ELECTION_NAMESPACE", ""),
		FortiGate: FortiGateConfig{
			BaseURL:            envString("FORTIGATE_URL", ""),
			APIToken:           envString("FORTIGATE_API_TOKEN", ""),
			VDOM:               envString("FORTIGATE_VDOM", "root"),
			Zone:               envString("FORTIGATE_ZONE", ""),
			InsecureSkipVerify: boolEnv("FORTIGATE_INSECURE_SKIP_VERIFY", false),
			Timeout:            durationEnv("FORTIGATE_TIMEOUT", DefaultTimeout),
			Retries:            int(int64Env("FORTIGATE_RETRIES", 2)),
		},
	}

	if len(parseErrs) > 0 {
		return Config{}, fmt.Errorf("invalid environment configuration: %w", errors.Join(parseErrs...))
	}

	var sources stringSlice
	var namespaces stringSlice
	var gatewayTargetNamespaces stringSlice
	var domains stringSlice

	fs := flag.NewFlagSet("fortigate-external-dns", flag.ContinueOnError)
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "DNS provider. Only fortigate is supported.")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig, "Path to kubeconfig. Uses in-cluster config or default kubeconfig when empty.")
	fs.BoolVar(&cfg.Once, "once", cfg.Once, "Run one reconciliation loop and exit.")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Print planned changes without mutating FortiGate.")
	fs.DurationVar(&cfg.Interval, "interval", cfg.Interval, "Reconciliation interval.")
	fs.DurationVar(&cfg.ReconcileTimeout, "reconcile-timeout", cfg.ReconcileTimeout, "Timeout bounding each reconciliation loop.")
	fs.Var(&sources, "source", "Enabled source. Repeat or comma-separate: service, ingress, gateway.")
	fs.Var(&namespaces, "namespace", "Namespace to include. Repeat or comma-separate. Empty means all namespaces.")
	fs.Var(&gatewayTargetNamespaces, "gateway-target-namespace", "Namespace to read for parent Gateway address lookup. Repeat or comma-separate. Lookup scope only; does not expand cleanup ownership.")
	fs.Var(&domains, "domain-filter", "Domain suffix to include. Repeat or comma-separate.")
	fs.Int64Var(&cfg.DefaultTTL, "default-ttl", cfg.DefaultTTL, "Default DNS record TTL in seconds.")
	fs.StringVar(&cfg.OwnerID, "owner-id", cfg.OwnerID, "Owner ID used to protect managed DNS records.")
	fs.StringVar(&cfg.CleanupPolicy, "cleanup-policy", cfg.CleanupPolicy, "Cleanup policy for stale managed records: delete, deactivate, or keep.")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "Bind address for the health, readiness, and metrics HTTP server. Empty disables it.")
	fs.BoolVar(&cfg.LeaderElection, "leader-election", cfg.LeaderElection, "Enable Kubernetes Lease-based leader election. Ignored with --once.")
	fs.StringVar(&cfg.LeaderElectionID, "leader-election-id", cfg.LeaderElectionID, "Lease name used for leader election.")
	fs.StringVar(&cfg.LeaderElectionNamespace, "leader-election-namespace", cfg.LeaderElectionNamespace, "Namespace for the leader election Lease. Defaults to the pod namespace.")
	fs.StringVar(&cfg.FortiGate.BaseURL, "fortigate-url", cfg.FortiGate.BaseURL, "FortiGate API base URL.")
	fs.StringVar(&cfg.FortiGate.APIToken, "fortigate-api-token", cfg.FortiGate.APIToken, "FortiGate API token. Prefer FORTIGATE_API_TOKEN from a Kubernetes Secret.")
	fs.StringVar(&cfg.FortiGate.VDOM, "fortigate-vdom", cfg.FortiGate.VDOM, "FortiGate VDOM.")
	fs.StringVar(&cfg.FortiGate.Zone, "fortigate-zone", cfg.FortiGate.Zone, "FortiGate DNS database zone name.")
	fs.BoolVar(&cfg.FortiGate.InsecureSkipVerify, "fortigate-insecure-skip-verify", cfg.FortiGate.InsecureSkipVerify, "Skip TLS certificate verification for FortiGate.")
	fs.DurationVar(&cfg.FortiGate.Timeout, "fortigate-timeout", cfg.FortiGate.Timeout, "FortiGate API request timeout.")
	fs.IntVar(&cfg.FortiGate.Retries, "fortigate-retries", cfg.FortiGate.Retries, "FortiGate API retry count for retryable failures.")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if len(sources) > 0 {
		cfg.Sources = normalizeList(sources)
	} else {
		cfg.Sources = normalizeList(cfg.Sources)
	}
	if len(namespaces) > 0 {
		cfg.Namespaces = normalizeList(namespaces)
	} else {
		cfg.Namespaces = normalizeList(cfg.Namespaces)
	}
	if len(gatewayTargetNamespaces) > 0 {
		cfg.GatewayTargetNamespaces = normalizeList(gatewayTargetNamespaces)
	} else {
		cfg.GatewayTargetNamespaces = normalizeList(cfg.GatewayTargetNamespaces)
	}
	if len(domains) > 0 {
		cfg.DomainFilters = normalizeList(domains)
	} else {
		cfg.DomainFilters = normalizeList(cfg.DomainFilters)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Provider != DefaultProvider {
		return fmt.Errorf("unsupported provider %q: only %q is supported", c.Provider, DefaultProvider)
	}
	if c.Interval <= 0 {
		return errors.New("interval must be greater than zero")
	}
	if c.ReconcileTimeout <= 0 {
		return errors.New("reconcile timeout must be greater than zero")
	}
	if c.DefaultTTL <= 0 {
		return errors.New("default TTL must be greater than zero")
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return errors.New("owner ID is required")
	}
	switch c.CleanupPolicy {
	case "delete", "deactivate", "keep":
	default:
		return fmt.Errorf("unsupported cleanup policy %q", c.CleanupPolicy)
	}
	if len(c.Sources) == 0 {
		return errors.New("at least one source must be enabled")
	}
	for _, source := range c.Sources {
		switch source {
		case "service", "ingress", "gateway":
		default:
			return fmt.Errorf("unsupported source %q", source)
		}
	}
	return c.FortiGate.Validate()
}

func (c FortiGateConfig) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("FortiGate URL is required")
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("FortiGate URL is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("FortiGate URL must be an absolute URL with scheme and host: %q", c.BaseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("FortiGate URL scheme must be http or https, got %q", parsed.Scheme)
	}
	if strings.TrimSpace(c.APIToken) == "" {
		return errors.New("FortiGate API token is required")
	}
	if strings.TrimSpace(c.Zone) == "" {
		return errors.New("FortiGate DNS zone is required")
	}
	if c.Timeout <= 0 {
		return errors.New("FortiGate timeout must be greater than zero")
	}
	if c.Retries < 0 {
		return errors.New("FortiGate retries must be zero or greater")
	}
	return nil
}

func (c FortiGateConfig) Redacted() map[string]any {
	return map[string]any{
		"baseURL":            c.BaseURL,
		"vdom":               c.VDOM,
		"zone":               c.Zone,
		"insecureSkipVerify": c.InsecureSkipVerify,
		"timeout":            c.Timeout.String(),
		"retries":            c.Retries,
		"apiToken":           "<redacted>",
	}
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid boolean", name, value)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid duration", name, value)
	}
	return parsed, nil
}

func envInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid integer", name, value)
	}
	return parsed, nil
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		for _, item := range splitCSV(value) {
			item = strings.ToLower(strings.TrimSpace(item))
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}
