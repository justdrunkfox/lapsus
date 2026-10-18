package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Capture.Method != "clipboard" {
		t.Errorf("default capture method = %q, want %q", c.Capture.Method, "clipboard")
	}
	if c.Daemon.UndoWindowMs != 2000 {
		t.Errorf("default undo_window_ms = %d, want 2000", c.Daemon.UndoWindowMs)
	}
	if !strings.Contains(c.Dictionary.UserDir, "lapsus") {
		t.Errorf("default user dict dir should point at lapsus, got %q", c.Dictionary.UserDir)
	}
}

func TestValidateUndoWindow(t *testing.T) {
	c := Defaults()
	c.Daemon.UndoWindowMs = 150
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for undo_window_ms=150")
	}
	c.Daemon.UndoWindowMs = 0
	if err := c.Validate(); err != nil {
		t.Errorf("undo_window_ms=0 (disabled) should validate: %v", err)
	}
	c.Daemon.UndoWindowMs = 2000
	if err := c.Validate(); err != nil {
		t.Errorf("undo_window_ms=2000 should validate: %v", err)
	}
}

func TestParseTOML(t *testing.T) {
	toml := `
[capture]
method = "cut"

[dictionary]
user_dir = "/custom/dicts"
`
	c, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if c.Capture.Method != "cut" {
		t.Errorf("method = %q, want %q", c.Capture.Method, "cut")
	}
	if c.Dictionary.UserDir != "/custom/dicts" {
		t.Errorf("user_dir = %q, want %q", c.Dictionary.UserDir, "/custom/dicts")
	}
}

func TestParseIgnoresRemovedKeys(t *testing.T) {
	// hotkey.source and autodetect.mode were removed from the config
	// (dead keys); existing user configs carrying them must keep parsing.
	toml := `
[hotkey]
source = "evdev"
key = "Super+K"

[autodetect]
mode = "continuous"
`
	c, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if c.Capture.Method != "clipboard" {
		t.Errorf("unknown keys must not disturb defaults, method = %q", c.Capture.Method)
	}
}

func TestParsePartial(t *testing.T) {
	toml := `[capture]
method = "cut"
`
	c, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if c.Capture.Method != "cut" {
		t.Errorf("method = %q, want %q", c.Capture.Method, "cut")
	}
}

func TestValidate(t *testing.T) {
	c := Defaults()
	c.Capture.Method = "invalid"
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for invalid capture method")
	}

	c.Capture.Method = "clipboard"
	if err := c.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestDefaultsFix(t *testing.T) {
	c := Defaults()
	if !c.Fix.SwitchLayout {
		t.Error("default fix.switch_layout should be true")
	}
	if c.Fix.PauseMs != 50 {
		t.Errorf("default fix.pause_ms = %d, want 50", c.Fix.PauseMs)
	}
	found := false
	for _, term := range c.Hotkey.Terminals {
		if term == "foot" {
			found = true
		}
	}
	if !found {
		t.Errorf("default hotkey.terminals should contain \"foot\", got %v", c.Hotkey.Terminals)
	}
}

func TestParseFixSection(t *testing.T) {
	toml := `
[hotkey]
terminals = ["foot", "my-term"]

[fix]
switch_layout = false
pause_ms = 120
`
	c, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(c.Hotkey.Terminals) != 2 || c.Hotkey.Terminals[1] != "my-term" {
		t.Errorf("terminals = %v, want [foot my-term]", c.Hotkey.Terminals)
	}
	if c.Fix.SwitchLayout {
		t.Error("switch_layout = true, want false")
	}
	if c.Fix.PauseMs != 120 {
		t.Errorf("pause_ms = %d, want 120", c.Fix.PauseMs)
	}
}

func TestValidatePauseMs(t *testing.T) {
	c := Defaults()
	c.Fix.PauseMs = -1
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for negative pause_ms")
	}
	c.Fix.PauseMs = 5000
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for huge pause_ms")
	}
	c.Fix.PauseMs = 50
	if err := c.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestDefaultsFeedback(t *testing.T) {
	c := Defaults()
	if !c.Daemon.Tray {
		t.Error("default daemon.tray should be true")
	}
	if !c.Daemon.RememberWindowLayout {
		t.Error("default daemon.remember_window_layout should be true")
	}
}

func TestDefaultsFeedbackValues(t *testing.T) {
	c := Defaults()
	if !c.Feedback.Notify {
		t.Error("default feedback.notify should be true")
	}
	if c.Feedback.Sound != "bell" {
		t.Errorf("default feedback.sound = %q, want bell", c.Feedback.Sound)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	c := Defaults()
	c.Feedback.Notify = false
	c.Feedback.Sound = ""
	c.Daemon.MinWordLen = 4
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if loaded.Feedback.Notify || loaded.Feedback.Sound != "" || loaded.Daemon.MinWordLen != 4 {
		t.Errorf("roundtrip mismatch: %+v", loaded)
	}
}

func TestSaveCreatesMissingDir(t *testing.T) {
	path := t.TempDir() + "/lapsus/nested/config.toml" // dir does not exist
	if err := Save(path, Defaults()); err != nil {
		t.Fatalf("Save into missing dir: %v", err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Fatalf("saved config unreadable: %v", err)
	}
}

func TestValidateDefaultLayout(t *testing.T) {
	c := Defaults()
	c.Daemon.DefaultLayout = "de"
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for default_layout=de")
	}
	c.Daemon.DefaultLayout = "ru"
	if err := c.Validate(); err != nil {
		t.Errorf("default_layout=ru should validate: %v", err)
	}
	c.Daemon.DefaultLayout = ""
	if err := c.Validate(); err != nil {
		t.Errorf("default_layout=\"\" should validate: %v", err)
	}
}
