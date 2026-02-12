// Package cli provides the command-line interface for ssh-manager, built with Cobra.
//
// The CLI serves as one of two user-facing entry points (the other being the TUI
// dashboard in internal/ui). When the user invokes ssh-manager with a subcommand,
// the CLI handles the operation and exits. When invoked without a subcommand, the
// root command launches the TUI dashboard.
//
// Command tree:
//
//	ssh-manager                  → launches the TUI dashboard (default behavior)
//	ssh-manager list             → lists all parsed hosts from ~/.ssh/config
//	ssh-manager tunnel up <host> → starts SSH tunnel(s) for a host
//	ssh-manager tunnel down <id> → stops a tunnel by ID or all tunnels for a host
//	ssh-manager tunnel status    → shows the current state of all managed tunnels
//
// The CLI and TUI share the same backend packages (internal/config, internal/tunnel,
// internal/sshclient) so their behavior is consistent. Business logic is NOT
// duplicated between the two interfaces — both delegate to the same underlying
// functions.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/config"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/security"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/tunnel"
	"github.com/treykane/ssh-manager/internal/ui"
	"github.com/treykane/ssh-manager/internal/util"
)

// NewRootCommand creates and returns the top-level Cobra command for ssh-manager.
//
// The root command has no arguments of its own — when executed directly (i.e.,
// without a subcommand), it launches the TUI dashboard via ui.Run(). This makes
// the TUI the default experience while keeping CLI subcommands available for
// scripting and automation.
//
// Subcommands are registered here:
//   - "list":   displays a table of SSH hosts parsed from the user's config.
//   - "tunnel": parent command for tunnel management (up, down, status).
//
// Returns a fully-configured *cobra.Command ready to be executed via cmd.Execute().
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ssh-manager",
		Short: "Modern SSH config and tunnel manager",
		// RunE is used (instead of Run) so errors can be propagated to main()
		// and result in a non-zero exit code.
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.Run()
		},
	}

	root.AddCommand(newListCmd())
	root.AddCommand(newTunnelCmd())
	root.AddCommand(newSecurityCmd())
	return root
}

// newListCmd creates the "list" subcommand, which parses the user's ~/.ssh/config
// and prints a formatted table of all discovered concrete host entries.
//
// Output columns:
//   - ALIAS:    the SSH host alias (what you'd type in "ssh <alias>")
//   - HOSTNAME: the resolved hostname or IP (from the HostName directive)
//   - PORT:     the SSH port (defaults to 22)
//   - USER:     the SSH user (shown as "-" if not set)
//   - FORWARDS: the count of LocalForward rules configured for this host
//
// Any parse warnings (malformed lines, missing includes, etc.) are printed to
// stderr after the host table so they don't interfere with stdout parsing by
// scripts.
func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List parsed hosts from ~/.ssh/config",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse the user's SSH config (including any Include directives).
			res, err := config.ParseDefault()
			if err != nil {
				return err
			}

			// Print a formatted table header and rows.
			// Hosts are already sorted alphabetically by the parser.
			fmt.Printf("%-24s %-24s %-8s %-16s %s\n", "ALIAS", "HOSTNAME", "PORT", "USER", "FORWARDS")
			for _, h := range res.Hosts {
				fmt.Printf("%-24s %-24s %-8d %-16s %d\n", h.Alias, h.DisplayTarget(), h.Port, util.EmptyDash(h.User), len(h.Forwards))
			}

			// Surface any warnings to stderr so users can diagnose config issues.
			// These are non-fatal: the parse still succeeded for the hosts shown above.
			if len(res.Warnings) > 0 {
				fmt.Fprintln(os.Stderr, "warnings:")
				for _, w := range res.Warnings {
					fmt.Fprintf(os.Stderr, "  - %s\n", w)
				}
			}
			return nil
		},
	}
}

// newTunnelCmd creates the "tunnel" parent command and its subcommands (up, down, status).
//
// A single tunnel.Manager instance is created and shared across all tunnel
// subcommands within a single CLI invocation. The manager loads persisted tunnel
// state from runtime.json on construction so that "tunnel status" can display
// tunnels started by previous invocations, and "tunnel down" can stop them.
//
// Subcommands:
//
//	tunnel up <host>        — Start tunnel(s) for a host.
//	                          Starts all LocalForward rules by default, or a
//	                          specific one via --forward (index or explicit spec).
//
//	tunnel down <id|host>   — Stop a tunnel by its full ID (contains "|") or
//	                          stop all tunnels for a given host alias.
//
//	tunnel status           — Print a table of all managed tunnels, or emit
//	                          JSON with --json for programmatic consumption.
func newTunnelCmd() *cobra.Command {
	// Create a shared SSH client and tunnel manager for all tunnel subcommands.
	client := sshclient.New()
	mgr := tunnel.NewManager(client)
	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		slog.Warn("failed to load config, using defaults", "error", cfgErr)
		cfg = appconfig.Default()
	}
	mgr.SetBindPolicy(cfg.Security.BindPolicy)
	mgr.SetRedactErrors(cfg.Security.RedactErrors)
	client.SetHostKeyPolicy(cfg.Security.HostKeyPolicy)

	// Restore persisted tunnel state from disk. This allows "tunnel status" to
	// show tunnels that were started in a previous session (if their processes
	// are still alive).
	if err := mgr.LoadRuntime(); err != nil {
		slog.Warn("failed to load tunnel runtime", "error", err)
	}

	var root = &cobra.Command{Use: "tunnel", Short: "Manage SSH tunnels"}

	// --- tunnel up -----------------------------------------------------------

	// forwardArg is the --forward flag value for the "up" subcommand. It can be:
	//   - Empty string: start ALL LocalForward rules for the host.
	//   - A numeric index (0-based): start a specific forward from the host's config.
	//   - An explicit spec like "8080:localhost:80": define a forward on the fly.
	var forwardArg string
	var allowPublicBind bool
	var hostKeyPolicy string

	up := &cobra.Command{
		Use:   "up <host>",
		Short: "Start tunnel(s) for host",
		// Require exactly one argument: the host alias.
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Verify that the system SSH binary is available before attempting
			// to start any tunnels — provides a clear early error message.
			if err := sshclient.EnsureSSHBinary(); err != nil {
				return err
			}

			// Look up the host by alias in the user's SSH config.
			host, err := findHost(args[0])
			if err != nil {
				return err
			}

			// Determine which forward(s) to start based on the --forward flag.
			forwards, err := resolveForwards(host, forwardArg)
			if err != nil {
				return err
			}
			mgr.SetAllowPublicBind(allowPublicBind)
			client.SetHostKeyPolicy(effectiveHostKeyPolicy(cfg, hostKeyPolicy))

			// Start each resolved forward as a separate tunnel.
			for _, fwd := range forwards {
				rt, err := mgr.Start(host, fwd)
				if err != nil {
					return fmt.Errorf("%s", security.UserMessage(err, cfg.Security.RedactErrors))
				}
				fmt.Printf("started %s pid=%d %s -> %s\n", rt.ID, rt.PID, rt.Local, rt.Remote)
			}
			return nil
		},
	}
	up.Flags().StringVar(&forwardArg, "forward", "", "forward index (0-based) or explicit spec localPort:remoteHost:remotePort")
	up.Flags().BoolVar(&allowPublicBind, "allow-public-bind", false, "allow 0.0.0.0/:: local binds for this command")
	up.Flags().StringVar(&hostKeyPolicy, "host-key-policy", "", "host key policy override: strict, accept-new, insecure")

	// --- tunnel down ---------------------------------------------------------

	down := &cobra.Command{
		Use:   "down <tunnel-id|host>",
		Short: "Stop a tunnel by id or all tunnels for host",
		// Require exactly one argument: either a full tunnel ID or a host alias.
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idOrHost := args[0]

			// Tunnel IDs contain "|" (e.g., "prod|127.0.0.1:8080|localhost:80"),
			// so we use that as a heuristic to distinguish between a tunnel ID
			// and a host alias.
			if strings.Contains(idOrHost, "|") {
				// Stop a specific tunnel by its full ID.
				if err := mgr.Stop(idOrHost); err != nil {
					return err
				}
				fmt.Printf("stopped %s\n", idOrHost)
				return nil
			}

			// No "|" found — treat it as a host alias and stop all tunnels
			// for that host.
			if err := mgr.StopByHost(idOrHost); err != nil {
				return err
			}
			fmt.Printf("stopped tunnels for host %s\n", idOrHost)
			return nil
		},
	}

	restart := &cobra.Command{
		Use:   "restart <tunnel-id|host>",
		Short: "Restart a tunnel by id or restart all tunnels for host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idOrHost := args[0]
			sn := mgr.Snapshot()
			sort.Slice(sn, func(i, j int) bool { return sn[i].ID < sn[j].ID })

			var targets []model.TunnelRuntime
			if strings.Contains(idOrHost, "|") {
				for _, rt := range sn {
					if rt.ID == idOrHost {
						targets = append(targets, rt)
						break
					}
				}
			} else {
				for _, rt := range sn {
					if rt.HostAlias == idOrHost {
						targets = append(targets, rt)
					}
				}
			}
			if len(targets) == 0 {
				return fmt.Errorf("no tunnels found for %s", idOrHost)
			}

			hostCache := map[string]model.HostEntry{}
			for _, rt := range targets {
				if err := mgr.Stop(rt.ID); err != nil {
					return err
				}
				host, ok := hostCache[rt.HostAlias]
				if !ok {
					var err error
					host, err = findHost(rt.HostAlias)
					if err != nil {
						return err
					}
					hostCache[rt.HostAlias] = host
				}
				mgr.SetAllowPublicBind(allowPublicBind)
				client.SetHostKeyPolicy(effectiveHostKeyPolicy(cfg, hostKeyPolicy))
				forward, ferr := forwardFromRuntime(rt)
				if ferr != nil {
					return ferr
				}
				next, err := mgr.Start(host, forward)
				if err != nil {
					return fmt.Errorf("%s", security.UserMessage(err, cfg.Security.RedactErrors))
				}
				fmt.Printf("restarted %s pid=%d\n", next.ID, next.PID)
			}
			return nil
		},
	}
	restart.Flags().BoolVar(&allowPublicBind, "allow-public-bind", false, "allow 0.0.0.0/:: local binds for this command")
	restart.Flags().StringVar(&hostKeyPolicy, "host-key-policy", "", "host key policy override: strict, accept-new, insecure")

	// --- tunnel status -------------------------------------------------------

	// jsonOut is the --json flag for the "status" subcommand.
	var jsonOut bool

	status := &cobra.Command{
		Use:   "status",
		Short: "Show tunnel status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get a snapshot of all tunnels with health-check data.
			sn := mgr.Snapshot()

			// Sort by ID for deterministic output (important for scripting
			// and for consistent visual ordering).
			sort.Slice(sn, func(i, j int) bool { return sn[i].ID < sn[j].ID })

			if jsonOut {
				// JSON output mode: emit the full snapshot as a JSON array.
				// Field names are stable (see model.TunnelRuntime doc comment)
				// and form the public API contract for programmatic consumers.
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sn)
			}

			// Table output mode: print a human-readable table.
			fmt.Printf("%-42s %-16s %-22s %-22s %-10s %-8s %-10s\n", "ID", "HOST", "LOCAL", "REMOTE", "STATE", "PID", "LAT(ms)")
			for _, rt := range sn {
				fmt.Printf("%-42s %-16s %-22s %-22s %-10s %-8d %-10d\n", rt.ID, rt.HostAlias, rt.Local, rt.Remote, rt.State, rt.PID, rt.LatencyMS)
			}
			return nil
		},
	}
	status.Flags().BoolVar(&jsonOut, "json", false, "output JSON")

	root.AddCommand(up, down, status, restart)
	return root
}

func forwardFromRuntime(rt model.TunnelRuntime) (model.ForwardSpec, error) {
	if rt.Forward.LocalPort > 0 && rt.Forward.RemotePort > 0 {
		return rt.Forward, nil
	}
	// Rehydrate from stable local/remote endpoint strings loaded from runtime.json.
	fwd, err := tunnel.ParseForwardArg(fmt.Sprintf("%s:%s", rt.Local, rt.Remote))
	if err != nil {
		return model.ForwardSpec{}, fmt.Errorf("cannot reconstruct forward for %s: %w", rt.ID, err)
	}
	return fwd, nil
}

// findHost looks up a host entry by its alias in the user's SSH config.
//
// This re-parses ~/.ssh/config on each call, which ensures the CLI always
// reflects the latest config changes without requiring a restart. The parse
// is fast enough for CLI use (typically <10ms for most configs).
//
// Returns the matching HostEntry or an error if the alias is not found.
func findHost(alias string) (model.HostEntry, error) {
	res, err := config.ParseDefault()
	if err != nil {
		return model.HostEntry{}, err
	}
	for _, h := range res.Hosts {
		if h.Alias == alias {
			return h, nil
		}
	}
	return model.HostEntry{}, fmt.Errorf("host not found: %s", alias)
}

// resolveForwards determines which ForwardSpec(s) to use for a "tunnel up" command
// based on the host's configuration and the --forward flag value.
//
// Resolution logic:
//
//  1. If forwardArg is empty (no --forward flag), return ALL LocalForward entries
//     from the host's SSH config. Returns an error if the host has no forwards.
//
//  2. If forwardArg is a valid integer, treat it as a 0-based index into the
//     host's Forwards slice. Returns an error if the index is out of range.
//
//  3. Otherwise, treat forwardArg as an explicit forward specification string
//     (e.g., "8080:localhost:80") and parse it via tunnel.ParseForwardArg.
//     This allows users to define ad-hoc tunnels that aren't in their SSH config.
//
// Returns a slice of ForwardSpec(s) to start, or an error describing the problem.
func resolveForwards(host model.HostEntry, forwardArg string) ([]model.ForwardSpec, error) {
	if strings.TrimSpace(forwardArg) == "" {
		// No --forward flag: use all forwards from the SSH config.
		if len(host.Forwards) == 0 {
			return nil, fmt.Errorf("host %s has no LocalForward entries", host.Alias)
		}
		return host.Forwards, nil
	}

	// Try parsing as an integer index first (e.g., --forward 0).
	if idx, err := strconv.Atoi(forwardArg); err == nil {
		if idx < 0 || idx >= len(host.Forwards) {
			return nil, fmt.Errorf("forward index out of range")
		}
		return []model.ForwardSpec{host.Forwards[idx]}, nil
	}

	// Not an integer — try parsing as an explicit forward spec
	// (e.g., --forward "8080:localhost:80").
	fwd, err := tunnel.ParseForwardArg(forwardArg)
	if err != nil {
		return nil, err
	}
	return []model.ForwardSpec{fwd}, nil
}

// ConnectOnce establishes an interactive SSH session to the given host.
//
// This function is available for programmatic use by the TUI when the user
// presses Enter on a selected host. It creates a fresh sshclient.Client and
// runs an interactive PTY-based SSH session.
//
// The session has a generous 24-hour timeout to accommodate long-running
// interactive work. The context timeout acts as a safety net — in practice,
// the session ends when the user types "exit" or the connection drops.
//
// Returns nil on clean session exit, or an error if the SSH connection fails.
func ConnectOnce(host model.HostEntry) error {
	// Use a long timeout for interactive sessions (user may work for hours).
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	c := sshclient.New()
	return c.RunInteractive(ctx, host)
}

func effectiveHostKeyPolicy(cfg appconfig.Config, override string) string {
	if strings.TrimSpace(override) != "" {
		return appconfig.NormalizeHostKeyPolicy(strings.TrimSpace(override))
	}
	return cfg.Security.HostKeyPolicy
}

func newSecurityCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Security checks and local posture tools",
	}
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Run a local security audit",
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := security.RunLocalAudit()
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			if len(report.Findings) == 0 {
				fmt.Println("No security findings.")
				return nil
			}
			fmt.Printf("%-8s %-34s %-36s %s\n", "SEV", "TARGET", "MESSAGE", "RECOMMENDATION")
			for _, f := range report.Findings {
				fmt.Printf("%-8s %-34s %-36s %s\n", strings.ToUpper(string(f.Severity)), f.Target, f.Message, f.Recommendation)
			}
			return nil
		},
	}
	audit.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	cmd.AddCommand(audit)
	return cmd
}
