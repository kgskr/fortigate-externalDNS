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
