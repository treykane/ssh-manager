// Package config parses OpenSSH configuration files into structured HostEntry models.
//
// This parser supports a subset of OpenSSH's ssh_config(5) directives, focusing on
// the ones relevant to ssh-manager's connection and tunnel management features:
//
//   - Host (with wildcard and negation pattern matching)
//   - HostName, User, Port, IdentityFile, ProxyJump
//   - LocalForward (parsed into model.ForwardSpec for tunnel management)
//   - Include (recursive, with glob expansion and cycle detection)
//
// Unsupported or malformed directives are captured as warnings rather than causing
// parse failures. This "best-effort" approach ensures the parser degrades gracefully
// when encountering config files with advanced or proprietary directives.
//
// The parser follows OpenSSH's block semantics:
//   - A "Host" line opens a new block; subsequent directives belong to that block.
//   - Directives before any "Host" line (or in included files) apply to the implicit "*" block.
//   - Multiple blocks can match a single alias; their directives are merged (first match wins
//     per directive, which is OpenSSH's behavior — though this parser takes the last value
//     for simplicity in the merge pass).
//
// Include directives are resolved recursively up to a maximum depth (MaxIncludeDepth = 16)
// to prevent infinite loops. Circular includes are detected by tracking absolute paths
// of already-visited files.
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
	"github.com/treykane/ssh-manager/internal/util"
)

// ParseResult contains the outcome of parsing an SSH config file.
// Hosts is the list of concrete (non-wildcard) host entries extracted from the config.
// Warnings collects non-fatal issues encountered during parsing, such as malformed
// lines, missing include targets, or cycle detection — allowing callers to surface
// these to the user without aborting the entire parse.
type ParseResult struct {
	Hosts    []model.HostEntry
	Warnings []string
}

// rawBlock represents a single "Host <patterns>" block from an SSH config file,
// along with all the key-value directives that belong to it.
//
// Before any "Host" directive is encountered, directives are accumulated into
// an implicit block with patterns = ["*"], which matches all hosts.
//
// Fields:
//   - patterns: the whitespace-separated patterns from the "Host" line (e.g. ["app-*", "!app-staging"]).
//   - values:   a map from lowercase directive name to a list of values. Multiple values
//     can appear for directives like "LocalForward" that are additive.
//   - source:   the absolute path of the file this block was parsed from (for diagnostics).
type rawBlock struct {
	patterns []string
	values   map[string][]string
	source   string
}

// ParseDefault is the main entry point for production use. It parses the user's
// default SSH config file at ~/.ssh/config, including any files referenced by
// Include directives.
//
// Returns a ParseResult with all discovered concrete hosts and any warnings.
// Returns an error only for unrecoverable problems (e.g. cannot determine home directory).
func ParseDefault() (ParseResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ParseResult{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return ParseFile(filepath.Join(home, ".ssh", "config"))
}

// ParseFile parses a single SSH config file at the given path and recursively
// expands any Include directives found within it.
//
// This is the primary entry point for both production use (via ParseDefault) and
// testing (where a temporary config file path can be provided directly).
//
// The parsing pipeline has two phases:
//  1. parseRecursive: reads the file(s) line-by-line, handles Include expansion,
//     and produces a flat list of rawBlocks.
//  2. compileHosts: resolves wildcard patterns, merges directives from matching
//     blocks, and produces the final sorted list of concrete HostEntry values.
func ParseFile(path string) (ParseResult, error) {
	// Track visited files by absolute path to detect include cycles.
	seen := map[string]bool{}
	blocks, warnings, err := parseRecursive(path, seen, 0)
	if err != nil {
		return ParseResult{}, err
	}
	hosts := compileHosts(blocks)
	return ParseResult{Hosts: hosts, Warnings: warnings}, nil
}

// parseRecursive reads and parses a single SSH config file, recursively expanding
// any Include directives. It returns the parsed rawBlocks, any warnings, and an error
// for unrecoverable failures.
//
// Parameters:
//   - path:  the file path to parse (may be relative; will be resolved to absolute).
//   - seen:  set of absolute paths already visited, used for cycle detection.
//   - depth: current recursion depth, bounded by util.MaxIncludeDepth to prevent
//     runaway recursion from deeply nested or circular includes.
//
// The function handles several edge cases gracefully:
//   - Missing config files produce a warning instead of an error (the user may have
//     an Include pointing to an optional file).
//   - Circular includes are detected and skipped with a warning.
//   - Malformed directive lines are skipped with a warning.
//   - Include patterns that match no files produce a warning.
func parseRecursive(path string, seen map[string]bool, depth int) ([]rawBlock, []string, error) {
	// Guard against excessively deep or infinite Include chains.
	if depth > util.MaxIncludeDepth {
		return nil, nil, fmt.Errorf("include depth exceeded at %s (max %d)", path, util.MaxIncludeDepth)
	}

	// Resolve to an absolute path for consistent cycle detection.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}

	// If we've already parsed this file in the current chain, skip it to
	// avoid infinite loops (e.g. file A includes file B which includes file A).
	if seen[abs] {
		return nil, []string{fmt.Sprintf("include cycle skipped: %s", abs)}, nil
	}
	seen[abs] = true

	f, err := os.Open(abs)
	if err != nil {
		// Missing config files are common (e.g. optional includes), so we
		// downgrade this to a warning rather than failing the entire parse.
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{fmt.Sprintf("config file not found: %s", abs)}, nil
		}
		return nil, nil, fmt.Errorf("open %s: %w", abs, err)
	}
	defer f.Close()

	var (
		blocks   []rawBlock
		warnings []string
		// Initialize with an implicit wildcard block that captures any
		// directives appearing before the first "Host" line. These directives
		// apply to all hosts (same as OpenSSH behavior).
		current     = rawBlock{patterns: []string{"*"}, values: map[string][]string{}, source: abs}
		hasHostDecl bool // tracks whether we've seen at least one "Host" directive
	)

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and full-line comments (lines starting with #).
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip inline comments (e.g. "HostName foo.com # production server")
		// while respecting quoted strings.
		line = stripInlineComment(line)
		if line == "" {
			continue
		}

		// Split the line into a directive key and value. SSH config uses
		// whitespace or "=" as the delimiter (e.g. "HostName foo" or "HostName=foo").
		key, value, ok := splitDirective(line)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s:%d invalid directive", abs, lineNo))
			continue
		}

		// Normalize the key to lowercase for case-insensitive matching.
		// SSH config directives are case-insensitive per the specification.
		lowerKey := strings.ToLower(key)

		switch lowerKey {
		case "include":
			// Include directives can contain multiple space-separated glob patterns.
			// Each pattern is expanded, and the resulting files are parsed recursively.
			for _, pattern := range strings.Fields(value) {
				// Expand ~ to the user's home directory (SSH config convention).
				incPattern := expandHome(pattern)

				// Relative paths in Include are resolved relative to the
				// directory containing the current config file.
				if !filepath.IsAbs(incPattern) {
					incPattern = filepath.Join(filepath.Dir(abs), incPattern)
				}

				// Use glob to expand wildcards in the include path
				// (e.g. "conf.d/*.conf" -> ["conf.d/a.conf", "conf.d/b.conf"]).
				matches, globErr := filepath.Glob(incPattern)
				if globErr != nil {
					warnings = append(warnings, fmt.Sprintf("%s:%d bad include pattern %q", abs, lineNo, pattern))
					continue
				}
				if len(matches) == 0 {
					warnings = append(warnings, fmt.Sprintf("%s:%d include matched nothing: %q", abs, lineNo, pattern))
				}

				// Sort matches for deterministic ordering, matching OpenSSH behavior.
				sort.Strings(matches)
				for _, m := range matches {
					childBlocks, childWarnings, childErr := parseRecursive(m, seen, depth+1)
					warnings = append(warnings, childWarnings...)
					if childErr != nil {
						// Include failures are downgraded to warnings so that
						// a broken include doesn't prevent parsing the rest.
						warnings = append(warnings, fmt.Sprintf("include %s failed: %v", m, childErr))
						continue
					}
					blocks = append(blocks, childBlocks...)
				}
			}

		case "host":
			// A "Host" line starts a new block. Flush the current block if it
			// has any content (either a prior Host declaration or pre-Host directives).
			if hasHostDecl || len(current.values) > 0 {
				blocks = append(blocks, current)
			}

			// Parse the patterns from the Host line (e.g. "Host app-* db-*" -> ["app-*", "db-*"]).
			patterns := strings.Fields(value)
			if len(patterns) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s:%d Host missing patterns", abs, lineNo))
				// Fallback to wildcard so subsequent directives aren't lost.
				patterns = []string{"*"}
			}
			current = rawBlock{patterns: patterns, values: map[string][]string{}, source: abs}
			hasHostDecl = true

		default:
			// All other directives are accumulated into the current block's
			// values map. Using append allows directives like "LocalForward"
			// to appear multiple times (they are additive in SSH config).
			current.values[lowerKey] = append(current.values[lowerKey], value)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan %s: %w", abs, err)
	}

	// Don't forget to flush the final block (the file may end without a
	// trailing "Host" line).
	if hasHostDecl || len(current.values) > 0 {
		blocks = append(blocks, current)
	}
	return blocks, warnings, nil
}

// compileHosts resolves the flat list of rawBlocks into concrete HostEntry values.
//
// The compilation process:
//  1. Scan all blocks and collect concrete aliases (non-wildcard, non-negated patterns).
//  2. For each concrete alias, iterate through ALL blocks in order and merge directives
//     from any block whose patterns match the alias.
//  3. For single-valued directives (HostName, User, Port, etc.), the last matching
//     block's value wins. For multi-valued directives (LocalForward), all values
//     from all matching blocks are accumulated.
//  4. The resulting hosts are sorted alphabetically by alias for consistent output.
//
// This approach mirrors how OpenSSH resolves its config: wildcard blocks (Host *)
// provide defaults, and more specific blocks override them.
func compileHosts(blocks []rawBlock) []model.HostEntry {
	// Phase 1: Collect all unique concrete aliases from all blocks.
	// Wildcards (*, app-*) and negations (!staging) are excluded — they only
	// serve as matching patterns, not as host entries themselves.
	aliasSet := map[string]struct{}{}
	for _, b := range blocks {
		for _, p := range b.patterns {
			if isConcreteAlias(p) {
				aliasSet[p] = struct{}{}
			}
		}
	}

	// Sort aliases for deterministic output ordering.
	aliases := make([]string, 0, len(aliasSet))
	for a := range aliasSet {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)

	// Phase 2: For each alias, walk all blocks and merge matching directives.
	hosts := make([]model.HostEntry, 0, len(aliases))
	for _, alias := range aliases {
		// Start with sensible defaults: HostName defaults to the alias itself,
		// and Port defaults to 22 (standard SSH port).
		h := model.HostEntry{Alias: alias, HostName: alias, Port: 22}

		for _, b := range blocks {
			// Skip blocks that don't match this alias (including negation logic).
			if !matchesAny(alias, b.patterns) {
				continue
			}

			// For single-valued directives, take the last value in the list
			// (if a directive appears multiple times in one block, the last wins).
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
				// Expand ~ in identity file paths (e.g. "~/.ssh/id_rsa" -> "/home/user/.ssh/id_rsa").
				h.IdentityFile = expandHome(vals[len(vals)-1])
			}
			if vals := b.values["proxyjump"]; len(vals) > 0 {
				h.ProxyJump = vals[len(vals)-1]
			}

			// LocalForward is additive: each matching block can contribute
			// additional port forwarding rules. This matches OpenSSH behavior
			// where multiple LocalForward lines create multiple tunnels.
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

	// Final sort by alias for consistent output (should already be sorted,
	// but this ensures correctness regardless of block ordering).
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Alias < hosts[j].Alias })
	return hosts
}

// parseLocalForward parses a single "LocalForward" directive value into a ForwardSpec.
//
// OpenSSH supports two formats for LocalForward:
//
//	LocalForward <local_endpoint> <remote_endpoint>
//
// Where each endpoint can be:
//   - A bare port number: "8080"          → defaults to 127.0.0.1 (local) or localhost (remote)
//   - An address:port pair: "0.0.0.0:8080" → explicit bind address
//
// Returns (ForwardSpec, true) on success, or (ForwardSpec{}, false) if the value
// cannot be parsed (malformed lines are silently skipped by the caller).
func parseLocalForward(v string) (model.ForwardSpec, bool) {
	// Split into exactly two whitespace-separated parts: local and remote endpoint.
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

// parseEndpoint parses a single endpoint string (local or remote side of a forward).
//
// Accepted formats:
//   - "8080"          → bare port, address defaults based on the 'local' flag
//   - "127.0.0.1:8080" → explicit address and port
//
// The 'local' parameter controls the default address:
//   - local=true  → default address is "127.0.0.1" (bind to loopback only)
//   - local=false → default address is "localhost"
//
// Returns (address, port, ok). Returns ok=false if the port is not a valid integer.
func parseEndpoint(s string, local bool) (string, int, bool) {
	// If no colon is present, the entire string should be a port number.
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

	// Split on the last colon to handle IPv4 addresses (e.g. "192.168.1.1:8080").
	// Note: IPv6 addresses in brackets (e.g. "[::1]:8080") are not currently handled.
	idx := strings.LastIndex(s, ":")
	addr := s[:idx]
	portStr := s[idx+1:]
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, false
	}

	// If the address portion is empty (e.g. ":8080"), use the default.
	if addr == "" {
		if local {
			addr = "127.0.0.1"
		} else {
			addr = "localhost"
		}
	}
	return addr, p, true
}

// matchesAny checks whether a given alias matches any of the provided patterns.
// Supports OpenSSH-style pattern matching:
//   - Wildcards: "app-*" matches "app-1", "app-staging", etc.
//   - Negations: "!staging" excludes "staging" even if a prior pattern matched it.
//
// The function processes patterns in order: if a negated pattern matches, the
// alias is immediately rejected (returns false). Otherwise, if any non-negated
// pattern matches, the alias is accepted.
func matchesAny(alias string, patterns []string) bool {
	matched := false
	for _, p := range patterns {
		negated := strings.HasPrefix(p, "!")
		pat := strings.TrimPrefix(p, "!")
		ok := globMatch(alias, pat)
		if !ok {
			continue
		}
		// Negated patterns act as exclusions: if the alias matches a negated
		// pattern, it is immediately rejected regardless of other matches.
		if negated {
			return false
		}
		matched = true
	}
	return matched
}

// globMatch performs a single glob pattern match using filepath.Match semantics.
// Returns false for empty patterns or if the pattern syntax is invalid.
// Supports *, ?, and [] character class notation from filepath.Match.
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

// isConcreteAlias returns true if the pattern represents a concrete host alias
// (as opposed to a wildcard or negation pattern).
//
// Concrete aliases are used to generate HostEntry records. Wildcard patterns
// (containing * or ?) and negated patterns (starting with !) only affect which
// blocks match during directive merging — they don't produce host entries.
func isConcreteAlias(pattern string) bool {
	if strings.HasPrefix(pattern, "!") {
		return false
	}
	if strings.ContainsAny(pattern, "*?") {
		return false
	}
	return pattern != ""
}

// splitDirective splits an SSH config line into its key and value components.
//
// Supports two delimiter styles used in SSH config files:
//   - Whitespace-delimited: "HostName foo.example.com"
//   - Equals-delimited:     "HostName=foo.example.com"
//
// Returns (key, value, true) on success, or ("", "", false) if the line cannot
// be split into a non-empty key and value.
func splitDirective(line string) (key, value string, ok bool) {
	// First, try splitting on whitespace (the more common format).
	if i := strings.IndexAny(line, " \t"); i > 0 {
		key = strings.TrimSpace(line[:i])
		value = strings.TrimSpace(line[i+1:])
		return key, value, key != "" && value != ""
	}
	// Fall back to splitting on "=" (alternative SSH config syntax).
	if i := strings.Index(line, "="); i > 0 {
		key = strings.TrimSpace(line[:i])
		value = strings.TrimSpace(line[i+1:])
		return key, value, key != "" && value != ""
	}
	return "", "", false
}

// stripInlineComment removes an inline comment from a config line while
// respecting quoted strings.
//
// In SSH config, a "#" character outside of double quotes starts a comment
// that extends to the end of the line. For example:
//
//	HostName foo.com # production server   → "HostName foo.com"
//	HostName "foo#bar.com"                 → "HostName \"foo#bar.com\"" (unchanged)
//
// Returns the line with the comment stripped and whitespace trimmed.
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

// expandHome expands a leading "~/" in a file path to the user's home directory.
// This is a common convention in SSH config for paths like IdentityFile and Include.
//
// Example: "~/.ssh/id_rsa" → "/home/user/.ssh/id_rsa"
//
// If the home directory cannot be determined, the path is returned unchanged.
// Non-tilde paths are returned as-is.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
