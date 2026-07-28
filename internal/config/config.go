package config

import (
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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
	DefaultLogFormat        = "text"
	DefaultLogLevel         = "info"

	// MinHealthzStaleness is the floor of the auto-derived liveness staleness
	// window so short reconcile intervals cannot produce a hair-trigger liveness
	// probe that restarts the pod during a single slow-but-bounded loop.
	MinHealthzStaleness = 5 * time.Minute

	// MaxDefaultTTL is a generous upper bound (7 days) on the record TTL; values
	// above it are almost certainly a paste/overflow error and FortiGate would
	// reject them at apply time anyway.
	MaxDefaultTTL = 604800
	// MaxRetries bounds the FortiGate retry count so an obviously-wrong value
	// cannot multiply request latency unboundedly.
	MaxRetries = 10
	// These names are referenced both when the flags are registered and when
	// detecting whether they were explicitly set (single source of truth).
	flagFortiGateURL      = "fortigate-url"
	flagFortiGateAPIToken = "fortigate-api-token"
)

type Config struct {
	Provider                string
	Kubeconfig              string
	Once                    bool
	DryRun                  bool
	PlanOutput              string
	PlanOutputOverwrite     bool
	ApprovedPlanHash        string
	TargetMode              bool
	PlatformNamespace       string
	PolicyEnforcement       bool
	EventDriven             bool
	Debounce                time.Duration
	Resync                  time.Duration
	StatusRetention         int
	PublishExternalName     bool
	PublishHeadless         bool
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
	LogFormat               string
	LogLevel                string
	// HealthzMaxStaleness is the liveness heartbeat window. Zero means auto:
	// max(5*Interval, MinHealthzStaleness). Use ResolvedHealthzMaxStaleness.
	HealthzMaxStaleness time.Duration
	// AllowEmptyDesiredCleanup permits cleanup operations in a cycle whose
	// successful discovery produced zero desired endpoints. Off by default so a
	// misconfiguration (wrong domain filter or namespace) cannot mass-delete
	// every owned record; enable it only for intentional decommissioning.
	AllowEmptyDesiredCleanup bool
	// MaxCleanupPerCycle refuses a cycle's cleanup operations when more than
	// this many are planned. Zero means unlimited.
	MaxCleanupPerCycle int
	FortiGate          FortiGateConfig

	// APITokenFromFlag is set when the token was supplied via the
	// --fortigate-api-token flag rather than the environment. The flag places the
	// token on the process command line and in any rendered manifest, so the
	// controller warns about it at startup.
	APITokenFromFlag bool
}

type FortiGateConfig struct {
	BaseURL                string
	APIToken               string
	VDOM                   string
	Zone                   string
	InsecureSkipVerify     bool
	ExclusiveZoneOwnership bool
	// CAFile is a path to a PEM CA bundle that replaces the system roots for
	// FortiGate TLS verification, so private-CA devices can be verified without
	// disabling verification entirely. Mutually exclusive with
	// InsecureSkipVerify.
	CAFile string
	// CAData is the in-memory equivalent used by CRD targets. It is never
	// rendered, logged, or persisted by configuration diagnostics.
	CAData  []byte
	Timeout time.Duration
	Retries int
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
	return load(args, nil)
}

func load(args []string, output io.Writer) (Config, error) {
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
		Provider:                 envString("PROVIDER", DefaultProvider),
		Kubeconfig:               envString("KUBECONFIG", ""),
		Once:                     boolEnv("ONCE", false),
		DryRun:                   boolEnv("DRY_RUN", false),
		PlanOutput:               envString("PLAN_OUTPUT", ""),
		PlanOutputOverwrite:      boolEnv("PLAN_OUTPUT_OVERWRITE", false),
		ApprovedPlanHash:         envString("APPROVED_PLAN_HASH", ""),
		TargetMode:               boolEnv("TARGET_MODE", false),
		PlatformNamespace:        envString("PLATFORM_NAMESPACE", envString("POD_NAMESPACE", "")),
		PolicyEnforcement:        boolEnv("POLICY_ENFORCEMENT", false),
		EventDriven:              boolEnv("EVENT_DRIVEN", false),
		Debounce:                 durationEnv("DEBOUNCE", 2*time.Second),
		Resync:                   durationEnv("RESYNC", time.Minute),
		StatusRetention:          int(int64Env("STATUS_RETENTION", 20)),
		PublishExternalName:      boolEnv("PUBLISH_EXTERNAL_NAME_SERVICES", false),
		PublishHeadless:          boolEnv("PUBLISH_HEADLESS_SERVICES", false),
		Interval:                 durationEnv("INTERVAL", DefaultInterval),
		ReconcileTimeout:         durationEnv("RECONCILE_TIMEOUT", DefaultReconcileTimeout),
		Sources:                  splitCSV(envString("SOURCES", "service,ingress,gateway")),
		Namespaces:               splitCSV(envString("NAMESPACES", "")),
		GatewayTargetNamespaces:  splitCSV(envString("GATEWAY_TARGET_NAMESPACES", "")),
		DomainFilters:            splitCSV(envString("DOMAIN_FILTERS", "")),
		DefaultTTL:               int64Env("DEFAULT_TTL", 300),
		OwnerID:                  envString("OWNER_ID", DefaultOwnerID),
		CleanupPolicy:            envString("CLEANUP_POLICY", DefaultCleanupPolicy),
		MetricsAddr:              envString("METRICS_ADDR", DefaultMetricsAddr),
		LeaderElection:           boolEnv("LEADER_ELECTION", true),
		LeaderElectionID:         envString("LEADER_ELECTION_ID", DefaultLeaderElectionID),
		LeaderElectionNamespace:  envString("LEADER_ELECTION_NAMESPACE", ""),
		LogFormat:                envString("LOG_FORMAT", DefaultLogFormat),
		LogLevel:                 envString("LOG_LEVEL", DefaultLogLevel),
		HealthzMaxStaleness:      durationEnv("HEALTHZ_MAX_STALENESS", 0),
		AllowEmptyDesiredCleanup: boolEnv("ALLOW_EMPTY_DESIRED_CLEANUP", false),
		MaxCleanupPerCycle:       int(int64Env("MAX_CLEANUP_PER_CYCLE", 0)),
		FortiGate: FortiGateConfig{
			BaseURL:                envString("FORTIGATE_URL", ""),
			APIToken:               envString("FORTIGATE_API_TOKEN", ""),
			VDOM:                   envString("FORTIGATE_VDOM", "root"),
			Zone:                   envString("FORTIGATE_ZONE", ""),
			InsecureSkipVerify:     boolEnv("FORTIGATE_INSECURE_SKIP_VERIFY", false),
			ExclusiveZoneOwnership: boolEnv("FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP", false),
			CAFile:                 envString("FORTIGATE_CA_FILE", ""),
			Timeout:                durationEnv("FORTIGATE_TIMEOUT", DefaultTimeout),
			Retries:                int(int64Env("FORTIGATE_RETRIES", 2)),
		},
	}

	var sources stringSlice
	var namespaces stringSlice
	var gatewayTargetNamespaces stringSlice
	var domains stringSlice
	var fortiGateAPITokenFlag string
	// Do not bind an environment-derived URL as the flag default: flag help
	// renders non-zero defaults and URLs may contain legacy userinfo. Restore the
	// environment value after parsing only when the URL flag was not visited.
	fortiGateURLFromEnv := cfg.FortiGate.BaseURL
	cfg.FortiGate.BaseURL = ""

	fs := flag.NewFlagSet("fortigate-external-dns", flag.ContinueOnError)
	if output != nil {
		fs.SetOutput(output)
	}
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "DNS provider. Only fortigate is supported.")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig, "Path to kubeconfig. Uses in-cluster config or default kubeconfig when empty.")
	fs.BoolVar(&cfg.Once, "once", cfg.Once, "Run one reconciliation loop and exit.")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Print planned changes without mutating FortiGate.")
	fs.StringVar(&cfg.PlanOutput, "plan-output", cfg.PlanOutput, "Write the canonical one-shot reconciliation plan to this file atomically.")
	fs.BoolVar(&cfg.PlanOutputOverwrite, "plan-output-overwrite", cfg.PlanOutputOverwrite, "Explicitly permit --plan-output to replace an existing file.")
	fs.StringVar(&cfg.ApprovedPlanHash, "approved-plan-hash", cfg.ApprovedPlanHash, "Apply a one-shot plan only when its lowercase SHA-256 hash exactly matches this value.")
	fs.BoolVar(&cfg.TargetMode, "target-mode", cfg.TargetMode, "Load FortiGate targets from namespaced FortiGateDNSTarget resources instead of direct connection flags.")
	fs.StringVar(&cfg.PlatformNamespace, "platform-namespace", cfg.PlatformNamespace, "Namespace containing FortiGate platform resources. Defaults to POD_NAMESPACE.")
	fs.BoolVar(&cfg.PolicyEnforcement, "policy-enforcement", cfg.PolicyEnforcement, "Evaluate namespaced FortiGateDNSPolicy resources before planning.")
	fs.BoolVar(&cfg.EventDriven, "event-driven", cfg.EventDriven, "Enable informer/workqueue reconciliation in target mode.")
	fs.DurationVar(&cfg.Debounce, "debounce", cfg.Debounce, "Minimum debounce for semantic target events.")
	fs.DurationVar(&cfg.Resync, "resync", cfg.Resync, "Periodic full-audit resync interval.")
	fs.IntVar(&cfg.StatusRetention, "status-retention", cfg.StatusRetention, "Bounded per-target status and audit history retention (1..100).")
	fs.BoolVar(&cfg.PublishExternalName, "publish-external-name-services", cfg.PublishExternalName, "Allow explicitly opted-in ExternalName Service publication.")
	fs.BoolVar(&cfg.PublishHeadless, "publish-headless-services", cfg.PublishHeadless, "Allow explicitly opted-in headless Service EndpointSlice publication.")
	fs.DurationVar(&cfg.Interval, "interval", cfg.Interval, "Reconciliation interval.")
	fs.DurationVar(&cfg.ReconcileTimeout, "reconcile-timeout", cfg.ReconcileTimeout, "Timeout bounding each reconciliation loop.")
	fs.Var(&sources, "source", "Enabled source. Repeat or comma-separate: service, ingress, gateway.")
	fs.Var(&namespaces, "namespace", "Namespace to include. Repeat or comma-separate. Empty means all namespaces.")
	fs.Var(&gatewayTargetNamespaces, "gateway-target-namespace", "Namespace to read for parent Gateway address lookup. Repeat or comma-separate. Lookup scope only; does not expand cleanup ownership.")
	fs.Var(&domains, "domain-filter", "Domain suffix to include. Repeat or comma-separate.")
	fs.Int64Var(&cfg.DefaultTTL, "default-ttl", cfg.DefaultTTL, "Default DNS record TTL in seconds.")
	fs.StringVar(&cfg.OwnerID, "owner-id", cfg.OwnerID, "Logical owner ID assigned during exclusive-zone reconciliation.")
	fs.StringVar(&cfg.CleanupPolicy, "cleanup-policy", cfg.CleanupPolicy, "Cleanup policy for stale managed records: delete, deactivate, or keep.")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "Bind address for the health, readiness, and metrics HTTP server. Empty disables it.")
	fs.BoolVar(&cfg.LeaderElection, "leader-election", cfg.LeaderElection, "Enable Kubernetes Lease-based leader election. Ignored with --once.")
	fs.StringVar(&cfg.LeaderElectionID, "leader-election-id", cfg.LeaderElectionID, "Lease name used for leader election.")
	fs.StringVar(&cfg.LeaderElectionNamespace, "leader-election-namespace", cfg.LeaderElectionNamespace, "Namespace for the leader election Lease. Defaults to the pod namespace.")
	fs.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log output format: text or json.")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level: debug, info, warn, or error.")
	fs.DurationVar(&cfg.HealthzMaxStaleness, "healthz-max-staleness", cfg.HealthzMaxStaleness, "Liveness heartbeat window: /healthz fails when the reconciling replica completes no attempt within it. 0 derives max(5*interval, 5m).")
	fs.BoolVar(&cfg.AllowEmptyDesiredCleanup, "allow-empty-desired-cleanup", cfg.AllowEmptyDesiredCleanup, "Allow cleanup operations when discovery succeeds with zero desired endpoints. Off by default to prevent misconfiguration from mass-deleting owned records.")
	fs.IntVar(&cfg.MaxCleanupPerCycle, "max-cleanup-per-cycle", cfg.MaxCleanupPerCycle, "Refuse a cycle's cleanup operations when more than this many are planned. 0 means unlimited.")
	fs.StringVar(&cfg.FortiGate.BaseURL, flagFortiGateURL, "", "FortiGate API base URL.")
	fs.StringVar(&fortiGateAPITokenFlag, flagFortiGateAPIToken, "", "FortiGate API token. Prefer FORTIGATE_API_TOKEN from a Kubernetes Secret.")
	fs.StringVar(&cfg.FortiGate.VDOM, "fortigate-vdom", cfg.FortiGate.VDOM, "FortiGate VDOM.")
	fs.StringVar(&cfg.FortiGate.Zone, "fortigate-zone", cfg.FortiGate.Zone, "FortiGate DNS database zone name.")
	fs.BoolVar(&cfg.FortiGate.InsecureSkipVerify, "fortigate-insecure-skip-verify", cfg.FortiGate.InsecureSkipVerify, "Skip TLS certificate verification for FortiGate. Prefer --fortigate-ca-file for private-CA devices.")
	fs.BoolVar(&cfg.FortiGate.ExclusiveZoneOwnership, "fortigate-exclusive-zone-ownership", cfg.FortiGate.ExclusiveZoneOwnership, "Assert that this controller exclusively owns every record in the configured FortiGate DNS zone.")
	fs.StringVar(&cfg.FortiGate.CAFile, "fortigate-ca-file", cfg.FortiGate.CAFile, "Path to a PEM CA bundle used (instead of system roots) to verify the FortiGate TLS certificate.")
	fs.DurationVar(&cfg.FortiGate.Timeout, "fortigate-timeout", cfg.FortiGate.Timeout, "FortiGate API request timeout.")
	fs.IntVar(&cfg.FortiGate.Retries, "fortigate-retries", cfg.FortiGate.Retries, "FortiGate API retry count for retryable failures.")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	// Help must remain available even when an unrelated environment variable is
	// malformed. flag.ErrHelp returns above; real executions still fail closed on
	// every environment parsing error before any configuration is used.
	if len(parseErrs) > 0 {
		return Config{}, fmt.Errorf("invalid environment configuration: %w", errors.Join(parseErrs...))
	}

	// Detect which flags were explicitly set so a repeated/CSV slice flag replaces
	// the env-derived default wholesale — even an explicit empty value — instead of
	// silently falling back based on the resulting length.
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if !visited[flagFortiGateURL] {
		cfg.FortiGate.BaseURL = fortiGateURLFromEnv
	}
	cfg.APITokenFromFlag = visited[flagFortiGateAPIToken]
	if cfg.APITokenFromFlag {
		cfg.FortiGate.APIToken = fortiGateAPITokenFlag
	}

	if visited["source"] {
		cfg.Sources = normalizeList(sources)
	} else {
		cfg.Sources = normalizeList(cfg.Sources)
	}
	if visited["namespace"] {
		cfg.Namespaces = normalizeList(namespaces)
	} else {
		cfg.Namespaces = normalizeList(cfg.Namespaces)
	}
	if visited["gateway-target-namespace"] {
		cfg.GatewayTargetNamespaces = normalizeList(gatewayTargetNamespaces)
	} else {
		cfg.GatewayTargetNamespaces = normalizeList(cfg.GatewayTargetNamespaces)
	}
	if visited["domain-filter"] {
		cfg.DomainFilters = normalizeList(domains)
	} else {
		cfg.DomainFilters = normalizeList(cfg.DomainFilters)
	}
	// Normalize the cleanup policy so validation and the planner compare a
	// consistent lowercase value (matching how sources are normalized).
	cfg.CleanupPolicy = strings.ToLower(strings.TrimSpace(cfg.CleanupPolicy))
	cfg.LogFormat = strings.ToLower(strings.TrimSpace(cfg.LogFormat))
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	cfg.PlanOutput = strings.TrimSpace(cfg.PlanOutput)
	cfg.ApprovedPlanHash = strings.TrimSpace(cfg.ApprovedPlanHash)
	cfg.PlatformNamespace = strings.TrimSpace(cfg.PlatformNamespace)
	return cfg, nil
}

// ResolvedHealthzMaxStaleness returns the effective liveness heartbeat window:
// the configured value, or max(5*Interval, MinHealthzStaleness) when unset.
func (c Config) ResolvedHealthzMaxStaleness() time.Duration {
	if c.HealthzMaxStaleness > 0 {
		return c.HealthzMaxStaleness
	}
	window := 5 * c.Interval
	if window < MinHealthzStaleness {
		window = MinHealthzStaleness
	}
	return window
}

func (c Config) Validate() error {
	if !strings.EqualFold(strings.TrimSpace(c.Provider), DefaultProvider) {
		return fmt.Errorf("unsupported provider %q: only %q is supported", c.Provider, DefaultProvider)
	}
	if c.Interval <= 0 {
		return errors.New("interval must be greater than zero")
	}
	if c.ReconcileTimeout <= 0 {
		return errors.New("reconcile timeout must be greater than zero")
	}
	if c.Debounce < 0 {
		return errors.New("debounce must not be negative")
	}
	if c.Resync <= 0 {
		return errors.New("resync must be greater than zero")
	}
	if c.StatusRetention < 1 || c.StatusRetention > 100 {
		return errors.New("status retention must be between 1 and 100")
	}
	if c.TargetMode {
		if c.PlatformNamespace == "" {
			return errors.New("platform namespace is required in target mode")
		}
		if strings.TrimSpace(c.FortiGate.BaseURL) != "" || strings.TrimSpace(c.FortiGate.APIToken) != "" || strings.TrimSpace(c.FortiGate.Zone) != "" || strings.TrimSpace(c.FortiGate.CAFile) != "" || c.FortiGate.InsecureSkipVerify || c.FortiGate.ExclusiveZoneOwnership {
			return errors.New("direct FortiGate connection settings are mutually exclusive with target mode")
		}
	}
	if c.DefaultTTL <= 0 {
		return errors.New("default TTL must be greater than zero")
	}
	if c.DefaultTTL > MaxDefaultTTL {
		return fmt.Errorf("default TTL must not exceed %d seconds", MaxDefaultTTL)
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return errors.New("owner ID is required")
	}
	if err := validateMetricsAddr(c.MetricsAddr); err != nil {
		return err
	}
	if c.LeaderElection && !c.Once && strings.TrimSpace(c.LeaderElectionID) == "" {
		return errors.New("leader election ID is required when leader election is enabled")
	}
	switch c.CleanupPolicy {
	case "delete", "deactivate", "keep":
	default:
		return fmt.Errorf("unsupported cleanup policy %q", c.CleanupPolicy)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("unsupported log format %q: must be text or json", c.LogFormat)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q: must be debug, info, warn, or error", c.LogLevel)
	}
	if c.HealthzMaxStaleness < 0 {
		return errors.New("healthz max staleness must be zero (auto) or greater")
	}
	if c.MaxCleanupPerCycle < 0 {
		return errors.New("max cleanup per cycle must be zero (unlimited) or greater")
	}
	if (c.PlanOutput != "" || c.ApprovedPlanHash != "" || c.PlanOutputOverwrite) && !c.Once {
		return errors.New("plan output and hash approval settings require one-shot mode")
	}
	if c.PlanOutputOverwrite && c.PlanOutput == "" {
		return errors.New("plan output overwrite requires a plan output path")
	}
	if c.ApprovedPlanHash != "" && !isLowerSHA256(c.ApprovedPlanHash) {
		return errors.New("approved plan hash must be exactly 64 lowercase hexadecimal characters")
	}
	if c.EventDriven && !c.TargetMode {
		return errors.New("event-driven reconciliation requires target mode")
	}
	if c.TargetMode {
		if c.PlanOutput != "" || c.ApprovedPlanHash != "" || c.PlanOutputOverwrite {
			return errors.New("one-shot file plan settings are unavailable in target mode; use FortiGateDNSChangePlan approval")
		}
		return nil
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
	if !c.DryRun && !c.FortiGate.ExclusiveZoneOwnership {
		return errors.New("FortiGate exclusive zone ownership must be explicitly enabled for write mode")
	}
	if c.FortiGate.ExclusiveZoneOwnership && c.CleanupPolicy != "keep" && (len(c.Namespaces) > 0 || sourcesAreRestrictive(c.Sources)) {
		return errors.New("cleanup policy must be keep when namespaces or a restricted source set are configured with exclusive zone ownership")
	}
	return c.FortiGate.Validate()
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func sourcesAreRestrictive(sources []string) bool {
	enabled := map[string]struct{}{}
	for _, source := range sources {
		enabled[strings.ToLower(strings.TrimSpace(source))] = struct{}{}
	}
	for _, required := range []string{"service", "ingress", "gateway"} {
		if _, ok := enabled[required]; !ok {
			return true
		}
	}
	return false
}

func (c FortiGateConfig) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("FortiGate URL is required")
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return errors.New("FortiGate URL is invalid")
	}
	if parsed.User != nil {
		return errors.New("FortiGate URL must not include user information; use the API token setting for authentication")
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("FortiGate URL must be an absolute URL with scheme and host")
	}
	if parsed.Scheme != "https" {
		return errors.New("FortiGate URL scheme must be https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("FortiGate URL must not include a query or fragment")
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
	if c.Retries > MaxRetries {
		return fmt.Errorf("FortiGate retries must not exceed %d", MaxRetries)
	}
	if strings.TrimSpace(c.CAFile) != "" && len(c.CAData) != 0 {
		return errors.New("FortiGate CA file and in-memory CA data are mutually exclusive")
	}
	if strings.TrimSpace(c.CAFile) != "" || len(c.CAData) != 0 {
		if c.InsecureSkipVerify {
			return errors.New("FortiGate CA file and insecure-skip-verify are mutually exclusive: a CA bundle expresses trust while skip-verify disables it")
		}
		// Read and parse at validation time so a bad path or non-PEM content is a
		// clear startup error instead of a TLS failure on the first reconcile.
		data := c.CAData
		if len(data) == 0 {
			var err error
			data, err = os.ReadFile(c.CAFile)
			if err != nil {
				return fmt.Errorf("FortiGate CA file is unreadable: %w", err)
			}
		}
		if !x509.NewCertPool().AppendCertsFromPEM(data) {
			return errors.New("FortiGate CA bundle contains no PEM certificates")
		}
	}
	return nil
}

// validateMetricsAddr rejects a non-empty metrics/probe bind address that is not
// a valid host:port, so a typo fails at startup instead of asynchronously in a
// background goroutine that leaves Kubernetes probes unanswered. An empty value
// disables the server and is allowed.
func validateMetricsAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("metrics address %q is not a valid host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("metrics address %q must include a port", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("metrics address %q has an invalid port", addr)
	}
	return nil
}

func (c FortiGateConfig) Redacted() map[string]any {
	return map[string]any{
		"baseURL":                redactedBaseURL(c.BaseURL),
		"vdom":                   c.VDOM,
		"zone":                   c.Zone,
		"insecureSkipVerify":     c.InsecureSkipVerify,
		"exclusiveZoneOwnership": c.ExclusiveZoneOwnership,
		"caFile":                 c.CAFile,
		"timeout":                c.Timeout.String(),
		"retries":                c.Retries,
		"apiToken":               "<redacted>",
	}
}

func redactedBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
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
