package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treykane/ssh-manager/internal/appconfig"
)

func TestRunIncludesDuplicateBindIssue(t *testing.T) {
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
		"  LocalForward 127.0.0.1:9601 localhost:80",
		"Host db",
		"  HostName 127.0.0.1",
		"  LocalForward 127.0.0.1:9601 localhost:5432",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Run()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Check == "duplicate-local-bind" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected duplicate-local-bind issue, got %+v", report.Issues)
	}
}

func TestRunJSONShapeDeterministic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Host api\n  HostName 127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	rawRuntime := `[{"id":"api|127.0.0.1:9602|localhost:80","host_alias":"api","local":"127.0.0.1:9602","remote":"localhost:80","state":"quarantined","pid":0}]`
	if err := os.WriteFile(path, []byte(rawRuntime), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Run()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["issues"]; !ok {
		t.Fatalf("expected issues key in json output: %s", string(b))
	}
}
