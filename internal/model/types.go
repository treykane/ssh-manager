package model

import "time"

// ForwardSpec defines one local->remote SSH tunnel mapping.
type ForwardSpec struct {
	LocalAddr  string `json:"local_addr"`
	LocalPort  int    `json:"local_port"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort int    `json:"remote_port"`
}

func (f ForwardSpec) LocalString() string {
	if f.LocalAddr == "" {
		return "localhost"
	}
	return f.LocalAddr
}

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

func (h HostEntry) DisplayTarget() string {
	if h.HostName != "" {
		return h.HostName
	}
	return h.Alias
}

type TunnelState string

const (
	TunnelDown     TunnelState = "down"
	TunnelStarting TunnelState = "starting"
	TunnelUp       TunnelState = "up"
	TunnelError    TunnelState = "error"
	TunnelStopping TunnelState = "stopping"
)

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
