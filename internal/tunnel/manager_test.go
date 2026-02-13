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
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/events"
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

type flakyStarter struct {
	failures int32
	calls    int32
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

func (f *flakyStarter) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error) {
	call := atomic.AddInt32(&f.calls, 1)
	var cmd *exec.Cmd
	if call <= atomic.LoadInt32(&f.failures) {
		cmd = exec.CommandContext(ctx, "sh", "-c", "exit 1")
	} else {
		cmd = exec.CommandContext(ctx, "sleep", "30")
	}
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

func TestParseForwardArgIPv6(t *testing.T) {
	fwd, err := ParseForwardArg("[::1]:8080:[2001:db8::1]:5432")
	if err != nil {
		t.Fatalf("expected bracketed IPv6 parse to succeed: %v", err)
	}
	if fwd.LocalAddr != "::1" || fwd.RemoteAddr != "2001:db8::1" {
		t.Fatalf("unexpected IPv6 parse result: %+v", fwd)
	}

	if _, err := ParseForwardArg("::1:8080:localhost:80"); err == nil {
		t.Fatal("expected unbracketed IPv6 local address to fail")
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

func TestManagerLifecycleEmitsEvents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9450, RemoteAddr: "localhost", RemotePort: 80}

	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Stop(rt.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	recs, err := m.Events(events.Query{HostAlias: "api"})
	if err != nil {
		t.Fatalf("events read: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("expected lifecycle events, got none")
	}
	seenStart := false
	seenStop := false
	for _, evt := range recs {
		if evt.EventType == "start_succeeded" {
			seenStart = true
		}
		if evt.EventType == "stop_succeeded" {
			seenStop = true
		}
	}
	if !seenStart || !seenStop {
		t.Fatalf("expected start/stop events, got %+v", recs)
	}
}

func TestManagerReconcileQuarantinesSuspiciousRuntime(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})

	id := "api|127.0.0.1:9999|localhost:80"
	m.runtime[id] = model.TunnelRuntime{
		ID:        id,
		HostAlias: "api",
		Local:     "127.0.0.1:9999",
		Remote:    "localhost:80",
		State:     model.TunnelUp,
		PID:       0,
	}

	actions, err := m.Reconcile("api", false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	got, err := m.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != model.TunnelQuarantined {
		t.Fatalf("expected quarantined state, got %s", got.State)
	}
}

func TestManagerStart_RejectsPublicBindByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	_, err := m.Start(h, model.ForwardSpec{LocalAddr: "0.0.0.0", LocalPort: 9300, RemoteAddr: "localhost", RemotePort: 80})
	if err == nil {
		t.Fatal("expected public bind to be rejected by default policy")
	}
}

func TestManagerStart_AllowsPublicBindWithOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	m.SetAllowPublicBind(true)
	h := model.HostEntry{Alias: "api"}
	rt, err := m.Start(h, model.ForwardSpec{LocalAddr: "0.0.0.0", LocalPort: 9301, RemoteAddr: "localhost", RemotePort: 80})
	if err != nil {
		t.Fatalf("expected override to allow bind: %v", err)
	}
	defer func() { _ = m.Stop(rt.ID) }()
}

func TestLoadRuntimeQuarantinesMismatchedProcess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := []model.TunnelRuntime{{
		ID:        "prod|127.0.0.1:15432|localhost:5432",
		HostAlias: "prod",
		Local:     "127.0.0.1:15432",
		Remote:    "localhost:5432",
		State:     model.TunnelUp,
		PID:       cmd.Process.Pid,
	}}
	b, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(fakeStarter{})
	if err := m.LoadRuntime(); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(runtime[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.TunnelQuarantined {
		t.Fatalf("expected quarantined state, got %s", got.State)
	}
}

func TestPreflight_Pass(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9401, RemoteAddr: "localhost", RemotePort: 80}

	rep := m.Preflight(h, fwd)
	if !rep.OK {
		t.Fatalf("expected preflight pass, got %+v", rep)
	}
}

func TestPreflight_FailsForPortInUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: port, RemoteAddr: "localhost", RemotePort: 80}
	rep := m.Preflight(h, fwd)
	if rep.OK {
		t.Fatalf("expected port-in-use preflight failure, got %+v", rep)
	}
}

func TestPreflight_FailsForPublicBindWithoutOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "0.0.0.0", LocalPort: 9402, RemoteAddr: "localhost", RemotePort: 80}
	rep := m.Preflight(h, fwd)
	if rep.OK {
		t.Fatalf("expected bind-policy failure, got %+v", rep)
	}
}

func TestManagerAutoRestartUnexpectedExit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSSHConfig(t, home, "api")

	starter := &flakyStarter{failures: 1}
	m := NewManager(starter)
	m.SetRestartPolicy(true, 2, 1, 1)

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9511, RemoteAddr: "localhost", RemotePort: 80}
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer func() { _ = m.Stop(rt.ID) }()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got, gerr := m.Get(rt.ID)
		if gerr == nil && got.State == model.TunnelUp && got.PID > 0 && atomic.LoadInt32(&starter.calls) >= 2 {
			stats := m.RestartStats()[rt.ID]
			if stats.Attempts < 1 || stats.Successes < 1 {
				t.Fatalf("expected restart stats attempts/successes, got %+v", stats)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, _ := m.Get(rt.ID)
	t.Fatalf("expected restarted tunnel to become up; state=%s calls=%d", got.State, starter.calls)
}

func TestManagerAutoRestartQuarantinesAtMaxAttempts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSSHConfig(t, home, "api")

	starter := &flakyStarter{failures: 10}
	m := NewManager(starter)
	m.SetRestartPolicy(true, 2, 1, 1)

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9512, RemoteAddr: "localhost", RemotePort: 80}
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	time.Sleep(3500 * time.Millisecond)
	got, gerr := m.Get(rt.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.State != model.TunnelQuarantined {
		t.Fatalf("expected quarantined state after max attempts, got %s", got.State)
	}
	stats := m.RestartStats()[rt.ID]
	if stats.Failures < 1 || stats.Attempts < 1 {
		t.Fatalf("expected restart failures/attempts stats, got %+v", stats)
	}
	if atomic.LoadInt32(&starter.calls) != 3 {
		t.Fatalf("expected initial + 2 restart attempts, got %d", starter.calls)
	}
}

func TestManagerAutoRestartStableWindowResetsCounter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSSHConfig(t, home, "api")

	starter := &flakyStarter{failures: 0}
	m := NewManager(starter)
	m.SetRestartPolicy(true, 2, 1, 1)

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9513, RemoteAddr: "localhost", RemotePort: 80}
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer func() { _ = m.Stop(rt.ID) }()

	time.Sleep(1500 * time.Millisecond)
	m.mu.Lock()
	// Seed a non-zero counter and confirm stable window reset clears it.
	m.restartAttempts[rt.ID] = 2
	m.mu.Unlock()
	m.scheduleRestartReset(rt.ID, rt.StartedAt)
	time.Sleep(1500 * time.Millisecond)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.restartAttempts[rt.ID] != 0 {
		t.Fatalf("expected restart counter reset, got %d", m.restartAttempts[rt.ID])
	}
}

func writeSSHConfig(t *testing.T, home string, alias string) {
	t.Helper()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "Host " + alias + "\n  HostName 127.0.0.1\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRecoverQuarantinedTunnel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSSHConfig(t, home, "api")

	starter := &flakyStarter{failures: 10}
	m := NewManager(starter)
	m.SetRestartPolicy(true, 1, 1, 1)

	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9514, RemoteAddr: "localhost", RemotePort: 80}
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	time.Sleep(2500 * time.Millisecond)
	q, err := m.Get(rt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if q.State != model.TunnelQuarantined {
		t.Fatalf("expected quarantined state, got %s", q.State)
	}

	atomic.StoreInt32(&starter.failures, 0)
	recovered, err := m.Recover(rt.ID)
	if err != nil {
		t.Fatalf("recover failed: %v", err)
	}
	defer func() { _ = m.Stop(recovered.ID) }()
	if recovered.State != model.TunnelUp {
		t.Fatalf("expected recovered tunnel up, got %s", recovered.State)
	}
}

func TestManagerRecoverByHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSSHConfig(t, home, "api")

	m := NewManager(fakeStarter{})
	fwd1 := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9515, RemoteAddr: "localhost", RemotePort: 80}
	fwd2 := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9516, RemoteAddr: "localhost", RemotePort: 81}
	id1 := RuntimeID("api", fwd1)
	id2 := RuntimeID("api", fwd2)

	m.mu.Lock()
	m.runtime[id1] = model.TunnelRuntime{
		ID:        id1,
		HostAlias: "api",
		Forward:   fwd1,
		Local:     "127.0.0.1:9515",
		Remote:    "localhost:80",
		State:     model.TunnelQuarantined,
	}
	m.runtime[id2] = model.TunnelRuntime{
		ID:        id2,
		HostAlias: "api",
		Forward:   fwd2,
		Local:     "127.0.0.1:9516",
		Remote:    "localhost:81",
		State:     model.TunnelQuarantined,
	}
	m.mu.Unlock()

	recovered, err := m.RecoverByHost("api")
	if err != nil {
		t.Fatalf("recover by host failed: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("expected 2 recovered tunnels, got %d", len(recovered))
	}
	for _, rt := range recovered {
		defer func(id string) { _ = m.Stop(id) }(rt.ID)
		if rt.State != model.TunnelUp {
			t.Fatalf("expected recovered tunnel up, got %s", rt.State)
		}
	}
}
