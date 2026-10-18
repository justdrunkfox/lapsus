package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/evdev"
	"github.com/voev/lapsus/niri"
)

// fakeInj doubles as the daemon's WordInjector: ReplaceWord records the
// same call shapes the wtype tools used to emit, so the string
// assertions in older tests keep working. The pause-timer goroutine
// writes calls while the test goroutine polls them, so the slice is
// guarded.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) ReplaceWord(old, corrected string) error {
	n := utf8.RuneCountInString(old)
	args := make([]string, 0, 2*n)
	for i := 0; i < n; i++ {
		args = append(args, "-k", "BackSpace")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > 0 {
		r.calls = append(r.calls, "wtype "+strings.Join(args, " "))
	}
	r.calls = append(r.calls, "wtype -- "+corrected)
	return nil
}

func (r *recorder) injType(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == "wtype -- "+s {
			return true
		}
	}
	return false
}

func (r *recorder) run(name string, args []string, stdin []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func (r *recorder) hasCall(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	d := New(cfg, analyze.New(dict_), &niri.Client{Run: nir.run}, rec, false, false)
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

	// "руддщ" typed while RU is active (meant English), closed by space.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if !rec.hasCall("wtype -- hello ") {
		t.Errorf("expected the corrected word with the space restored, calls: %v", rec.calls)
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

func TestDaemonPunctuationInsideWord(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)

	// "hs,f" is "рыба" typed with EN active: б sits on the comma key.
	// The comma joins the buffer, and the whole word fixes at the space.
	d.typeKeys([]uint16{35, 31, 51, 33}...) // h s , f
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !rec.hasCall("wtype -- рыба ") {
		t.Errorf("hs,f should fix to рыба whole, calls: %v", rec.calls)
	}
	exact5 := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	found := false
	for _, c := range rec.calls {
		if c == exact5 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 5 backspaces (hs,f+space), calls: %v", rec.calls)
	}

	// Real punctuation is protected by the dictionary gate: "hi," flips
	// to "рш," — not a word, so nothing is injected.
	d.typeKeys([]uint16{35, 23, 51}...) // h i ,
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	for _, c := range rec.calls {
		if strings.Contains(c, "рш") {
			t.Errorf("punctuated non-word must stay untouched, got %q", c)
		}
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

func TestDoubleAltFlipsThroughPunctuation(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1} // RU active globally
	d := newTestDaemon(t, rec, nir, 0)
	d.cur = layoutRU
	d.Cfg.Daemon.DoubleAltFlip = true

	// User on RU types "руддщ," (Shift+/ gives ','), meaning "hello?".
	// The double-Alt is an explicit toggle: the whole buffer flips
	// positionally, punctuation included — ',' on the / key becomes '?'.
	d.typeKeys(keysRuddsh...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 53, Value: evdev.ValKeyDown})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 53, Value: evdev.ValKeyUp})
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftShift, Value: evdev.ValKeyUp})
	altTap(d)
	altTap(d)

	if !rec.injType("hello?") {
		t.Errorf("double-alt should flip the buffer whole (руддщ, → hello?), calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 0") {
		t.Errorf("layout should follow the flipped word, niri calls: %v", nir.calls)
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
	nir.cur = 1                                                 // the manual toggle really changed the layout
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
		t.Fatalf("buffer = %d presses, want 2", len(f.buf))
	}
	f.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":1}}}`)
	if len(f.buf) != 2 {
		t.Fatalf("same-layout poll must keep the buffer, got %d presses", len(f.buf))
	}
}

func TestLayoutChangeKeepsWordAndFixesUnderNewLayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1}
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.RememberWindowLayout = false
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1}) // RU believed

	// The word is typed while the daemon's layout is stale (a missed
	// switch); the reconciliation lands only mid-test. The presses
	// survive the change and the word is read under the reconciled
	// layout: "ghbdtn" is not a word, its flip "привет" is — the fix
	// fires as if the layout had been right all along.
	f.typeKeys(keysGhbdtn...)
	f.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":0}}}`)
	if len(f.buf) != len(keysGhbdtn) {
		t.Fatalf("layout change must keep the presses, got %d", len(f.buf))
	}
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !rec.hasCall("wtype -- привет ") {
		t.Errorf("word typed across a layout change must still fix, calls: %v", rec.calls)
	}
}

func TestLayoutChangeMidWordGarbageStaysUntouched(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.RememberWindowLayout = false
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0}) // EN

	// A word typed across a real mid-word layout switch: the app
	// receives mixed text, and the pure readings stay gibberish in both
	// layouts — the dictionary gate must stay silent.
	f.typeKeys([]uint16{45, 47, 39}...) // x v ; — EN chars in the app
	f.handleStreamEvent(`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":["English (US)","Russian"],"current_idx":1}}}`)
	f.typeKeys([]uint16{26, 49, 23}...) // [ n i — RU chars in the app
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype") {
			t.Errorf("garbage word must not be fixed, got %q", c)
		}
	}
}

func TestOwnDeviceFilter(t *testing.T) {
	// The daemon must never read its own virtual keyboard: the injections
	// would feed back into the word buffer (plan А2).
	if !ownDevice("lapsus-virtual-keyboard") {
		t.Error("the lapsus uinput keyboard must be recognized as our own")
	}
	if !ownDevice("LAPSUS something") {
		t.Error("matching is case-insensitive by name substring")
	}
	for _, name := range []string{"AT Translated Set 2 keyboard", "TPPS/2 IBM TrackPoint", "Synaptics s3203"} {
		if ownDevice(name) {
			t.Errorf("%q must not be treated as our own device", name)
		}
	}
}

func altTap(f *Daemon) {
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftAlt, Value: evdev.ValKeyDown})
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeyLeftAlt, Value: evdev.ValKeyUp})
}

func TestDoubleAltFlipsBufferWordAndLayout(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0} // EN active
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.DoubleAltFlip = true

	// Type a wrong-layout word and double-tap left Alt.
	f.typeKeys(keysGhbdtn...)
	altTap(f)
	altTap(f)

	if !rec.injType("привет") {
		t.Errorf("double-alt should flip the buffer word, calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("layout should follow the flipped word, niri calls: %v", nir.calls)
	}
}

func TestDoubleAltSingleTapDoesNothing(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 0) // pause boundary off: it would fix the word itself
	f.Cfg.Daemon.DoubleAltFlip = true
	f.Cfg.Daemon.DoubleAltWindowMs = 100 // tiny window: expires fast

	f.typeKeys(keysGhbdtn...)
	altTap(f)
	time.Sleep(150 * time.Millisecond) // window expires
	altTap(f)

	if rec.hasCall("wtype -- привет") {
		t.Errorf("single tap must not flip, calls: %v", rec.calls)
	}
}

func TestDoubleAltCancelledByLetter(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.DoubleAltFlip = true

	f.typeKeys(keysGhbdtn...)
	altTap(f)
	// A letter between the taps (Alt+x combo or plain typing) cancels.
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 45, Value: evdev.ValKeyDown})
	f.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: 45, Value: evdev.ValKeyUp})
	altTap(f)

	for _, c := range rec.calls {
		if strings.HasPrefix(c, "wtype -- ") {
			t.Errorf("cancelled tap must not flip, got %q", c)
		}
	}
}

func TestDoubleAltDisabledByConfig(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.DoubleAltFlip = false

	f.typeKeys(keysGhbdtn...)
	altTap(f)
	altTap(f)

	if rec.injType("привет") || rec.hasCall("wtype -- привет") {
		t.Errorf("disabled trigger must not flip, calls: %v", rec.calls)
	}
}

func TestDoubleAltEmptyBufferNoop(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 300)
	f.Cfg.Daemon.DoubleAltFlip = true

	// No word typed: the flip is a no-op (nothing in the buffer).
	altTap(f)
	altTap(f)

	if rec.hasCall("wtype -- ") {
		t.Errorf("empty buffer must not inject, calls: %v", rec.calls)
	}
}

func TestDoubleAltRUtoEN(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1} // RU active
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.DoubleAltFlip = true
	f.Cfg.Daemon.RememberWindowLayout = false
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1})

	f.typeKeys(keysRuddsh...) // руддщ
	altTap(f)
	altTap(f)

	if !rec.hasCall("wtype -- hello") {
		t.Errorf("RU→EN flip failed, calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 0") {
		t.Errorf("layout should switch to EN, niri calls: %v", nir.calls)
	}
}

func TestDoubleAltTogglesCorrectWordToo(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0} // EN active
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.DoubleAltFlip = true
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 0})

	// The toggle flips any word — even a correct one (explicit intent).
	f.typeKeys(keysHello...)
	altTap(f)
	altTap(f)

	if !rec.injType("руддщ") {
		t.Errorf("correct word should flip too (pure toggle), calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 1") {
		t.Errorf("layout should follow the flipped word, niri calls: %v", nir.calls)
	}
}

func TestDoubleAltFlipsRUToEN(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 1} // RU active
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.DoubleAltFlip = true
	f.setLayouts(&niri.KeyboardLayouts{Names: []string{"English (US)", "Russian"}, CurrentIdx: 1})

	// "руддщ" is gibberish in RU — flips to hello, layout follows.
	f.typeKeys(keysRuddsh...)
	altTap(f)
	altTap(f)

	if !rec.injType("hello") {
		t.Errorf("gibberish word should flip, calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 0") {
		t.Errorf("layout should follow to EN, niri calls: %v", nir.calls)
	}
}

func TestDoubleAltFlipsGibberish(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	f := newTestDaemon(t, rec, nir, 0)
	f.Cfg.Daemon.DoubleAltFlip = true

	// Non-dictionary gibberish still flips positionally.
	f.typeKeys([]uint16{45, 47, 39, 26, 49, 23}...) // x v ; ] n i  →  ч м ж х т ш? — просто коды
	altTap(f)
	altTap(f)

	if len(rec.calls) == 0 {
		t.Log("no mappable chars — nothing to check")
	}
	// The point: the toggle is unconditional, so SOME flip must happen
	// if any char mapped. Codes above are letters → all mappable.
	if !rec.hasCall("wtype -- ") {
		t.Errorf("gibberish should flip unconditionally, calls: %v", rec.calls)
	}
}

func TestUndoByFixKey(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)

	// The daemon auto-fixes "ghbdtn " → "привет " (and, with
	// switch_after_words=1 in tests, moves the layout to RU).
	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	if !rec.hasCall("wtype -- привет ") {
		t.Fatalf("the fix must fire first, calls: %v", rec.calls)
	}

	// The fix key then asks the daemon to revert: the word goes back as
	// typed, the layout move is taken back, the fix-run resets.
	if !d.Undo() {
		t.Fatal("fresh auto-fix must be undoable")
	}
	if !rec.hasCall("wtype -- ghbdtn ") {
		t.Errorf("undo must re-type the word as typed, calls: %v", rec.calls)
	}
	exact7 := "wtype -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace -k BackSpace"
	found := false
	for _, c := range rec.calls {
		if c == exact7 {
			found = true
		}
	}
	if !found {
		t.Errorf("undo must delete the fixed word+space (7), calls: %v", rec.calls)
	}
	if !nir.hasCall("switch-layout 1") || !nir.hasCall("switch-layout 0") {
		t.Errorf("undo must take the fix's layout move back, niri calls: %v", nir.calls)
	}
}

func TestUndoRefusesAfterTyping(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	// Text was typed since the fix — the caret may be anywhere.
	d.typeKeys([]uint16{45}...)

	if d.Undo() {
		t.Error("undo must be refused once text was typed after the fix")
	}
	if rec.hasCall("wtype -- ghbdtn ") {
		t.Errorf("refused undo must not inject, calls: %v", rec.calls)
	}
}

func TestUndoRefusesWhenStale(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)
	d.Cfg.Daemon.UndoWindowMs = 30

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	time.Sleep(60 * time.Millisecond)

	if d.Undo() {
		t.Error("undo window must expire")
	}
}

func TestUndoRefusesOtherApp(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})
	d.mu.Lock()
	d.appID = "zed" // focus moved to another application
	d.mu.Unlock()

	if d.Undo() {
		t.Error("undo must be refused in another application")
	}
}

func TestUndoDisabledByConfig(t *testing.T) {
	rec := &recorder{}
	nir := &fakeNiri{cur: 0}
	d := newTestDaemon(t, rec, nir, 0)
	d.Cfg.Daemon.UndoWindowMs = 0

	d.typeKeys(keysGhbdtn...)
	d.handleEvent(evdev.Event{Type: evdev.TypeKey, Code: evdev.KeySpace, Value: evdev.ValKeyDown})

	if d.Undo() {
		t.Error("undo_window_ms=0 must disable the undo")
	}
}

func TestSingletonLock(t *testing.T) {
	// Isolate from the real daemon's lock: lockSingleton reads
	// XDG_RUNTIME_DIR at call time (t.Setenv also forbids t.Parallel —
	// the package uses none).
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := newTestDaemon(t, &recorder{}, &fakeNiri{cur: 0}, 300)
	unlock, err := f.lockSingleton()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer unlock()
	if _, err := f.lockSingleton(); err == nil {
		t.Error("second singleton lock must fail")
	}
}
