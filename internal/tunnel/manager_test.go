// Package tunnel tests verify the tunnel manager's ability to start, stop, and
// monitor SSH tunnel processes, as well as parse forward specification strings
// from CLI arguments.
//
// These tests use a fakeStarter implementation of the TunnelStarter interface
// to simulate SSH tunnel processes without actually launching SSH. The fake
// uses "sleep 30" as a stand-in process that can be started, monitored, and
// killed like a real SSH tunnel — but without requiring network connectivity
// or SSH configuration.
//
// All tests in this file isolate their configuration and runtime state by
// setting XDG_CONFIG_HOME to a temporary directory via t.Setenv(). This
// prevents tests from reading or writing the user's real config files at
// ~/.config/ssh-manager/ and ensures deterministic, reproducible behavior
// across environments (local dev, CI, containers, etc.).
//
// Test naming convention:
//   - TestManager*: tests that exercise the Manager's lifecycle methods
//     (Start, Stop, Get, Snapshot).
//   - TestParseForwardArg: tests the CLI forward spec parsing utility.
package tunnel

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/sshclient"
)

// fakeStarter is a test double that implements the TunnelStarter interface.
// Instead of launching a real SSH tunnel process, it starts a "sleep 30"
// process that behaves like a long-running background process:
//   - It can be started and will run until killed or until context cancellation.
//   - It has a real OS PID that can be inspected and signaled.
//   - It produces a valid exec.Cmd that supports Wait() for process exit detection.
//
// The 'fail' field controls whether StartTunnel returns an error, allowing tests
// to simulate SSH process launch failures (e.g., binary not found, port in use).
type fakeStarter struct {
	fail bool
}

// StartTunnel implements TunnelStarter. If fail is true, it returns
// exec.ErrNotFound to simulate a missing SSH binary. Otherwise, it starts a
// "sleep 30" process and returns a TunnelProcess wrapping it.
//
// The process is started with the provided context, so cancelling the context
// will terminate the sleep process — mirroring how real tunnel processes are
// stopped via context cancellation in production code.
func (f fakeStarter) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error) {
	if f.fail {
		return nil, exec.ErrNotFound
	}

	// Use "sleep 30" as a stand-in for an SSH tunnel process. It's a simple,
	// universally available command that runs long enough for tests to inspect
	// state before it exits naturally.
	cmd := exec.CommandContext(ctx, "sleep", "30")

	// Capture stderr to satisfy the TunnelProcess struct contract (the real
	// SSH client also captures stderr for error messages).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &sshclient.TunnelProcess{Cmd: cmd, Stderr: stderr}, nil
}

// TestManagerStartStopTransition verifies the complete start→up→stop→down
// lifecycle of a tunnel.
//
// This test exercises the most common user workflow:
//  1. Start a tunnel for a host with a specific forward spec.
//  2. Verify the tunnel transitions to the "up" state with a valid PID.
//  3. Stop the tunnel by its ID.
//  4. Verify the tunnel transitions to the "down" state.
//
// This also implicitly tests:
//   - RuntimeID generation (the ID is used to stop and retrieve the tunnel).
//   - State persistence (Start and Stop both call persist() internally).
//   - The watchProcess goroutine (it must not interfere with the explicit Stop).
func TestManagerStartStopTransition(t *testing.T) {
	// Isolate config/runtime paths to a temp directory so we don't touch
	// the user's real ~/.config/ssh-manager/ directory.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	m := NewManager(fakeStarter{})

	// Define a minimal host and forward spec for the test. These don't need
	// to correspond to a real SSH config — the fakeStarter ignores them and
	// just starts "sleep 30".
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9000, RemoteAddr: "localhost", RemotePort: 80}

	// Start the tunnel and verify it transitions to the "up" state.
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatal(err)
	}
	if rt.State != model.TunnelUp {
		t.Fatalf("expected up, got %s", rt.State)
	}
	// A valid PID confirms that the fake process was actually started.
	if rt.PID <= 0 {
		t.Fatalf("expected pid > 0, got %d", rt.PID)
	}

	// Stop the tunnel and verify it transitions to the "down" state.
	if err := m.Stop(rt.ID); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(rt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.TunnelDown {
		t.Fatalf("expected down, got %s", got.State)
	}
}

// TestManagerStartFailure verifies that the Manager correctly handles a tunnel
// process that fails to start.
//
// This tests the error path in Manager.Start(), where the TunnelStarter returns
// an error (simulated by fakeStarter{fail: true}). The expected behavior is:
//   - Start() returns both the error AND a TunnelRuntime record.
//   - The TunnelRuntime's State is set to TunnelError (not TunnelDown or TunnelUp).
//   - The error is recorded in TunnelRuntime.LastError (not tested here but
//     verified by inspecting the returned error).
//
// This scenario covers real-world failures like:
//   - SSH binary not found on PATH.
//   - The local port is already in use by another process.
//   - Permission denied when binding to a privileged port.
func TestManagerStartFailure(t *testing.T) {
	// Isolate config/runtime paths to a temp directory.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Create a manager with a fakeStarter that always fails.
	m := NewManager(fakeStarter{fail: true})

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9100, RemoteAddr: "localhost", RemotePort: 80}

	// Attempt to start the tunnel — expect an error.
	rt, err := m.Start(h, fwd)
	if err == nil {
		t.Fatal("expected start error")
	}

	// Even though Start() returned an error, the TunnelRuntime should be
	// populated with the error state so it can be displayed in the UI.
	if rt.State != model.TunnelError {
		t.Fatalf("expected error state, got %s", rt.State)
	}
}

// TestParseForwardArg verifies the parsing of forward specification strings
// used in the "tunnel up --forward" CLI argument.
//
// This tests two scenarios:
//
//  1. Valid three-part format ("8080:localhost:80"):
//     Should parse into a ForwardSpec with LocalPort=8080, RemoteAddr="localhost",
//     RemotePort=80, and a default LocalAddr of "127.0.0.1".
//
//  2. Malformed input ("bad"):
//     Should return an error because the string cannot be split into the expected
//     three or four colon-separated parts.
//
// Note: The four-part format ("localAddr:localPort:remoteHost:remotePort") and
// port validation edge cases are not covered here — they could be added as
// additional test cases if needed.
func TestParseForwardArg(t *testing.T) {
	// Test the valid three-part format: localPort:remoteHost:remotePort
	fwd, err := ParseForwardArg("8080:localhost:80")
	if err != nil {
		t.Fatal(err)
	}
	if fwd.LocalPort != 8080 || fwd.RemotePort != 80 {
		t.Fatalf("unexpected parsed forward: %+v", fwd)
	}

	// Test that malformed input is rejected with a clear error.
	_, err = ParseForwardArg("bad")
	if err == nil {
		t.Fatal("expected error for malformed forward")
	}
}

// TestSnapshotAddsUptime verifies that the Snapshot() method correctly computes
// and populates the UptimeSec field for active tunnels.
//
// This test:
//  1. Starts a tunnel and waits slightly over 1 second.
//  2. Calls Snapshot() to get the tunnel status.
//  3. Verifies that UptimeSec is at least 1, confirming that the uptime
//     calculation (time.Since(StartedAt)) is working correctly.
//
// The 1100ms sleep ensures we cross the 1-second boundary even with minor
// timing jitter. This is a pragmatic tradeoff between test speed and reliability.
//
// Note: This test also implicitly exercises the health-check probe logic in
// Snapshot(). Since the tunnel's local endpoint isn't actually listening (it's
// a "sleep 30" process, not a real port forwarder), the probe will fail — but
// Snapshot() should handle that gracefully (logging a debug message) and still
// return the tunnel with LatencyMS=0.
func TestSnapshotAddsUptime(t *testing.T) {
	// Isolate config/runtime paths to a temp directory.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	m := NewManager(fakeStarter{})

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9200, RemoteAddr: "localhost", RemotePort: 80}

	// Start a tunnel so we have an active entry to snapshot.
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatal(err)
	}

	// Ensure the tunnel is cleaned up after the test completes, regardless
	// of pass/fail. This prevents leaked "sleep 30" processes.
	defer func() { _ = m.Stop(rt.ID) }()

	// Wait just over 1 second so that UptimeSec will be at least 1.
	time.Sleep(1100 * time.Millisecond)

	// Take a snapshot and verify uptime was computed.
	sn := m.Snapshot()
	if len(sn) == 0 || sn[0].UptimeSec < 1 {
		t.Fatalf("expected uptime to be populated, got %+v", sn)
	}
}
