// Package model defines shared data types used across the application.
package model

import "time"

// ForwardSpec defines one local->remote SSH tunnel mapping.
type ForwardSpec struct {
	LocalAddr  string `json:"local_addr"`
	LocalPort  int    `json:"local_port"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort int    `json:"remote_port"`
}

// LocalString returns the local address with default "localhost".
func (f ForwardSpec) LocalString() string {
	if f.LocalAddr == "" {
		return "localhost"
	}
	return f.LocalAddr
}

// RemoteString returns the remote address with default "localhost".
func (f ForwardSpec) RemoteString() string {
	if f.RemoteAddr == "" {
		return "localhost"
	}
	return f.RemoteAddr
}

// HostEntry is a normalized host configuration extracted from ssh config.
type HostEntry struct {
	Alias        string        `json:"alias"`
	HostName     string        `json:"host_name"`
	User         string        `json:"user,omitempty"`
	Port         int           `json:"port,omitempty"`
	IdentityFile string        `json:"identity_file,omitempty"`
	ProxyJump    string        `json:"proxy_jump,omitempty"`
	Forwards     []ForwardSpec `json:"forwards,omitempty"`
}

// DisplayTarget returns the hostname for display, falling back to alias.
func (h HostEntry) DisplayTarget() string {
	if h.HostName != "" {
		return h.HostName
	}
	return h.Alias
}

// TunnelState represents the lifecycle state of an SSH tunnel.
type TunnelState string

const (
	TunnelDown     TunnelState = "down"
	TunnelStarting TunnelState = "starting"
	TunnelUp       TunnelState = "up"
	TunnelError    TunnelState = "error"
	TunnelStopping TunnelState = "stopping"
)

// TunnelRuntime tracks the runtime state of an active or historical tunnel.
type TunnelRuntime struct {
	ID        string      `json:"id"`
	HostAlias string      `json:"host_alias"`
	Local     string      `json:"local"`
	Remote    string      `json:"remote"`
	Forward   ForwardSpec `json:"-"`
	PID       int         `json:"pid,omitempty"`
	State     TunnelState `json:"state"`
	StartedAt time.Time   `json:"-"`
	UptimeSec int64       `json:"uptime_seconds"`
	LatencyMS int64       `json:"latency_ms"`
	LastError string      `json:"last_error,omitempty"`
	StatusMsg string      `json:"-"`
}
