// Package main is the entry point for the ssh-manager binary.
//
// ssh-manager is a terminal application that combines a TUI dashboard (built with
// Bubble Tea) and a CLI (built with Cobra) for managing SSH connections and tunnels.
//
// When invoked without arguments, it launches the interactive TUI dashboard.
// When invoked with subcommands (e.g. "list", "tunnel up/down/status"), it runs
// the corresponding CLI operation and exits.
//
// Usage:
//
//	ssh-manager            # launch the TUI dashboard
//	ssh-manager list       # list parsed SSH hosts from ~/.ssh/config
//	ssh-manager tunnel up  # start an SSH tunnel
//
// The CLI is constructed in internal/cli and the TUI in internal/ui. This file
// simply wires them together and handles top-level error reporting.
package main

import (
	"fmt"
	"os"

	"github.com/treykane/ssh-manager/internal/cli"
)

func main() {
	// Build the root Cobra command tree, which includes all subcommands
	// (list, tunnel up/down/status) and defaults to launching the TUI
	// dashboard when no subcommand is provided.
	cmd := cli.NewRootCommand()

	// Execute the resolved command. Cobra handles argument parsing,
	// subcommand routing, and help/usage output automatically.
	// Any error returned by a RunE handler is printed to stderr
	// and the process exits with a non-zero status code.
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
