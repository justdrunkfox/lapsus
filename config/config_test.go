package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Hotkey.Source != "niri" {
		t.Errorf("default hotkey source = %q, want %q", c.Hotkey.Source, "niri")
	}
	if c.Hotkey.Key != "Ctrl+Alt+K" {
		t.Errorf("default hotkey key = %q, want %q", c.Hotkey.Key, "Ctrl+Alt+K")
	}
	if c.Capture.Method != "clipboard" {
		t.Errorf("default capture method = %q, want %q", c.Capture.Method, "clipboard")
	}
	if c.AutoDetect.Mode != "both" {
		t.Errorf("default autodetect mode = %q, want %q", c.AutoDetect.Mode, "both")
	}
	if !strings.Contains(c.Dictionary.UserDir, "lapsus") {
		t.Errorf("default user dict dir should point at lapsus, got %q", c.Dictionary.UserDir)
	}
}

func TestParseTOML(t *testing.T) {
	toml := `
[hotkey]
source = "evdev"
key = "Super+K"

[capture]
method = "cut"

[autodetect]
mode = "continuous"

[dictionary]
user_dir = "/custom/dicts"
`
	c, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if c.Hotkey.Source != "evdev" {
		t.Errorf("source = %q, want %q", c.Hotkey.Source, "evdev")
	}
	if c.Hotkey.Key != "Super+K" {
		t.Errorf("key = %q, want %q", c.Hotkey.Key, "Super+K")
	}
	if c.Capture.Method != "cut" {
		t.Errorf("method = %q, want %q", c.Capture.Method, "cut")
	}
	if c.AutoDetect.Mode != "continuous" {
		t.Errorf("mode = %q, want %q", c.AutoDetect.Mode, "continuous")
	}
	if c.Dictionary.UserDir != "/custom/dicts" {
		t.Errorf("user_dir = %q, want %q", c.Dictionary.UserDir, "/custom/dicts")
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
	if c.Hotkey.Source != "niri" {
		t.Errorf("hotkey source should be default %q", c.Hotkey.Source)
	}
}

func TestValidate(t *testing.T) {
	c := Defaults()
	c.Hotkey.Source = "invalid"
	err := c.Validate()
	if err == nil {
		t.Error("expected validation error for invalid hotkey source")
	}

	c.Hotkey.Source = "niri"
	c.Capture.Method = "invalid"
	err = c.Validate()
	if err == nil {
		t.Error("expected validation error for invalid capture method")
	}

	c.Capture.Method = "clipboard"
	err = c.Validate()
	if err != nil {
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
