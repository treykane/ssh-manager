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

type fakeStarter struct {
	fail bool
}

func (f fakeStarter) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*sshclient.TunnelProcess, error) {
	if f.fail {
		return nil, exec.ErrNotFound
	}
	cmd := exec.CommandContext(ctx, "sleep", "30")
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

func TestManagerStartStopTransition(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9000, RemoteAddr: "localhost", RemotePort: 80}

	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatal(err)
	}
	if rt.State != model.TunnelUp {
		t.Fatalf("expected up, got %s", rt.State)
	}
	if rt.PID <= 0 {
		t.Fatalf("expected pid > 0, got %d", rt.PID)
	}

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

func TestManagerStartFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{fail: true})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9100, RemoteAddr: "localhost", RemotePort: 80}

	rt, err := m.Start(h, fwd)
	if err == nil {
		t.Fatal("expected start error")
	}
	if rt.State != model.TunnelError {
		t.Fatalf("expected error state, got %s", rt.State)
	}
}

func TestParseForwardArg(t *testing.T) {
	fwd, err := ParseForwardArg("8080:localhost:80")
	if err != nil {
		t.Fatal(err)
	}
	if fwd.LocalPort != 8080 || fwd.RemotePort != 80 {
		t.Fatalf("unexpected parsed forward: %+v", fwd)
	}
	_, err = ParseForwardArg("bad")
	if err == nil {
		t.Fatal("expected error for malformed forward")
	}
}

func TestSnapshotAddsUptime(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(fakeStarter{})
	h := model.HostEntry{Alias: "api"}
	fwd := model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 9200, RemoteAddr: "localhost", RemotePort: 80}
	rt, err := m.Start(h, fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(rt.ID) }()
	time.Sleep(1100 * time.Millisecond)
	sn := m.Snapshot()
	if len(sn) == 0 || sn[0].UptimeSec < 1 {
		t.Fatalf("expected uptime to be populated, got %+v", sn)
	}
}
