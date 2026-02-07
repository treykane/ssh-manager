package util

import "fmt"

const (
	MinPort = 1
	MaxPort = 65535
)

// ValidatePort checks if port is in valid range (1-65535).
func ValidatePort(port int) error {
	if port < MinPort || port > MaxPort {
		return fmt.Errorf("port %d out of range (must be %d-%d)", port, MinPort, MaxPort)
	}
	return nil
}
