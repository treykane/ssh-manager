// Package ui provides the terminal user interface (TUI) dashboard for ssh-manager.
//
// The dashboard is built with Bubble Tea (a Go framework for terminal apps based on
// The Elm Architecture) and styled with Lip Gloss. It presents the user with:
//
//   - A filterable list of SSH hosts parsed from ~/.ssh/config
//   - A detail panel showing the selected host's configuration
//   - A live tunnel status table with health-check latency
//   - Contextual guidance for available actions
//
// The TUI is the default entry point when ssh-manager is run without subcommands.
// It supports the following keyboard interactions:
//
//	j/k or ↑/↓  — Navigate the host list
//	Enter        — Open an interactive SSH session to the selected host
//	t            — Toggle the first LocalForward tunnel for the selected host
//	/            — Enter filter mode (type to search hosts by alias or hostname)
//	r            — Reload SSH config and refresh tunnel status
//	?            — Toggle the help panel
//	q / Ctrl+C   — Quit (stops all managed tunnels before exiting)
//
// Architecture notes:
//
// The TUI follows the Elm Architecture (Model-Update-View) enforced by Bubble Tea:
//   - Model (dashboardModel): holds all application state (hosts, tunnels, selection, etc.)
//   - Update: processes messages (key presses, tick events, window resizes) and returns
//     an updated model plus optional commands.
//   - View: renders the current model state as a string for terminal display.
//
// The dashboard periodically refreshes tunnel status via a tick command, performing
// asynchronous TCP health checks on active tunnels to measure latency without
// blocking the UI.
package ui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/config"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/security"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/tunnel"
	"github.com/treykane/ssh-manager/internal/util"
)

// tickMsg is a Bubble Tea message emitted by the periodic refresh timer.
// When received in Update(), it triggers a tunnel status snapshot refresh.
type tickMsg time.Time

// statusMsg is a Bubble Tea message used to update the status bar text.
// It is typically sent after an asynchronous operation completes (e.g., an
// SSH session ending) to communicate the result back to the dashboard.
type statusMsg string

// dashboardModel is the central Bubble Tea model for the TUI dashboard.
// It holds all state needed to render the UI and process user input.
//
// This struct is intentionally unexported — the only public entry point is
// the Run() function, which creates the model internally and starts the
// Bubble Tea program.
type dashboardModel struct {
	// hosts contains all concrete SSH host entries parsed from ~/.ssh/config.
	// This is the full, unfiltered list. Populated by reloadConfig().
	hosts []model.HostEntry

	// filtered contains the subset of hosts that match the current filter string.
	// This is what the host list panel actually displays. Updated by applyFilter().
	filtered []model.HostEntry

	// sel is the index of the currently selected host in the filtered list.
	// Used for keyboard navigation (j/k/up/down) and for determining which
	// host to connect to or toggle tunnels on.
	sel int

	// filter is the current search/filter string entered by the user in filter mode.
	// When non-empty, only hosts whose alias or hostname contain this substring
	// (case-insensitive) are shown in the filtered list.
	filter string

	// filterMode indicates whether the user is currently typing a filter string.
	// When true, all single-character key presses are appended to the filter
	// string instead of being interpreted as commands.
	filterMode bool

	// showHelp indicates whether the help panel is currently visible.
	// Toggled by pressing '?'.
	showHelp bool

	// status is the text displayed in the status bar at the bottom of the dashboard.
	// Updated after user actions (tunnel start/stop, SSH session exit, errors, etc.).
	status string

	// warnings contains non-fatal warnings from the SSH config parser (e.g.,
	// malformed lines, missing include targets). Displayed at the bottom of
	// the UI so users can diagnose config issues.
	warnings []string

	// tunnels holds the most recent snapshot of all managed tunnel states,
	// including health-check latency data. Refreshed on every tick and after
	// tunnel start/stop operations.
	tunnels []model.TunnelRuntime

	// width and height track the terminal dimensions, updated via WindowSizeMsg.
	// Used to adapt the layout (e.g., side-by-side vs. stacked panels) and
	// to size panels appropriately.
	width  int
	height int

	// cfg holds the loaded application configuration (refresh interval, etc.).
	// Loaded from ~/.config/ssh-manager/config.yaml on startup.
	cfg appconfig.Config

	// mgr is the tunnel manager instance that handles starting, stopping,
	// and monitoring SSH tunnel processes. Shared with the periodic refresh
	// ticker and tunnel toggle actions.
	mgr *tunnel.Manager

	// ssh is the SSH client used to create interactive SSH session commands.
	// Used when the user presses Enter to connect to a host.
	ssh *sshclient.Client

	// form holds the state for the "new connection" configurator.
	// nil when the form is not active.
	form *newConnForm

	// adHocHosts stores session-only hosts created via the form so they
	// survive config reloads (press 'r').
	adHocHosts []model.HostEntry
}

// initialModel creates the initial dashboardModel with loaded configuration,
// restored tunnel state, and parsed SSH hosts.
//
// This function is called once when the Bubble Tea program starts. It:
//  1. Loads the application config (or falls back to defaults on error).
//  2. Creates an SSH client and tunnel manager.
//  3. Restores persisted tunnel state from runtime.json.
//  4. Parses the user's SSH config to populate the host list.
//  5. Sets an initial status message with usage hints.
func initialModel() dashboardModel {
	cfg, err := appconfig.Load()
	if err != nil {
		slog.Warn("failed to load app config, using defaults", "error", err)
		cfg = appconfig.Default()
	}

	ssh := sshclient.New()
	mgr := tunnel.NewManager(ssh)
	mgr.SetBindPolicy(cfg.Security.BindPolicy)
	mgr.SetRedactErrors(cfg.Security.RedactErrors)
	ssh.SetHostKeyPolicy(cfg.Security.HostKeyPolicy)

	// Restore tunnel state from a previous session. If the runtime file
	// doesn't exist or can't be read, we proceed with an empty state.
	if err := mgr.LoadRuntime(); err != nil {
		slog.Warn("failed to load tunnel runtime", "error", err)
	}

	m := dashboardModel{cfg: cfg, mgr: mgr, ssh: ssh}
	m.reloadConfig()
	m.status = "Ready. Select a host, Enter to connect, t for first tunnel, T for all."
	return m
}

// reloadConfig re-parses the user's SSH config file and refreshes the host list
// and tunnel status snapshot.
//
// Called on startup and when the user presses 'r' to refresh. Any parse errors
// are shown in the status bar rather than crashing the app.
func (m *dashboardModel) reloadConfig() {
	res, err := config.ParseDefault()
	if err != nil {
		m.status = "config parse error: " + err.Error()
		return
	}
	// Hosts are already sorted alphabetically by the parser.
	m.hosts = res.Hosts
	// Merge back session-only ad-hoc hosts so they survive reloads.
	m.hosts = append(m.hosts, m.adHocHosts...)
	m.warnings = res.Warnings
	m.applyFilter()
	m.tunnels = m.mgr.Snapshot()
}

// applyFilter updates the filtered host list based on the current filter string.
//
// If the filter is empty or whitespace-only, all hosts are shown. Otherwise,
// hosts are included only if their alias or display target (hostname) contains
// the filter string (case-insensitive substring match).
//
// After filtering, the selection index is clamped to the valid range to prevent
// it from pointing beyond the end of the filtered list.
func (m *dashboardModel) applyFilter() {
	if strings.TrimSpace(m.filter) == "" {
		// No filter active — show all hosts. We create a copy of the slice
		// to avoid aliasing issues if the original is later modified.
		m.filtered = append([]model.HostEntry(nil), m.hosts...)
	} else {
		f := strings.ToLower(strings.TrimSpace(m.filter))
		m.filtered = nil
		for _, h := range m.hosts {
			if strings.Contains(strings.ToLower(h.Alias), f) || strings.Contains(strings.ToLower(h.DisplayTarget()), f) {
				m.filtered = append(m.filtered, h)
			}
		}
	}

	// Clamp the selection index to the valid range for the new filtered list.
	if m.sel >= len(m.filtered) {
		m.sel = len(m.filtered) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

// tickCmd returns a Bubble Tea command that emits a tickMsg after the configured
// refresh interval. This drives the periodic tunnel status refresh in the UI.
//
// If the provided seconds value is <= 0, it falls back to the default refresh
// interval (DefaultRefreshSeconds) to prevent spinning at maximum speed.
func tickCmd(seconds int) tea.Cmd {
	if seconds <= 0 {
		seconds = util.DefaultRefreshSeconds
	}
	return tea.Tick(time.Duration(seconds)*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model. It returns the initial command to run when the
// Bubble Tea program starts — in this case, the periodic refresh ticker.
func (m dashboardModel) Init() tea.Cmd {
	return tickCmd(m.cfg.UI.RefreshSeconds)
}

// Update implements tea.Model. It processes incoming messages (key presses,
// timer ticks, window resizes, and status updates) and returns the updated
// model along with any commands to execute.
//
// Message handling:
//
//   - tickMsg: refreshes tunnel status by taking a new snapshot from the manager.
//     Reschedules the next tick automatically.
//
//   - tea.WindowSizeMsg: records the new terminal dimensions for responsive layout.
//
//   - tea.KeyMsg: handles keyboard input. Behavior differs based on whether
//     filter mode is active (typing into the search field) or normal mode
//     (navigating and executing commands).
//
//   - statusMsg: updates the status bar text (e.g., after an SSH session ends).
func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		// Periodic refresh: snapshot tunnel states (including health checks)
		// and schedule the next tick.
		m.tunnels = m.mgr.Snapshot()
		return m, tickCmd(m.cfg.UI.RefreshSeconds)

	case tea.WindowSizeMsg:
		// Terminal was resized — store new dimensions for layout calculations.
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// --- Filter mode: capture keystrokes as search input ---
		if m.filterMode {
			switch msg.String() {
			case "enter", "esc":
				// Exit filter mode. The filter remains applied; the user can
				// press "/" again to modify it, or press "/" and clear it.
				m.filterMode = false
				m.applyFilter()
				return m, nil
			case "backspace":
				// Delete the last character from the filter string.
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
				}
				m.applyFilter()
				return m, nil
			default:
				// Append printable characters to the filter string.
				// Multi-character key names (e.g., "ctrl+a") are ignored.
				if len(msg.String()) == 1 {
					m.filter += msg.String()
					m.applyFilter()
				}
				return m, nil
			}
		}

		// --- New connection form mode ---
		if m.form != nil {
			if msg.String() == "esc" {
				m.form = nil
				m.status = "New connection cancelled"
				return m, nil
			}
			result, cmd := m.form.update(msg)
			if result != nil {
				m.handleFormResult(result)
				m.form = nil
			}
			return m, cmd
		}

		// --- Normal mode: process navigation and action keys ---
		switch msg.String() {
		case "q", "ctrl+c":
			// Quit the application. Stop all managed tunnels first to avoid
			// leaving orphaned SSH processes.
			m.mgr.StopAll()
			return m, tea.Quit

		case "j", "down":
			// Move selection down in the host list.
			if m.sel < len(m.filtered)-1 {
				m.sel++
			}

		case "k", "up":
			// Move selection up in the host list.
			if m.sel > 0 {
				m.sel--
			}

		case "/":
			// Enter filter mode: subsequent keystrokes will be appended to
			// the filter string instead of being treated as commands.
			m.filterMode = true
			m.status = "Filter mode: type and press Enter"

		case "?":
			// Toggle the help panel visibility.
			m.showHelp = !m.showHelp

		case "r":
			// Reload the SSH config from disk and refresh tunnel status.
			// This picks up any changes the user made to ~/.ssh/config
			// without restarting the app.
			m.reloadConfig()
			m.status = "Refreshed config and tunnel status"

		case "enter":
			// Open an interactive SSH session to the selected host.
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]

			// Create the SSH command and hand it off to Bubble Tea's
			// ExecProcess, which suspends the TUI, gives the SSH process
			// full control of the terminal, and resumes the TUI when the
			// SSH session ends.
			cmd := m.ssh.ConnectCommand(h)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				if err != nil {
					return statusMsg("ssh exited: " + security.UserMessage(err, m.cfg.Security.RedactErrors))
				}
				return statusMsg("ssh session closed")
			})

		case "n":
			// Open the new connection configurator form.
			m.form = newForm()
			m.status = "New connection: choose Quick Connect or Full Config"

		case "t":
			// Toggle the first LocalForward tunnel for the selected host.
			// If the tunnel is currently up or starting, stop it.
			// If it's down or errored, start it.
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]

			// Check that the host has at least one LocalForward configured.
			if len(h.Forwards) == 0 {
				m.status = "No LocalForward entries for host " + h.Alias
				break
			}

			// Use the first forward for the toggle action. Users who need
			// to manage specific forwards can use the CLI:
			//   ssh-manager tunnel up <host> --forward <index>
			m.status = m.toggleForward(h, 0)
			m.tunnels = m.mgr.Snapshot()

		case "T":
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]
			if len(h.Forwards) == 0 {
				m.status = "No LocalForward entries for host " + h.Alias
				break
			}
			stopped := 0
			started := 0
			for i := range h.Forwards {
				status := m.toggleForward(h, i)
				if strings.HasPrefix(status, "Tunnel stopped:") {
					stopped++
				}
				if strings.HasPrefix(status, "Tunnel started:") {
					started++
				}
			}
			m.status = fmt.Sprintf("Processed %d forwards for %s (started=%d, stopped=%d)", len(h.Forwards), h.Alias, started, stopped)
			m.tunnels = m.mgr.Snapshot()

		case "R":
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]
			if len(h.Forwards) == 0 {
				m.status = "No LocalForward entries for host " + h.Alias
				break
			}
			id := tunnel.RuntimeID(h.Alias, h.Forwards[0])
			_ = m.mgr.Stop(id)
			m.status = m.toggleForward(h, 0)
			m.tunnels = m.mgr.Snapshot()
		}

	case statusMsg:
		// Update the status bar with a message from an async operation
		// (e.g., SSH session completion).
		m.status = string(msg)
	}
	return m, nil
}

// View implements tea.Model. It renders the entire dashboard UI as a single
// string that Bubble Tea displays in the terminal.
//
// The layout is composed of several panels arranged vertically:
//
//	┌─────────────────────────────────────────┐
//	│ Header (title, stats, filter, keybinds) │
//	├────────────────────┬────────────────────┤
//	│ Hosts Panel        │ Details Panel      │  ← side-by-side if width >= 96
//	│ (filterable list)  │ (selected host)    │     otherwise stacked vertically
//	├────────────────────┴────────────────────┤
//	│ Active Tunnels Panel                    │
//	├─────────────────────────────────────────┤
//	│ Help Panel (if visible)                 │
//	├─────────────────────────────────────────┤
//	│ Warnings (if any)                       │
//	├─────────────────────────────────────────┤
//	│ Status Bar                              │
//	└─────────────────────────────────────────┘
func (m dashboardModel) View() string {
	// --- Header section ---

	// Application title with accent color.
	head := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("SSH Manager Dashboard")

	// Summary statistics line showing counts and refresh interval.
	subhead := fmt.Sprintf("hosts=%d shown=%d tunnels=%d refresh=%ds",
		len(m.hosts), len(m.filtered), len(m.tunnels), clampRefresh(m.cfg.UI.RefreshSeconds))

	// --- Hosts panel (left side) ---

	left := strings.Builder{}
	left.WriteString("j/k to navigate; [T] means active tunnel.\n")
	for i, h := range m.filtered {
		// Selection cursor: ">" for the selected host, " " for others.
		cursor := " "
		if i == m.sel {
			cursor = ">"
		}
		// Tunnel indicator: "T" if the host has at least one active tunnel.
		tunnelMark := " "
		if m.hostHasActiveTunnel(h.Alias) {
			tunnelMark = "T"
		}
		left.WriteString(fmt.Sprintf("%s[%s] %-22s %-22s\n", cursor, tunnelMark, h.Alias, h.DisplayTarget()))
	}
	if len(m.filtered) == 0 {
		left.WriteString("  (no hosts matched)\n")
	}

	// --- Details panel (right side) ---

	detail := strings.Builder{}
	if len(m.filtered) > 0 {
		h := m.filtered[m.sel]

		// Show key configuration fields for the selected host.
		detail.WriteString(fmt.Sprintf("Alias: %s\nHost: %s\nUser: %s\nPort: %d\nProxyJump: %s\n",
			h.Alias, h.DisplayTarget(), util.EmptyDash(h.User), h.Port, util.EmptyDash(h.ProxyJump)))

		// List all LocalForward entries with their index numbers.
		detail.WriteString("Forwards:\n")
		if len(h.Forwards) == 0 {
			detail.WriteString("  (none)\n")
		}
		for i, fwd := range h.Forwards {
			detail.WriteString(fmt.Sprintf("  [%d] %s:%d -> %s:%d\n", i, fwd.LocalString(), fwd.LocalPort, fwd.RemoteString(), fwd.RemotePort))
		}

		// Contextual guidance telling the user what actions are available
		// for the selected host based on its current state.
		detail.WriteString("\nNext steps:\n")
		detail.WriteString(m.guidanceForHost(h))
	} else {
		detail.WriteString("Pick a host to view connection and tunnel options.\n")
	}

	// --- Tunnels table ---

	tbl := strings.Builder{}
	tbl.WriteString(fmt.Sprintf("%-24s %-20s %-20s %-10s %-8s %-8s\n", "HOST", "LOCAL", "REMOTE", "STATE", "PID", "LAT"))
	for _, rt := range m.tunnels {
		tbl.WriteString(fmt.Sprintf("%-24s %-20s %-20s %-10s %-8d %-8d\n", rt.HostAlias, rt.Local, rt.Remote, rt.State, rt.PID, rt.LatencyMS))
	}
	if len(m.tunnels) == 0 {
		tbl.WriteString("(none)\n")
	}

	// --- Warnings line (only shown if there are parse warnings) ---

	warn := ""
	if len(m.warnings) > 0 {
		warn = "Warnings: " + strings.Join(m.warnings, " | ") + "\n"
	}

	// --- Filter indicator ---

	filterLine := fmt.Sprintf("Filter: %s", m.filter)
	if m.filterMode {
		filterLine += " (typing...)"
	}

	// --- Quick-reference keybinding bar ---

	quickHelp := "Keys: Enter connect | n new | t first tunnel | T all tunnels | R restart first | / filter | r refresh | ? help | q quit"

	// --- Compose the final layout ---

	// If the new connection form is open, render it instead of the normal panels.
	var main string
	if m.form != nil {
		main = m.form.view(m.renderPanel, m.effectiveWidth())
	} else {
		// renderMainPanels handles responsive layout: side-by-side panels on
		// wide terminals (>= 96 cols), stacked vertically on narrow ones.
		main = m.renderMainPanels(left.String(), detail.String())
	}

	// Render the tunnel table and status bar as full-width panels.
	tunnels := m.renderPanel("Active Tunnels", tbl.String(), m.effectiveWidth(), lipgloss.Color("63"))
	status := m.renderPanel("Status", m.status, m.effectiveWidth(), lipgloss.Color("205"))

	// Conditionally render the help panel.
	help := ""
	if m.showHelp {
		help = m.renderPanel("Help", m.helpBlock(), m.effectiveWidth(), lipgloss.Color("244"))
	}

	// Stack all sections vertically into the final layout string.
	layout := lipgloss.JoinVertical(
		lipgloss.Left,
		head,
		subhead,
		filterLine,
		quickHelp,
		main,
		tunnels,
		help,
		warn,
		status,
	)
	return layout
}

// Run starts the TUI dashboard as a full-screen terminal application.
//
// This is the main entry point for the TUI, called by:
//   - The root CLI command (when ssh-manager is invoked without subcommands).
//   - Directly in tests or alternative entry points.
//
// It verifies that the system SSH binary is available (for connecting to hosts
// and starting tunnels), then creates and runs the Bubble Tea program in
// alternate screen mode (which preserves the user's terminal scrollback).
//
// Returns nil on clean exit, or an error if the SSH binary is missing or the
// terminal cannot be initialized.
func Run() error {
	if err := sshclient.EnsureSSHBinary(); err != nil {
		return err
	}
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// clampRefresh ensures the refresh interval is a positive value.
// Returns DefaultRefreshSeconds (3) if the input is <= 0.
// This prevents the tick timer from firing at maximum speed due to a
// misconfigured or zero-valued refresh_seconds in config.yaml.
func clampRefresh(seconds int) int {
	if seconds <= 0 {
		return util.DefaultRefreshSeconds
	}
	return seconds
}

// hostHasActiveTunnel checks whether any tunnel for the given host alias is
// currently in the "up" or "starting" state. Used to display the "[T]" indicator
// next to hosts in the host list panel.
func (m dashboardModel) hostHasActiveTunnel(alias string) bool {
	for _, rt := range m.tunnels {
		if rt.HostAlias != alias {
			continue
		}
		if rt.State == model.TunnelUp || rt.State == model.TunnelStarting {
			return true
		}
	}
	return false
}

// guidanceForHost generates contextual "next steps" text for the detail panel
// based on the selected host's configuration and current tunnel state.
//
// The guidance adapts to the host's situation:
//   - Always suggests pressing Enter for an interactive SSH session.
//   - If no LocalForward entries exist, explains that tunnel controls require
//     configuring forwards in the SSH config.
//   - If the first tunnel is active, suggests pressing 't' to stop it and
//     shows the current state/PID.
//   - If the first tunnel is not active, suggests pressing 't' to start it.
//   - If the host has multiple forwards, provides a hint about using the CLI
//     to select a specific forward by index.
func (m dashboardModel) guidanceForHost(h model.HostEntry) string {
	var lines []string
	lines = append(lines, "  - Press Enter to open an interactive ssh session.")

	if len(h.Forwards) == 0 {
		lines = append(lines, "  - No LocalForward configured. Add one in ssh config to enable tunnel controls.")
		return strings.Join(lines, "\n") + "\n"
	}

	// Check the state of the first forward's tunnel to provide accurate guidance.
	id := tunnel.RuntimeID(h.Alias, h.Forwards[0])
	if rt, err := m.mgr.Get(id); err == nil && (rt.State == model.TunnelUp || rt.State == model.TunnelStarting) {
		lines = append(lines, "  - Press t to stop the first LocalForward tunnel.")
		lines = append(lines, fmt.Sprintf("  - Current tunnel state: %s (pid=%d).", rt.State, rt.PID))
	} else {
		lines = append(lines, "  - Press t to start the first LocalForward tunnel.")
	}
	lines = append(lines, "  - Press T to process all forwards, or R to restart the first forward.")

	// If the host has multiple forwards, hint that the CLI can target specific ones.
	if len(h.Forwards) > 1 {
		lines = append(lines, fmt.Sprintf("  - This host has %d forwards; CLI supports selecting a specific one:", len(h.Forwards)))
		lines = append(lines, fmt.Sprintf("    ssh-manager tunnel up %s --forward 1", h.Alias))
	}

	return strings.Join(lines, "\n") + "\n"
}

// renderMainPanels arranges the hosts panel and details panel based on the
// current terminal width.
//
// Layout behavior:
//   - Wide terminals (>= 96 columns): panels are placed side-by-side using
//     lipgloss.JoinHorizontal, with the width split evenly between them.
//   - Narrow terminals (< 96 columns): panels are stacked vertically using
//     lipgloss.JoinVertical, each taking the full width.
//
// This responsive behavior ensures the UI remains usable on both wide monitors
// and narrow terminal windows.
func (m dashboardModel) renderMainPanels(hostsPanel, detailsPanel string) string {
	width := m.effectiveWidth()
	if width < 96 {
		// Narrow layout: stack panels vertically.
		return lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderPanel("Hosts", hostsPanel, width, lipgloss.Color("39")),
			m.renderPanel("Details", detailsPanel, width, lipgloss.Color("69")),
		)
	}
	// Wide layout: place panels side-by-side.
	leftWidth := width / 2
	rightWidth := width - leftWidth
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		m.renderPanel("Hosts", hostsPanel, leftWidth, lipgloss.Color("39")),
		m.renderPanel("Details", detailsPanel, rightWidth, lipgloss.Color("69")),
	)
}

// helpBlock returns the content for the help panel, listing all available
// keyboard shortcuts with brief descriptions.
func (m dashboardModel) helpBlock() string {
	return strings.Join([]string{
		"  Navigation: j/k or arrow keys move selection.",
		"  Filtering: press /, type alias/host text, then Enter.",
		"  Connect: press Enter on selected host.",
		"  New: press n to configure a new SSH connection.",
		"  Tunnel: t toggles first forward; T processes all forwards; R restarts first forward.",
		"  Refresh: press r to reparse ssh config and refresh runtime snapshot.",
		"  Quit: press q (or Ctrl+C) and all managed tunnels are stopped.",
	}, "\n")
}

func (m *dashboardModel) toggleForward(host model.HostEntry, idx int) string {
	if idx < 0 || idx >= len(host.Forwards) {
		return "Tunnel index out of range"
	}
	fwd := host.Forwards[idx]
	id := tunnel.RuntimeID(host.Alias, fwd)
	rt, err := m.mgr.Get(id)
	if err == nil && (rt.State == model.TunnelUp || rt.State == model.TunnelStarting) {
		_ = m.mgr.Stop(id)
		return "Tunnel stopped: " + id
	}
	newRT, serr := m.mgr.Start(host, fwd)
	if serr != nil {
		return "Tunnel start failed: " + security.UserMessage(serr, m.cfg.Security.RedactErrors)
	}
	return fmt.Sprintf("Tunnel started: %s (pid=%d)", newRT.ID, newRT.PID)
}

// handleFormResult processes a completed new-connection form. It either saves
// the host to ~/.ssh/config and reloads, or adds it as a session-only ad-hoc
// host to the in-memory list.
func (m *dashboardModel) handleFormResult(result *formResult) {
	h := result.host

	if !h.IsAdHoc {
		// Validate alias before writing.
		if err := config.ValidateAlias(h.Alias); err != nil {
			m.status = "Validation error: " + security.UserMessage(err, m.cfg.Security.RedactErrors)
			return
		}
		if err := config.AppendHostEntry(h); err != nil {
			m.status = "Failed to save to SSH config: " + security.UserMessage(err, m.cfg.Security.RedactErrors)
			return
		}
		m.reloadConfig()
		m.status = fmt.Sprintf("Saved host %q to ~/.ssh/config", h.Alias)
	} else {
		m.adHocHosts = append(m.adHocHosts, h)
		m.hosts = append(m.hosts, h)
		m.applyFilter()
		m.status = fmt.Sprintf("Added session host %q (%s)", h.Alias, h.DisplayTarget())
	}
}

// effectiveWidth returns the terminal width to use for layout calculations.
// Returns a sensible default of 100 columns if the terminal width has not been
// reported yet (e.g., before the first WindowSizeMsg is received).
func (m dashboardModel) effectiveWidth() int {
	if m.width <= 0 {
		return 100
	}
	return m.width
}

// renderPanel creates a styled panel with a colored header, bordered content,
// and the specified width.
//
// Each panel consists of:
//   - A bold, accented title line
//   - The body content (trailing newlines are trimmed for consistent spacing)
//   - A rounded border in the accent color with 1-column horizontal padding
//
// The accent color parameter allows each panel to have a distinct visual identity
// (e.g., blue for hosts, purple for tunnels, pink for status).
//
// The minimum width is clamped to 24 to prevent rendering artifacts with very
// narrow terminals.
func (m dashboardModel) renderPanel(title, body string, width int, accent lipgloss.Color) string {
	if width < 24 {
		width = 24
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(title)
	content := strings.TrimSuffix(body, "\n")
	panel := strings.TrimSpace(header + "\n" + content)
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1).
		Render(panel)
}
