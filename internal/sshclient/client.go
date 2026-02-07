// Package sshclient provides SSH client operations including interactive sessions
// and tunnel management via the system ssh binary.
package sshclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/util"
)

// TunnelProcess represents a running SSH tunnel process.
type TunnelProcess struct {
	Cmd    *exec.Cmd
	Stderr io.ReadCloser
}

// Client manages SSH operations.
type Client struct{}

// New creates a new SSH client.
func New() *Client { return &Client{} }

// EnsureSSHBinary checks that the ssh binary is available on PATH.
func EnsureSSHBinary() error {
	_, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found in PATH")
	}
	return nil
}

// ConnectCommand creates an exec.Cmd for an interactive SSH session.
func (c *Client) ConnectCommand(host model.HostEntry) *exec.Cmd {
	args := []string{host.Alias}
	return exec.Command("ssh", args...)
}

// RunInteractive starts an interactive SSH session in a PTY.
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

// StartTunnel starts an SSH tunnel process in the background.
func (c *Client) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*TunnelProcess, error) {
	args := []string{
		"-N",
		"-L",
		fmt.Sprintf("%s:%d:%s:%d", util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort, util.NormalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort),
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

// BuildTunnelArgs constructs SSH arguments for a tunnel.
func (c *Client) BuildTunnelArgs(hostAlias string, fwd model.ForwardSpec) []string {
	return []string{"-N", "-L", fmt.Sprintf("%s:%d:%s:%d", util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort, util.NormalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort), hostAlias}
}
