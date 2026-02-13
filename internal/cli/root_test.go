package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treykane/ssh-manager/internal/sshclient"
)

func TestTunnelCheckTextOutput(t *testing.T) {
	if err := sshclient.EnsureSSHBinary(); err != nil {
		t.Skip("ssh binary not available in test environment")
	}
	setupSSHConfigForCLI(t)

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"tunnel", "check", "api", "--forward", "0"})

	got, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "[PASS] api") {
		t.Fatalf("expected pass output, got: %s", got)
	}
}

func TestTunnelCheckJSONOutput(t *testing.T) {
	if err := sshclient.EnsureSSHBinary(); err != nil {
		t.Skip("ssh binary not available in test environment")
	}
	setupSSHConfigForCLI(t)

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"tunnel", "check", "api", "--forward", "0", "--json"})

	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var reports []map[string]any
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("json parse: %v; output=%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("expected one report, got %d", len(reports))
	}
	if reports[0]["host_alias"] != "api" {
		t.Fatalf("unexpected host_alias: %v", reports[0]["host_alias"])
	}
}

func captureStdout(fn func() error) (string, error) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	b, readErr := io.ReadAll(r)
	if readErr != nil {
		return "", readErr
	}
	return string(b), runErr
}

func setupSSHConfigForCLI(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := strings.Join([]string{
		"Host api",
		"  HostName 127.0.0.1",
		"  User test",
		"  Port 22",
		"  LocalForward 127.0.0.1:9501 localhost:80",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}
