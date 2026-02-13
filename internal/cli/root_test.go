package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treykane/ssh-manager/internal/bundle"
	"github.com/treykane/ssh-manager/internal/events"
	"github.com/treykane/ssh-manager/internal/history"
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

func TestTunnelRecoverHostErrorWhenNoQuarantined(t *testing.T) {
	setupSSHConfigForCLI(t)

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"tunnel", "recover", "api"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no quarantined tunnels exist")
	}
	if !strings.Contains(err.Error(), "no quarantined tunnel for host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBundleCreateListDeleteLifecycle(t *testing.T) {
	setupSSHConfigForCLI(t)

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"bundle", "create", "daily", "--host", "api", "--forward", "0"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	cmd = NewRootCommand()
	cmd.SetArgs([]string{"bundle", "list"})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("list bundle: %v", err)
	}
	if !strings.Contains(out, "daily") {
		t.Fatalf("expected bundle in list output, got: %s", out)
	}

	cmd = NewRootCommand()
	cmd.SetArgs([]string{"bundle", "delete", "daily"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("delete bundle: %v", err)
	}
}

func TestBundleRunReturnsSummaryOnPartialFailures(t *testing.T) {
	setupSSHConfigForCLI(t)
	if err := bundle.Create("mixed", []bundle.Entry{
		{HostAlias: "api", ForwardSelector: "0"},
		{HostAlias: "missing"},
	}); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"bundle", "run", "mixed"})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("run bundle: %v", err)
	}
	if !strings.Contains(out, "bundle mixed summary:") {
		t.Fatalf("expected summary output, got: %s", out)
	}
}

func TestDoctorJSONOutput(t *testing.T) {
	setupSSHConfigForCLI(t)
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"doctor", "--json"})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid doctor json: %v", err)
	}
	if _, ok := payload["issues"]; !ok {
		t.Fatalf("expected issues key in doctor output: %s", out)
	}
}

func TestListRecentOrdering(t *testing.T) {
	setupSSHConfigForCLI(t)
	home := os.Getenv("HOME")
	cfg := strings.Join([]string{
		"Host api",
		"  HostName 127.0.0.1",
		"Host db",
		"  HostName 127.0.0.1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := history.Touch("db"); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"list", "--recent"})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("unexpected output: %s", out)
	}
	if !strings.Contains(lines[1], "db") {
		t.Fatalf("expected db first after header, got: %s", lines[1])
	}
}

func TestTunnelEventsJSONOutput(t *testing.T) {
	setupSSHConfigForCLI(t)
	store := events.NewStore()
	if err := store.Append(events.Event{
		Timestamp: time.Now().UTC(),
		TunnelID:  "api|127.0.0.1:9501|localhost:80",
		HostAlias: "api",
		EventType: "start_succeeded",
		Message:   "started",
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"tunnel", "events", "--host", "api", "--json"})
	out, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("events json: %v", err)
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid events json: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("expected 1 event, got %d", len(payload))
	}
	if payload[0]["event_type"] != "start_succeeded" {
		t.Fatalf("unexpected event: %v", payload[0]["event_type"])
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
