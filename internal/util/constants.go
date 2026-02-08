// Package util provides common utility functions and constants used across the
// ssh-manager application. This package is intentionally kept dependency-free
// (no imports from other internal/* packages) to serve as a shared foundation
// without introducing circular dependencies.
package util

import "time"

const (
	// MaxIncludeDepth is the maximum nesting level for SSH config Include directives.
	// This limit prevents infinite recursion when config files form an include cycle
	// that escapes the cycle-detection logic (e.g., via symlinks that resolve to
	// different absolute paths). The value of 16 is generous enough for any
	// reasonable config hierarchy while still providing a safety bound.
	// Used by: internal/config/parser.go (parseRecursive).
	MaxIncludeDepth = 16

	// TunnelProbeTimeout is the maximum time allowed for a single TCP health-check
	// probe against a tunnel's local endpoint. If the connection is not established
	// within this duration, the probe is considered failed.
	//
	// This timeout is used in two places within internal/tunnel/manager.go:
	//   - As the dial timeout for net.DialTimeout in Snapshot().
	//   - As the base for the overall probe collection timeout (TunnelProbeTimeout + 100ms).
	//
	// The 500ms value balances responsiveness (the UI shouldn't freeze) with
	// reliability (local TCP connections should complete well under 500ms unless
	// the tunnel is genuinely unhealthy).
	TunnelProbeTimeout = 500 * time.Millisecond

	// DefaultRefreshSeconds is the fallback interval (in seconds) for the TUI
	// dashboard's periodic tunnel status refresh. This value is used when:
	//   - The user's config.yaml has an invalid or missing refresh_seconds value.
	//   - The application config has not been loaded yet.
	//
	// A 3-second interval provides near-real-time tunnel status updates without
	// generating excessive CPU load from health-check probes.
	// Used by: internal/ui/ui.go (tickCmd, clampRefresh) and
	//          internal/appconfig/config.go (Default, Load).
	DefaultRefreshSeconds = 3
)
