package daemon

import (
	"fmt"
	"strconv"
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
	appID string
	// cur is the layout index the fake reports and mutates on
	// switch-layout, so refetches reflect the daemon's own switches.
	cur int
}

func (f *fakeNiri) run(name string, args ...string) ([]byte, error) {
	joined := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	if strings.Contains(joined, "focused-window") {
		return []byte(fmt.Sprintf(`{"id":1,"app_id":%q,"is_focused":true}`, f.appID)), nil
	}
	if strings.Contains(joined, "keyboard-layouts") {
		return []byte(fmt.Sprintf(`{"names":["English (US)","Russian"],"current_idx":%d}`, f.cur)), nil
	}
	if strings.Contains(joined, "switch-layout") {
		fields := strings.Fields(joined)
		if idx, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
			f.cur = idx
		}
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
	// Tests must never touch the desktop: no real notifications, no sound.
	cfg.Feedback.Notify = false
	cfg.Feedback.Sound = ""
	// Tests assert layout switching explicitly; the daemon-side switch is
	// off by default (the hotkey owns the layout move).
	cfg.Daemon.SwitchLayout = true
	cfg.Daemon.SwitchAfterWords = 1
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
	keysVbh    = []uint16{47, 48, 35}                 // vbh → мир in RU
	keysMozhet = []uint16{47, 36, 39, 20, 21}         // может (ж on ;)
)

func TestDaemonFixesENWordTypedInENLayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 1}
	d := newTestDaemon(t, rec, nir, 300)
	d.cur = layoutRU

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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 300)

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyEnter, Value: evdev.ValKeyDown})

	if len(rec.calls) != 0 || nir.hasCall("switch-layout") {
		t.Errorf("enter must clear the buffer without fixing, calls: %v / %v", rec.calls, nir.calls)
	}
}

func TestDaemonCtrlComboClearsBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 1}
	d := newTestDaemon(t, rec, nir, 300)
	d.cur = layoutRU

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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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

func TestDaemonMinWordLenSkipsFragments(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
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
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)
	d.cur = layoutRU

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
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)
	d.cur = layoutRU

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
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)
	d.cur = layoutRU

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
	nir := &fakeNiri{cur: 0}
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

func TestRememberRestoresAppLayoutOnFocus(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1} // RU active globally
	f := newTestDaemon(t, rec, nir, 300)
	f.appID = "zcode"

	// The user deliberately toggles to RU in zcode: a layout switch is
	// the app's intended language.
	f.handleStreamEvent(`{"KeyboardLayoutSwitched":{"idx":1}}`)
	if nir.cur != 1 || f.appLayouts["zcode"] != layoutRU {
		t.Fatalf("learning failed: cur=%d memory=%v", nir.cur, f.appLayouts)
	}

	// Focus waterfox (never typed in): no memory → no switch.
	nir.appID = "waterfox"
	f.handleStreamEvent(`{"WindowFocusChanged":{"id":4}}`)
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout") {
			t.Errorf("no memory for waterfox — must not switch, calls: %v", nir.calls)
		}
	}

	// The user switches to EN there (deliberate): learn waterfox=EN.
	nir.cur = 0
	f.handleStreamEvent(`{"KeyboardLayoutSwitched":{"idx":0}}`)

	// Back to zcode: current is EN, remembered RU → restore fires.
	nir.appID = "zcode"
	f.handleStreamEvent(`{"WindowFocusChanged":{"id":27}}`)
	t.Logf("state: memory=%v cur=%v appID=%s", f.appLayouts, f.cur, f.appID)
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("expected restore to RU for zcode, niri calls: %v", nir.calls)
	}
}

func TestRememberDisabledByConfig(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1}
	f := newTestDaemon(t, rec, nir, 300)
	f.appID = "zcode"
	f.Cfg.Daemon.RememberWindowLayout = false

	f.handleStreamEvent(`{"KeyboardLayoutSwitched":{"idx":1}}`)
	if len(f.appLayouts) != 0 {
		t.Errorf("learning must be off with remember_window_layout=false")
	}
}

func TestDefaultLayoutForUnknownApp(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0} // EN active
	f := newTestDaemon(t, rec, nir, 300)
	nir.appID = "new-app"
	f.Cfg.Daemon.DefaultLayout = "ru"

	// Unknown app + configured default: focus applies the default (RU).
	f.handleStreamEvent(`{"WindowFocusChanged":{"id":9}}`)
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("default layout should apply for unknown app, niri calls: %v", nir.calls)
	}
}

func TestRememberedBeatsDefault(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.DefaultLayout = "ru" // would say RU for unknown apps

	// Learn zcode=EN by a deliberate switch there.
	nir.appID = "zcode"
	f.handleStreamEvent(`{"KeyboardLayoutSwitched":{"idx":0}}`)

	// Focus zcode: remembered EN wins over the "ru" default → no switch.
	f.handleStreamEvent(`{"WindowFocusChanged":{"id":27}}`)
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout") {
			t.Errorf("remembered layout must beat the default, calls: %v", nir.calls)
		}
	}
}

func TestDefaultLayoutOff(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.appID = "new-app"
	f.Cfg.Daemon.DefaultLayout = ""

	f.handleStreamEvent(`{"WindowFocusChanged":{"id":9}}`)
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout") {
			t.Errorf("default off — must not switch, calls: %v", nir.calls)
		}
	}
}

func TestSwitchLayoutAfterTwoConsecutiveFixes(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0} // EN active
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.SwitchAfterWords = 2

	// First wrong-layout word: fixed, layout not moved yet.
	f.typeKeys(keysGhbdtn...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout") {
			t.Errorf("single fixed word must not move the layout, calls: %v", nir.calls)
		}
	}

	// Second consecutive fix to RU: the layout follows.
	f.typeKeys(keysVbh...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("layout should follow after 2 consecutive fixes, niri calls: %v", nir.calls)
	}
}

func TestRealWordResetsFixRun(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.SwitchAfterWords = 2

	// One wrong-layout word, then a real word: the run resets, so the
	// next wrong word is fixed without moving the layout.
	f.typeKeys(keysGhbdtn...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	f.typeKeys(keysHello...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	f.typeKeys(keysGhbdtn...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout") {
			t.Errorf("real word must reset the run, calls: %v", nir.calls)
		}
	}
}

func TestLayoutSwitchEventResetsFixRun(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.SwitchAfterWords = 2

	f.typeKeys(keysGhbdtn...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	nir.cur = 1 // the manual toggle really changed the layout
	f.handleStreamEvent(`{"KeyboardLayoutSwitched":{"idx":1}}`) // daemon learns of it
	f.typeKeys(keysVbh...)
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	for _, c := range nir.calls {
		if strings.Contains(c, "switch-layout ") && !strings.Contains(c, "keyboard-layouts") {
			t.Errorf("manual layout switch must reset the run, calls: %v", nir.calls)
		}
	}
}

func TestLayoutPollSameLayoutKeepsBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.RememberWindowLayout = false
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1})

	// The safety-net poll re-reads the same layout: the half-typed word
	// must survive it.
	f.typeKeys(keysMozhet[:2]...)
	if len(f.buf) != 2 {
		t.Fatalf("buffer = %q, want 2 chars", string(f.buf))
	}
	f.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":1}}}`)
	if len(f.buf) != 2 {
		t.Fatalf("same-layout poll must keep the buffer, got %q", string(f.buf))
	}
}

func TestLayoutPollChangedLayoutDropsBuffer(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.RememberWindowLayout = false
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1})

	f.typeKeys(keysMozhet[:2]...)
	f.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":0}}}`)
	if len(f.buf) != 0 {
		t.Errorf("changed layout must drop the buffer, got %q", string(f.buf))
	}
}
