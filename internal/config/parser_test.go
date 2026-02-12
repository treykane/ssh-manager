// Package config tests verify the SSH config parser's ability to correctly extract
// host entries, merge wildcard blocks, handle Include directives, and gracefully
// degrade when encountering malformed input.
//
// All tests in this file use isolated temporary directories for config files,
// ensuring they never read from or write to the user's real ~/.ssh/config.
// This makes tests deterministic and safe to run in any environment (local dev,
// CI, containers, etc.).
//
// Test naming convention:
//   - TestParseFile_*: tests that exercise ParseFile() with various config scenarios.
//   - Each test name describes the scenario being tested (e.g., BasicAndWildcard,
//     IncludeAndMalformed).
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseFile_BasicAndWildcard verifies that the parser correctly handles:
//
//  1. Wildcard blocks ("Host *") that provide default values for all hosts.
//  2. Pattern blocks ("Host app-*") that match a subset of hosts with overrides.
//  3. Concrete host blocks ("Host app-1") with specific configuration.
//  4. Directive merging: when multiple blocks match a host, their directives are
//     merged. In this test, "app-1" matches three blocks: "Host *" (User=default),
//     "Host app-*" (User=wildcard), and "Host app-1" (HostName, LocalForward).
//     The last matching User value ("wildcard" from app-*) should win.
//  5. LocalForward parsing: the "LocalForward 8080 localhost:80" directive should
//     produce a ForwardSpec with LocalPort=8080 and RemotePort=80.
//  6. Wildcard-only blocks do NOT produce concrete host entries — only "app-1"
//     should appear in the results, not "*" or "app-*".
func TestParseFile_BasicAndWildcard(t *testing.T) {
	// Create a temporary directory for the test config file.
	// This isolates the test from the user's real SSH config.
	d := t.TempDir()

	// Write a minimal SSH config with three blocks:
	//   - "Host *"     → applies to all hosts (provides default User and Port)
	//   - "Host app-*" → applies to any host starting with "app-" (overrides User)
	//   - "Host app-1" → concrete host with HostName and LocalForward
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

	// Parse the config file.
	res, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that only one concrete host ("app-1") was extracted.
	// Wildcard patterns ("*", "app-*") should NOT produce host entries.
	if len(res.Hosts) != 1 {
		t.Fatalf("expected 1 concrete host, got %d", len(res.Hosts))
	}

	h := res.Hosts[0]

	// Verify the host's identity and merged directives:
	//   - Alias should be "app-1" (the concrete pattern).
	//   - User should be "wildcard" (from "Host app-*", which overrides "Host *").
	//   - HostName should be "10.0.0.10" (from "Host app-1").
	if h.Alias != "app-1" || h.User != "wildcard" || h.HostName != "10.0.0.10" {
		t.Fatalf("unexpected host parse: %+v", h)
	}

	// Verify that the LocalForward directive was correctly parsed into a ForwardSpec.
	// "LocalForward 8080 localhost:80" → LocalPort=8080, RemotePort=80.
	if len(h.Forwards) != 1 || h.Forwards[0].LocalPort != 8080 || h.Forwards[0].RemotePort != 80 {
		t.Fatalf("unexpected localforward parse: %+v", h.Forwards)
	}
}

// TestParseFile_IncludeAndMalformed verifies that the parser correctly handles:
//
//  1. Include directives: an "Include inc.conf" line in the root config should
//     cause the parser to recursively parse inc.conf and merge its host entries.
//  2. Relative Include paths: "Include inc.conf" (without a leading /) is resolved
//     relative to the directory containing the root config file.
//  3. Malformed directives: a line like "BadLine" (no key-value structure) should
//     be captured as a warning rather than causing a parse failure. This ensures
//     the parser degrades gracefully when encountering unsupported or broken lines.
//  4. Host merging across files: hosts from included files ("db") and from the
//     root file ("api") should all appear in the final result.
func TestParseFile_IncludeAndMalformed(t *testing.T) {
	// Create a temporary directory for the test config files.
	d := t.TempDir()

	// Write an included config file with one host ("db").
	// This file will be referenced by the root config via "Include inc.conf".
	inc := filepath.Join(d, "inc.conf")
	if err := os.WriteFile(inc, []byte("Host db\n  HostName 10.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write the root config file with:
	//   - An Include directive pointing to inc.conf (relative path).
	//   - A malformed line ("BadLine") that should generate a warning.
	//   - A concrete host ("api") with a HostName directive.
	root := filepath.Join(d, "config")
	content := "Include inc.conf\nBadLine\nHost api\n  HostName api.internal\n"
	if err := os.WriteFile(root, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Parse the root config file (which includes inc.conf).
	res, err := ParseFile(root)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that both hosts were discovered:
	//   - "db" from the included file (inc.conf)
	//   - "api" from the root file
	if len(res.Hosts) != 2 {
		t.Fatalf("expected 2 hosts from include+root, got %d", len(res.Hosts))
	}

	// Verify that the malformed "BadLine" generated at least one warning.
	// The parser should NOT fail on malformed lines — it should capture them
	// as warnings and continue parsing the rest of the file.
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning for malformed line")
	}
}

func TestParseFile_LocalForwardBracketedIPv6(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "config")
	cfg := `
Host db
  HostName db.internal
  LocalForward [::1]:8080 [2001:db8::1]:5432
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosts) != 1 || len(res.Hosts[0].Forwards) != 1 {
		t.Fatalf("expected one host with one forward, got %+v", res.Hosts)
	}
	fwd := res.Hosts[0].Forwards[0]
	if fwd.LocalAddr != "::1" || fwd.RemoteAddr != "2001:db8::1" {
		t.Fatalf("unexpected forward parse: %+v", fwd)
	}
}

func TestParseFile_LocalForwardRejectsUnbracketedIPv6(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "config")
	cfg := `
Host db
  HostName db.internal
  LocalForward ::1:8080 localhost:5432
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosts) != 1 {
		t.Fatalf("expected one host, got %d", len(res.Hosts))
	}
	if len(res.Hosts[0].Forwards) != 0 {
		t.Fatalf("expected invalid forward to be skipped, got %+v", res.Hosts[0].Forwards)
	}
}
