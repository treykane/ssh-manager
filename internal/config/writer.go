package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/util"
)

// AppendHostEntry appends a formatted Host block to the user's ~/.ssh/config file.
// The block is appended at the end of the file, which means it has the lowest
// priority in OpenSSH's first-match-wins resolution.
func AppendHostEntry(entry model.HostEntry) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	path := filepath.Join(home, ".ssh", "config")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ssh config: %w", err)
	}

	block := FormatHostBlock(entry)

	// Ensure separation from existing content.
	var prefix string
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open ssh config for append: %w", err)
	}
	defer f.Close()

	_, err = f.WriteString(prefix + "\n" + block)
	if err != nil {
		return fmt.Errorf("write host block: %w", err)
	}
	return nil
}

// FormatHostBlock produces a properly formatted SSH config Host block string
// from the given HostEntry. Only non-empty, non-default fields are included.
func FormatHostBlock(entry model.HostEntry) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Host %s\n", entry.Alias))
	if entry.HostName != "" && entry.HostName != entry.Alias {
		b.WriteString(fmt.Sprintf("  HostName %s\n", entry.HostName))
	}
	if entry.User != "" {
		b.WriteString(fmt.Sprintf("  User %s\n", entry.User))
	}
	if entry.Port != 0 && entry.Port != 22 {
		b.WriteString(fmt.Sprintf("  Port %d\n", entry.Port))
	}
	if entry.IdentityFile != "" {
		b.WriteString(fmt.Sprintf("  IdentityFile %s\n", entry.IdentityFile))
	}
	if entry.ProxyJump != "" {
		b.WriteString(fmt.Sprintf("  ProxyJump %s\n", entry.ProxyJump))
	}
	for _, fwd := range entry.Forwards {
		local := fmt.Sprintf("%s:%d",
			util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort)
		remote := fmt.Sprintf("%s:%d",
			util.NormalizeAddr(fwd.RemoteAddr, "localhost"), fwd.RemotePort)
		b.WriteString(fmt.Sprintf("  LocalForward %s %s\n", local, remote))
	}
	return b.String()
}

// ValidateAlias checks whether a proposed alias is valid and does not conflict
// with an existing host entry in the SSH config.
func ValidateAlias(alias string) error {
	if strings.TrimSpace(alias) == "" {
		return fmt.Errorf("alias cannot be empty")
	}
	if strings.ContainsAny(alias, " \t*?!") {
		return fmt.Errorf("alias cannot contain spaces or wildcard characters")
	}

	res, err := ParseDefault()
	if err != nil {
		return nil // If we can't parse, allow the alias
	}
	for _, h := range res.Hosts {
		if strings.EqualFold(h.Alias, alias) {
			return fmt.Errorf("alias %q already exists in SSH config", alias)
		}
	}
	return nil
}
