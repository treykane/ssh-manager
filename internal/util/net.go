package util

import "strings"

// NormalizeAddr returns fallback if addr is empty or whitespace.
// Used for normalizing local and remote addresses in SSH forwards.
func NormalizeAddr(addr, fallback string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fallback
	}
	return addr
}
