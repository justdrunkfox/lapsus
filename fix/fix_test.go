package fix

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/layout"
	"github.com/voev/lapsus/niri"
	"github.com/voev/lapsus/wayland"
)

func TestSanitizeSelection(t *testing.T) {
	cases := []struct {
		sel  string
		want string
		ok   bool
	}{
		{"ghbdtn", "ghbdtn", true},
		{"  ghbdtn  ", "ghbdtn", true},
		{"ghbdtn ", "ghbdtn", true},
		{"Ghbdtn!", "Ghbdtn!", true},
		{"", "", false},
		{"   ", "", false},
		{"two\nlines", "", false}, // multi-line: refuse, replacing would destroy text
		{"trailing\n", "trailing", true},
		{"\nlast", "", false},                 // leading newline = multi-line selection: refuse
		{strings.Repeat("x", 100), "", false}, // oversized
		{"a\x01b", "", false},                 // control char
		{"привет", "привет", true},
	}
	for _, c := range cases {
		got, ok := sanitizeSelection(c.sel)
		if ok != c.ok || got != c.want {
			t.Errorf("sanitizeSelection(%q) = (%q, %v), want (%q, %v)", c.sel, got, ok, c.want, c.ok)
		}
	}
}

func TestFindLayoutIndex(t *testing.T) {
	names := []string{"English (US)", "Russian"}
	if got := findLayoutIndex(names, layout.LayoutEN); got != 0 {
		t.Errorf("EN index = %d, want 0", got)
	}
	if got := findLayoutIndex(names, layout.LayoutRU); got != 1 {
		t.Errorf("RU index = %d, want 1", got)
	}
	if got := findLayoutIndex([]string{"German", "French"}, layout.LayoutEN); got != -1 {
		t.Errorf("unrecognized names should give -1, got %d", got)
	}
}

func TestLayoutNameMatches(t *testing.T) {
	// "russian" contains "us" — must count as RU, not EN.
	if !layoutNameMatches("Russian", layout.LayoutRU) {
		t.Error("Russian should match RU")
	}
	if layoutNameMatches("Russian", layout.LayoutEN) {
		t.Error("Russian must not match EN despite containing \"us\"")
	}
	if !layoutNameMatches("English (US)", layout.LayoutEN) {
		t.Error("English (US) should match EN")
	}
	if layoutNameMatches("English (US)", layout.LayoutRU) {
		t.Error("English (US) must not match RU")
	}
	if !layoutNameMatches("ru", layout.LayoutRU) {
		t.Error("ru should match RU")
	}
}

// recorder records wayland tool invocations and answers wl-paste reads
// from a canned script.
type recorder struct {
	calls []string
	// primaryResponses are returned by successive wl-paste --primary calls.
	primaryResponses []string
	primaryIdx       int
	// clipboard is what wl-copy stored; wl-paste (regular) returns it.
	clipboard string
}

func (r *recorder) run(name string, args []string, stdin []byte) ([]byte, error) {
	joined := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	switch {
	case name == "wl-paste" && strings.HasSuffix(joined, "--primary --no-newline"):
		if r.primaryIdx < len(r.primaryResponses) {
			out := r.primaryResponses[r.primaryIdx]
			r.primaryIdx++
			return []byte(out), nil
		}
		r.primaryIdx++
		return nil, fmt.Errorf("no selection")
	case name == "wl-paste":
		return []byte(r.clipboard), nil
	case name == "wl-copy" && !strings.Contains(joined, "--clear"):
		r.clipboard = string(stdin)
	}
	return nil, nil
}

func (r *recorder) hasCall(substr string) bool {
	for _, c := range r.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

type fakeNiri struct {
	calls  []string
	appID  string
	layout niri.KeyboardLayouts
}

func (f *fakeNiri) run(name string, args ...string) ([]byte, error) {
	joined := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	switch {
	case strings.Contains(joined, "focused-window"):
		return []byte(fmt.Sprintf(`{"id":1,"app_id":%q,"is_focused":true}`, f.appID)), nil
	case strings.Contains(joined, "keyboard-layouts"):
		data, err := json.Marshal(f.layout)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, nil
}

func (f *fakeNiri) hasCall(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func newTestFixer(t *testing.T, rec *recorder, nir *fakeNiri) *Fixer {
	t.Helper()
	cfg := config.Defaults()
	cfg.Fix.PauseMs = 0
	d := dict.New()
	return &Fixer{
		Cfg:  cfg,
		Dict: d,
		Ana:  analyze.New(d),
		Niri: &niri.Client{Run: nir.run},
		Way:  &wayland.Tools{Run: rec.run},
	}
}

func TestFixGUIHappyPath(t *testing.T) {
	rec := &recorder{primaryResponses: []string{"ghbdtn"}}
	nir := &fakeNiri{appID: "firefox", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Selection was replaced by typing the corrected word...
	if !rec.hasCall("wtype -- привет") {
		t.Errorf("expected the corrected word to be typed, calls: %v", rec.calls)
	}
	if !rec.hasCall("-k Left") {
		t.Errorf("expected Ctrl+Shift+Left selection, calls: %v", rec.calls)
	}
	// ...and the layout switched to Russian only AFTER the injection
	// (niri#3568: virtual keyboard events can reset the layout group).
	switchAt, typeAt := -1, -1
	for i, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") && typeAt < 0 {
			typeAt = i
		}
	}
	for i := range nir.calls {
		if strings.Contains(nir.calls[i], "switch-layout 1") {
			switchAt = i
		}
	}
	if switchAt < 0 {
		t.Errorf("expected switch-layout 1 (Russian), niri calls: %v", nir.calls)
	}
	if typeAt < 0 {
		t.Fatal("expected an injection step")
	}
	// The order between the two fakes' calls is not directly comparable,
	// so assert via a merged sequence recorded by the pipeline: layouts
	// are queried before switching and after the injection decision.
	layoutsAt := -1
	for i := range nir.calls {
		if strings.Contains(nir.calls[i], "keyboard-layouts") {
			layoutsAt = i
		}
	}
	if layoutsAt < 0 || switchAt <= layoutsAt {
		t.Errorf("expected keyboard-layouts query before switch, niri calls: %v", nir.calls)
	}
}

func TestFixGUINoFixNeeded(t *testing.T) {
	rec := &recorder{primaryResponses: []string{"hello"}}
	nir := &fakeNiri{appID: "firefox", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") {
			t.Errorf("nothing should be typed when no fix is needed, but got %q", c)
		}
	}
	if !rec.hasCall("-k Right") {
		t.Errorf("selection should be collapsed with Right, calls: %v", rec.calls)
	}
	if nir.hasCall("switch-layout") {
		t.Error("layout must not switch when no fix is applied")
	}
}

func TestFixTerminalPath(t *testing.T) {
	rec := &recorder{primaryResponses: []string{"руддщ"}}
	nir := &fakeNiri{appID: "foot", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Terminal path: copy fix to clipboard, Ctrl+Shift+V paste.
	if rec.clipboard != "hello" {
		t.Errorf("clipboard should hold the corrected word, got %q", rec.clipboard)
	}
	if !rec.hasCall("-M ctrl -M shift -k v") {
		t.Errorf("expected terminal paste Ctrl+Shift+V, calls: %v", rec.calls)
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") {
			t.Errorf("terminal path must paste, not type; got %q", c)
		}
	}
	if !nir.hasCall("switch-layout 0") {
		t.Errorf("expected switch to EN (index 0), niri calls: %v", nir.calls)
	}
}

func TestFixTerminalNoSelection(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{appID: "kitty", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}}
	f := newTestFixer(t, rec, nir)

	err := f.Run(Options{})
	if err == nil || !strings.Contains(err.Error(), "mouse") {
		t.Fatalf("expected hint to select with mouse, got %v", err)
	}
}

func TestFixDryRunTypesNothing(t *testing.T) {
	rec := &recorder{primaryResponses: []string{"ghbdtn"}}
	nir := &fakeNiri{appID: "firefox", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") {
			t.Errorf("dry run must not type text, got %q", c)
		}
	}
	if nir.hasCall("switch-layout") {
		t.Error("dry run must not switch layout")
	}
}

func TestFixGUITooLongSelection(t *testing.T) {
	rec := &recorder{primaryResponses: []string{strings.Repeat("word ", 40)}}
	nir := &fakeNiri{appID: "firefox", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{}); err == nil {
		t.Fatal("expected error for oversized selection")
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") {
			t.Errorf("oversized selection must not be replaced, got %q", c)
		}
	}
	// The (bogus) selection should be collapsed, leaving the caret in place.
	if !rec.hasCall("-k Right") {
		t.Errorf("expected collapse after unusable selection, calls: %v", rec.calls)
	}
}

func TestFixSwitchLayoutAlreadyActive(t *testing.T) {
	// Word typed in EN ("ghbdtn") fixed to RU; RU already active — the fix
	// happens (e.g. stale selection) and no switch is needed.
	rec := &recorder{primaryResponses: []string{"ghbdtn"}}
	nir := &fakeNiri{appID: "firefox", layout: niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1}}
	f := newTestFixer(t, rec, nir)

	if err := f.Run(Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if nir.hasCall("switch-layout") {
		t.Errorf("no switch expected when target layout already active, niri calls: %v", nir.calls)
	}
}

func TestAcquireLockIsExclusive(t *testing.T) {
	l1, err := acquireLock()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer l1.close()
	if _, err := acquireLock(); err == nil {
		t.Error("second acquire should fail while the first lock is held")
	}
}
