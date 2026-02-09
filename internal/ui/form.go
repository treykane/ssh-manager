package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/treykane/ssh-manager/internal/model"
)

// formMode distinguishes between the mode-select, quick-connect, and full-config screens.
type formMode int

const (
	formModeSelect formMode = iota
	formModeQuick
	formModeFull
)

// Field indices for the full configurator form.
const (
	fieldAlias = iota
	fieldHostname
	fieldUser
	fieldPort
	fieldIdentityFile
	fieldProxyJump
	fieldCount
)

// formResult is returned when the user completes the form.
type formResult struct {
	host    model.HostEntry
	connect bool // true = connect immediately after adding
}

// newConnForm holds all state for the "new SSH connection" configurator.
type newConnForm struct {
	mode    formMode
	modeSel int // 0 = quick, 1 = full (for mode selection screen)

	// Quick connect
	quickInput textinput.Model

	// Full configurator
	fields   []textinput.Model
	focusIdx int

	// Persistence choice
	saveToConfig bool

	// Validation error
	errMsg string
}

// newForm creates an initialized form starting at mode selection.
func newForm() *newConnForm {
	f := &newConnForm{
		mode: formModeSelect,
	}

	// Quick connect input.
	qi := textinput.New()
	qi.Placeholder = "user@hostname:port or just hostname"
	qi.CharLimit = 256
	qi.Width = 50
	f.quickInput = qi

	// Full form fields.
	placeholders := []string{
		"my-server (required)",
		"192.168.1.1 or example.com (required)",
		"deploy (optional)",
		"22 (default)",
		"~/.ssh/id_rsa (optional)",
		"bastion.example.com (optional)",
	}
	limits := []int{64, 256, 64, 6, 256, 256}

	f.fields = make([]textinput.Model, fieldCount)
	for i := range f.fields {
		ti := textinput.New()
		ti.Placeholder = placeholders[i]
		ti.CharLimit = limits[i]
		ti.Width = 40
		f.fields[i] = ti
	}

	return f
}

// update processes a key message and returns a formResult if the form is complete.
func (f *newConnForm) update(msg tea.KeyMsg) (*formResult, tea.Cmd) {
	switch f.mode {
	case formModeSelect:
		return f.updateModeSelect(msg)
	case formModeQuick:
		return f.updateQuick(msg)
	case formModeFull:
		return f.updateFull(msg)
	}
	return nil, nil
}

func (f *newConnForm) updateModeSelect(msg tea.KeyMsg) (*formResult, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if f.modeSel < 1 {
			f.modeSel++
		}
	case "k", "up":
		if f.modeSel > 0 {
			f.modeSel--
		}
	case "enter":
		if f.modeSel == 0 {
			f.mode = formModeQuick
			f.quickInput.Focus()
			return nil, f.quickInput.Cursor.BlinkCmd()
		}
		f.mode = formModeFull
		f.focusIdx = 0
		f.fields[0].Focus()
		return nil, f.fields[0].Cursor.BlinkCmd()
	}
	return nil, nil
}

func (f *newConnForm) updateQuick(msg tea.KeyMsg) (*formResult, tea.Cmd) {
	switch msg.String() {
	case "enter":
		host, err := parseQuickConnect(f.quickInput.Value())
		if err != nil {
			f.errMsg = err.Error()
			return nil, nil
		}
		return &formResult{host: host, connect: true}, nil
	default:
		var cmd tea.Cmd
		f.quickInput, cmd = f.quickInput.Update(msg)
		f.errMsg = ""
		return nil, cmd
	}
}

func (f *newConnForm) updateFull(msg tea.KeyMsg) (*formResult, tea.Cmd) {
	switch msg.String() {
	case "tab", "shift+tab":
		// Navigate between fields.
		f.fields[f.focusIdx].Blur()
		if msg.String() == "tab" {
			f.focusIdx = (f.focusIdx + 1) % fieldCount
		} else {
			f.focusIdx = (f.focusIdx - 1 + fieldCount) % fieldCount
		}
		f.fields[f.focusIdx].Focus()
		return nil, f.fields[f.focusIdx].Cursor.BlinkCmd()
	case "ctrl+s":
		f.saveToConfig = !f.saveToConfig
		return nil, nil
	case "enter":
		host, err := f.buildHostEntry()
		if err != nil {
			f.errMsg = err.Error()
			return nil, nil
		}
		return &formResult{host: host, connect: true}, nil
	default:
		var cmd tea.Cmd
		f.fields[f.focusIdx], cmd = f.fields[f.focusIdx].Update(msg)
		f.errMsg = ""
		return nil, cmd
	}
}

func (f *newConnForm) buildHostEntry() (model.HostEntry, error) {
	alias := strings.TrimSpace(f.fields[fieldAlias].Value())
	hostname := strings.TrimSpace(f.fields[fieldHostname].Value())
	user := strings.TrimSpace(f.fields[fieldUser].Value())
	portStr := strings.TrimSpace(f.fields[fieldPort].Value())
	identityFile := strings.TrimSpace(f.fields[fieldIdentityFile].Value())
	proxyJump := strings.TrimSpace(f.fields[fieldProxyJump].Value())

	if alias == "" {
		return model.HostEntry{}, fmt.Errorf("alias is required")
	}
	if hostname == "" {
		return model.HostEntry{}, fmt.Errorf("hostname is required")
	}

	port := 22
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return model.HostEntry{}, fmt.Errorf("port must be 1-65535")
		}
		port = p
	}

	h := model.HostEntry{
		Alias:        alias,
		HostName:     hostname,
		User:         user,
		Port:         port,
		IdentityFile: identityFile,
		ProxyJump:    proxyJump,
		IsAdHoc:      !f.saveToConfig,
	}
	return h, nil
}

// view renders the form panel.
func (f *newConnForm) view(renderPanel func(string, string, int, lipgloss.Color) string, width int) string {
	accent := lipgloss.Color("214")
	switch f.mode {
	case formModeSelect:
		return renderPanel("New Connection", f.modeSelectView(), width, accent)
	case formModeQuick:
		return renderPanel("Quick Connect", f.quickView(), width, accent)
	case formModeFull:
		return renderPanel("New Connection - Full Config", f.fullView(), width, accent)
	}
	return ""
}

func (f *newConnForm) modeSelectView() string {
	var b strings.Builder
	b.WriteString("Choose connection type:\n\n")

	options := []struct {
		label string
		desc  string
	}{
		{"Quick Connect", "Enter user@host:port and connect immediately"},
		{"Full Config", "Configure all SSH options with optional save"},
	}

	for i, opt := range options {
		cursor := "  "
		if i == f.modeSel {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s[%s]  %s\n", cursor, opt.label, opt.desc))
	}

	b.WriteString("\nj/k to select, Enter to confirm, Esc to cancel")
	return b.String()
}

func (f *newConnForm) quickView() string {
	var b strings.Builder
	b.WriteString("Destination:\n\n")
	b.WriteString("  " + f.quickInput.View() + "\n\n")
	b.WriteString("Formats: hostname | user@hostname | hostname:port | user@host:port\n")

	if f.errMsg != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		b.WriteString("\n" + errStyle.Render("Error: "+f.errMsg) + "\n")
	}

	b.WriteString("\nEnter to connect, Esc to cancel")
	return b.String()
}

func (f *newConnForm) fullView() string {
	labels := []string{"Alias:", "Hostname:", "User:", "Port:", "IdentityFile:", "ProxyJump:"}

	var b strings.Builder
	for i, label := range labels {
		cursor := "  "
		if i == f.focusIdx {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s\n", cursor, label, f.fields[i].View()))
	}

	b.WriteString("\n")
	saveMarker := " "
	sessionMarker := "x"
	if f.saveToConfig {
		saveMarker = "x"
		sessionMarker = " "
	}
	b.WriteString(fmt.Sprintf("  Save: (%s) Session only  (%s) Save to ~/.ssh/config\n", sessionMarker, saveMarker))

	if f.errMsg != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		b.WriteString("\n" + errStyle.Render("Error: "+f.errMsg) + "\n")
	}

	b.WriteString("\nTab/Shift-Tab navigate | Ctrl+S toggle save | Enter submit | Esc cancel")
	return b.String()
}

// parseQuickConnect parses a quick-connect string into a HostEntry.
// Supported formats: hostname, user@hostname, hostname:port, user@hostname:port
func parseQuickConnect(input string) (model.HostEntry, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return model.HostEntry{}, fmt.Errorf("destination cannot be empty")
	}

	h := model.HostEntry{Port: 22, IsAdHoc: true}

	// Extract user@ prefix.
	if atIdx := strings.Index(input, "@"); atIdx > 0 {
		h.User = input[:atIdx]
		input = input[atIdx+1:]
	}

	// Extract :port suffix.
	if colonIdx := strings.LastIndex(input, ":"); colonIdx > 0 {
		portStr := input[colonIdx+1:]
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			h.Port = port
			input = input[:colonIdx]
		}
	}

	h.HostName = input
	h.Alias = input

	if h.HostName == "" {
		return model.HostEntry{}, fmt.Errorf("hostname cannot be empty")
	}
	return h, nil
}
