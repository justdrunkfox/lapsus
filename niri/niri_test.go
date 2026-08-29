package niri

import (
	"errors"
	"strings"
	"testing"
)

func testClient(stdout string) *Client {
	return &Client{Run: func(name string, args ...string) ([]byte, error) {
		return []byte(stdout), nil
	}}
}

func TestFocusedWindow(t *testing.T) {
	c := testClient(`{"id":27,"title":"ZCode","app_id":"zcode","pid":13563,"workspace_id":2,"is_focused":true}`)
	w, err := c.FocusedWindow()
	if err != nil {
		t.Fatalf("FocusedWindow: %v", err)
	}
	if w.AppIDOr("") != "zcode" {
		t.Errorf("app_id = %q, want %q", w.AppIDOr(""), "zcode")
	}
	if w.ID != 27 || !w.IsFocused || w.PID != 13563 {
		t.Errorf("unexpected window: %+v", w)
	}
}

func TestFocusedWindowNullAppID(t *testing.T) {
	c := testClient(`{"id":5,"title":null,"app_id":null,"pid":10,"workspace_id":1,"is_focused":true}`)
	w, err := c.FocusedWindow()
	if err != nil {
		t.Fatalf("FocusedWindow: %v", err)
	}
	if got := w.AppIDOr("xwayland"); got != "xwayland" {
		t.Errorf("AppIDOr = %q, want %q", got, "xwayland")
	}
}

func TestFocusedWindowBadJSON(t *testing.T) {
	c := testClient("not json")
	if _, err := c.FocusedWindow(); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestKeyboardLayouts(t *testing.T) {
	c := testClient(`{"names":["English (US)","Russian"],"current_idx":1}`)
	ls, err := c.KeyboardLayouts()
	if err != nil {
		t.Fatalf("KeyboardLayouts: %v", err)
	}
	if len(ls.Names) != 2 || ls.Names[0] != "English (US)" || ls.Names[1] != "Russian" {
		t.Errorf("names = %v", ls.Names)
	}
	if ls.CurrentIdx != 1 {
		t.Errorf("current_idx = %d, want 1", ls.CurrentIdx)
	}
}

func TestSwitchLayoutArgs(t *testing.T) {
	var gotArgs []string
	c := &Client{Run: func(name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(""), nil
	}}
	if err := c.SwitchLayout(1); err != nil {
		t.Fatalf("SwitchLayout: %v", err)
	}
	want := []string{"msg", "action", "switch-layout", "1"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestKeyboardLayoutsOutOfRange(t *testing.T) {
	c := testClient(`{"names":["English (US)","Russian"],"current_idx":7}`)
	if _, err := c.KeyboardLayouts(); err == nil {
		t.Error("expected error for out-of-range current_idx")
	}
}

func TestIsTerminalApp(t *testing.T) {
	terminals := []string{"foot", "kitty", "Alacritty", "wezterm", "ghostty", "st"}
	cases := []struct {
		appID string
		want  bool
	}{
		{"foot", true},
		{"kitty", true},
		{"Alacritty", true},
		{"alacritty", true}, // case-insensitive
		{"ghostty", true},
		{"zcode", false},
		{"firefox", false},
		{"", false},
		// substring traps: must not match
		{"ghostty2", false},
		{"my-foot-client", false},
		{"vscode", false},
	}
	for _, c := range cases {
		if got := IsTerminalApp(c.appID, terminals); got != c.want {
			t.Errorf("IsTerminalApp(%q) = %v, want %v", c.appID, got, c.want)
		}
	}
}

func TestRunnerErrorPropagates(t *testing.T) {
	c := &Client{Run: func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}}
	if _, err := c.FocusedWindow(); err == nil {
		t.Error("expected runner error to propagate")
	}
}
