package niri

import (
	"strings"
	"testing"

	"github.com/voev/lapsus/layout"
)

func TestUmbrielKeyboardLayouts(t *testing.T) {
	c := &Client{Backend: BackendUmbriel, Run: func(name string, args ...string) ([]byte, error) {
		if name != "umbriel" || strings.Join(args, " ") != "keyboard-layouts --json" {
			t.Errorf("unexpected command: %s %v", name, args)
		}
		return []byte(`{"current_index":1,"names":["English (US)","Russian"]}`), nil
	}}
	ls, err := c.KeyboardLayouts()
	if err != nil {
		t.Fatalf("KeyboardLayouts: %v", err)
	}
	if ls.CurrentIdx != 1 || len(ls.Names) != 2 {
		t.Errorf("layouts = %+v", ls)
	}
	if l, ok := ls.Current(); !ok || l != layout.LayoutRU {
		t.Errorf("current layout = %v ok=%v, want RU", l, ok)
	}
}

func TestUmbrielFocusedWindowPicksActive(t *testing.T) {
	// `focused` is per-output (several windows carry it); `active` marks
	// the single globally focused window and must win.
	out := `[{"app_id":"a","focused":true,"active":false},{"app_id":"b","focused":false,"active":true},{"app_id":"c","focused":true,"active":false}]`
	w, err := parseUmbrielFocused([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if w.AppIDOr("") != "b" {
		t.Errorf("active window must win, got app_id=%q", w.AppIDOr(""))
	}
	if !w.IsFocused || w.PID != 0 {
		t.Errorf("window = %+v", w)
	}
	// No window at all: empty result without an app id.
	empty, err := parseUmbrielFocused([]byte(`[]`))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if empty.AppID != nil {
		t.Errorf("empty list must yield no app_id, got %q", *empty.AppID)
	}
}

func TestUmbrielSwitchLayoutSpinsNext(t *testing.T) {
	// umbriel has no set-by-index: switching 1→0 (two layouts) is one
	// keyboard-layout-next.
	var cmds []string
	c := &Client{Backend: BackendUmbriel, Run: func(name string, args ...string) ([]byte, error) {
		cmds = append(cmds, strings.Join(args, " "))
		if strings.Contains(cmds[len(cmds)-1], "keyboard-layouts") {
			return []byte(`{"current_index":1,"names":["English (US)","Russian"]}`), nil
		}
		return nil, nil
	}}
	if err := c.SwitchLayout(0); err != nil {
		t.Fatalf("SwitchLayout: %v", err)
	}
	next := 0
	for _, cmd := range cmds {
		if cmd == "msg keyboard-layout-next" {
			next++
		}
	}
	if next != 1 {
		t.Errorf("expected exactly one keyboard-layout-next, commands: %v", cmds)
	}
}

func TestTranslateUmbrielLine(t *testing.T) {
	prevIdx, prevApp := -1, "\x00"

	// First keyboard_layout observation: state only, no Switched event.
	out := translateUmbrielLine(
		`{"data":{"current_index":0,"names":["English (US)","Russian"]},"event":"keyboard_layout"}`,
		&prevIdx, &prevApp)
	if len(out) != 1 || !strings.Contains(out[0], `"current_idx":0`) || strings.Contains(out[0], "Switched") {
		t.Errorf("first observation = %v", out)
	}

	// Index change: state + Switched (drives per-app layout learning).
	out = translateUmbrielLine(
		`{"data":{"current_index":1,"names":["English (US)","Russian"]},"event":"keyboard_layout"}`,
		&prevIdx, &prevApp)
	if len(out) != 2 || !strings.Contains(out[1], `"KeyboardLayoutSwitched":{"idx":1}`) {
		t.Errorf("index change = %v", out)
	}

	// First windows observation: remembered, no event (the daemon
	// fetched the state at startup).
	out = translateUmbrielLine(
		`{"data":[{"app_id":"zcode","active":true,"focused":true}],"event":"windows"}`,
		&prevIdx, &prevApp)
	if len(out) != 0 {
		t.Errorf("first windows observation = %v", out)
	}

	// Focus moved to another app: WindowFocusChanged (the daemon
	// refetches the window itself).
	out = translateUmbrielLine(
		`{"data":[{"app_id":"zcode","active":false},{"app_id":"waterfox","active":true,"focused":true}],"event":"windows"}`,
		&prevIdx, &prevApp)
	if len(out) != 1 || !strings.Contains(out[0], "WindowFocusChanged") {
		t.Errorf("focus change = %v", out)
	}

	// Unrelated windows churn (same active app): no event.
	out = translateUmbrielLine(
		`{"data":[{"app_id":"waterfox","active":true,"focused":true},{"app_id":"zcode","active":false,"focused":false}],"event":"windows"}`,
		&prevIdx, &prevApp)
	if len(out) != 0 {
		t.Errorf("same-app churn = %v", out)
	}

	// Junk line: no events, no panic.
	if out := translateUmbrielLine(`not json`, &prevIdx, &prevApp); out != nil {
		t.Errorf("junk must produce no events, got %v", out)
	}
}

func TestBackendAutoDetectPrefersNiriWithRunner(t *testing.T) {
	// Tests inject a Runner and speak the niri dialect: auto-detect must
	// not follow the machine's live session.
	c := &Client{Run: func(name string, args ...string) ([]byte, error) { return nil, nil }}
	if got := c.backend(); got != BackendNiri {
		t.Errorf("injected runner must resolve to niri backend, got %q", got)
	}
	pinned := &Client{Backend: BackendUmbriel, Run: func(name string, args ...string) ([]byte, error) { return nil, nil }}
	if got := pinned.backend(); got != BackendUmbriel {
		t.Errorf("pinned backend must be respected, got %q", got)
	}
	// "auto" must resolve, not fall through as an unknown backend (a
	// regression here silently routes every call to the niri dialect).
	auto := &Client{Backend: Backend("auto"), Run: func(name string, args ...string) ([]byte, error) { return nil, nil }}
	if got := auto.backend(); got != BackendNiri {
		t.Errorf(`"auto" with a runner must resolve to niri, got %q`, got)
	}
	autoNoRunner := &Client{Backend: Backend("auto")}
	if got := autoNoRunner.backend(); got != BackendNiri && got != BackendUmbriel {
		t.Errorf(`"auto" must resolve to a real backend, got %q`, got)
	}
}
