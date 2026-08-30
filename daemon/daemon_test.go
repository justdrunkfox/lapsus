package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/evdev"
	"github.com/voev/lapsus/niri"
	"github.com/voev/lapsus/wayland"
)

type recorder struct {
	calls []string
}

func (r *recorder) run(name string, args []string, stdin []byte) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
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
	calls []string
	// layoutJSON is returned for keyboard-layouts queries.
	layoutJSON string
}

func (f *fakeNiri) run(name string, args ...string) ([]byte, error) {
	joined := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	if strings.Contains(joined, "keyboard-layouts") {
		return []byte(f.layoutJSON), nil
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

func newTestDaemon(t *testing.T, rec *recorder, nir *fakeNiri, pauseMs int) *Daemon {
	t.Helper()
	cfg := config.Defaults()
	cfg.Fix.PauseMs = 0
	cfg.Fix.SwitchLayout = true
	// Tests assert layout switching explicitly; the daemon-side switch is
	// off by default (the hotkey owns the layout move).
	cfg.Daemon.SwitchLayout = true
	cfg.Daemon.BoundaryPauseMs = pauseMs
	dict_ := dict.New()
	d := New(cfg, analyze.New(dict_), &niri.Client{Run: nir.run}, &wayland.Tools{Run: rec.run}, false, false)
	d.appID = "zcode"
	d.cur = layoutEN
	return d
}

func (d *Daemon) typeKeys(codes ...uint16) {
	for _, c := range codes {
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: c, Value: evdev.ValKeyDown})
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: c, Value: evdev.ValKeyUp})
	}
}

const (
	layoutEN = 0
	layoutRU = 1
)

// Keycode sequences for words (US QWERTY positions).
var (
	keysGhbdtn = []uint16{34, 35, 48, 32, 20, 49}     // ghbdtn
	keysRuddsh = []uint16{35, 18, 38, 38, 24}         // руддщ/hello
	keysHello  = []uint16{35, 18, 38, 38, 24}         // same keys, EN layout
	keysPriv   = []uint16{42, 34, 35, 48, 32, 20, 49} // Shift+g + hbdtn → Ghbdtn
	keysRto    = []uint16{19, 20, 24}                 // rto → кто in RU
)

func TestDaemonFixesENWordTypedInENLayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	// User types "ghbdtn " while EN is active (meant Russian).
	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- привет") {
		t.Errorf("expected the corrected word to be typed, calls: %v", rec.calls)
	}
	// Unified replacement: BackSpace ×7 for "ghbdtn"+space, the fix is
	// typed with the trailing space restored.
	exact7 := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	found7 := false
	for _, c := range rec.calls {
		if c == exact7 {
			found7 = true
		}
	}
	if !found7 {
		t.Errorf("expected 7 backspaces (word+space), calls: %v", rec.calls)
	}
	if !rec.hasCall("wtype -- привет ") {
		t.Errorf("expected the separator to be re-typed, calls: %v", rec.calls)
	}
	// Layout must switch to Russian for the next word.
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("expected switch to RU, niri calls: %v", nir.calls)
	}
}

func TestDaemonFixesRUWordTypedInRULayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.setLayouts(mustLayouts(t, `{"names":["English (US)","Russian"],"current_idx":1}`))

	// "руддщ" typed while RU is active (meant English).
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySlash, Value: evdev.ValKeyDown})

	// Slash in RU produces '.' — a fix boundary; "руддщ" maps to "hello",
	// and the punctuation flips to the other layout along with the word:
	// RU '.' is the EN '/' key.
	if !rec.hasCall("wtype -- hello/") {
		t.Errorf("expected the corrected word with the flipped dot, calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 0") {
		t.Errorf("expected switch to EN, niri calls: %v", nir.calls)
	}
}

func TestDaemonNoFixForRealWords(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	// "hello" typed in EN — a real word, nothing to fix.
	d.typeKeys(keysHello...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if len(rec.calls) != 0 {
		t.Errorf("nothing should be injected for a real word, calls: %v", rec.calls)
	}
}

func TestDaemonPreservesCase(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 34, Value: evdev.ValKeyDown}) // Shift+g
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 34, Value: evdev.ValKeyUp})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyUp})
	for _, c := range keysGhbdtn[1:] {
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: c, Value: evdev.ValKeyDown})
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: c, Value: evdev.ValKeyUp})
	}
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- Привет") {
		t.Errorf("expected case-preserving fix, calls: %v", rec.calls)
	}
}

func TestDaemonEnterClearsWithoutFix(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyEnter, Value: evdev.ValKeyDown})

	if len(rec.calls) != 0 || nir.hasCall("switch-layout") {
		t.Errorf("enter must clear the buffer without fixing, calls: %v / %v", rec.calls, nir.calls)
	}
}

func TestDaemonCtrlComboClearsBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	d.typeKeys(keysGhbdtn[:3]...)
	// Ctrl+C style shortcut in the middle.
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftCtrl, Value: evdev.ValKeyDown})
	d.typeKeys([]uint16{46}...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftCtrl, Value: evdev.ValKeyUp})
	// Garbage after the shortcut: not a word in either dictionary.
	d.typeKeys([]uint16{45, 27, 17, 19, 23, 50}...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("shortcut must clear the buffer, but got %q", c)
		}
	}
}

func TestDaemonBackspaceShrinksWord(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.setLayouts(mustLayouts(t, `{"names":["English (US)","Russian"],"current_idx":1}`))

	// "руддщ", then BackSpace, then space: buffer becomes "рудд" → "hell"
	// is a known EN word → fix fires with 4 backspaces.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyBackspace, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- hell") {
		t.Errorf("expected the shortened word to be fixed, calls: %v", rec.calls)
	}
	// Exactly 5 backspaces in one wtype call (word + space).
	exact := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	found := false
	for _, c := range rec.calls {
		if c == exact {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a call with exactly 5 backspaces (word+space), calls: %v", rec.calls)
	}
}

func TestDaemonPauseBoundary(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 30)

	d.typeKeys(keysGhbdtn...)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !rec.hasCall("wtype -- привет") {
		time.Sleep(10 * time.Millisecond)
	}
	if !rec.hasCall("wtype -- привет") {
		t.Errorf("idle pause should complete the word, calls: %v", rec.calls)
	}
}

func TestDaemonPausedSkipsFix(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.TogglePause()

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if len(rec.calls) != 0 {
		t.Errorf("paused daemon must not inject, calls: %v", rec.calls)
	}
}

func TestDaemonExcludedApp(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.Cfg.Daemon.ExcludeAppIDs = []string{"qemu", "MyGame"}
	d.appID = "mygame" // case-insensitive match

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if len(rec.calls) != 0 {
		t.Errorf("excluded app must not be fixed, calls: %v", rec.calls)
	}
}

func TestDaemonStreamEventsUpdateLayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 300)

	d.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":1}}}`)
	d.mu.Lock()
	cur := d.cur
	d.mu.Unlock()
	if cur != layoutRU {
		t.Errorf("layout after stream event = %v, want RU", cur)
	}

	// A wrong-layout word typed in RU now fixes to EN.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !rec.hasCall("wtype -- hello") {
		t.Errorf("expected RU→EN fix after layout tracking, calls: %v", rec.calls)
	}
}

func TestDaemonDryRunInjectsNothing(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.DryRun = true

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("dry run must not inject, got %q", c)
		}
	}
}

func mustLayouts(t *testing.T, json_ string) *niri.KeyboardLayouts {
	t.Helper()
	ls, err := func() (*niri.KeyboardLayouts, error) {
		c := &niri.Client{Run: func(name string, args ...string) ([]byte, error) {
			return []byte(json_), nil
		}}
		return c.KeyboardLayouts()
	}()
	if err != nil {
		t.Fatalf("parse layouts %q: %v", json_, err)
	}
	return ls
}

func TestDaemonMinWordLenSkipsFragments(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)

	// "kf" maps to "ла" (a known RU word), but 2 runes < min_word_len=3:
	// the daemon must not touch it (e.g. it is a fragment of a word
	// split by a mid-word pause).
	d.typeKeys([]uint16{36, 33}...) // k, f
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("fragment must not be auto-fixed, got %q", c)
		}
	}
}

func TestDaemonAnyPrintableSeparatorCompletesWord(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 0)

	// "ghbdtn" closed by ")" (Shift+0): a printable separator completes
	// the word, the fix replaces only the word — the paren stays.
	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 11, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 11, Value: evdev.ValKeyUp})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyUp})

	if !rec.hasCall("wtype -- привет") {
		t.Errorf("closing paren should complete the word, calls: %v", rec.calls)
	}
}

func TestDaemonPauseBoundaryDisabledByDefault(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 0)

	// A word typed and abandoned (no separator): nothing must happen,
	// the buffer just waits for a real separator.
	d.typeKeys(keysGhbdtn...)
	time.Sleep(150 * time.Millisecond)
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("pause boundary must be off by default, got %q", c)
		}
	}

	// The word survives the pause: a space completes the WHOLE word.
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !rec.hasCall("wtype -- привет") {
		t.Errorf("word typed across pauses must still be fixed whole, calls: %v", rec.calls)
	}
}

func TestDaemonHeldBackspaceInvalidatesBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 0)
	d.setLayouts(mustLayouts(t, `{"names":["English (US)","Russian"],"current_idx":1}`))

	// "руддщ", then a HELD backspace: one press plus kernel repeats the
	// app also acts on. The daemon cannot know how much was deleted.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyBackspace, Value: evdev.ValKeyDown})
	for i := 0; i < 5; i++ {
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyBackspace, Value: evdev.ValKeyRepeat})
	}
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyBackspace, Value: evdev.ValKeyUp})

	// User retypes a word and hits space. The fix must use the length of
	// the word tracked AFTER the hold (5), not a mix with stale content.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- hello") {
		t.Errorf("expected the retyped word to be fixed, calls: %v", rec.calls)
	}
	exact6 := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	exact7 := exact6 + " -k BackSpace"
	for _, c := range rec.calls {
		if c == exact7 {
			t.Errorf("stale buffer inflated the deletion length, calls: %v", rec.calls)
		}
		if !strings.HasPrefix(c, "wtype -k BackSpace") {
			continue
		}
		if c != exact6 {
			t.Errorf("expected exactly 6 backspaces (word+space) after a held-key edit, got %q", c)
		}
	}
}

func TestDaemonHeldLetterInvalidatesBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 0)
	d.setLayouts(mustLayouts(t, `{"names":["English (US)","Russian"],"current_idx":1}`))

	// "рудд" + HELD key (app gets "руддооо", daemon can only see "руддо").
	d.typeKeys(keysRuddsh[:4]...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 24, Value: evdev.ValKeyDown})
	for i := 0; i < 4; i++ {
		d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 24, Value: evdev.ValKeyRepeat})
	}
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 24, Value: evdev.ValKeyUp})

	// Buffer was invalidated by the hold: the fragment must not be fixed.
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("held-key fragment must not be fixed, got %q", c)
		}
	}
}

func TestDaemonSeparatorFlipsLikeTheWord(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":1}`}
	d := newTestDaemon(t, rec, nir, 0)
	d.setLayouts(mustLayouts(t, `{"names":["English (US)","Russian"],"current_idx":1}`))

	// User on RU layout presses Shift+/ (produces ','), meaning the EN
	// question mark: "руддщ," must become "hello?" — the separator flips
	// to the other layout along with the word.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 53, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 53, Value: evdev.ValKeyUp})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyUp})

	if !rec.hasCall("wtype -- hello?") {
		t.Errorf("expected comma to flip to ?, calls: %v", rec.calls)
	}
	exact6 := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	found := false
	for _, c := range rec.calls {
		if c == exact6 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 6 backspaces (word+separator), calls: %v", rec.calls)
	}
}

func TestDaemonDoesNotSwitchLayoutByDefault(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{layoutJSON: `{"names":["English (US)","Russian"],"current_idx":0}`}
	d := newTestDaemon(t, rec, nir, 300)
	d.Cfg.Daemon.SwitchLayout = false // project default: daemon leaves the layout alone

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- привет ") {
		t.Errorf("the word must still be fixed, calls: %v", rec.calls)
	}
	if nir.hasCall("switch-layout") {
		t.Errorf("daemon must not switch the layout by default, niri calls: %v", nir.calls)
	}
}
