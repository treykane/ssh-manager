// Package tunnel manages SSH tunnel lifecycle, persistence, and health monitoring.
//
// This package is the "supervisor" layer that sits between the SSH process launcher
// (internal/sshclient) and the user-facing layers (internal/ui and internal/cli).
// It is responsible for:
//
//   - Starting tunnels: validating forward specs, launching SSH processes via the
//     TunnelStarter interface, and tracking the resulting process state.
//
//   - Stopping tunnels: sending SIGTERM to tunnel processes, updating state, and
//     cleaning up resources (context cancellation, PID tracking).
//
//   - Monitoring tunnels: a background goroutine per tunnel watches for process
//     exit and updates the tunnel's state accordingly (error vs. clean exit).
//
//   - Health checking: Snapshot() performs asynchronous TCP probes against each
//     active tunnel's local endpoint to measure latency and detect dead tunnels.
//
//   - Persistence: tunnel state is serialized to runtime.json after every state
//     change, and restored on startup via LoadRuntime(). This allows the UI to
//     show tunnel status across app restarts. Orphaned processes (where the PID
//     is no longer alive) are automatically marked as down during restoration.
//
// Concurrency model:
//
//	All tunnel state is protected by a sync.Mutex. The Manager is safe for
//	concurrent use from multiple goroutines (e.g., the TUI refresh ticker
//	calling Snapshot() while the CLI calls Start() or Stop()).
//
// The Manager does NOT implement a persistent daemon — tunnels are tied to the
// app's lifecycle. When the app exits, StopAll() should be called to clean up
// all running tunnel processes.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/security"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/util"
)

// Manager coordinates SSH tunnel processes and tracks their runtime state.
//
// It maintains an in-memory map of all known tunnels (active, errored, and stopped)
// keyed by a deterministic tunnel ID. Each active tunnel has an associated
// context.CancelFunc that can be used to signal the SSH process to terminate.
//
// Usage:
//
//	client := sshclient.New()
//	mgr := tunnel.NewManager(client)
//	mgr.LoadRuntime()  // restore state from disk
//
//	rt, err := mgr.Start(host, fwd)   // start a tunnel
//	mgr.Stop(rt.ID)                    // stop it
//	snapshot := mgr.Snapshot()          // get all tunnel states with health info
//	mgr.StopAll()                      // cleanup on shutdown
type Manager struct {
	// mu protects all mutable state (runtime and cancel maps).
	// Any read or write to these maps must hold this lock.
	mu sync.Mutex

	// client is the abstraction used to launch SSH tunnel processes.
	// In production this is *sshclient.Client; in tests it's a fake.
	client TunnelStarter

	// runtime maps tunnel IDs to their current runtime state.
	// This map is the source of truth for tunnel status and is persisted
	// to disk after every state change.
	runtime map[string]model.TunnelRuntime

	// cancel maps tunnel IDs to their context cancel functions.
	// Calling cancel() for a tunnel ID will cause the exec.CommandContext
	// to kill the SSH process. Entries are removed when the tunnel stops
	// or the process exits naturally.
	cancel map[string]context.CancelFunc

	// bindPolicy determines whether local forwards may bind non-loopback addresses.
	bindPolicy string

	// allowPublicBind permits one-off non-loopback binds (CLI override).
	allowPublicBind bool

	// redactErrors controls whether stored/displayed errors should hide home paths.
	redactErrors bool
}

// TunnelStarter abstracts SSH tunnel process creation for testing.
//
// In production, *sshclient.Client implements this interface. In tests, a fake
// implementation (e.g., fakeStarter) can be used to simulate process behavior
// without actually launching SSH processes.
//
// The context parameter allows the Manager to signal cancellation — when the
// context is cancelled, the tunnel process should terminate.
type TunnelStarter interface {
	StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error)
}

// NewManager creates a new tunnel manager with the given SSH process launcher.
//
// The returned Manager has empty state; call LoadRuntime() after creation to
// restore any previously persisted tunnel state from disk.
func NewManager(client TunnelStarter) *Manager {
	return &Manager{
		client:       client,
		runtime:      make(map[string]model.TunnelRuntime),
		cancel:       make(map[string]context.CancelFunc),
		bindPolicy:   appconfig.BindPolicyLoopbackOnly,
		redactErrors: true,
	}
}

func (m *Manager) SetBindPolicy(policy string) {
	m.bindPolicy = appconfig.NormalizeBindPolicy(policy)
}

func (m *Manager) SetAllowPublicBind(allow bool) {
	m.allowPublicBind = allow
}

func (m *Manager) SetRedactErrors(redact bool) {
	m.redactErrors = redact
}

// RuntimeID generates a unique, deterministic identifier for a tunnel based on
// the host alias and the full local/remote endpoint specification.
//
// Format: "alias|localAddr:localPort|remoteAddr:remotePort"
// Example: "prod-db|127.0.0.1:5432|localhost:5432"
//
// Empty addresses are normalized to defaults (127.0.0.1 for local, localhost
// for remote) so that the same tunnel always produces the same ID regardless
// of whether the address was explicitly specified in the SSH config.
func RuntimeID(hostAlias string, fwd model.ForwardSpec) string {
	return fmt.Sprintf("%s|%s:%d|%s:%d",
		hostAlias,
		util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"),
		fwd.LocalPort,
		util.NormalizeAddr(fwd.RemoteAddr, "localhost"),
		fwd.RemotePort,
	)
}

// Start initiates a tunnel for the given host and forward specification.
//
// Behavior:
//   - If a tunnel with the same ID is already running (state == TunnelUp), the
//     existing tunnel's runtime info is returned without starting a duplicate.
//   - Port numbers are validated before attempting to start.
//   - A background goroutine (watchProcess) is spawned to monitor the SSH process
//     and update state when it exits.
//   - Tunnel state is persisted to disk after start (or after a start failure).
//
// Returns the TunnelRuntime record for the tunnel (which may be in "error" state
// if the SSH process failed to start) and any error from the start attempt.
func (m *Manager) Start(host model.HostEntry, fwd model.ForwardSpec) (model.TunnelRuntime, error) {
	defer func() {
		// One-off override is consumed by a single start attempt.
		m.allowPublicBind = false
	}()

	// Validate that both local and remote ports are in the valid TCP range (1-65535).
	if err := util.ValidatePort(fwd.LocalPort); err != nil {
		return model.TunnelRuntime{}, fmt.Errorf("invalid local port: %w", err)
	}
	if err := util.ValidatePort(fwd.RemotePort); err != nil {
		return model.TunnelRuntime{}, fmt.Errorf("invalid remote port: %w", err)
	}
	if err := validateForwardSpec(fwd); err != nil {
		return model.TunnelRuntime{}, security.NewClassifiedError("invalid forward specification", err.Error())
	}
	if m.bindPolicy == appconfig.BindPolicyLoopbackOnly && !m.allowPublicBind && isPublicBindAddr(fwd.LocalAddr) {
		return model.TunnelRuntime{}, security.NewClassifiedError(
			"public bind rejected by security policy",
			fmt.Sprintf("local bind address %q requires allow-public override", fwd.LocalAddr),
		)
	}

	id := RuntimeID(host.Alias, fwd)

	// Check for an already-running tunnel with the same ID. This prevents
	// duplicate SSH processes for the same host+forward combination.
	m.mu.Lock()
	if rt, ok := m.runtime[id]; ok && rt.State == model.TunnelUp {
		m.mu.Unlock()
		return rt, nil
	}

	// Create a cancellable context for this tunnel. Calling cancel() will
	// cause exec.CommandContext to send SIGKILL to the SSH process.
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize the runtime record in "starting" state. The local/remote
	// strings are pre-formatted for display and health-check use.
	rt := model.TunnelRuntime{
		ID:        id,
		HostAlias: host.Alias,
		Forward:   fwd,
		Local:     fmt.Sprintf("%s:%d", util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort),
		Remote:    fmt.Sprintf("%s:%d", util.NormalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort),
		State:     model.TunnelStarting,
		StartedAt: time.Now(),
	}
	m.runtime[id] = rt
	m.cancel[id] = cancel
	m.mu.Unlock()

	// Attempt to launch the SSH tunnel process. This calls the system SSH
	// binary with -N -L flags (see sshclient.StartTunnel for details).
	proc, err := m.client.StartTunnel(ctx, host, fwd)
	if err != nil {
		// Start failed — update the tunnel state to "error" and record the
		// error message so it can be displayed in the UI or CLI output.
		m.mu.Lock()
		rt.State = model.TunnelError
		rt.LastError = security.UserMessage(err, m.redactErrors)
		m.runtime[id] = rt
		delete(m.cancel, id) // No process to cancel; clean up the cancel func.
		m.mu.Unlock()

		if persistErr := m.persist(); persistErr != nil {
			slog.Warn("failed to persist tunnel state after start error", "error", persistErr)
		}
		return rt, security.NewClassifiedError("failed to start tunnel", security.DebugMessage(err))
	}

	// Process started successfully — record the PID and transition to "up" state.
	m.mu.Lock()
	rt.PID = proc.Cmd.Process.Pid
	rt.State = model.TunnelUp
	m.runtime[id] = rt
	m.mu.Unlock()

	// Spawn a goroutine to wait for the SSH process to exit. This goroutine
	// will update the tunnel state to "down" or "error" when the process
	// terminates, and persist the updated state to disk.
	go m.watchProcess(id, proc)

	if err := m.persist(); err != nil {
		slog.Warn("failed to persist tunnel state after start", "error", err)
	}
	return m.Get(id)
}

// watchProcess blocks until the SSH tunnel process exits, then updates the
// tunnel's runtime state accordingly.
//
// This method runs in a dedicated goroutine for each active tunnel. It
// distinguishes between three exit scenarios:
//
//  1. State is TunnelStopping: the user explicitly stopped the tunnel via
//     Stop(), so we leave the state as-is (Stop() sets it to TunnelDown).
//
//  2. Process exited with an error: unexpected failure — set state to TunnelError
//     and record the error message for display.
//
//  3. Process exited cleanly (err == nil): the SSH connection closed normally
//     (e.g., remote server closed the connection) — set state to TunnelDown.
func (m *Manager) watchProcess(id string, proc *sshclient.TunnelProcess) {
	// Cmd.Wait() blocks until the process exits and returns its exit status.
	err := proc.Cmd.Wait()

	m.mu.Lock()
	rt, ok := m.runtime[id]
	if !ok {
		// The tunnel was removed from the map (shouldn't happen in normal
		// operation, but guard against it to avoid panics).
		m.mu.Unlock()
		return
	}

	// If the tunnel is in "stopping" state, it means Stop() was called and
	// already handled the state transition. Don't overwrite it.
	if rt.State != model.TunnelStopping {
		if err != nil {
			rt.State = model.TunnelError
			rt.LastError = security.UserMessage(err, m.redactErrors)
		} else {
			rt.State = model.TunnelDown
		}
		m.runtime[id] = rt
	}

	// Remove the cancel function — the process has already exited, so
	// there's nothing to cancel.
	delete(m.cancel, id)
	m.mu.Unlock()

	if persistErr := m.persist(); persistErr != nil {
		slog.Warn("failed to persist tunnel state after process exit", "error", persistErr)
	}
}

// Stop terminates a tunnel by its ID.
//
// The shutdown sequence:
//  1. Set the tunnel state to TunnelStopping (prevents watchProcess from
//     treating the exit as an unexpected failure).
//  2. Cancel the tunnel's context (causes exec.CommandContext to SIGKILL the process).
//  3. Send SIGTERM to the process (for a more graceful shutdown signal).
//  4. Set the state to TunnelDown and clear the PID.
//  5. Persist the updated state to disk.
//
// Returns an error if the tunnel ID is not found in the runtime map.
// Does NOT return an error if the process is already dead (idempotent stop).
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	rt, ok := m.runtime[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found: %s", id)
	}

	// Mark as stopping BEFORE sending signals. This tells the watchProcess
	// goroutine that this is an intentional shutdown, not an unexpected crash.
	rt.State = model.TunnelStopping
	m.runtime[id] = rt
	cancel := m.cancel[id]
	m.mu.Unlock()

	// Cancel the context first. For exec.CommandContext, this sends SIGKILL
	// to the process group.
	if cancel != nil {
		cancel()
	}

	// Also send SIGTERM as a courtesy signal. Some SSH processes may have
	// cleanup to do (e.g., closing control sockets). We check if the process
	// is still alive first to avoid "no such process" errors.
	if rt.PID > 0 && processAlive(rt.PID) {
		if p, err := os.FindProcess(rt.PID); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}

	// Finalize the state: mark as down and clear the PID.
	m.mu.Lock()
	rt.State = model.TunnelDown
	rt.PID = 0
	m.runtime[id] = rt
	delete(m.cancel, id)
	m.mu.Unlock()

	if err := m.persist(); err != nil {
		slog.Warn("failed to persist tunnel state after stop", "error", err)
	}
	return nil
}

// StopByHost stops all active tunnels for a given host alias.
//
// This is a convenience method used by the CLI's "tunnel down <host>" command,
// which allows users to stop all tunnels for a host without specifying each
// tunnel ID individually.
//
// A tunnel is considered "active" if its state is TunnelUp, TunnelStarting, or
// TunnelError (error tunnels may still have lingering processes).
//
// Returns an error if no active tunnels are found for the given host alias.
// Individual stop errors are silently ignored (best-effort cleanup).
func (m *Manager) StopByHost(hostAlias string) error {
	// Collect tunnel IDs under the lock, then stop them outside the lock
	// to avoid holding the lock during potentially slow signal operations.
	m.mu.Lock()
	ids := make([]string, 0)
	for id, rt := range m.runtime {
		if rt.HostAlias == hostAlias && (rt.State == model.TunnelUp || rt.State == model.TunnelStarting || rt.State == model.TunnelError) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	if len(ids) == 0 {
		return fmt.Errorf("no active tunnel for host %s", hostAlias)
	}

	for _, id := range ids {
		_ = m.Stop(id)
	}
	return nil
}

// StopAll stops all managed tunnels regardless of host or state.
//
// This is called during application shutdown (e.g., when the user presses 'q'
// in the TUI) to ensure no orphaned SSH processes are left behind.
//
// Individual stop errors are silently ignored (best-effort cleanup).
func (m *Manager) StopAll() {
	// Snapshot the IDs under the lock, then stop them outside the lock.
	m.mu.Lock()
	ids := make([]string, 0, len(m.runtime))
	for id := range m.runtime {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		_ = m.Stop(id)
	}
}

// Get retrieves a tunnel's current runtime state by ID.
//
// The returned TunnelRuntime has its UptimeSec field computed dynamically
// from StartedAt, so it always reflects the current elapsed time.
//
// Returns an error if the tunnel ID is not found.
func (m *Manager) Get(id string) (model.TunnelRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.runtime[id]
	if !ok {
		return model.TunnelRuntime{}, fmt.Errorf("not found")
	}

	// Compute uptime dynamically so callers always get a fresh value.
	if !rt.StartedAt.IsZero() {
		rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
	}
	return rt, nil
}

// Snapshot returns a read-only snapshot of all tunnels with current uptime and
// latency measurements.
//
// This is the primary method used by the TUI dashboard to populate the tunnel
// status table. It performs TCP health checks on all "up" tunnels asynchronously
// to measure round-trip latency without blocking the UI.
//
// Health check behavior:
//   - For each tunnel in TunnelUp state, a goroutine attempts a TCP connection
//     to the tunnel's local endpoint (e.g., "127.0.0.1:8080").
//   - If the connection succeeds, LatencyMS is set to the round-trip time.
//   - If the connection fails, a debug log is emitted but the tunnel state is
//     NOT modified (Snapshot is read-only; actual state changes happen in
//     watchProcess).
//   - All probes must complete within TunnelProbeTimeout + 100ms, after which
//     any remaining probes are abandoned.
//
// The returned slice is a copy — modifying it does not affect the Manager's
// internal state.
func (m *Manager) Snapshot() []model.TunnelRuntime {
	// Take a snapshot of current state under the lock.
	m.mu.Lock()
	out := make([]model.TunnelRuntime, 0, len(m.runtime))
	for _, rt := range m.runtime {
		// Compute uptime dynamically for each tunnel.
		if !rt.StartedAt.IsZero() {
			rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
		}
		out = append(out, rt)
	}
	m.mu.Unlock()

	// Probe active tunnels asynchronously to measure local endpoint latency.
	// Each probe runs in its own goroutine and reports back via a channel.
	type probeResult struct {
		index     int   // index into the `out` slice
		latencyMS int64 // measured round-trip time in milliseconds
		err       error // non-nil if the TCP connection failed
	}

	results := make(chan probeResult, len(out))

	// Launch a probe goroutine for each tunnel that is currently "up".
	for i, rt := range out {
		if rt.State != model.TunnelUp {
			continue
		}
		go func(idx int, local string) {
			// Attempt a TCP connection to the tunnel's local endpoint.
			// This verifies that the local port is actually listening and
			// measures the connection latency.
			start := time.Now()
			conn, err := net.DialTimeout("tcp", local, util.TunnelProbeTimeout)
			if err != nil {
				results <- probeResult{index: idx, err: err}
				return
			}
			_ = conn.Close()
			results <- probeResult{index: idx, latencyMS: time.Since(start).Milliseconds()}
		}(i, rt.Local)
	}

	// Collect probe results with a timeout to prevent the Snapshot call from
	// hanging indefinitely if a probe gets stuck.
	timeout := time.After(util.TunnelProbeTimeout + 100*time.Millisecond)
	collected := 0
	expected := 0
	for i := range out {
		if out[i].State == model.TunnelUp {
			expected++
		}
	}

	for collected < expected {
		select {
		case result := <-results:
			if result.err != nil {
				// Log the probe failure but don't modify tunnel state —
				// Snapshot is a read-only operation. The watchProcess goroutine
				// is responsible for detecting actual process failures.
				slog.Debug("tunnel probe failed", "local", out[result.index].Local, "error", result.err)
			} else {
				out[result.index].LatencyMS = result.latencyMS
			}
			collected++
		case <-timeout:
			// Some probes didn't complete in time. Log a warning and return
			// what we have — missing latency values will remain at 0.
			slog.Warn("tunnel probe timeout", "collected", collected, "expected", expected)
			goto done
		}
	}
done:

	return out
}

// LoadRuntime restores tunnel state from the runtime.json file on disk.
//
// This method should be called once after creating a new Manager, before any
// Start/Stop operations. It allows the UI to display tunnel status from a
// previous session.
//
// For each tunnel record loaded from disk:
//   - If the recorded PID is still alive (checked via a signal-0 probe), the
//     tunnel is kept in its saved state. Note: the Manager does NOT adopt the
//     process (no watchProcess goroutine is spawned), so these tunnels cannot
//     be stopped via the Manager. This is a known limitation of v1.
//   - If the PID is dead or zero, the tunnel is marked as TunnelDown with PID 0.
//
// If runtime.json does not exist, this method returns nil (no error) since it
// simply means no previous state exists.
func (m *Manager) LoadRuntime() error {
	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		return err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No runtime file yet — this is normal on first run.
			return nil
		}
		return err
	}

	var arr []model.TunnelRuntime
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rt := range arr {
		if rt.PID > 0 && processAlive(rt.PID) {
			cmdline, cmdErr := processCommand(rt.PID)
			if cmdErr == nil && isManagedTunnelProcess(cmdline, rt) {
				// The process from a previous session is still running and appears
				// to be one of our managed tunnel commands.
				m.runtime[rt.ID] = rt
				continue
			}

			// Process is alive but cannot be confidently attributed to this tunnel.
			rt.State = model.TunnelQuarantined
			rt.LastError = "recovered runtime entry was quarantined (process mismatch)"
			rt.PID = 0
			m.runtime[rt.ID] = rt
		} else {
			// The process is dead — mark the tunnel as down so the UI
			// doesn't show stale "up" entries.
			rt.State = model.TunnelDown
			rt.PID = 0
			m.runtime[rt.ID] = rt
		}
	}
	return nil
}

// persist serializes the current tunnel state to runtime.json.
//
// This method is called after every state change (start, stop, process exit)
// to ensure that tunnel state survives app restarts. The file is written
// atomically (via os.WriteFile) with 0600 permissions (owner-only read/write)
// since it may contain process IDs and host aliases.
//
// Errors are logged by the caller but not propagated — persistence failures
// should not prevent tunnel operations from succeeding.
func (m *Manager) persist() error {
	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		return err
	}

	// Ensure the config directory exists (handles first-run scenario).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Snapshot the state under the lock, then write outside the lock
	// to minimize lock hold time.
	m.mu.Lock()
	arr := make([]model.TunnelRuntime, 0, len(m.runtime))
	for _, rt := range m.runtime {
		// Compute uptime at persist time so the saved value is meaningful.
		if !rt.StartedAt.IsZero() {
			rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
		}
		arr = append(arr, rt)
	}
	m.mu.Unlock()

	b, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}

	// Write with 0600 permissions — only the file owner can read/write.
	// This is slightly more secure than 0644 since the file contains PIDs
	// and host alias information.
	return os.WriteFile(path, b, 0o600)
}

// ParseForwardArg parses a forward specification string provided as a CLI argument.
//
// This function supports two formats for specifying port forwarding rules on the
// command line:
//
//	Three-part format: "localPort:remoteHost:remotePort"
//	  - The local address defaults to "127.0.0.1"
//	  - Example: "8080:db.internal:5432"
//
//	Four-part format: "localAddr:localPort:remoteHost:remotePort"
//	  - Explicit local bind address
//	  - Example: "0.0.0.0:8080:db.internal:5432"
//
// All port numbers are validated to be in the 1-65535 range.
//
// Returns the parsed ForwardSpec or an error describing what's wrong with the input.
//
// This is used by the "tunnel up" CLI command when the user provides an explicit
// --forward argument instead of using a forward from the SSH config.
func ParseForwardArg(s string) (model.ForwardSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return model.ForwardSpec{}, fmt.Errorf("forward cannot be empty")
	}

	// Parse from right to left so remote host may be bracketed IPv6.
	remoteAddr, remotePort, rest, err := parseAddrPortTail(s)
	if err != nil {
		return model.ForwardSpec{}, err
	}

	// The remaining left side is either "localPort" or "localAddr:localPort".
	var localAddr string
	localPort, err := strconv.Atoi(rest)
	if err != nil {
		localAddr, localPort, err = splitAddrPort(rest)
		if err != nil {
			return model.ForwardSpec{}, fmt.Errorf("invalid local endpoint: %w", err)
		}
	}
	if err := util.ValidatePort(localPort); err != nil {
		return model.ForwardSpec{}, fmt.Errorf("invalid local port: %w", err)
	}
	if err := util.ValidatePort(remotePort); err != nil {
		return model.ForwardSpec{}, fmt.Errorf("invalid remote port: %w", err)
	}

	fwd := model.ForwardSpec{
		LocalAddr:  util.NormalizeAddr(localAddr, "127.0.0.1"),
		LocalPort:  localPort,
		RemoteAddr: util.NormalizeAddr(remoteAddr, "localhost"),
		RemotePort: remotePort,
	}
	if err := validateForwardSpec(fwd); err != nil {
		return model.ForwardSpec{}, err
	}
	return fwd, nil
}

func parseAddrPortTail(s string) (addr string, port int, rest string, err error) {
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, "", fmt.Errorf("forward format must include remote host and remote port")
	}
	port, err = strconv.Atoi(s[idx+1:])
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid remote port: %w", err)
	}
	left := s[:idx]

	if strings.HasSuffix(left, "]") {
		// localPart:[ipv6]
		start := strings.LastIndex(left, "[")
		if start == -1 || start == 0 || left[start-1] != ':' {
			return "", 0, "", fmt.Errorf("invalid bracketed IPv6 remote host")
		}
		addr = left[start+1 : len(left)-1]
		rest = left[:start-1]
		return addr, port, rest, nil
	}

	hostSep := strings.LastIndex(left, ":")
	if hostSep <= 0 || hostSep == len(left)-1 {
		return "", 0, "", fmt.Errorf("forward format must be localPort:remoteHost:remotePort or localAddr:localPort:remoteHost:remotePort")
	}
	addr = left[hostSep+1:]
	rest = left[:hostSep]
	return addr, port, rest, nil
}

func splitAddrPort(s string) (string, int, error) {
	if strings.HasPrefix(s, "[") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return "", 0, err
		}
		port, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, err
		}
		return h, port, nil
	}
	if strings.Count(s, ":") > 1 {
		return "", 0, fmt.Errorf("IPv6 local bind addresses must be bracketed, e.g. [::1]:8080")
	}
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, fmt.Errorf("expected host:port")
	}
	port, err := strconv.Atoi(s[idx+1:])
	if err != nil {
		return "", 0, err
	}
	return s[:idx], port, nil
}

func validateForwardSpec(fwd model.ForwardSpec) error {
	local := util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1")
	remote := util.NormalizeAddr(fwd.RemoteAddr, "localhost")
	if err := validateEndpointHost(local); err != nil {
		return fmt.Errorf("invalid local address %q: %w", local, err)
	}
	if err := validateEndpointHost(remote); err != nil {
		return fmt.Errorf("invalid remote address %q: %w", remote, err)
	}
	return nil
}

func validateEndpointHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("host cannot be empty")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("host cannot contain whitespace")
	}
	unwrapped := strings.Trim(host, "[]")
	if ip := net.ParseIP(unwrapped); ip != nil {
		return nil
	}
	if strings.EqualFold(unwrapped, "localhost") {
		return nil
	}
	if strings.ContainsAny(unwrapped, "/\\") {
		return fmt.Errorf("host cannot contain path separators")
	}
	return nil
}

func isPublicBindAddr(addr string) bool {
	v := strings.Trim(strings.TrimSpace(addr), "[]")
	if v == "" {
		return false
	}
	if v == "*" {
		return true
	}
	ip := net.ParseIP(v)
	return ip != nil && ip.IsUnspecified()
}

func processCommand(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	cmd := strings.TrimSpace(string(out))
	if cmd == "" {
		return "", fmt.Errorf("empty command line")
	}
	return cmd, nil
}

func isManagedTunnelProcess(cmdline string, rt model.TunnelRuntime) bool {
	cmdline = strings.TrimSpace(cmdline)
	if !strings.Contains(cmdline, "ssh") || !strings.Contains(cmdline, "-N") || !strings.Contains(cmdline, "-L") {
		return false
	}
	if !strings.Contains(cmdline, rt.HostAlias) {
		return false
	}
	if !strings.Contains(cmdline, rt.Local) || !strings.Contains(cmdline, rt.Remote) {
		return false
	}
	return true
}

// processAlive checks whether a process with the given PID is still running.
//
// It uses the Unix convention of sending signal 0 to the process: if the signal
// can be delivered (err == nil), the process exists and we have permission to
// signal it. If the signal fails, the process is either dead or owned by another
// user.
//
// This is used during:
//   - LoadRuntime: to detect orphaned tunnel processes from previous sessions.
//   - Stop: to avoid sending SIGTERM to a process that has already exited
//     (which would produce a "no such process" error).
//
// Returns false for PID <= 0 (invalid) or if the signal probe fails.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 doesn't actually send a signal — it just checks whether the
	// process exists and we have permission to signal it.
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
