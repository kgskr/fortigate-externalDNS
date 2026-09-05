package config

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSourceFlagOverridesDefaultSources(t *testing.T) {
	cfg, err := Load([]string{
		"--source=service",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0] != "service" {
		t.Fatalf("expected only service source, got %#v", cfg.Sources)
	}
}

func TestLoadFailsOnMalformedBoolEnv(t *testing.T) {
	t.Setenv("DRY_RUN", "ture")
	_, err := Load([]string{
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err == nil {
		t.Fatal("expected loading to fail for a malformed DRY_RUN, which must not silently default to write-enabled mode")
	}
	if !strings.Contains(err.Error(), "DRY_RUN") {
		t.Fatalf("error should name the offending variable, got %v", err)
	}
}

func TestLoadFailsOnMalformedDurationEnv(t *testing.T) {
	t.Setenv("INTERVAL", "30") // missing unit
	_, err := Load([]string{
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err == nil || !strings.Contains(err.Error(), "INTERVAL") {
		t.Fatalf("expected a duration parse error naming INTERVAL, got %v", err)
	}
}

func TestLoadAcceptsValidEnv(t *testing.T) {
	t.Setenv("DRY_RUN", "true")
	t.Setenv("FORTIGATE_RETRIES", "3")
	cfg, err := Load([]string{
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DryRun || cfg.FortiGate.Retries != 3 {
		t.Fatalf("env values not applied: dryRun=%v retries=%d", cfg.DryRun, cfg.FortiGate.Retries)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config should pass validation: %v", err)
	}
}

func TestLoadUsesFortiGateAPITokenEnvWithoutLeakingHelpDefault(t *testing.T) {
	envToken := "env-secret-value-123456"
	t.Setenv("FORTIGATE_API_TOKEN", envToken)

	cfg, err := Load([]string{
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FortiGate.APIToken != envToken {
		t.Fatalf("env token should be applied, got %q", cfg.FortiGate.APIToken)
	}
	if cfg.APITokenFromFlag {
		t.Fatal("env token must not be reported as supplied by flag")
	}

	var help bytes.Buffer
	_, err = load([]string{"-h"}, &help)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag help error, got %v", err)
	}
	if strings.Contains(help.String(), envToken) {
		t.Fatalf("help output leaked FORTIGATE_API_TOKEN value: %s", help.String())
	}
	if !strings.Contains(help.String(), "fortigate-api-token string") {
		t.Fatalf("help output should still document the token flag: %s", help.String())
	}
}

func TestLoadRestoresFortiGateURLEnvOnlyWhenFlagIsAbsent(t *testing.T) {
	const envURL = "https://env-fortigate.example.com"
	t.Setenv("FORTIGATE_URL", envURL)

	t.Run("environment value", func(t *testing.T) {
		cfg, err := Load(nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.FortiGate.BaseURL != envURL {
			t.Fatalf("environment URL was not restored after flag parsing: %q", cfg.FortiGate.BaseURL)
		}
	})

	t.Run("flag overrides environment", func(t *testing.T) {
		const flagURL = "https://flag-fortigate.example.com"
		cfg, err := Load([]string{"--fortigate-url=" + flagURL})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.FortiGate.BaseURL != flagURL {
			t.Fatalf("explicit URL flag must override the environment, got %q", cfg.FortiGate.BaseURL)
		}
	})

	t.Run("explicit empty flag overrides environment", func(t *testing.T) {
		cfg, err := Load([]string{"--fortigate-url="})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.FortiGate.BaseURL != "" {
			t.Fatalf("explicit empty URL flag must not fall back to the environment, got %q", cfg.FortiGate.BaseURL)
		}
	})
}

func TestLoadFortiGateURLDoesNotLeakIntoHelp(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		sensitive []string
	}{
		{
			name:      "username only",
			url:       "https://demo-user@fortigate.example.com",
			sensitive: []string{"demo-user"},
		},
		{
			name:      "username and password",
			url:       "https://demo-user:demo-password@fortigate.example.com",
			sensitive: []string{"demo-user", "demo-password"},
		},
		{
			name:      "malformed percent escape",
			url:       "https://demo-user:demo-password%zz@fortigate.example.com",
			sensitive: []string{"demo-user", "demo-password", "%zz"},
		},
		{
			name:      "query credential and fragment",
			url:       "https://fortigate.example.com?access_token=query-secret#fragment-secret",
			sensitive: []string{"access_token", "query-secret", "fragment-secret"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FORTIGATE_URL", tc.url)
			var help bytes.Buffer
			_, err := load([]string{"--help"}, &help)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("expected flag help error, got %v", err)
			}
			output := help.String()
			if strings.Contains(output, tc.url) {
				t.Fatalf("help output leaked the complete FORTIGATE_URL: %s", output)
			}
			for _, secret := range tc.sensitive {
				if strings.Contains(output, secret) {
					t.Fatalf("help output leaked %q from FORTIGATE_URL: %s", secret, output)
				}
			}
			if !strings.Contains(output, "fortigate-url string") {
				t.Fatalf("help output should still document the URL flag: %s", output)
			}
		})
	}
}

func TestLoadHelpTakesPriorityOverMalformedEnvironment(t *testing.T) {
	t.Setenv("DRY_RUN", "ture")
	var help bytes.Buffer
	_, err := load([]string{"--help"}, &help)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help must remain available despite malformed environment configuration, got %v", err)
	}
	if !strings.Contains(help.String(), "Usage of fortigate-external-dns") {
		t.Fatalf("expected usage output, got %q", help.String())
	}
	if _, err := load(nil, &help); err == nil || !strings.Contains(err.Error(), "DRY_RUN") {
		t.Fatalf("a real execution must still reject the malformed environment, got %v", err)
	}
}

func TestLoadExclusiveZoneOwnershipFromEnvironmentAndFlag(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		t.Setenv("FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP", "true")
		cfg, err := Load(nil)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.FortiGate.ExclusiveZoneOwnership {
			t.Fatal("exclusive zone ownership environment setting was not loaded")
		}
	})
	t.Run("flag overrides environment", func(t *testing.T) {
		t.Setenv("FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP", "false")
		cfg, err := Load([]string{"--fortigate-exclusive-zone-ownership"})
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.FortiGate.ExclusiveZoneOwnership {
			t.Fatal("exclusive zone ownership flag was not loaded")
		}
	})
}

func TestLoadFortiGateAPITokenFlagOverridesEnv(t *testing.T) {
	envToken := "env-secret-value-123456"
	flagToken := "flag-secret-value-123456"
	t.Setenv("FORTIGATE_API_TOKEN", envToken)

	cfg, err := Load([]string{
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=" + flagToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FortiGate.APIToken != flagToken {
		t.Fatalf("flag token should override env token, got %q", cfg.FortiGate.APIToken)
	}
	if !cfg.APITokenFromFlag {
		t.Fatal("flag token should be reported as supplied by flag")
	}
}

func TestLoadNormalizesCleanupPolicyCase(t *testing.T) {
	cfg, err := Load([]string{
		"--cleanup-policy=Delete",
		"--fortigate-exclusive-zone-ownership",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CleanupPolicy != "delete" {
		t.Fatalf("cleanup policy should be normalized to lowercase, got %q", cfg.CleanupPolicy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("case-insensitive cleanup policy should validate: %v", err)
	}
}

func TestValidateAcceptsOwnerIDWithoutCommentEncodingRestrictions(t *testing.T) {
	cfg := baseValidConfig()
	cfg.OwnerID = "team;a=" + strings.Repeat("long", 100)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("owner ID is no longer serialized into a FortiGate comment and should only need to be non-empty: %v", err)
	}
}

func TestValidateRequiresHTTPSFortiGateURL(t *testing.T) {
	for _, rawURL := range []string{"http://fortigate.example.com", "ftp://fortigate.example.com"} {
		cfg, err := Load([]string{
			"--fortigate-exclusive-zone-ownership",
			"--fortigate-url=" + rawURL,
			"--fortigate-zone=example.com",
			"--fortigate-api-token=unit-test-credential",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err == nil || err.Error() != "FortiGate URL scheme must be https" {
			t.Fatalf("expected non-HTTPS URL scheme to be rejected without echoing its value, got %v", err)
		} else if strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "ftp") {
			t.Fatalf("unsupported scheme error leaked the supplied scheme: %v", err)
		}
	}
}

func TestValidateRejectsUnsafeFortiGateURLsWithoutLeakingThem(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		wantError    string
		wantRedacted string
		sensitive    []string
	}{
		{
			name:         "username only",
			url:          "https://demo-user@fortigate.example.com",
			wantError:    "FortiGate URL must not include user information; use the API token setting for authentication",
			wantRedacted: "https://fortigate.example.com",
			sensitive:    []string{"demo-user"},
		},
		{
			name:         "username and password",
			url:          "https://demo-user:demo-password@fortigate.example.com",
			wantError:    "FortiGate URL must not include user information; use the API token setting for authentication",
			wantRedacted: "https://fortigate.example.com",
			sensitive:    []string{"demo-user", "demo-password"},
		},
		{
			name:         "malformed percent escape",
			url:          "https://demo-user:demo-password%zz@fortigate.example.com",
			wantError:    "FortiGate URL is invalid",
			wantRedacted: "<invalid>",
			sensitive:    []string{"demo-user", "demo-password", "%zz"},
		},
		{
			name:         "not absolute",
			url:          "demo-user:demo-password@fortigate.example.com",
			wantError:    "FortiGate URL must be an absolute URL with scheme and host",
			wantRedacted: "<invalid>",
			sensitive:    []string{"demo-user", "demo-password"},
		},
		{
			name:         "query credential and fragment",
			url:          "https://fortigate.example.com?access_token=query-secret#fragment-secret",
			wantError:    "FortiGate URL must not include a query or fragment",
			wantRedacted: "https://fortigate.example.com",
			sensitive:    []string{"access_token", "query-secret", "fragment-secret"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.FortiGate.BaseURL = tc.url
			err := cfg.Validate()
			if err == nil || err.Error() != tc.wantError {
				t.Fatalf("unsafe FortiGate URL should fail with a safe generic error %q, got %v", tc.wantError, err)
			}
			if strings.Contains(err.Error(), tc.url) {
				t.Fatalf("validation error leaked the complete URL: %v", err)
			}
			for _, secret := range tc.sensitive {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("validation error leaked %q from the URL: %v", secret, err)
				}
			}

			redacted := cfg.FortiGate.Redacted()
			baseURL, ok := redacted["baseURL"].(string)
			if !ok {
				t.Fatalf("redacted baseURL has unexpected type: %#v", redacted["baseURL"])
			}
			if baseURL != tc.wantRedacted {
				t.Fatalf("redacted URL = %q, want %q", baseURL, tc.wantRedacted)
			}
			for _, secret := range tc.sensitive {
				if strings.Contains(baseURL, secret) {
					t.Fatalf("redacted URL leaked %q: %q", secret, baseURL)
				}
			}
			if got := redacted["exclusiveZoneOwnership"]; got != true {
				t.Fatalf("redacted configuration must expose the non-secret exclusive ownership mode, got %#v", got)
			}
		})
	}
}

func TestValidateRequiresExclusiveZoneOwnershipForWriteMode(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FortiGate.ExclusiveZoneOwnership = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exclusive zone ownership") {
		t.Fatalf("write mode without explicit exclusive ownership must fail, got %v", err)
	}
	cfg.DryRun = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dry-run must be usable to inspect configuration before asserting exclusive ownership: %v", err)
	}
}

func TestValidateRequiresKeepCleanupForScopedDiscovery(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "namespace filter", mutate: func(cfg *Config) { cfg.Namespaces = []string{"apps"} }},
		{name: "restricted sources", mutate: func(cfg *Config) { cfg.Sources = []string{"service", "ingress"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cleanup policy must be keep") {
				t.Fatalf("scoped discovery with destructive cleanup must fail, got %v", err)
			}
			cfg.CleanupPolicy = "keep"
			if err := cfg.Validate(); err != nil {
				t.Fatalf("keep cleanup must make scoped discovery safe, got %v", err)
			}
		})
	}
	dryRun := baseValidConfig()
	dryRun.DryRun = true
	dryRun.FortiGate.ExclusiveZoneOwnership = false
	dryRun.Namespaces = []string{"apps"}
	if err := dryRun.Validate(); err != nil {
		t.Fatalf("non-exclusive dry-run may preview scoped destructive plans without mutating the zone: %v", err)
	}
}

func TestValidateAcceptsCaseInsensitiveProvider(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Provider = "FortiGate"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("provider should be matched case-insensitively: %v", err)
	}
}

func TestValidateRejectsMalformedMetricsAddr(t *testing.T) {
	cfg := baseValidConfig()
	cfg.MetricsAddr = "8080" // missing colon/port separator
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "metrics address") {
		t.Fatalf("malformed metrics address must be rejected at startup, got %v", err)
	}
}

func TestValidateAllowsEmptyMetricsAddr(t *testing.T) {
	cfg := baseValidConfig()
	cfg.MetricsAddr = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty metrics address disables the server and must be allowed: %v", err)
	}
}

func TestValidateRejectsEmptyLeaderElectionIDWhenEnabled(t *testing.T) {
	cfg := baseValidConfig()
	cfg.LeaderElection = true
	cfg.Once = false
	cfg.LeaderElectionID = "  "
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty leader election ID must be rejected when leader election is enabled")
	}
}

func TestValidateRejectsOutOfRangeNumbers(t *testing.T) {
	cfg := baseValidConfig()
	cfg.DefaultTTL = MaxDefaultTTL + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("an excessive default TTL must be rejected")
	}
	cfg = baseValidConfig()
	cfg.FortiGate.Retries = MaxRetries + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("an excessive retry count must be rejected")
	}
}

func baseValidConfig() Config {
	return Config{
		Provider:         DefaultProvider,
		Interval:         DefaultInterval,
		ReconcileTimeout: DefaultReconcileTimeout,
		Debounce:         2 * time.Second,
		Resync:           time.Minute,
		StatusRetention:  20,
		PlanRetention:    20,
		DefaultTTL:       300,
		OwnerID:          DefaultOwnerID,
		CleanupPolicy:    DefaultCleanupPolicy,
		MetricsAddr:      DefaultMetricsAddr,
		LeaderElection:   true,
		LeaderElectionID: DefaultLeaderElectionID,
		LogFormat:        DefaultLogFormat,
		LogLevel:         DefaultLogLevel,
		Sources:          []string{"service", "ingress", "gateway"},
		FortiGate: FortiGateConfig{
			BaseURL:                "https://fortigate.example.com",
			APIToken:               "unit-test-credential",
			Zone:                   "example.com",
			Timeout:                DefaultTimeout,
			Retries:                2,
			ExclusiveZoneOwnership: true,
		},
	}
}

func TestLoadAndValidateTargetModePlatformSettings(t *testing.T) {
	cfg, err := Load([]string{
		"--target-mode", "--platform-namespace=network-system", "--policy-enforcement", "--event-driven",
		"--debounce=3s", "--resync=2m", "--status-retention=25", "--plan-retention=17",
		"--publish-external-name-services", "--publish-headless-services",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TargetMode || !cfg.PolicyEnforcement || !cfg.EventDriven || cfg.PlatformNamespace != "network-system" ||
		cfg.Debounce != 3*time.Second || cfg.Resync != 2*time.Minute || cfg.StatusRetention != 25 || cfg.PlanRetention != 17 ||
		!cfg.PublishExternalName || !cfg.PublishHeadless {
		t.Fatalf("platform settings were not loaded: %#v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid target mode rejected: %v", err)
	}
}

func TestValidateTargetModeFailsClosed(t *testing.T) {
	base := func() Config {
		cfg := baseValidConfig()
		cfg.TargetMode = true
		cfg.PlatformNamespace = "network-system"
		cfg.FortiGate = FortiGateConfig{}
		return cfg
	}
	tests := map[string]func(*Config){
		"direct URL":          func(cfg *Config) { cfg.FortiGate.BaseURL = "https://fortigate.example.com" },
		"missing namespace":   func(cfg *Config) { cfg.PlatformNamespace = "" },
		"negative debounce":   func(cfg *Config) { cfg.Debounce = -time.Second },
		"zero resync":         func(cfg *Config) { cfg.Resync = 0 },
		"excess retention":    func(cfg *Config) { cfg.StatusRetention = 101 },
		"zero plan retention": func(cfg *Config) { cfg.PlanRetention = 0 },
		"file approval":       func(cfg *Config) { cfg.Once = true; cfg.PlanOutput = "/tmp/plan.json" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe target mode unexpectedly validated")
			}
		})
	}
}

func TestValidateRejectsCAFileWithInsecureSkipVerify(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FortiGate.CAFile = writeTempCA(t)
	cfg.FortiGate.InsecureSkipVerify = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("CA file combined with insecure-skip-verify must fail validation, got %v", err)
	}
}

func TestValidateRejectsMissingCAFile(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FortiGate.CAFile = filepath.Join(t.TempDir(), "does-not-exist.pem")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("missing CA file must fail validation, got %v", err)
	}
}

func TestValidateRejectsNonPEMCAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-cert.pem")
	if err := os.WriteFile(path, []byte("this is not PEM data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := baseValidConfig()
	cfg.FortiGate.CAFile = path
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Fatalf("non-PEM CA file must fail validation, got %v", err)
	}
}

func TestValidateAcceptsValidCAFile(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FortiGate.CAFile = writeTempCA(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid PEM CA file should pass validation: %v", err)
	}
}

func TestValidateRejectsInvalidLogFormatAndLevel(t *testing.T) {
	cfg := baseValidConfig()
	cfg.LogFormat = "xml"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "log format") {
		t.Fatalf("invalid log format must be rejected, got %v", err)
	}
	cfg = baseValidConfig()
	cfg.LogLevel = "verbose"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "log level") {
		t.Fatalf("invalid log level must be rejected, got %v", err)
	}
}

func TestLoadNormalizesLogFormatAndLevelCase(t *testing.T) {
	t.Setenv("LOG_FORMAT", "JSON")
	t.Setenv("LOG_LEVEL", "Warn")
	cfg, err := Load([]string{
		"--fortigate-exclusive-zone-ownership",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFormat != "json" || cfg.LogLevel != "warn" {
		t.Fatalf("log format/level should normalize to lowercase, got %q/%q", cfg.LogFormat, cfg.LogLevel)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("normalized log settings should validate: %v", err)
	}
}

func TestValidateRejectsNegativeCleanupGuardValues(t *testing.T) {
	cfg := baseValidConfig()
	cfg.MaxCleanupPerCycle = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("a negative cleanup cap must be rejected")
	}
	cfg = baseValidConfig()
	cfg.HealthzMaxStaleness = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("a negative healthz staleness must be rejected")
	}
}

func TestResolvedHealthzMaxStaleness(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Interval = time.Minute
	if got := cfg.ResolvedHealthzMaxStaleness(); got != MinHealthzStaleness {
		t.Fatalf("1m interval should derive the %s floor, got %s", MinHealthzStaleness, got)
	}
	cfg.Interval = 2 * time.Minute
	if got := cfg.ResolvedHealthzMaxStaleness(); got != 10*time.Minute {
		t.Fatalf("2m interval should derive 5x = 10m, got %s", got)
	}
	cfg.HealthzMaxStaleness = 90 * time.Second
	if got := cfg.ResolvedHealthzMaxStaleness(); got != 90*time.Second {
		t.Fatalf("an explicit staleness window must win, got %s", got)
	}
}

// writeTempCA writes a self-signed certificate PEM to a temp file and returns
// its path.
func writeTempCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unit-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateRejectsNonFortiGateProvider(t *testing.T) {
	cfg, err := Load([]string{
		"--provider=route53",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-FortiGate provider to be rejected")
	}
}

func TestLoadOneShotPlanSettings(t *testing.T) {
	planHash := strings.Repeat("a", 64)
	cfg, err := Load([]string{
		"--once",
		"--plan-output=/tmp/plan.json",
		"--plan-output-overwrite",
		"--approved-plan-hash=" + planHash,
		"--fortigate-exclusive-zone-ownership",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlanOutput != "/tmp/plan.json" || !cfg.PlanOutputOverwrite || cfg.ApprovedPlanHash != planHash {
		t.Fatalf("plan settings were not loaded: %#v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid one-shot plan settings were rejected: %v", err)
	}
}

func TestValidatePlanSettingsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"output requires once", func(cfg *Config) { cfg.PlanOutput = "/tmp/plan.json" }},
		{"approval requires once", func(cfg *Config) { cfg.ApprovedPlanHash = strings.Repeat("a", 64) }},
		{"overwrite requires output", func(cfg *Config) { cfg.Once = true; cfg.PlanOutputOverwrite = true }},
		{"short hash", func(cfg *Config) { cfg.Once = true; cfg.ApprovedPlanHash = "abc" }},
		{"uppercase hash", func(cfg *Config) { cfg.Once = true; cfg.ApprovedPlanHash = strings.Repeat("A", 64) }},
		{"non-hex hash", func(cfg *Config) { cfg.Once = true; cfg.ApprovedPlanHash = strings.Repeat("z", 64) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe plan settings unexpectedly validated")
			}
		})
	}
}

func TestLoadPlanSettingsFromEnvironment(t *testing.T) {
	t.Setenv("ONCE", "true")
	t.Setenv("PLAN_OUTPUT", "/tmp/env-plan.json")
	t.Setenv("PLAN_OUTPUT_OVERWRITE", "true")
	t.Setenv("APPROVED_PLAN_HASH", strings.Repeat("b", 64))
	cfg, err := Load([]string{
		"--fortigate-exclusive-zone-ownership",
		"--fortigate-url=https://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.PlanOutput != "/tmp/env-plan.json" || !cfg.PlanOutputOverwrite || cfg.ApprovedPlanHash != strings.Repeat("b", 64) {
		t.Fatalf("environment plan settings mismatch: %#v", cfg)
	}
}
