package config

import "testing"

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
