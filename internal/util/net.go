// Package util provides common utility functions and constants used across the
// ssh-manager application. This package is intentionally kept dependency-free
// (no imports from other internal/* packages) to serve as a shared foundation
// without introducing circular dependencies.
package util

import "strings"

// NormalizeAddr returns the provided address if it is non-empty (after trimming
// whitespace), or the fallback value if the address is empty or whitespace-only.
//
// This function is used throughout the application to fill in default bind
// addresses for SSH tunnel forward specifications. In OpenSSH config, the local
// and remote addresses in a LocalForward directive are optional — when omitted,
// they default to "127.0.0.1" (local side) or "localhost" (remote side).
//
// By centralizing this defaulting logic here, we ensure consistent address
// normalization across:
//   - Tunnel ID generation (internal/tunnel.RuntimeID)
//   - SSH argument composition (internal/sshclient.StartTunnel, BuildTunnelArgs)
//   - Display strings in TunnelRuntime.Local and TunnelRuntime.Remote
//
// Examples:
//
//	NormalizeAddr("",          "127.0.0.1") → "127.0.0.1"  // empty → fallback
//	NormalizeAddr("  ",        "127.0.0.1") → "127.0.0.1"  // whitespace → fallback
//	NormalizeAddr("0.0.0.0",   "127.0.0.1") → "0.0.0.0"   // explicit → kept
//	NormalizeAddr("10.0.0.1",  "localhost")  → "10.0.0.1"  // explicit → kept
func NormalizeAddr(addr, fallback string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fallback
	}
	return addr
}
