// Package model defines shared data types used across the ssh-manager application.
//
// These types serve as the common contracts between packages:
//   - internal/config (parser) produces []HostEntry
//   - internal/sshclient (SSH process launcher) consumes HostEntry and ForwardSpec
//   - internal/tunnel (tunnel supervisor) produces/consumes TunnelRuntime
//   - internal/ui (TUI dashboard) reads all of the above for display
//   - internal/cli (CLI commands) reads all of the above for output
//
// Keeping these types in a dedicated package prevents circular dependencies
// between the packages that produce and consume them.
package model

import "time"

// ForwardSpec defines a single local-to-remote SSH port forwarding rule.
// It corresponds to an OpenSSH "LocalForward" directive, which binds a local
// address:port and tunnels traffic to a remote address:port through the SSH
// connection.
//
// Example SSH config line:
//
//	LocalForward 127.0.0.1:8080 db.internal:5432
//
// This would be represented as:
//
//	ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 8080, RemoteAddr: "db.internal", RemotePort: 5432}
type ForwardSpec struct {
	// LocalAddr is the local bind address for the tunnel (e.g. "127.0.0.1").
	// If empty, defaults to "127.0.0.1" when used in SSH arguments.
	LocalAddr string `json:"local_addr"`

	// LocalPort is the local TCP port to listen on (1-65535).
	LocalPort int `json:"local_port"`

	// RemoteAddr is the remote destination hostname or IP that traffic is
	// forwarded to through the SSH connection (e.g. "localhost", "db.internal").
	// If empty, defaults to "localhost" when used in SSH arguments.
	RemoteAddr string `json:"remote_addr"`

	// RemotePort is the remote TCP port to forward traffic to (1-65535).
	RemotePort int `json:"remote_port"`
}

// LocalString returns a human-readable local address string for display purposes.
// Returns "localhost" as a fallback if LocalAddr is empty.
func (f ForwardSpec) LocalString() string {
	if f.LocalAddr == "" {
		return "localhost"
	}
	return f.LocalAddr
}

// RemoteString returns a human-readable remote address string for display purposes.
// Returns "localhost" as a fallback if RemoteAddr is empty.
func (f ForwardSpec) RemoteString() string {
	if f.RemoteAddr == "" {
		return "localhost"
	}
	return f.RemoteAddr
}

// HostEntry is a normalized host configuration extracted from an OpenSSH config file.
// Each HostEntry represents a single concrete host alias (wildcards like "Host *"
// are not included as entries but are merged into matching concrete hosts during
// parsing).
//
// The parser in internal/config produces these by reading ~/.ssh/config (and any
// included files), then resolving wildcard blocks and merging directives according
// to OpenSSH's first-match-wins semantics.
type HostEntry struct {
	// Alias is the SSH host alias as declared in the "Host" directive (e.g. "prod-db").
	// This is the name users type when running "ssh <alias>".
	Alias string `json:"alias"`

	// HostName is the actual hostname or IP address to connect to (from the
	// "HostName" directive). Falls back to Alias if not explicitly set.
	HostName string `json:"host_name"`

	// User is the SSH username (from the "User" directive).
	// Empty string means the system default (typically the current OS user) is used.
	User string `json:"user,omitempty"`

	// Port is the SSH port number (from the "Port" directive).
	// Defaults to 22 if not specified in the config.
	Port int `json:"port,omitempty"`

	// IdentityFile is the path to the SSH private key file (from the "IdentityFile"
	// directive). Tilde (~) prefixes are expanded to the user's home directory
	// during parsing.
	IdentityFile string `json:"identity_file,omitempty"`

	// ProxyJump is the jump host specification (from the "ProxyJump" directive),
	// used for multi-hop SSH connections (e.g. "bastion.example.com").
	ProxyJump string `json:"proxy_jump,omitempty"`

	// Forwards contains all LocalForward rules parsed from the SSH config for
	// this host. Each entry represents one local-to-remote port forwarding tunnel
	// that can be started independently.
	Forwards []ForwardSpec `json:"forwards,omitempty"`

	// IsAdHoc indicates this host was created via the TUI's new connection
	// configurator for the current session only (not read from ~/.ssh/config).
	// Ad-hoc hosts require explicit SSH args rather than alias-based resolution.
	IsAdHoc bool `json:"is_adhoc,omitempty"`
}

// DisplayTarget returns the hostname for display in the UI and CLI output.
// Prefers the explicit HostName if set; otherwise falls back to the Alias.
// This is useful because HostName may be an IP address or FQDN that provides
// more context than the short alias.
func (h HostEntry) DisplayTarget() string {
	if h.HostName != "" {
		return h.HostName
	}
	return h.Alias
}

// TunnelState represents the lifecycle state of a managed SSH tunnel.
// Tunnels progress through these states as they are started, monitored,
// and stopped.
//
// State transitions:
//
//	(new) --> TunnelStarting --> TunnelUp --> TunnelStopping --> TunnelDown
//	                        \-> TunnelError (on start failure)
//	              TunnelUp --\-> TunnelError (on unexpected process exit)
//	              TunnelUp --\-> TunnelDown  (on clean process exit)
type TunnelState string

const (
	// TunnelDown indicates the tunnel process is not running. This is the
	// terminal state after a clean shutdown or after loading stale runtime
	// state from disk where the process is no longer alive.
	TunnelDown TunnelState = "down"

	// TunnelStarting indicates the tunnel process is being launched but
	// has not yet been confirmed as running. This is a brief transitional
	// state between calling Start() and receiving the process PID.
	TunnelStarting TunnelState = "starting"

	// TunnelUp indicates the tunnel process is running and the local port
	// is expected to be forwarding traffic. Health checks (TCP probes) are
	// performed in Snapshot() to verify liveness.
	TunnelUp TunnelState = "up"

	// TunnelError indicates the tunnel process failed to start or exited
	// unexpectedly. The LastError field on TunnelRuntime contains details.
	TunnelError TunnelState = "error"

	// TunnelStopping indicates a stop has been requested but the process
	// has not yet fully terminated. This prevents the watch goroutine from
	// misinterpreting the exit as an unexpected failure.
	TunnelStopping TunnelState = "stopping"

	// TunnelQuarantined indicates a runtime entry was restored from disk but
	// could not be confidently matched to a managed ssh process.
	TunnelQuarantined TunnelState = "quarantined"
)

// TunnelRuntime tracks the runtime state of an active or historical SSH tunnel.
// These records are persisted to runtime.json so that tunnel state survives
// app restarts (though orphaned processes are detected and marked as down on reload).
//
// JSON field names are stable and form the public contract for "tunnel status --json"
// output. The following fields MUST remain stable:
//
//	id, host_alias, local, remote, state, pid, uptime_seconds, latency_ms, last_error
type TunnelRuntime struct {
	// ID is a unique identifier for this tunnel, composed from the host alias
	// and the full local/remote endpoint specification. Generated by RuntimeID().
	// Format: "alias|localAddr:localPort|remoteAddr:remotePort"
	ID string `json:"id"`

	// HostAlias is the SSH host alias this tunnel belongs to (matches HostEntry.Alias).
	HostAlias string `json:"host_alias"`

	// Local is the formatted local endpoint string (e.g. "127.0.0.1:8080").
	// Used for display and for TCP health-check probes.
	Local string `json:"local"`

	// Remote is the formatted remote endpoint string (e.g. "localhost:80").
	// Used for display purposes.
	Remote string `json:"remote"`

	// Forward holds the parsed ForwardSpec for this tunnel. Not serialized to JSON
	// because the local/remote strings already capture this information for output.
	Forward ForwardSpec `json:"-"`

	// PID is the operating system process ID of the SSH tunnel process.
	// Set to 0 when the tunnel is not running.
	PID int `json:"pid,omitempty"`

	// State is the current lifecycle state of this tunnel (down/starting/up/error/stopping).
	State TunnelState `json:"state"`

	// StartedAt records when the tunnel was started. Not serialized to JSON;
	// instead, UptimeSec is computed from this value at read time.
	StartedAt time.Time `json:"-"`

	// UptimeSec is the number of seconds since the tunnel was started.
	// Computed dynamically in Get() and Snapshot() from StartedAt.
	UptimeSec int64 `json:"uptime_seconds"`

	// LatencyMS is the round-trip latency in milliseconds for a TCP probe
	// to the local tunnel endpoint. Populated by Snapshot() during health checks.
	// A value of 0 means no probe has been performed or the tunnel is not up.
	LatencyMS int64 `json:"latency_ms"`

	// LastError contains the error message from the most recent failure,
	// such as a failed start or unexpected process exit. Empty when no error
	// has occurred.
	LastError string `json:"last_error,omitempty"`

	// StatusMsg is a transient human-readable status message for UI display.
	// Not persisted to JSON.
	StatusMsg string `json:"-"`
}
