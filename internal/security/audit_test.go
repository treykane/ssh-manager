package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/treykane/ssh-manager/internal/appconfig"
)

func TestRunLocalAudit_FindsInsecurePolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := appconfig.Default()
	cfg.Security.BindPolicy = appconfig.BindPolicyAllowPublic
	cfg.Security.HostKeyPolicy = appconfig.HostKeyPolicyInsecure
	if err := appconfig.Save(cfg); err != nil {
		t.Fatal(err)
	}

	report, err := RunLocalAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected findings for insecure configuration")
	}
	if !report.HasHigh() {
		t.Fatal("expected high severity finding for insecure host key policy")
	}
}

func TestRedactMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	msg := home + "/.ssh/id_ed25519 permission denied"
	got := RedactMessage(msg)
	if got == msg {
		t.Fatalf("expected message to be redacted")
	}
}

func TestRunLocalAudit_FindsLoosePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(sshDir, "config")
	if err := os.WriteFile(cfgPath, []byte("Host test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunLocalAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected permission findings")
	}
}
