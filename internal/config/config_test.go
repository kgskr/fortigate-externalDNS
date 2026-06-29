package config

import (
	"strings"
	"testing"
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

func TestValidateRejectsNonHTTPFortiGateURL(t *testing.T) {
	cfg, err := Load([]string{
		"--fortigate-url=ftp://fortigate.example.com",
		"--fortigate-zone=example.com",
		"--fortigate-api-token=unit-test-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected non-http(s) URL scheme to be rejected, got %v", err)
	}
}

func TestValidateAcceptsCaseInsensitiveProvider(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Provider = "FortiGate"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("provider should be matched case-insensitively: %v", err)
	}
}

func TestValidateRejectsOwnerIDWithCommentDelimiter(t *testing.T) {
	for _, owner := range []string{"team;a", "team=a"} {
		cfg := baseValidConfig()
		cfg.OwnerID = owner
		if err := cfg.Validate(); err == nil {
			t.Fatalf("owner ID %q with a reserved delimiter must be rejected", owner)
		}
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
		DefaultTTL:       300,
		OwnerID:          DefaultOwnerID,
		CleanupPolicy:    DefaultCleanupPolicy,
		MetricsAddr:      DefaultMetricsAddr,
		LeaderElection:   true,
		LeaderElectionID: DefaultLeaderElectionID,
		Sources:          []string{"service"},
		FortiGate: FortiGateConfig{
			BaseURL:  "https://fortigate.example.com",
			APIToken: "unit-test-credential",
			Zone:     "example.com",
			Timeout:  DefaultTimeout,
			Retries:  2,
		},
	}
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
