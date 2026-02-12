// Package appconfig manages application-level configuration and runtime file paths
// for ssh-manager.
//
// This package is responsible for:
//   - Resolving the configuration directory (respects XDG_CONFIG_HOME)
//   - Loading and saving config.yaml (application preferences)
//   - Providing the path to runtime.json (tunnel state persistence)
//
// Directory layout under the config dir (default: ~/.config/ssh-manager/):
//
//	config.yaml   — User-editable application settings (refresh interval, health command, etc.)
//	runtime.json  — Auto-managed tunnel runtime state (written by internal/tunnel)
//
// The XDG_CONFIG_HOME environment variable is respected for portability and to
// enable test isolation (tests set it to t.TempDir() to avoid touching real config).
package appconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UIConfig contains settings that control the TUI dashboard's behavior.
type UIConfig struct {
	// RefreshSeconds controls how often the dashboard refreshes tunnel status
	// and health-check data. Must be a positive integer; values <= 0 are
	// clamped to the default (3 seconds) during Load().
	RefreshSeconds int `yaml:"refresh_seconds"`
}

const (
	BindPolicyLoopbackOnly = "loopback-only"
	BindPolicyAllowPublic  = "allow-public"

	HostKeyPolicyStrict    = "strict"
	HostKeyPolicyAcceptNew = "accept-new"
	HostKeyPolicyInsecure  = "insecure"
)

// SecurityConfig controls transport and tunnel safety defaults.
type SecurityConfig struct {
	// BindPolicy controls whether local tunnel binds may use public interfaces.
	// Allowed values: loopback-only, allow-public.
	BindPolicy string `yaml:"bind_policy"`

	// HostKeyPolicy controls SSH host key verification behavior.
	// Allowed values: strict, accept-new, insecure.
	HostKeyPolicy string `yaml:"host_key_policy"`

	// AuditLogEnabled reserves an opt-in switch for security audit logging.
	AuditLogEnabled bool `yaml:"audit_log_enabled"`

	// RedactErrors strips home paths and .ssh details from user-facing errors.
	RedactErrors bool `yaml:"redact_errors"`
}

// Config holds the top-level application configuration, loaded from config.yaml.
// Fields map directly to YAML keys for straightforward editing by users.
type Config struct {
	// DefaultHealthCommand is the shell command executed on remote hosts to
	// verify connectivity (e.g. "uptime"). This may be used in future health-check
	// features. Defaults to "uptime" if empty or missing from the config file.
	DefaultHealthCommand string `yaml:"default_health_command"`

	// UI contains TUI-specific display and refresh settings.
	UI UIConfig `yaml:"ui"`

	// Security contains app-wide security behavior defaults.
	Security SecurityConfig `yaml:"security"`
}

// Default returns the default configuration values. These are used when:
//   - No config.yaml file exists yet (first run)
//   - A config.yaml field is missing or has an invalid value
//
// Defaults:
//   - DefaultHealthCommand: "uptime"
//   - UI.RefreshSeconds: 3
func Default() Config {
	return Config{
		DefaultHealthCommand: "uptime",
		UI:                   UIConfig{RefreshSeconds: 3},
		Security: SecurityConfig{
			BindPolicy:      BindPolicyLoopbackOnly,
			HostKeyPolicy:   HostKeyPolicyStrict,
			AuditLogEnabled: false,
			RedactErrors:    true,
		},
	}
}

func NormalizeBindPolicy(policy string) string {
	switch policy {
	case BindPolicyAllowPublic:
		return BindPolicyAllowPublic
	default:
		return BindPolicyLoopbackOnly
	}
}

func NormalizeHostKeyPolicy(policy string) string {
	switch policy {
	case HostKeyPolicyAcceptNew:
		return HostKeyPolicyAcceptNew
	case HostKeyPolicyInsecure:
		return HostKeyPolicyInsecure
	default:
		return HostKeyPolicyStrict
	}
}

// ConfigDir returns the absolute path to the ssh-manager configuration directory.
//
// Resolution order:
//  1. If XDG_CONFIG_HOME is set and non-empty, returns $XDG_CONFIG_HOME/ssh-manager
//  2. Otherwise, returns ~/.config/ssh-manager
//
// This function does NOT create the directory; callers should use os.MkdirAll
// if they intend to write files into it.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ssh-manager"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "ssh-manager"), nil
}

// RuntimeFilePath returns the full path to runtime.json, which is used by the
// tunnel manager (internal/tunnel) to persist tunnel state across app restarts.
//
// The file is stored inside the config directory alongside config.yaml.
// Example: ~/.config/ssh-manager/runtime.json
func RuntimeFilePath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "runtime.json"), nil
}

// Load reads and parses config.yaml from the configuration directory.
//
// Behavior:
//   - If config.yaml does not exist, a new file is created with Default() values
//     and those defaults are returned. This provides a seamless first-run experience.
//   - If the file exists but contains invalid YAML, an error is returned.
//   - Missing or invalid field values are replaced with sensible defaults:
//     RefreshSeconds <= 0 is clamped to 3, empty DefaultHealthCommand becomes "uptime".
//   - The config directory is created (with parents) if it doesn't already exist.
func Load() (Config, error) {
	d, err := ConfigDir()
	if err != nil {
		return Config{}, err
	}

	// Ensure the config directory exists before attempting to read or write.
	if err := os.MkdirAll(d, 0o700); err != nil {
		return Config{}, err
	}

	path := filepath.Join(d, "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// First run: create config.yaml with defaults so users can discover
			// and edit the available settings.
			cfg := Default()
			if err := Save(cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
		return Config{}, err
	}

	// Start from defaults so that any fields missing from the YAML file
	// retain sensible values rather than being zero-valued.
	cfg := Default()
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	// Clamp invalid values to defaults. This guards against user typos like
	// "refresh_seconds: 0" or "refresh_seconds: -1" which would cause the
	// TUI to spin at maximum speed.
	if cfg.UI.RefreshSeconds <= 0 {
		cfg.UI.RefreshSeconds = 3
	}
	if cfg.DefaultHealthCommand == "" {
		cfg.DefaultHealthCommand = "uptime"
	}
	cfg.Security.BindPolicy = NormalizeBindPolicy(cfg.Security.BindPolicy)
	cfg.Security.HostKeyPolicy = NormalizeHostKeyPolicy(cfg.Security.HostKeyPolicy)

	return cfg, nil
}

// Save writes the given Config to config.yaml in the configuration directory.
// The config directory is created (with parents) if it doesn't already exist.
//
// The file is written with 0600 permissions to keep local policy settings private.
func Save(cfg Config) error {
	d, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	path := filepath.Join(d, "config.yaml")
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
