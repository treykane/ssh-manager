package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFile_BasicAndWildcard(t *testing.T) {
	d := t.TempDir()
	cfg := `
Host *
  User default
  Port 22

Host app-*
  User wildcard

Host app-1
  HostName 10.0.0.10
  LocalForward 8080 localhost:80
`
	path := filepath.Join(d, "config")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosts) != 1 {
		t.Fatalf("expected 1 concrete host, got %d", len(res.Hosts))
	}
	h := res.Hosts[0]
	if h.Alias != "app-1" || h.User != "wildcard" || h.HostName != "10.0.0.10" {
		t.Fatalf("unexpected host parse: %+v", h)
	}
	if len(h.Forwards) != 1 || h.Forwards[0].LocalPort != 8080 || h.Forwards[0].RemotePort != 80 {
		t.Fatalf("unexpected localforward parse: %+v", h.Forwards)
	}
}

func TestParseFile_IncludeAndMalformed(t *testing.T) {
	d := t.TempDir()
	inc := filepath.Join(d, "inc.conf")
	if err := os.WriteFile(inc, []byte("Host db\n  HostName 10.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(d, "config")
	content := "Include inc.conf\nBadLine\nHost api\n  HostName api.internal\n"
	if err := os.WriteFile(root, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ParseFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosts) != 2 {
		t.Fatalf("expected 2 hosts from include+root, got %d", len(res.Hosts))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning for malformed line")
	}
}
