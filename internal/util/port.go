// Package util provides common utility functions and constants used across the
// ssh-manager application. This package is intentionally kept dependency-free
// (no imports from other internal/* packages) to serve as a shared foundation
// without introducing circular dependencies.
package util

import "fmt"

const (
	// MinPort is the lowest valid TCP/UDP port number. Port 0 is reserved and
	// cannot be used as an explicit bind target for SSH tunnels (though the OS
	// may assign it dynamically in other contexts).
	MinPort = 1

	// MaxPort is the highest valid TCP/UDP port number. TCP and UDP use 16-bit
	// unsigned integers for port numbers, giving a maximum value of 65535.
	MaxPort = 65535
)

// ValidatePort checks whether the given port number falls within the valid
// TCP/UDP port range (1–65535).
//
// This validation is used before starting SSH tunnels to catch invalid port
// numbers early with a clear error message, rather than letting the SSH binary
// fail with a less descriptive error.
//
// Call sites:
//   - internal/tunnel.Manager.Start: validates both local and remote ports
//     before launching the SSH process.
//   - internal/tunnel.ParseForwardArg: validates ports parsed from CLI
//     --forward arguments (e.g., "8080:localhost:80").
//
// Returns nil if the port is valid, or a descriptive error indicating the
// port value and the allowed range.
func ValidatePort(port int) error {
	if port < MinPort || port > MaxPort {
		return fmt.Errorf("port %d out of range (must be %d-%d)", port, MinPort, MaxPort)
	}
	return nil
}
