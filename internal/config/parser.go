package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/treykane/ssh-manager/internal/model"
)

type ParseResult struct {
	Hosts    []model.HostEntry
	Warnings []string
}

type rawBlock struct {
	patterns []string
	values   map[string][]string
	source   string
}

// ParseDefault parses ~/.ssh/config with common directives.
func ParseDefault() (ParseResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ParseResult{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return ParseFile(filepath.Join(home, ".ssh", "config"))
}

// ParseFile parses a single root SSH config and expands Include directives.
func ParseFile(path string) (ParseResult, error) {
	seen := map[string]bool{}
	blocks, warnings, err := parseRecursive(path, seen, 0)
	if err != nil {
		return ParseResult{}, err
	}
	hosts := compileHosts(blocks)
	return ParseResult{Hosts: hosts, Warnings: warnings}, nil
}

func parseRecursive(path string, seen map[string]bool, depth int) ([]rawBlock, []string, error) {
	if depth > 16 {
		return nil, nil, fmt.Errorf("include depth exceeded at %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	if seen[abs] {
		return nil, []string{fmt.Sprintf("include cycle skipped: %s", abs)}, nil
	}
	seen[abs] = true

	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{fmt.Sprintf("config file not found: %s", abs)}, nil
		}
		return nil, nil, fmt.Errorf("open %s: %w", abs, err)
	}
	defer f.Close()

	var (
		blocks      []rawBlock
		warnings    []string
		current     = rawBlock{patterns: []string{"*"}, values: map[string][]string{}, source: abs}
		hasHostDecl bool
	)

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = stripInlineComment(line)
		if line == "" {
			continue
		}

		key, value, ok := splitDirective(line)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s:%d invalid directive", abs, lineNo))
			continue
		}
		lowerKey := strings.ToLower(key)

		switch lowerKey {
		case "include":
			for _, pattern := range strings.Fields(value) {
				incPattern := expandHome(pattern)
				if !filepath.IsAbs(incPattern) {
					incPattern = filepath.Join(filepath.Dir(abs), incPattern)
				}
				matches, globErr := filepath.Glob(incPattern)
				if globErr != nil {
					warnings = append(warnings, fmt.Sprintf("%s:%d bad include pattern %q", abs, lineNo, pattern))
					continue
				}
				if len(matches) == 0 {
					warnings = append(warnings, fmt.Sprintf("%s:%d include matched nothing: %q", abs, lineNo, pattern))
				}
				sort.Strings(matches)
				for _, m := range matches {
					childBlocks, childWarnings, childErr := parseRecursive(m, seen, depth+1)
					warnings = append(warnings, childWarnings...)
					if childErr != nil {
						warnings = append(warnings, fmt.Sprintf("include %s failed: %v", m, childErr))
						continue
					}
					blocks = append(blocks, childBlocks...)
				}
			}
		case "host":
			if hasHostDecl || len(current.values) > 0 {
				blocks = append(blocks, current)
			}
			patterns := strings.Fields(value)
			if len(patterns) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s:%d Host missing patterns", abs, lineNo))
				patterns = []string{"*"}
			}
			current = rawBlock{patterns: patterns, values: map[string][]string{}, source: abs}
			hasHostDecl = true
		default:
			current.values[lowerKey] = append(current.values[lowerKey], value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan %s: %w", abs, err)
	}

	if hasHostDecl || len(current.values) > 0 {
		blocks = append(blocks, current)
	}
	return blocks, warnings, nil
}

func compileHosts(blocks []rawBlock) []model.HostEntry {
	aliasSet := map[string]struct{}{}
	for _, b := range blocks {
		for _, p := range b.patterns {
			if isConcreteAlias(p) {
				aliasSet[p] = struct{}{}
			}
		}
	}

	aliases := make([]string, 0, len(aliasSet))
	for a := range aliasSet {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)

	hosts := make([]model.HostEntry, 0, len(aliases))
	for _, alias := range aliases {
		h := model.HostEntry{Alias: alias, HostName: alias, Port: 22}
		for _, b := range blocks {
			if !matchesAny(alias, b.patterns) {
				continue
			}
			if vals := b.values["hostname"]; len(vals) > 0 {
				h.HostName = vals[len(vals)-1]
			}
			if vals := b.values["user"]; len(vals) > 0 {
				h.User = vals[len(vals)-1]
			}
			if vals := b.values["port"]; len(vals) > 0 {
				if p, err := strconv.Atoi(vals[len(vals)-1]); err == nil {
					h.Port = p
				}
			}
			if vals := b.values["identityfile"]; len(vals) > 0 {
				h.IdentityFile = expandHome(vals[len(vals)-1])
			}
			if vals := b.values["proxyjump"]; len(vals) > 0 {
				h.ProxyJump = vals[len(vals)-1]
			}
			if vals := b.values["localforward"]; len(vals) > 0 {
				for _, lf := range vals {
					if fwd, ok := parseLocalForward(lf); ok {
						h.Forwards = append(h.Forwards, fwd)
					}
				}
			}
		}
		hosts = append(hosts, h)
	}
	return hosts
}

func parseLocalForward(v string) (model.ForwardSpec, bool) {
	parts := strings.Fields(v)
	if len(parts) != 2 {
		return model.ForwardSpec{}, false
	}
	localAddr, localPort, ok := parseEndpoint(parts[0], true)
	if !ok {
		return model.ForwardSpec{}, false
	}
	remoteAddr, remotePort, ok := parseEndpoint(parts[1], false)
	if !ok {
		return model.ForwardSpec{}, false
	}
	return model.ForwardSpec{LocalAddr: localAddr, LocalPort: localPort, RemoteAddr: remoteAddr, RemotePort: remotePort}, true
}

func parseEndpoint(s string, local bool) (string, int, bool) {
	if !strings.Contains(s, ":") {
		p, err := strconv.Atoi(s)
		if err != nil {
			return "", 0, false
		}
		if local {
			return "127.0.0.1", p, true
		}
		return "localhost", p, true
	}
	idx := strings.LastIndex(s, ":")
	addr := s[:idx]
	portStr := s[idx+1:]
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, false
	}
	if addr == "" {
		if local {
			addr = "127.0.0.1"
		} else {
			addr = "localhost"
		}
	}
	return addr, p, true
}

func matchesAny(alias string, patterns []string) bool {
	matched := false
	for _, p := range patterns {
		negated := strings.HasPrefix(p, "!")
		pat := strings.TrimPrefix(p, "!")
		ok := globMatch(alias, pat)
		if !ok {
			continue
		}
		if negated {
			return false
		}
		matched = true
	}
	return matched
}

func globMatch(alias, pattern string) bool {
	if pattern == "" {
		return false
	}
	ok, err := filepath.Match(pattern, alias)
	if err != nil {
		return false
	}
	return ok
}

func isConcreteAlias(pattern string) bool {
	if strings.HasPrefix(pattern, "!") {
		return false
	}
	if strings.ContainsAny(pattern, "*?") {
		return false
	}
	return pattern != ""
}

func splitDirective(line string) (key, value string, ok bool) {
	if i := strings.IndexAny(line, " \t"); i > 0 {
		key = strings.TrimSpace(line[:i])
		value = strings.TrimSpace(line[i+1:])
		return key, value, key != "" && value != ""
	}
	if i := strings.Index(line, "="); i > 0 {
		key = strings.TrimSpace(line[:i])
		value = strings.TrimSpace(line[i+1:])
		return key, value, key != "" && value != ""
	}
	return "", "", false
}

func stripInlineComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return strings.TrimSpace(line)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
