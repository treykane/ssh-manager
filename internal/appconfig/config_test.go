package appconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultSecurityValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.BindPolicy != BindPolicyLoopbackOnly {
		t.Fatalf("unexpected bind policy: %s", cfg.Security.BindPolicy)
	}
	if cfg.Security.HostKeyPolicy != HostKeyPolicyStrict {
		t.Fatalf("unexpected host key policy: %s", cfg.Security.HostKeyPolicy)
	}
	if !cfg.Security.RedactErrors {
		t.Fatal("expected redact_errors default true")
	}
}

func TestLoad_NormalizesSecurityPolicies(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "ssh-manager")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("security:\n  bind_policy: invalid\n  host_key_policy: invalid\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.BindPolicy != BindPolicyLoopbackOnly {
		t.Fatalf("expected normalized bind policy, got %s", cfg.Security.BindPolicy)
	}
	if cfg.Security.HostKeyPolicy != HostKeyPolicyStrict {
		t.Fatalf("expected normalized host key policy, got %s", cfg.Security.HostKeyPolicy)
	}
}
