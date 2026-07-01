package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestConfigUsesExplicitKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	const kubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://example.test:6443
  name: c
contexts:
- context:
    cluster: c
    user: u
  name: ctx
current-context: ctx
users:
- name: u
  user:
    token: t
`
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := restConfig(path)
	if err != nil {
		t.Fatalf("explicit kubeconfig should load: %v", err)
	}
	if cfg.Host != "https://example.test:6443" {
		t.Fatalf("unexpected host %q, want https://example.test:6443", cfg.Host)
	}
}
