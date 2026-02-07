// Package util provides common utility functions used across the application.
package util

import "strings"

// DefaultString returns fallback if v is empty or whitespace.
func DefaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// EmptyDash returns "-" if s is empty or whitespace, otherwise returns s.
func EmptyDash(s string) string {
	return DefaultString(s, "-")
}
