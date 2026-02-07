package util

import "time"

const (
	// MaxIncludeDepth is the maximum nesting level for SSH config Include directives.
	MaxIncludeDepth = 16

	// TunnelProbeTimeout is the timeout for TCP health checks on tunnel endpoints.
	TunnelProbeTimeout = 500 * time.Millisecond

	// DefaultRefreshSeconds is the default UI refresh interval.
	DefaultRefreshSeconds = 3
)
