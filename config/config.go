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
		// Terminals lists niri app_id values of terminal emulators.
		// Terminals get a different fix path (mouse selection +
		// clipboard paste) than GUI apps.
		Terminals []string `toml:"terminals"`
	} `toml:"hotkey"`

	Capture struct {
		Method string `toml:"method"`
	} `toml:"capture"`

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
		// SwitchLayout lets the daemon move the layout to the fixed
		// words' language after a run of fixes. The explicit hotkey
		// also moves the layout (fix.switch_layout).
		SwitchLayout bool `toml:"switch_layout"`
		// SwitchAfterWords is how many consecutive fixes to the same
		// language happen before the layout moves: 1 = every fix, 2+ =
		// wait for a run of wrong-layout words so a single fixed word
		// doesn't yank the layout around.
		SwitchAfterWords int `toml:"switch_after_words"`
		// SwitchConfidenceK switches the layout immediately (skipping
		// the run counter) when a fixed word is real in BOTH layout
		// dictionaries and the flipped reading scores K times higher —
		// an unambiguous wrong-layout episode on an ambiguous word.
		// 0 (default) disables it: the immediate switch awaits a
		// replay corpus (see TODO.md).
		SwitchConfidenceK int `toml:"switch_confidence_k"`
		// BoundaryPauseMs is the idle time after which the buffered word
		// counts as finished even without a separator. 0 (default)
		// disables it: words complete only on the space separator, so a
		// thinking pause can never split a word into fragments. A
		// non-zero value enables the pause boundary at the user's own
		// risk of fragments.
		BoundaryPauseMs int `toml:"boundary_pause_ms"`
		// MinWordLen is the shortest word the daemon will touch. Short
		// fragments (mid-word pauses, stray letters) are risky to fix
		// automatically; the manual hotkey path has no such limit.
		MinWordLen int `toml:"min_word_len"`
		// Tray shows the tray icon with layout flag and toggles.
		Tray bool `toml:"tray"`
		// RememberWindowLayout restores, on window focus, the language
		// last typed in that application (learned passively from
		// keystrokes, per app_id).
		RememberWindowLayout bool `toml:"remember_window_layout"`
		// DefaultLayout is the language for windows of applications the
		// daemon has not learned yet: "" (off), "en" or "ru".
		DefaultLayout string `toml:"default_layout"`
		// InjectMethod selects the injection backend: "wtype" (keysyms
		// via the compositor) or "uinput" (raw keycodes via a virtual
		// kernel keyboard; needs access to /dev/uinput).
		InjectMethod string `toml:"inject_method"`
		// DoubleAltFlip flips the buffered word to the other layout and
		// moves the layout there on a double-tap of the left Alt.
		DoubleAltFlip bool `toml:"double_alt_flip"`
		// DoubleAltWindowMs is the max gap between the two Alt taps.
		DoubleAltWindowMs int `toml:"double_alt_window_ms"`
		// UndoWindowMs is how long after an auto-fix the fix key reverts
		// it instead of toggling: the press restores the word as typed,
		// resets the fix-run and takes back the fix's layout move.
		// 0 disables the undo; beyond the window the key toggles as
		// usual.
		UndoWindowMs int `toml:"undo_window_ms"`
	} `toml:"daemon"`

	Feedback struct {
		// Notify pops a desktop notification after a flip
		// (needs libnotify-bin: sudo apt install libnotify-bin).
		Notify bool `toml:"notify"`
		// Sound is a path to an audio file or "bell" for the
		// freedesktop bell; "" disables sound.
		Sound string `toml:"sound"`
	} `toml:"feedback"`

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
	c.Hotkey.Terminals = append(c.Hotkey.Terminals, DefaultTerminals...)
	c.Capture.Method = "clipboard"
	c.Fix.SwitchLayout = true
	c.Fix.PauseMs = 50
	c.Daemon.BoundaryPauseMs = 0
	c.Daemon.MinWordLen = 3
	c.Daemon.SwitchLayout = true
	c.Daemon.SwitchAfterWords = 2
	c.Daemon.SwitchConfidenceK = 0
	c.Daemon.DoubleAltFlip = false
	c.Daemon.DoubleAltWindowMs = 1000
	c.Daemon.UndoWindowMs = 2000
	c.Daemon.Tray = true
	c.Daemon.RememberWindowLayout = true
	c.Daemon.DefaultLayout = ""
	c.Daemon.InjectMethod = "wtype"
	c.Feedback.Notify = true
	c.Feedback.Sound = "bell"
	c.Dictionary.UserDir = filepath.Join(home, ".config", "lapsus", "dicts")
	return c
}

// Save writes the config back to a TOML file (used by the tray toggles
// to persist runtime switches).
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
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

	switch c.Capture.Method {
	case "clipboard", "cut":
	default:
		issues = append(issues, fmt.Sprintf("capture.method: must be 'clipboard' or 'cut', got %q", c.Capture.Method))
	}

	if c.Fix.PauseMs < 0 || c.Fix.PauseMs > 2000 {
		issues = append(issues, fmt.Sprintf("fix.pause_ms: must be in [0, 2000], got %d", c.Fix.PauseMs))
	}

	if ms := c.Daemon.BoundaryPauseMs; ms != 0 && (ms < 50 || ms > 5000) {
		issues = append(issues, fmt.Sprintf("daemon.boundary_pause_ms: must be 0 (disabled) or in [50, 5000], got %d", ms))
	}

	switch c.Daemon.InjectMethod {
	case "wtype", "uinput":
	default:
		issues = append(issues, fmt.Sprintf("daemon.inject_method: must be \"wtype\" or \"uinput\", got %q", c.Daemon.InjectMethod))
	}

	switch strings.ToLower(c.Daemon.DefaultLayout) {
	case "", "en", "ru":
	default:
		issues = append(issues, fmt.Sprintf("daemon.default_layout: must be \"\", \"en\" or \"ru\", got %q", c.Daemon.DefaultLayout))
	}

	if w := c.Daemon.DoubleAltWindowMs; w != 0 && (w < 100 || w > 5000) {
		issues = append(issues, fmt.Sprintf("daemon.double_alt_window_ms: must be 0 (off uses 1000) or in [100, 5000], got %d", w))
	}

	if w := c.Daemon.UndoWindowMs; w != 0 && (w < 200 || w > 10000) {
		issues = append(issues, fmt.Sprintf("daemon.undo_window_ms: must be 0 (disabled) or in [200, 10000], got %d", w))
	}

	if w := c.Daemon.SwitchAfterWords; w < 1 || w > 10 {
		issues = append(issues, fmt.Sprintf("daemon.switch_after_words: must be in [1, 10], got %d", w))
	}

	if w := c.Daemon.SwitchConfidenceK; w != 0 && (w < 2 || w > 1000) {
		issues = append(issues, fmt.Sprintf("daemon.switch_confidence_k: must be 0 (disabled) or in [2, 1000], got %d", w))
	}

	if c.Daemon.MinWordLen < 1 || c.Daemon.MinWordLen > 10 {
		issues = append(issues, fmt.Sprintf("daemon.min_word_len: must be in [1, 10], got %d", c.Daemon.MinWordLen))
	}

	if len(issues) > 0 {
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}
