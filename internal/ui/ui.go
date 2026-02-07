package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/config"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/tunnel"
)

type tickMsg time.Time

type modelUI struct {
	hosts      []model.HostEntry
	filtered   []model.HostEntry
	sel        int
	filter     string
	filterMode bool
	showHelp   bool
	status     string
	warnings   []string
	tunnels    []model.TunnelRuntime
	width      int
	height     int
	cfg        appconfig.Config
	mgr        *tunnel.Manager
	ssh        *sshclient.Client
}

func initialModel() modelUI {
	cfg, _ := appconfig.Load()
	ssh := sshclient.New()
	mgr := tunnel.NewManager(ssh)
	_ = mgr.LoadRuntime()
	m := modelUI{cfg: cfg, mgr: mgr, ssh: ssh}
	m.reloadConfig()
	m.status = "Ready. Select a host, then Enter to connect or t to manage its first tunnel."
	return m
}

func (m *modelUI) reloadConfig() {
	res, err := config.ParseDefault()
	if err != nil {
		m.status = "config parse error: " + err.Error()
		return
	}
	sort.Slice(res.Hosts, func(i, j int) bool { return res.Hosts[i].Alias < res.Hosts[j].Alias })
	m.hosts = res.Hosts
	m.warnings = res.Warnings
	m.applyFilter()
	m.tunnels = m.mgr.Snapshot()
}

func (m *modelUI) applyFilter() {
	if strings.TrimSpace(m.filter) == "" {
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
	if m.sel >= len(m.filtered) {
		m.sel = len(m.filtered) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func tickCmd(seconds int) tea.Cmd {
	if seconds <= 0 {
		seconds = 3
	}
	return tea.Tick(time.Duration(seconds)*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m modelUI) Init() tea.Cmd {
	return tickCmd(m.cfg.UI.RefreshSeconds)
}

func (m modelUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.tunnels = m.mgr.Snapshot()
		return m, tickCmd(m.cfg.UI.RefreshSeconds)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.filterMode {
			switch msg.String() {
			case "enter", "esc":
				m.filterMode = false
				m.applyFilter()
				return m, nil
			case "backspace":
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
				}
				m.applyFilter()
				return m, nil
			default:
				if len(msg.String()) == 1 {
					m.filter += msg.String()
					m.applyFilter()
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.mgr.StopAll()
			return m, tea.Quit
		case "j", "down":
			if m.sel < len(m.filtered)-1 {
				m.sel++
			}
		case "k", "up":
			if m.sel > 0 {
				m.sel--
			}
		case "/":
			m.filterMode = true
			m.status = "Filter mode: type and press Enter"
		case "?":
			m.showHelp = !m.showHelp
		case "r":
			m.reloadConfig()
			m.status = "Refreshed config and tunnel status"
		case "enter":
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]
			cmd := m.ssh.ConnectCommand(h)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				if err != nil {
					return statusMsg("ssh exited: " + err.Error())
				}
				return statusMsg("ssh session closed")
			})
		case "t":
			if len(m.filtered) == 0 {
				break
			}
			h := m.filtered[m.sel]
			if len(h.Forwards) == 0 {
				m.status = "No LocalForward entries for host " + h.Alias
				break
			}
			fwd := h.Forwards[0]
			id := tunnel.RuntimeID(h.Alias, fwd)
			rt, err := m.mgr.Get(id)
			if err == nil && (rt.State == model.TunnelUp || rt.State == model.TunnelStarting) {
				_ = m.mgr.Stop(id)
				m.status = "Tunnel stopped: " + id
			} else {
				newRT, serr := m.mgr.Start(h, fwd)
				if serr != nil {
					m.status = "Tunnel start failed: " + serr.Error()
				} else {
					m.status = fmt.Sprintf("Tunnel started: %s (pid=%d)", newRT.ID, newRT.PID)
				}
			}
			m.tunnels = m.mgr.Snapshot()
		}
	case statusMsg:
		m.status = string(msg)
	}
	return m, nil
}

type statusMsg string

func (m modelUI) View() string {
	head := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("SSH Manager Dashboard")
	subhead := fmt.Sprintf("hosts=%d shown=%d tunnels=%d refresh=%ds", len(m.hosts), len(m.filtered), len(m.tunnels), clampRefresh(m.cfg.UI.RefreshSeconds))
	left := strings.Builder{}
	left.WriteString("j/k to navigate; [T] means active tunnel.\n")
	for i, h := range m.filtered {
		cursor := " "
		if i == m.sel {
			cursor = ">"
		}
		tunnelMark := " "
		if m.hostHasActiveTunnel(h.Alias) {
			tunnelMark = "T"
		}
		left.WriteString(fmt.Sprintf("%s[%s] %-22s %-22s\n", cursor, tunnelMark, h.Alias, h.DisplayTarget()))
	}
	if len(m.filtered) == 0 {
		left.WriteString("  (no hosts matched)\n")
	}

	detail := strings.Builder{}
	if len(m.filtered) > 0 {
		h := m.filtered[m.sel]
		detail.WriteString(fmt.Sprintf("Alias: %s\nHost: %s\nUser: %s\nPort: %d\nProxyJump: %s\n", h.Alias, h.DisplayTarget(), emptyDash(h.User), h.Port, emptyDash(h.ProxyJump)))
		detail.WriteString("Forwards:\n")
		if len(h.Forwards) == 0 {
			detail.WriteString("  (none)\n")
		}
		for i, fwd := range h.Forwards {
			detail.WriteString(fmt.Sprintf("  [%d] %s:%d -> %s:%d\n", i, fwd.LocalString(), fwd.LocalPort, fwd.RemoteString(), fwd.RemotePort))
		}
		detail.WriteString("\nNext steps:\n")
		detail.WriteString(m.guidanceForHost(h))
	} else {
		detail.WriteString("Pick a host to view connection and tunnel options.\n")
	}

	tbl := strings.Builder{}
	tbl.WriteString(fmt.Sprintf("%-24s %-20s %-20s %-10s %-8s %-8s\n", "HOST", "LOCAL", "REMOTE", "STATE", "PID", "LAT"))
	for _, rt := range m.tunnels {
		tbl.WriteString(fmt.Sprintf("%-24s %-20s %-20s %-10s %-8d %-8d\n", rt.HostAlias, rt.Local, rt.Remote, rt.State, rt.PID, rt.LatencyMS))
	}
	if len(m.tunnels) == 0 {
		tbl.WriteString("(none)\n")
	}

	warn := ""
	if len(m.warnings) > 0 {
		warn = "Warnings: " + strings.Join(m.warnings, " | ") + "\n"
	}
	filterLine := fmt.Sprintf("Filter: %s", m.filter)
	if m.filterMode {
		filterLine += " (typing...)"
	}

	quickHelp := "Keys: Enter connect | t toggle tunnel | / filter | r refresh | ? help | q quit"
	main := m.renderMainPanels(left.String(), detail.String())
	tunnels := m.renderPanel("Active Tunnels", tbl.String(), m.effectiveWidth(), lipgloss.Color("63"))
	status := m.renderPanel("Status", m.status, m.effectiveWidth(), lipgloss.Color("205"))
	help := ""
	if m.showHelp {
		help = m.renderPanel("Help", m.helpBlock(), m.effectiveWidth(), lipgloss.Color("244"))
	}
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

func Run() error {
	if err := sshclient.EnsureSSHBinary(); err != nil {
		return err
	}
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clampRefresh(seconds int) int {
	if seconds <= 0 {
		return 3
	}
	return seconds
}

func (m modelUI) hostHasActiveTunnel(alias string) bool {
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

func (m modelUI) guidanceForHost(h model.HostEntry) string {
	var lines []string
	lines = append(lines, "  - Press Enter to open an interactive ssh session.")
	if len(h.Forwards) == 0 {
		lines = append(lines, "  - No LocalForward configured. Add one in ssh config to enable tunnel controls.")
		return strings.Join(lines, "\n") + "\n"
	}

	id := tunnel.RuntimeID(h.Alias, h.Forwards[0])
	if rt, err := m.mgr.Get(id); err == nil && (rt.State == model.TunnelUp || rt.State == model.TunnelStarting) {
		lines = append(lines, "  - Press t to stop the first LocalForward tunnel.")
		lines = append(lines, fmt.Sprintf("  - Current tunnel state: %s (pid=%d).", rt.State, rt.PID))
	} else {
		lines = append(lines, "  - Press t to start the first LocalForward tunnel.")
	}
	if len(h.Forwards) > 1 {
		lines = append(lines, fmt.Sprintf("  - This host has %d forwards; CLI supports selecting a specific one:", len(h.Forwards)))
		lines = append(lines, fmt.Sprintf("    ssh-manager tunnel up %s --forward 1", h.Alias))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m modelUI) renderMainPanels(hostsPanel, detailsPanel string) string {
	width := m.effectiveWidth()
	if width < 96 {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderPanel("Hosts", hostsPanel, width, lipgloss.Color("39")),
			m.renderPanel("Details", detailsPanel, width, lipgloss.Color("69")),
		)
	}
	leftWidth := width / 2
	rightWidth := width - leftWidth
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		m.renderPanel("Hosts", hostsPanel, leftWidth, lipgloss.Color("39")),
		m.renderPanel("Details", detailsPanel, rightWidth, lipgloss.Color("69")),
	)
}

func (m modelUI) helpBlock() string {
	return strings.Join([]string{
		"  Navigation: j/k or arrow keys move selection.",
		"  Filtering: press /, type alias/host text, then Enter.",
		"  Connect: press Enter on selected host.",
		"  Tunnel: press t toggles the first LocalForward for selected host.",
		"  Refresh: press r to reparse ssh config and refresh runtime snapshot.",
		"  Quit: press q (or Ctrl+C) and all managed tunnels are stopped.",
	}, "\n")
}

func (m modelUI) effectiveWidth() int {
	if m.width <= 0 {
		return 100
	}
	return m.width
}

func (m modelUI) renderPanel(title, body string, width int, accent lipgloss.Color) string {
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

func init() {
	_ = os.Setenv("TERM", os.Getenv("TERM"))
}
