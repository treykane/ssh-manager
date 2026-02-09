package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treykane/ssh-manager/internal/model"
)

func TestFormatHostBlock_Basic(t *testing.T) {
	entry := model.HostEntry{
		Alias:    "prod-db",
		HostName: "db.example.com",
		User:     "deploy",
		Port:     5432,
	}
	got := FormatHostBlock(entry)
	want := "Host prod-db\n  HostName db.example.com\n  User deploy\n  Port 5432\n"
	if got != want {
		t.Fatalf("block mismatch\nwant=%q\n got=%q", want, got)
	}
}

func TestFormatHostBlock_DefaultPort(t *testing.T) {
	entry := model.HostEntry{
		Alias:    "web",
		HostName: "web.example.com",
		Port:     22,
	}
	got := FormatHostBlock(entry)
	// Port 22 is default and should be omitted.
	if strings.Contains(got, "Port") {
		t.Fatalf("expected default port to be omitted, got: %q", got)
	}
}

func TestFormatHostBlock_SameHostName(t *testing.T) {
	entry := model.HostEntry{
		Alias:    "myhost",
		HostName: "myhost",
		Port:     22,
	}
	got := FormatHostBlock(entry)
	// HostName same as Alias should be omitted.
	if strings.Contains(got, "HostName") {
		t.Fatalf("expected same hostname to be omitted, got: %q", got)
	}
}

func TestFormatHostBlock_AllFields(t *testing.T) {
	entry := model.HostEntry{
		Alias:        "full",
		HostName:     "full.example.com",
		User:         "admin",
		Port:         2222,
		IdentityFile: "~/.ssh/id_ed25519",
		ProxyJump:    "bastion",
		Forwards: []model.ForwardSpec{
			{LocalAddr: "127.0.0.1", LocalPort: 8080, RemoteAddr: "localhost", RemotePort: 80},
		},
	}
	got := FormatHostBlock(entry)

	checks := []string{
		"Host full\n",
		"  HostName full.example.com\n",
		"  User admin\n",
		"  Port 2222\n",
		"  IdentityFile ~/.ssh/id_ed25519\n",
		"  ProxyJump bastion\n",
		"  LocalForward 127.0.0.1:8080 localhost:80\n",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Errorf("expected block to contain %q, got:\n%s", check, got)
		}
	}
}

func TestAppendHostEntry(t *testing.T) {
	// Create a temporary directory with a .ssh/config file.
	tmpDir := t.TempDir()
	sshDir := filepath.Join(tmpDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(sshDir, "config")
	initial := "Host existing\n  HostName existing.example.com\n"
	if err := os.WriteFile(configPath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	// Override HOME so AppendHostEntry writes to our temp dir.
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	entry := model.HostEntry{
		Alias:    "new-host",
		HostName: "new.example.com",
		User:     "deploy",
		Port:     22,
	}

	if err := AppendHostEntry(entry); err != nil {
		t.Fatalf("AppendHostEntry failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	got := string(content)

	// Should contain original content.
	if !strings.Contains(got, "Host existing") {
		t.Error("original content was lost")
	}
	// Should contain new host block.
	if !strings.Contains(got, "Host new-host") {
		t.Error("new host block not found")
	}
	if !strings.Contains(got, "HostName new.example.com") {
		t.Error("new hostname not found")
	}
	if !strings.Contains(got, "User deploy") {
		t.Error("new user not found")
	}
}

func TestValidateAlias_Empty(t *testing.T) {
	err := ValidateAlias("")
	if err == nil {
		t.Fatal("expected error for empty alias")
	}
}

func TestValidateAlias_Wildcards(t *testing.T) {
	for _, alias := range []string{"host *", "host?", "!host", "host\ttab"} {
		err := ValidateAlias(alias)
		if err == nil {
			t.Errorf("expected error for alias %q", alias)
		}
	}
}

func TestValidateAlias_Valid(t *testing.T) {
	// Use a temp dir with no SSH config so there are no conflicts.
	tmpDir := t.TempDir()
	sshDir := filepath.Join(tmpDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	err := ValidateAlias("my-new-server")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
