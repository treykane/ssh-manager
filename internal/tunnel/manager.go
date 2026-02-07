// Package tunnel manages SSH tunnel lifecycle, persistence, and health monitoring.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/util"
)

// Manager coordinates SSH tunnel processes and tracks their runtime state.
type Manager struct {
	mu      sync.Mutex
	client  TunnelStarter
	runtime map[string]model.TunnelRuntime
	cancel  map[string]context.CancelFunc
}

// TunnelStarter abstracts SSH tunnel process creation for testing.
type TunnelStarter interface {
	StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error)
}

// NewManager creates a new tunnel manager.
func NewManager(client TunnelStarter) *Manager {
	return &Manager{
		client:  client,
		runtime: make(map[string]model.TunnelRuntime),
		cancel:  make(map[string]context.CancelFunc),
	}
}

// RuntimeID generates a unique identifier for a tunnel based on host and forward spec.
func RuntimeID(hostAlias string, fwd model.ForwardSpec) string {
	return fmt.Sprintf("%s|%s:%d|%s:%d",
		hostAlias,
		util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"),
		fwd.LocalPort,
		util.NormalizeAddr(fwd.RemoteAddr, "localhost"),
		fwd.RemotePort,
	)
}

// Start initiates a tunnel for the given host and forward spec.
// If a tunnel with the same ID is already running, returns the existing tunnel.
func (m *Manager) Start(host model.HostEntry, fwd model.ForwardSpec) (model.TunnelRuntime, error) {
	// Validate ports
	if err := util.ValidatePort(fwd.LocalPort); err != nil {
		return model.TunnelRuntime{}, fmt.Errorf("invalid local port: %w", err)
	}
	if err := util.ValidatePort(fwd.RemotePort); err != nil {
		return model.TunnelRuntime{}, fmt.Errorf("invalid remote port: %w", err)
	}

	id := RuntimeID(host.Alias, fwd)
	m.mu.Lock()
	if rt, ok := m.runtime[id]; ok && rt.State == model.TunnelUp {
		m.mu.Unlock()
		return rt, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
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

	proc, err := m.client.StartTunnel(ctx, host, fwd)
	if err != nil {
		m.mu.Lock()
		rt.State = model.TunnelError
		rt.LastError = err.Error()
		m.runtime[id] = rt
		delete(m.cancel, id)
		m.mu.Unlock()
		if persistErr := m.persist(); persistErr != nil {
			slog.Warn("failed to persist tunnel state after start error", "error", persistErr)
		}
		return rt, err
	}

	m.mu.Lock()
	rt.PID = proc.Cmd.Process.Pid
	rt.State = model.TunnelUp
	m.runtime[id] = rt
	m.mu.Unlock()

	go m.watchProcess(id, proc)
	if err := m.persist(); err != nil {
		slog.Warn("failed to persist tunnel state after start", "error", err)
	}
	return m.Get(id)
}

func (m *Manager) watchProcess(id string, proc *sshclient.TunnelProcess) {
	err := proc.Cmd.Wait()
	m.mu.Lock()
	rt, ok := m.runtime[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	if rt.State != model.TunnelStopping {
		if err != nil {
			rt.State = model.TunnelError
			rt.LastError = err.Error()
		} else {
			rt.State = model.TunnelDown
		}
		m.runtime[id] = rt
	}
	delete(m.cancel, id)
	m.mu.Unlock()
	if persistErr := m.persist(); persistErr != nil {
		slog.Warn("failed to persist tunnel state after process exit", "error", persistErr)
	}
}

// Stop terminates a tunnel by its ID.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	rt, ok := m.runtime[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found: %s", id)
	}
	rt.State = model.TunnelStopping
	m.runtime[id] = rt
	cancel := m.cancel[id]
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Only send signal if process is actually alive
	if rt.PID > 0 && processAlive(rt.PID) {
		if p, err := os.FindProcess(rt.PID); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}

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
func (m *Manager) StopByHost(hostAlias string) error {
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

// StopAll stops all managed tunnels.
func (m *Manager) StopAll() {
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
func (m *Manager) Get(id string) (model.TunnelRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.runtime[id]
	if !ok {
		return model.TunnelRuntime{}, fmt.Errorf("not found")
	}
	if !rt.StartedAt.IsZero() {
		rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
	}
	return rt, nil
}

// Snapshot returns a read-only snapshot of all tunnels with current uptime and latency.
// This function performs TCP health checks asynchronously to avoid blocking.
func (m *Manager) Snapshot() []model.TunnelRuntime {
	m.mu.Lock()
	out := make([]model.TunnelRuntime, 0, len(m.runtime))
	for _, rt := range m.runtime {
		if !rt.StartedAt.IsZero() {
			rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
		}
		out = append(out, rt)
	}
	m.mu.Unlock()

	// Probe tunnels asynchronously to avoid blocking the caller
	type probeResult struct {
		index     int
		latencyMS int64
		err       error
	}

	results := make(chan probeResult, len(out))
	for i, rt := range out {
		if rt.State != model.TunnelUp {
			continue
		}
		go func(idx int, local string) {
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

	// Collect results with timeout to prevent hanging
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
				// Don't modify state in Snapshot - just log the probe failure
				slog.Debug("tunnel probe failed", "local", out[result.index].Local, "error", result.err)
			} else {
				out[result.index].LatencyMS = result.latencyMS
			}
			collected++
		case <-timeout:
			slog.Warn("tunnel probe timeout", "collected", collected, "expected", expected)
			goto done
		}
	}
done:

	return out
}

// LoadRuntime restores tunnel state from disk.
// Tunnels with dead processes are marked as down.
func (m *Manager) LoadRuntime() error {
	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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
			m.runtime[rt.ID] = rt
		} else {
			rt.State = model.TunnelDown
			rt.PID = 0
			m.runtime[rt.ID] = rt
		}
	}
	return nil
}

func (m *Manager) persist() error {
	path, err := appconfig.RuntimeFilePath()
	if err != nil {
		return err
	}
	// Use filepath.Dir instead of string manipulation
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m.mu.Lock()
	arr := make([]model.TunnelRuntime, 0, len(m.runtime))
	for _, rt := range m.runtime {
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
	// Use 0o600 for better security (only owner can read/write)
	return os.WriteFile(path, b, 0o600)
}

// ParseForwardArg parses a forward specification string.
// Accepts formats: "localPort:remoteHost:remotePort" or "localAddr:localPort:remoteHost:remotePort"
func ParseForwardArg(s string) (model.ForwardSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		lp, err := strconv.Atoi(parts[0])
		if err != nil {
			return model.ForwardSpec{}, fmt.Errorf("invalid local port: %w", err)
		}
		if err := util.ValidatePort(lp); err != nil {
			return model.ForwardSpec{}, err
		}

		rp, err := strconv.Atoi(parts[2])
		if err != nil {
			return model.ForwardSpec{}, fmt.Errorf("invalid remote port: %w", err)
		}
		if err := util.ValidatePort(rp); err != nil {
			return model.ForwardSpec{}, err
		}

		return model.ForwardSpec{
			LocalAddr:  "127.0.0.1",
			LocalPort:  lp,
			RemoteAddr: parts[1],
			RemotePort: rp,
		}, nil
	}

	if len(parts) == 4 {
		lp, err := strconv.Atoi(parts[1])
		if err != nil {
			return model.ForwardSpec{}, fmt.Errorf("invalid local port: %w", err)
		}
		if err := util.ValidatePort(lp); err != nil {
			return model.ForwardSpec{}, err
		}

		rp, err := strconv.Atoi(parts[3])
		if err != nil {
			return model.ForwardSpec{}, fmt.Errorf("invalid remote port: %w", err)
		}
		if err := util.ValidatePort(rp); err != nil {
			return model.ForwardSpec{}, err
		}

		return model.ForwardSpec{
			LocalAddr:  parts[0],
			LocalPort:  lp,
			RemoteAddr: parts[2],
			RemotePort: rp,
		}, nil
	}

	return model.ForwardSpec{}, fmt.Errorf("forward format must be localPort:remoteHost:remotePort or localAddr:localPort:remoteHost:remotePort")
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
