package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/sshclient"
)

type Manager struct {
	mu      sync.Mutex
	client  TunnelStarter
	runtime map[string]model.TunnelRuntime
	cancel  map[string]context.CancelFunc
}

type TunnelStarter interface {
	StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error)
}

func NewManager(client TunnelStarter) *Manager {
	return &Manager{client: client, runtime: map[string]model.TunnelRuntime{}, cancel: map[string]context.CancelFunc{}}
}

func RuntimeID(hostAlias string, fwd model.ForwardSpec) string {
	return fmt.Sprintf("%s|%s:%d|%s:%d", hostAlias, defaultAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort, defaultAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort)
}

func (m *Manager) Start(host model.HostEntry, fwd model.ForwardSpec) (model.TunnelRuntime, error) {
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
		Local:     fmt.Sprintf("%s:%d", defaultAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort),
		Remote:    fmt.Sprintf("%s:%d", defaultAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort),
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
		return rt, err
	}

	m.mu.Lock()
	rt.PID = proc.Cmd.Process.Pid
	rt.State = model.TunnelUp
	m.runtime[id] = rt
	m.mu.Unlock()

	go m.watchProcess(id, proc)
	_ = m.persist()
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
	_ = m.persist()
}

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
	if rt.PID > 0 {
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
	_ = m.persist()
	return nil
}

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

func (m *Manager) Snapshot() []model.TunnelRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.TunnelRuntime, 0, len(m.runtime))
	for _, rt := range m.runtime {
		if !rt.StartedAt.IsZero() {
			rt.UptimeSec = int64(time.Since(rt.StartedAt).Seconds())
		}
		if rt.State == model.TunnelUp {
			start := time.Now()
			conn, err := net.DialTimeout("tcp", rt.Local, 500*time.Millisecond)
			if err != nil {
				rt.State = model.TunnelError
				rt.LastError = "local port probe failed"
			} else {
				rt.LatencyMS = time.Since(start).Milliseconds()
				_ = conn.Close()
			}
		}
		out = append(out, rt)
	}
	return out
}

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
	if err := os.MkdirAll(strings.TrimSuffix(path, "/runtime.json"), 0o755); err != nil {
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
	return os.WriteFile(path, b, 0o644)
}

func ParseForwardArg(s string) (model.ForwardSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		lp, err := strconv.Atoi(parts[0])
		if err != nil {
			return model.ForwardSpec{}, err
		}
		rp, err := strconv.Atoi(parts[2])
		if err != nil {
			return model.ForwardSpec{}, err
		}
		return model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: lp, RemoteAddr: parts[1], RemotePort: rp}, nil
	}
	if len(parts) == 4 {
		lp, err := strconv.Atoi(parts[1])
		if err != nil {
			return model.ForwardSpec{}, err
		}
		rp, err := strconv.Atoi(parts[3])
		if err != nil {
			return model.ForwardSpec{}, err
		}
		return model.ForwardSpec{LocalAddr: parts[0], LocalPort: lp, RemoteAddr: parts[2], RemotePort: rp}, nil
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

func defaultAddr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
