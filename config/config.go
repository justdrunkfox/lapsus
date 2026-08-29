// Package config loads and validates the lapsus TOML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config holds the full application configuration.
type Config struct {
	Hotkey struct {
		Source string `toml:"source"`
		Key    string `toml:"key"`
		// Terminals lists niri app_id values of terminal emulators.
		// Terminals get a different fix path (mouse selection +
		// clipboard paste) than GUI apps.
		Terminals []string `toml:"terminals"`
	} `toml:"hotkey"`

	Capture struct {
		Method string `toml:"method"`
	} `toml:"capture"`

	AutoDetect struct {
		Mode string `toml:"mode"`
	} `toml:"autodetect"`

	Fix struct {
		// SwitchLayout switches to the layout of the corrected word
		// after injecting it. Must happen after injection (niri#3568).
		SwitchLayout bool `toml:"switch_layout"`
		// PauseMs is the delay after synthetic key events, giving the
		// focused application time to act before the next step.
		PauseMs int `toml:"pause_ms"`
	} `toml:"fix"`

	Daemon struct {
		// ExcludeAppIDs lists niri app_id values where auto-fix is
		// disabled (VMs, games, remote desktop...).
		ExcludeAppIDs []string `toml:"exclude_app_ids"`
		// BoundaryPauseMs is the idle time after which the buffered word
		// counts as finished even without a punctuation/space boundary.
		// Generous on purpose: a too-short pause splits slow-typed words
		// into fragments, and a fragment can be "fixed" wrongly.
		BoundaryPauseMs int `toml:"boundary_pause_ms"`
		// MinWordLen is the shortest word the daemon will touch. Short
		// fragments (mid-word pauses, stray letters) are risky to fix
		// automatically; the manual hotkey path has no such limit.
		MinWordLen int `toml:"min_word_len"`
	} `toml:"daemon"`

	Dictionary struct {
		UserDir string `toml:"user_dir"`
	} `toml:"dictionary"`
}

// DefaultTerminals is the default hotkey.terminals list: common terminal
// emulators' niri app_id values.
var DefaultTerminals = []string{"foot", "kitty", "Alacritty", "wezterm", "ghostty", "st"}

// Defaults returns a Config with sensible defaults.
func Defaults() *Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	c := &Config{}
	c.Hotkey.Source = "niri"
	c.Hotkey.Key = "Ctrl+Alt+K"
	c.Hotkey.Terminals = append(c.Hotkey.Terminals, DefaultTerminals...)
	c.Capture.Method = "clipboard"
	c.AutoDetect.Mode = "both"
	c.Fix.SwitchLayout = true
	c.Fix.PauseMs = 50
	c.Daemon.BoundaryPauseMs = 1000
	c.Daemon.MinWordLen = 3
	c.Dictionary.UserDir = filepath.Join(home, ".config", "lapsus", "dicts")
	return c
}

// Parse parses TOML bytes into a Config, merging with defaults.
func Parse(data []byte) (*Config, error) {
	c := Defaults()
	if len(data) > 0 {
		err := toml.Unmarshal(data, c)
		if err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	return c, nil
}

// LoadFile reads and parses a config file. A missing file yields defaults.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Validate checks that all config values are valid.
func (c *Config) Validate() error {
	var issues []string

	switch c.Hotkey.Source {
	case "niri", "evdev":
	default:
		issues = append(issues, fmt.Sprintf("hotkey.source: must be 'niri' or 'evdev', got %q", c.Hotkey.Source))
	}

	switch c.Capture.Method {
	case "clipboard", "cut":
	default:
		issues = append(issues, fmt.Sprintf("capture.method: must be 'clipboard' or 'cut', got %q", c.Capture.Method))
	}

	switch c.AutoDetect.Mode {
	case "hotkey", "continuous", "both", "":
	default:
		issues = append(issues, fmt.Sprintf("autodetect.mode: must be 'hotkey', 'continuous', 'both', or '', got %q", c.AutoDetect.Mode))
	}

	if c.Fix.PauseMs < 0 || c.Fix.PauseMs > 2000 {
		issues = append(issues, fmt.Sprintf("fix.pause_ms: must be in [0, 2000], got %d", c.Fix.PauseMs))
	}

	if c.Daemon.BoundaryPauseMs < 50 || c.Daemon.BoundaryPauseMs > 5000 {
		issues = append(issues, fmt.Sprintf("daemon.boundary_pause_ms: must be in [50, 5000], got %d", c.Daemon.BoundaryPauseMs))
	}

	if c.Daemon.MinWordLen < 1 || c.Daemon.MinWordLen > 10 {
		issues = append(issues, fmt.Sprintf("daemon.min_word_len: must be in [1, 10], got %d", c.Daemon.MinWordLen))
	}

	if len(issues) > 0 {
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}
