// Package appconfig manages application configuration and runtime file paths.
package appconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UIConfig contains TUI display settings.
type UIConfig struct {
	RefreshSeconds int `yaml:"refresh_seconds"`
}

// Config holds application-level configuration.
type Config struct {
	DefaultHealthCommand string   `yaml:"default_health_command"`
	UI                   UIConfig `yaml:"ui"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		DefaultHealthCommand: "uptime",
		UI:                   UIConfig{RefreshSeconds: 3},
	}
}

// ConfigDir returns the application config directory path.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config/ssh-manager.
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

// RuntimeFilePath returns the full path to runtime.json.
func RuntimeFilePath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "runtime.json"), nil
}

// Load reads config.yaml from the config directory.
// If the file doesn't exist, creates it with defaults.
func Load() (Config, error) {
	d, err := ConfigDir()
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return Config{}, err
	}
	path := filepath.Join(d, "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := Default()
			if err := Save(cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
		return Config{}, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.UI.RefreshSeconds <= 0 {
		cfg.UI.RefreshSeconds = 3
	}
	if cfg.DefaultHealthCommand == "" {
		cfg.DefaultHealthCommand = "uptime"
	}
	return cfg, nil
}

// Save writes config to config.yaml.
func Save(cfg Config) error {
	d, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	path := filepath.Join(d, "config.yaml")
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
