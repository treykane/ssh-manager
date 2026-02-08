// Package sshclient provides SSH client operations including interactive sessions
// and tunnel management via the system ssh binary.
//
// This package is responsible for launching SSH processes — it does NOT implement
// the SSH protocol itself. Instead, it shells out to the system's "ssh" binary,
// which means it automatically inherits the user's full SSH configuration (keys,
// agents, ProxyJump chains, etc.) without reimplementing any of that logic.
//
// There are two primary operations:
//
//   - Interactive sessions: RunInteractive() allocates a PTY and connects the
//     user's terminal to a live SSH session. This is used when the user presses
//     Enter on a host in the TUI dashboard.
//
//   - Tunnel processes: StartTunnel() launches a background SSH process with
//     the -N (no remote command) and -L (local forwarding) flags. The returned
//     TunnelProcess contains the exec.Cmd so the caller (internal/tunnel) can
//     monitor process lifecycle, send signals, and wait for exit.
//
// Security note: all SSH arguments are passed via exec.Command's argv (not via
// shell interpolation), which prevents injection attacks from host aliases or
// forward specs that contain shell metacharacters.
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
//
// The caller (typically internal/tunnel.Manager) owns the lifecycle of the process:
//   - It calls Cmd.Wait() (usually in a goroutine) to detect when the process exits.
//   - It may read from Stderr to capture SSH error output (e.g. "Connection refused").
//   - It sends signals via Cmd.Process to request graceful or forceful termination.
//
// Fields:
//   - Cmd:    the underlying exec.Cmd for the SSH process. The caller can access
//     Cmd.Process.Pid for the OS-level process ID and Cmd.ProcessState after Wait().
//   - Stderr: a pipe to the SSH process's stderr stream. SSH writes connection
//     errors, warnings, and debug output here. The caller should drain this pipe
//     to prevent the process from blocking on a full pipe buffer.
type TunnelProcess struct {
	Cmd    *exec.Cmd
	Stderr io.ReadCloser
}

// Client manages SSH operations by creating and launching SSH processes.
//
// Client is stateless and safe for concurrent use — each method call creates
// an independent exec.Cmd. Multiple interactive sessions or tunnel processes
// can be running simultaneously.
//
// The zero value is not useful; use New() to create a Client instance.
type Client struct{}

// New creates a new SSH client.
//
// The returned client is lightweight (no resources are allocated until methods
// are called) and can be reused for the lifetime of the application.
func New() *Client { return &Client{} }

// EnsureSSHBinary checks that the "ssh" binary is available on the system PATH.
//
// This should be called early during startup (before any connect or tunnel
// operations) to provide a clear error message if SSH is not installed, rather
// than failing later with a confusing exec error.
//
// Returns nil if "ssh" is found, or an error describing the problem.
func EnsureSSHBinary() error {
	_, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found in PATH")
	}
	return nil
}

// ConnectCommand creates an exec.Cmd for an interactive SSH session to the
// given host.
//
// The command uses the host's Alias (not HostName) as the SSH destination,
// which allows OpenSSH to resolve all config directives (HostName, User, Port,
// IdentityFile, ProxyJump, etc.) from the user's ~/.ssh/config. This means
// ssh-manager doesn't need to pass those options explicitly — OpenSSH handles it.
//
// The returned Cmd has no stdin/stdout/stderr configured — the caller is
// responsible for connecting them (see RunInteractive for PTY-based usage,
// or the TUI's tea.ExecProcess for Bubble Tea integration).
//
// This method does NOT start the process; the caller must call Cmd.Start()
// or Cmd.Run().
func (c *Client) ConnectCommand(host model.HostEntry) *exec.Cmd {
	args := []string{host.Alias}
	return exec.Command("ssh", args...)
}

// RunInteractive starts an interactive SSH session in a pseudo-terminal (PTY).
//
// This method:
//  1. Creates an exec.Cmd for the SSH connection via ConnectCommand().
//  2. Allocates a PTY and starts the SSH process attached to it.
//  3. Pipes the user's stdin to the PTY (so keystrokes reach the remote shell).
//  4. Pipes the PTY output to the user's stdout (so remote output is displayed).
//  5. Waits for the SSH process to exit.
//
// The PTY is necessary for interactive SSH sessions because SSH expects a
// terminal for features like password prompts, remote shell line editing,
// and terminal resizing.
//
// The ctx parameter can be used to cancel the session. If the context is
// cancelled while the session is active, the SSH process is killed.
//
// Returns nil on clean exit, or an error if the SSH process fails.
//
// Note: This method blocks until the SSH session ends. In the TUI, it is
// typically invoked via tea.ExecProcess, which suspends the Bubble Tea
// program and gives the SSH process full control of the terminal.
func (c *Client) RunInteractive(ctx context.Context, host model.HostEntry) error {
	cmd := c.ConnectCommand(host)

	// Start the SSH process inside a PTY. The pty.Start function allocates
	// a new pseudo-terminal, sets it as the process's controlling terminal,
	// and returns the master side file descriptor.
	f, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer f.Close()

	// Forward user input (os.Stdin) into the PTY master. This runs in a
	// goroutine because io.Copy blocks until EOF (i.e., until the PTY closes).
	// The goroutine will naturally terminate when the PTY file descriptor is
	// closed after the SSH process exits.
	go func() {
		_, _ = io.Copy(f, os.Stdin)
	}()

	// Forward PTY output to the user's terminal (os.Stdout). This blocks
	// until the SSH process exits and the PTY master returns EOF.
	_, _ = io.Copy(os.Stdout, f)

	// If the context was cancelled (e.g., user quit the TUI), ensure the
	// SSH process is killed rather than left orphaned.
	if ctx.Err() != nil {
		_ = cmd.Process.Kill()
	}

	// Wait for the process to fully exit and collect its exit status.
	return cmd.Wait()
}

// StartTunnel starts an SSH tunnel process in the background.
//
// The tunnel is created by invoking the system SSH binary with:
//
//	ssh -N -L <localAddr>:<localPort>:<remoteAddr>:<remotePort> <hostAlias>
//
// Flags:
//   - -N: Do not execute a remote command. This is appropriate for tunnels
//     where we only want port forwarding, not a shell session.
//   - -L: Set up local port forwarding. Connections to the local endpoint
//     are forwarded through the SSH connection to the remote endpoint.
//
// The process runs in the background (no PTY, no stdin). The caller is
// responsible for:
//   - Calling Cmd.Wait() (typically in a goroutine) to detect process exit.
//   - Sending SIGTERM to Cmd.Process when the tunnel should be stopped.
//   - Draining the Stderr pipe to prevent the process from blocking.
//
// The ctx parameter is used with exec.CommandContext, which will automatically
// kill the process if the context is cancelled. This is the primary mechanism
// used by tunnel.Manager to stop tunnels.
//
// Returns a TunnelProcess containing the running Cmd and a Stderr pipe, or
// an error if the process could not be started (e.g., SSH binary not found,
// port already in use at the OS level, etc.).
func (c *Client) StartTunnel(ctx context.Context, host model.HostEntry, fwd model.ForwardSpec) (*TunnelProcess, error) {
	// Build the -L argument: localAddr:localPort:remoteAddr:remotePort
	// NormalizeAddr fills in default addresses ("127.0.0.1" for local,
	// "localhost" for remote) when the ForwardSpec has empty address fields.
	args := []string{
		"-N",
		"-L",
		fmt.Sprintf("%s:%d:%s:%d",
			util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"),
			fwd.LocalPort,
			util.NormalizeAddr(fwd.RemoteAddr, "localhost"),
			fwd.RemotePort,
		),
		host.Alias,
	}

	// Use CommandContext so that cancelling the context automatically sends
	// a kill signal to the SSH process. This ties the tunnel's lifetime to
	// the context provided by tunnel.Manager.
	cmd := exec.CommandContext(ctx, "ssh", args...)

	// Capture stderr so the caller can read SSH error messages (e.g.,
	// "Permission denied", "Connection refused", "bind: Address already in use").
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	// Discard stdout — SSH tunnel mode (-N) produces no stdout output.
	cmd.Stdout = io.Discard

	// Explicitly set stdin to nil — the tunnel process should not read from
	// stdin, as there is no interactive session.
	cmd.Stdin = nil

	// Start the SSH process. After this point, the caller is responsible for
	// eventually calling Cmd.Wait() to reap the zombie process.
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &TunnelProcess{Cmd: cmd, Stderr: stderr}, nil
}

// BuildTunnelArgs constructs the SSH command-line arguments for a tunnel without
// actually starting a process. This is useful for:
//   - Displaying the command that would be run (debugging / dry-run)
//   - Unit testing argument composition independently from process execution
//
// The returned slice is suitable for passing to exec.Command("ssh", args...).
//
// Example output: ["-N", "-L", "127.0.0.1:8080:localhost:80", "prod-db"]
func (c *Client) BuildTunnelArgs(hostAlias string, fwd model.ForwardSpec) []string {
	return []string{
		"-N",
		"-L",
		fmt.Sprintf("%s:%d:%s:%d",
			util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"),
			fwd.LocalPort,
			util.NormalizeAddr(fwd.RemoteAddr, "localhost"),
			fwd.RemotePort,
		),
		hostAlias,
	}
}
