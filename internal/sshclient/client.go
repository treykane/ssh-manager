package sshclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
	"github.com/treykane/ssh-manager/internal/model"
)

type TunnelProcess struct {
	Cmd    *exec.Cmd
	Stderr io.ReadCloser
}

type Client struct{}

func New() *Client { return &Client{} }

func EnsureSSHBinary() error {
	_, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found in PATH")
	}
	return nil
}

func (c *Client) ConnectCommand(host model.HostEntry) *exec.Cmd {
	args := []string{host.Alias}
	return exec.Command("ssh", args...)
}

func (c *Client) RunInteractive(ctx context.Context, host model.HostEntry) error {
	cmd := c.ConnectCommand(host)
	f, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer f.Close()

	go func() {
		_, _ = io.Copy(f, os.Stdin)
	}()
	_, _ = io.Copy(os.Stdout, f)
	if ctx.Err() != nil {
		_ = cmd.Process.Kill()
	}
	return cmd.Wait()
}

func (c *Client) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*TunnelProcess, error) {
	args := []string{
		"-N",
		"-L",
		fmt.Sprintf("%s:%d:%s:%d", normalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort, normalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort),
		host.Alias,
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &TunnelProcess{Cmd: cmd, Stderr: stderr}, nil
}

func (c *Client) BuildTunnelArgs(hostAlias string, fwd model.ForwardSpec) []string {
	return []string{"-N", "-L", fmt.Sprintf("%s:%d:%s:%d", normalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort, normalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort), hostAlias}
}

func normalizeAddr(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}
