// Package util provides common utility functions and constants used across the
// ssh-manager application. This package is intentionally kept dependency-free
// (no imports from other internal/* packages) to serve as a shared foundation
// without introducing circular dependencies.
package util

import "strings"

// DefaultString returns the fallback value if v is empty or consists entirely
// of whitespace; otherwise it returns v unchanged.
//
// This is a general-purpose "coalesce" helper used when a value might be missing
// or blank and a sensible default should be substituted. It is the foundation
// for EmptyDash and similar display-formatting functions.
//
// Examples:
//
//	DefaultString("hello", "world")  → "hello"   // non-empty → kept
//	DefaultString("",      "world")  → "world"   // empty → fallback
//	DefaultString("  ",    "world")  → "world"   // whitespace-only → fallback
//	DefaultString("  hi",  "world")  → "  hi"    // leading space but non-blank → kept
func DefaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// EmptyDash returns "-" if s is empty or consists entirely of whitespace;
// otherwise it returns s unchanged.
//
// This is a convenience wrapper around DefaultString, used throughout the CLI
// and TUI to display a visible placeholder when an optional field (such as User
// or ProxyJump) has no value. Showing "-" instead of a blank space makes table
// output easier to read and avoids ambiguity about whether a field was omitted
// versus set to an empty string.
//
// Call sites:
//   - internal/cli/root.go (newListCmd): formats the USER column in the host list table.
//   - internal/ui/ui.go (View): formats the User and ProxyJump fields in the
//     host detail panel.
//
// Examples:
//
//	EmptyDash("deploy")  → "deploy"   // non-empty → kept
//	EmptyDash("")        → "-"        // empty → dash placeholder
//	EmptyDash("   ")     → "-"        // whitespace → dash placeholder
func EmptyDash(s string) string {
	return DefaultString(s, "-")
}
