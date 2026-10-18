// Package daemon implements the lapsus auto-fix daemon (M2): it reads
// keystrokes from evdev devices (without grabbing them), tracks the word
// being typed, and when the word ends in a way that is safe to fix — a
// space boundary or an idle pause — replaces it with the corrected
// version if the dictionaries are confident it was typed in the wrong
// layout.
//
// Because the daemon knows the word from the keystroke stream itself,
// the replacement is the same everywhere (GUI apps and terminals):
// BackSpace × word length, then type the fix. The caret is guaranteed
// to sit right after the word, since the user has just typed it.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/evdev"
	"github.com/voev/lapsus/feedback"
	"github.com/voev/lapsus/keymap"
	"github.com/voev/lapsus/layout"
	"github.com/voev/lapsus/niri"
)

const (
	// deviceRescan is the hotplug polling interval: new keyboards are
	// picked up (and dead ones retired) without restarts.
	deviceRescan = 3 * time.Second
	// niriRetry is the reconnect delay for the event stream watcher.
	niriRetry = 3 * time.Second
	// layoutPoll is the periodic layout resync interval (a safety net
	// for missed KeyboardLayoutSwitched events). The event stream keeps
	// d.cur in sync normally, so this is deliberately slow — every tick
	// spawns a `niri msg` process.
	layoutPoll = time.Second
	// maxDoubleTapGap bounds doubleTapWindow when the config value is 0.
	maxDoubleTapGap = 5 * time.Second
	// undoSocket is the unix socket (in XDG_RUNTIME_DIR) the one-shot
	// fix CLI uses to hand its press over to the daemon when an
	// auto-fix is still fresh enough to be undone (plan А4).
	undoSocket = "lapsus.daemon.sock"
)

// undoRecord remembers the last auto-fix so the fix key can revert it.
// old/fixed include the trailing space, exactly as injected.
type undoRecord struct {
	old      string
	fixed    string
	appID    string
	at       time.Time
	switched bool // this fix moved the layout (switch_after_words threshold)
}

// Daemon is the running auto-fix instance.
// WordInjector is the injection surface the daemon needs: replace the
// word before the caret (including its trailing separator, when there
// was one) with the corrected text.
type WordInjector interface {
	ReplaceWord(old, corrected string) error
}

// keyPress is one buffered keystroke: the raw keycode and the shift
// state at the moment of the press. The word buffer stores presses, not
// characters — the text is derived at the word boundary by reading the
// presses under a layout, so a layout change mid-word (or a missed
// switch event reconciled by the poll) can no longer corrupt or drop
// what was typed (plan А1 "двухпрочтение", see TODO.md M4).
type keyPress struct {
	code  uint16
	shift bool
}

// tailWord is one completed word in the daemon's phrase memory (plan
// Б2): what the application received, and the presses that produced it,
// so the tail can be flipped without selection or clipboard.
type tailWord struct {
	appText    string
	presses    []keyPress
	cur        layout.Layout
	spaceAfter bool // a space follows this word in the application
}

// tailCap bounds the phrase memory: words since the last hard boundary
// (Enter, arrows, combos all clear it) never reach this many.
const tailCap = 32

type Daemon struct {
	Cfg     *config.Config
	Ana     *analyze.Analyzer
	Niri    *niri.Client
	Inj     WordInjector
	FB      *feedback.F
	Verbose bool
	DryRun  bool

	// ConfigPath is where toggles (tray) persist feedback settings;
	// empty disables persistence.
	ConfigPath string

	// OnLayoutChange and OnPauseChange are notified outside the lock
	// (used by the tray to redraw its icon and checkboxes).
	OnLayoutChange func(l layout.Layout)
	OnPauseChange  func(paused bool)

	mu         sync.Mutex
	devices    map[string]bool
	appLayouts map[string]layout.Layout // app_id → last typed layout
	// modifier and double-Alt-tap state
	ctrlHeld    bool
	altHeld     bool
	leftAltHeld bool
	altTapWait  bool
	altDouble   bool
	altLastUp   time.Time
	// fix-run tracking: consecutive fixes to the same language move the
	// layout once the run reaches switch_after_words.
	fixRunLang  layout.Layout
	fixRunCount int
	shift       bool
	buf         []keyPress
	gen         uint64
	// undo state: the last auto-fix the fix key can still revert, and
	// the last text-affecting keydown (modifiers and the fix key itself
	// excluded) — undo is only allowed when nothing was typed since.
	undo       undoRecord
	hasUndo    bool
	lastAction time.Time
	// tail is the phrase memory: completed words since the last hard
	// boundary, oldest first (plan Б2).
	tail   []tailWord
	timer  *time.Timer
	cur    layout.Layout
	appID  string
	paused bool
	warned bool
}

// New builds a Daemon; Run blocks until its context is cancelled.
func New(cfg *config.Config, ana *analyze.Analyzer, nir *niri.Client, inj WordInjector, verbose, dryRun bool) *Daemon {
	return &Daemon{
		Cfg:  cfg,
		Ana:  ana,
		Niri: nir,
		Inj:  inj,
		FB: &feedback.F{
			Notify: cfg.Feedback.Notify,
			Sound:  cfg.Feedback.Sound,
		},
		Verbose:    verbose,
		DryRun:     dryRun,
		devices:    map[string]bool{},
		appLayouts: map[string]layout.Layout{},
		cur:        layout.LayoutEN,
	}
}

// Run starts the daemon and blocks until ctx is cancelled. SIGUSR1
// toggles the paused state (bind it in niri to:
// spawn "pkill" "-USR1" "lapsus").
func (d *Daemon) Run(ctx context.Context) error {
	unlock, err := d.lockSingleton()
	if err != nil {
		return err
	}
	defer unlock()

	d.refreshNiriState()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	go func() {
		for range sig {
			d.TogglePause()
			state := "running"
			if d.Paused() {
				state = "paused"
			}
			fmt.Fprintf(os.Stderr, "lapsus daemon: %s\n", state)
		}
	}()

	go d.watchNiri(ctx)
	go d.superviseDevices(ctx)
	go d.pollLayouts(ctx)
	go d.serveUndo(ctx)

	<-ctx.Done()
	return nil
}

// pollLayouts periodically re-reads the active layout. The event stream
// normally keeps d.cur in sync, but a missed event (e.g. a toggle pressed
// while the daemon is busy injecting) would silently corrupt every
// keycode translation; the poll bounds that damage to ~layoutPoll.
func (d *Daemon) pollLayouts(ctx context.Context) {
	t := time.NewTicker(layoutPoll)
	defer t.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ls, err := d.Niri.KeyboardLayouts(); err == nil {
			d.setLayouts(ls)
		}
	}
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Verbose {
		fmt.Fprintf(os.Stderr, "lapsus daemon: "+format+"\n", args...)
	}
}

// lockSingleton takes an exclusive lock so that only one daemon runs:
// two daemons would both react to the same keystrokes and inject twice.
// The lock lives in XDG_RUNTIME_DIR and dies with the process.
func (d *Daemon) lockSingleton() (func(), error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	path := filepath.Join(dir, "lapsus.daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("lapsus daemon уже запущен (lock %s занят)", path)
	}
	return func() { f.Close() }, nil
}

// refreshNiriState queries the initial focused window and layouts once,
// before the event stream takes over.
func (d *Daemon) refreshNiriState() {
	if win, err := d.Niri.FocusedWindow(); err == nil {
		d.mu.Lock()
		d.appID = win.AppIDOr("")
		d.mu.Unlock()
	}
	if ls, err := d.Niri.KeyboardLayouts(); err == nil {
		d.setLayouts(ls)
	}
}

// setLayouts updates the active layout. The word buffer survives: it
// stores raw presses, and the boundary reading adapts to the reconciled
// layout — a word typed entirely during a staleness window still fixes
// correctly. The reading under each layout stays pure; text received by
// the application across a real mid-word switch is mixed, and the
// dictionary gate simply stays silent unless the pure reading makes
// sense.
func (d *Daemon) setLayouts(ls *niri.KeyboardLayouts) {
	d.mu.Lock()
	var cur layout.Layout
	changed := false
	if l, ok := ls.Current(); ok {
		if l != d.cur {
			cur = l
			d.cur = cur
			d.warned = false
			changed = true
		}
	} else if !d.warned {
		d.warned = true
		d.logf("unrecognized layout names %v, assuming EN", ls.Names)
	}
	if changed {
		// The fix-run counter belongs to the old layout.
		d.gen++
		d.fixRunCount = 0
	}
	d.mu.Unlock()
	if changed && d.OnLayoutChange != nil {
		d.OnLayoutChange(cur)
	}
}

// watchNiri keeps the event stream open, tracking layout switches and
// window focus (track-layout "window" makes both per-window). Reconnects
// with a fixed delay on failures.
func (d *Daemon) watchNiri(ctx context.Context) {
	for ctx.Err() == nil {
		stream, wait, err := d.Niri.EventStream(ctx)
		if err != nil {
			d.logf("event stream unavailable: %v", err)
		} else {
			sc := bufio.NewScanner(stream)
			for sc.Scan() {
				if ctx.Err() != nil {
					break
				}
				d.handleStreamEvent(sc.Text())
			}
			stream.Close()
			// The child is dead by EOF; Wait only reaps it. Without this
			// every stream reconnect would leave a zombie process.
			_ = wait()
		}
		if ctx.Err() != nil {
			return
		}
		d.logf("reconnecting event stream")
		select {
		case <-ctx.Done():
			return
		case <-time.After(niriRetry):
		}
	}
}

// handleStreamEvent processes one JSON line of the niri event stream.
func (d *Daemon) handleStreamEvent(line string) {
	var ev struct {
		KeyboardLayoutsChanged *struct {
			KeyboardLayouts niri.KeyboardLayouts `json:"keyboard_layouts"`
		} `json:"KeyboardLayoutsChanged"`
		KeyboardLayoutSwitched *struct {
			Idx int `json:"idx"`
		} `json:"KeyboardLayoutSwitched"`
		WindowFocusChanged *struct {
			ID uint64 `json:"id"`
		} `json:"WindowFocusChanged"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		d.logf("skipping event %q: %v", line, err)
		return
	}
	switch {
	case ev.KeyboardLayoutsChanged != nil:
		ls := ev.KeyboardLayoutsChanged.KeyboardLayouts
		d.setLayouts(&ls)
	case ev.KeyboardLayoutSwitched != nil:
		// The event carries only the index; refetch for the name.
		if ls, err := d.Niri.KeyboardLayouts(); err == nil {
			d.setLayouts(ls)
		}
		d.learnAppLayout()
	case ev.WindowFocusChanged != nil:
		// track-layout "window": the focused window brings its own
		// layout, and app exclusions follow the focus too.
		if win, err := d.Niri.FocusedWindow(); err == nil {
			d.mu.Lock()
			if win.AppIDOr("") != d.appID {
				d.appID = win.AppIDOr("")
				d.clearBuf()  // the half-typed word belongs to the old window
				d.clearTail() // so does the phrase tail
			}
			d.mu.Unlock()
		}
		if ls, err := d.Niri.KeyboardLayouts(); err == nil {
			d.setLayouts(ls)
		}
		d.restoreAppLayout()
	}
}

// ownDevice reports whether the evdev device belongs to lapsus itself:
// reading our own virtual keyboard would feed the injections back into
// the word buffer. This is what keeps injections invisible on the evdev
// side (plan А2); the wtype backend is invisible structurally — the
// compositor consumes keysyms without creating evdev devices.
func ownDevice(name string) bool {
	return strings.Contains(strings.ToLower(name), "lapsus")
}

// superviseDevices periodically rescans input devices, opening new
// keyboards and retiring dead readers (hotplug).
func (d *Daemon) superviseDevices(ctx context.Context) {
	for ctx.Err() == nil {
		if devs, err := evdev.Discover(); err == nil {
			for _, dev := range devs {
				if ownDevice(dev.Name) {
					continue
				}
				d.mu.Lock()
				known := d.devices[dev.Path]
				d.devices[dev.Path] = true
				d.mu.Unlock()
				if known {
					continue
				}
				r, err := evdev.Open(dev)
				if err != nil {
					d.logf("cannot open %s: %v", dev, err)
					continue
				}
				go d.readDevice(ctx, dev.Path, r)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(deviceRescan):
		}
	}
}

// readDevice pumps events from one device until it disappears.
func (d *Daemon) readDevice(ctx context.Context, path string, r *evdev.Reader) {
	defer func() {
		r.Close()
		d.mu.Lock()
		delete(d.devices, path)
		d.mu.Unlock()
	}()
	d.logf("reading %s", path)
	for {
		ev, err := r.ReadEvent()
		if err != nil || ctx.Err() != nil {
			d.logf("device %s stopped: %v", path, err)
			return
		}
		d.handleEvent(ev)
	}
}

// handleEvent feeds one raw evdev event into the word state machine.
func (d *Daemon) handleEvent(ev evdev.Event) {
	if ev.Type != evdev.TypeKey {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if ev.Value == evdev.ValKeyRepeat {
		// Held-key autorepeat: the application receives compositor- and
		// kernel-generated repeats that cannot be counted from evdev,
		// so the buffer length is no longer trustworthy — and a fix
		// with a wrong length would delete unrelated text. Drop the
		// word and the phrase tail; tracking resumes with the next one.
		d.clearBuf()
		d.clearTail()
		return
	}

	switch ev.Code {
	case evdev.KeyLeftShift, evdev.KeyRightShift:
		d.shift = ev.Value != evdev.ValKeyUp
		return
	case evdev.KeyLeftCtrl, evdev.KeyRightCtrl:
		if ev.Value == evdev.ValKeyDown {
			// A shortcut is starting: the buffered word is not going to
			// end in a fixable way.
			d.ctrlHeld = true
			d.clearBuf()
		} else if ev.Value == evdev.ValKeyUp {
			d.ctrlHeld = false
		}
		return
	case evdev.KeyLeftAlt:
		if ev.Value == evdev.ValKeyDown {
			d.altHeld = true
			d.leftAltHeld = true
			if d.Cfg.Daemon.DoubleAltFlip {
				d.altDown()
			} else {
				d.clearBuf() // trigger off: an Alt press is a combo start
				d.clearTail()
			}
		} else if ev.Value == evdev.ValKeyUp {
			d.altHeld = false
			d.leftAltHeld = false
			if d.Cfg.Daemon.DoubleAltFlip {
				d.altUp()
			}
		}
		return
	case evdev.KeyRightAlt:
		// Right Alt belongs to the compositor hotkey (the fix key /
		// Compose); the daemon only tracks its held state. It must not
		// clear the phrase tail: the key is the user's flip habit, and
		// presses of it happen all the time around the tail.
		if ev.Value == evdev.ValKeyDown {
			d.altHeld = true
		} else if ev.Value == evdev.ValKeyUp {
			d.altHeld = false
		}
		return
	}
	// Everything past the modifier cases is a non-modifier key; a press
	// or autorepeat of it affects the text one way or another. The fix
	// key (right Alt) returns inside its own case and never counts here,
	// so the undo gate stays clean when the key doubles as undo.
	if ev.Value != evdev.ValKeyUp {
		d.lastAction = time.Now()
	}
	if ev.Value == evdev.ValKeyDown && ev.Code != evdev.KeyLeftAlt {
		// Any other key between the taps cancels the double-tap.
		d.altTapWait = false
		d.altDouble = false
	}
	if d.ctrlHeld || d.altHeld {
		// A combo is in progress (Alt+letter, Alt+Enter...): drop the
		// word, it is not going to end in a fixable way. The tail
		// survives a lone right-Alt hold (the fix key / Compose) but not
		// Ctrl or left-Alt combos.
		d.clearBuf()
		if d.ctrlHeld || d.leftAltHeld {
			d.clearTail()
		}
		return
	}
	if ev.Value != evdev.ValKeyDown {
		// Ignore key releases and autorepeats: repeats come from the
		// kernel timer, while the app receives compositor-generated
		// repeats, so counting them would desync the buffer.
		return
	}

	switch ev.Code {
	case evdev.KeyBackspace:
		if len(d.buf) > 0 {
			d.buf = d.buf[:len(d.buf)-1]
		} else {
			// Deleting into already-completed words: the presses no
			// longer match the text, the tail cannot be trusted.
			d.clearTail()
		}
		return
	case evdev.KeyDelete, evdev.KeyEnter, evdev.KeyTab, evdev.KeyEsc,
		evdev.KeyCapslock, evdev.KeyUp, evdev.KeyDown, evdev.KeyLeft, evdev.KeyRight,
		evdev.KeyHome, evdev.KeyEnd, evdev.KeyPageUp, evdev.KeyPageDown:
		// The word ends in a way that is not safe to fix after: text may
		// be committed (enter), completed (tab) or the caret moved.
		d.clearBuf()
		d.clearTail()
		return
	}

	ch, ok := keymap.CharFor(ev.Code, d.shift, d.cur)
	if !ok {
		d.clearBuf() // F-keys, numpad, media keys, anything untranslatable
		d.clearTail()
		return
	}
	// Deliberately without the character: -v goes to journald, and the
	// keystroke stream includes passwords typed anywhere.
	d.logf("key %d (layout %v)", ev.Code, d.cur)
	if ch == ' ' {
		// Only the space completes the word: it sits on the same key in
		// every layout, so restoring it after the fix is always right.
		d.finishWord(ch)
		return
	}
	// Everything else printable joins the word: the Russian б ж э ю х ъ
	// sit on the punctuation keys , . ; ' [ ], so a wrong-layout word
	// contains "punctuation" ("hs,f" is "рыба"). The dictionary gate in
	// maybeFix only fires when the whole flipped reading is a real word,
	// so genuinely punctuated text ("ок,") is never touched.
	//
	// The press is buffered raw, without the character: the reading is
	// derived at the boundary under the then-current layout.
	d.buf = append(d.buf, keyPress{code: ev.Code, shift: d.shift})
	d.armPauseTimer()
}

// reading renders the buffered presses as text under layout l. Every
// buffered press is translatable by construction (untranslatable keys
// clear the buffer before they can be appended).
func (d *Daemon) reading(presses []keyPress, l layout.Layout) string {
	var b strings.Builder
	for _, p := range presses {
		if ch, ok := keymap.CharFor(p.code, p.shift, l); ok {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// clearBuf drops the buffered word and invalidates the pause timer.
func (d *Daemon) clearBuf() {
	d.buf = nil
	d.gen++
}

// armPauseTimer (re)starts the idle timer that treats the buffered word
// as finished. It is only armed when the pause boundary is enabled:
// a non-zero boundary_pause_ms. A too-short pause splits slow-typed
// words into fragments, so the default is 0 (the space boundary only);
// the
// generation counter discards stale firings after the buffer changed.
func (d *Daemon) armPauseTimer() {
	pause := time.Duration(d.Cfg.Daemon.BoundaryPauseMs) * time.Millisecond
	if pause <= 0 {
		return
	}
	d.gen++
	gen := d.gen
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(pause, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if gen != d.gen {
			return
		}
		d.finishWord(0)
	})
}

// finishWord takes the buffered word and tries to fix it. sep is the
// separator that completed the word — always a space (0 for the pause
// boundary): it has already reached the application and sits between the
// word and the caret, so the fix must delete and re-type it as well.
// The completed word goes into the phrase memory (the tail). Caller must
// hold d.mu.
func (d *Daemon) finishWord(sep rune) {
	if len(d.buf) == 0 {
		// A space with nothing buffered still lands in the text: it
		// belongs to the newest tail word.
		if sep == ' ' {
			d.noteSpace()
		}
		return
	}
	presses := d.buf
	d.clearBuf()
	word := d.reading(presses, d.cur)
	if sep != 0 {
		d.logf("word %q complete (sep %q)", word, sep)
	} else {
		d.logf("word %q complete (pause)", word)
	}
	entry := tailWord{presses: presses, cur: d.cur, spaceAfter: sep == ' '}
	if fixed, ok := d.maybeFix(presses, word, sep); ok {
		// The application received the corrected text; maybeFix reports
		// it with the separator, the entry stores the bare word.
		entry.appText = strings.TrimSuffix(fixed, string(sep))
	} else {
		entry.appText = word
	}
	d.pushTail(entry)
}

// noteSpace marks a space typed with an empty buffer: it sits after the
// newest tail word (the pause boundary may have completed it earlier
// without one).
func (d *Daemon) noteSpace() {
	if n := len(d.tail); n > 0 {
		d.tail[n-1].spaceAfter = true
	}
}

// pushTail appends a completed word to the phrase memory.
func (d *Daemon) pushTail(w tailWord) {
	d.tail = append(d.tail, w)
	if len(d.tail) > tailCap {
		d.tail = d.tail[len(d.tail)-tailCap:]
	}
}

// clearTail drops the phrase memory: something happened that makes the
// tail's replacement unreliable (hard boundary, combo, autorepeat,
// focus moved). Caller must hold d.mu.
func (d *Daemon) clearTail() {
	d.tail = nil
}

// maybeFix decides whether the word must be corrected and injects the
// fix. word is the reading of presses under the layout believed active —
// the text the application received when d.cur is right, which the
// dictionaries then judge exactly like before. The returned corrected
// text is what the application holds now (ok=false when nothing was
// injected). Caller must hold d.mu.
func (d *Daemon) maybeFix(presses []keyPress, word string, sep rune) (string, bool) {
	if d.paused {
		d.logf("paused, skipping %q", word)
		return "", false
	}
	if niri.AppIDIn(d.appID, d.Cfg.Daemon.ExcludeAppIDs) {
		d.logf("app %q excluded, skipping %q", d.appID, word)
		return "", false
	}
	// Short words are risky to fix automatically: a mid-word pause can
	// split a word into fragments, and single letters map to real
	// one-letter Russian words ("c" → "с"). The manual hotkey path has
	// no such limit — there the intent is explicit.
	if utf8.RuneCountInString(word) < d.Cfg.Daemon.MinWordLen {
		d.logf("word %q shorter than min_word_len=%d, skipping", word, d.Cfg.Daemon.MinWordLen)
		return "", false
	}
	corrected, needsFix := d.Ana.Analyze(word, d.cur)
	if !needsFix {
		d.resetFixRun()
		d.logf("no fix needed for %q", word)
		return "", false
	}
	if d.DryRun {
		d.logf("dry run: would replace %q with %q", word, corrected)
		return "", false
	}
	// The space sits between the word and the caret: delete it with the
	// word and type it back after the fix, verbatim — the space is the
	// one separator identical in every layout. Only a space can complete
	// a word (the pause boundary passes sep=0), so there is nothing to
	// flip here.
	old, fixed := word, corrected
	if sep != 0 {
		old += string(sep)
		fixed += string(sep)
	}
	if err := d.Inj.ReplaceWord(old, fixed); err != nil {
		d.logf("inject failed: %v", err)
		return "", false
	}
	d.logf("fixed %q → %q", word, corrected)
	// The fix key can revert this while it is fresh (plan А4): remember
	// exactly what was injected and what stood there before.
	d.undo = undoRecord{old: old, fixed: fixed, appID: d.appID, at: time.Now()}
	d.hasUndo = true
	// Off the input path: Fire spawns processes, and a stall there must
	// not freeze keystroke handling (the FB fields are race-safe).
	go d.FB.Fire(word, corrected)
	if d.Cfg.Daemon.SwitchLayout {
		// Consecutive fixes to the same language mean a wrong-layout
		// episode: once it reaches the threshold, move the layout so the
		// remaining words type correctly. A single fixed word does not
		// yank the layout; a real word or a layout change resets the run.
		target := analyze.GuessLayout(corrected)
		if d.fixRunLang != target {
			d.fixRunLang, d.fixRunCount = target, 0
		}
		d.fixRunCount++
		if d.fixRunCount >= d.Cfg.Daemon.SwitchAfterWords {
			d.resetFixRun()
			switched, err := d.Niri.SwitchToLayoutOf(corrected)
			if err != nil {
				d.logf("cannot switch layout: %v", err)
			}
			if switched && d.hasUndo {
				d.undo.switched = true // the undo takes the move back
			}
		}
	}
	return fixed, true
}

// FixTail flips the words just typed — the daemon's phrase memory (plan
// Б2 hotkey) — to their other-layout readings. The replacement goes
// through the injector (BackSpace ×N + retype), no selection, no
// clipboard, the same in GUI apps and terminals. Words the auto-fix
// already corrected read the same under the other layout and stay in
// place, so a partially fixed phrase gets only its missed words
// flipped. ok=false when there is nothing to flip.
func (d *Daemon) FixTail() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.tail) == 0 {
		return false
	}
	var oldB, newB strings.Builder
	changed := false
	for i, w := range d.tail {
		flip := d.reading(w.presses, layout.Other(w.cur))
		d.tail[i].appText = flip
		if flip != w.appText {
			changed = true
		}
		oldB.WriteString(w.appText)
		newB.WriteString(flip)
		if w.spaceAfter {
			oldB.WriteString(" ")
			newB.WriteString(" ")
		}
	}
	if !changed {
		return false
	}
	// The state the undo reverts is being rewritten.
	d.hasUndo = false
	if err := d.Inj.ReplaceWord(oldB.String(), newB.String()); err != nil {
		d.logf("tail inject failed: %v", err)
		return false
	}
	d.logf("tail: %q → %q", oldB.String(), newB.String())
	// Off the input path (see maybeFix).
	go d.FB.Fire(oldB.String(), newB.String())
	return true
}

// learnAppLayout remembers the focused application's language at the
// moment its layout changes. Layout switches are deliberate (a manual
// toggle, or lapsus switching after a fix), so this is the app's
// intended language. Learning from raw keystrokes instead records the
// wrong-layout state during wrong-layout episodes and inverts the
// memory. Caller must hold d.mu.
func (d *Daemon) learnAppLayout() {
	if !d.Cfg.Daemon.RememberWindowLayout || d.appID == "" {
		return
	}
	d.appLayouts[d.appID] = d.cur
}

// Undo reverts the last auto-fix: the corrected text is replaced by what
// was typed before, the fix-run resets so the layout stops drifting, and
// a layout move made by this very fix is taken back. ok=false when there
// is nothing fresh to undo — the fix was too long ago, text was typed or
// moved since (the caret may be elsewhere), or the focus moved to
// another application — and the caller does its normal toggle instead.
func (d *Daemon) Undo() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasUndo {
		return false
	}
	window := time.Duration(d.Cfg.Daemon.UndoWindowMs) * time.Millisecond
	if window <= 0 || time.Since(d.undo.at) > window {
		return false
	}
	if !d.lastAction.Before(d.undo.at) {
		return false
	}
	if d.appID != d.undo.appID {
		return false
	}
	rec := d.undo
	d.hasUndo = false
	if err := d.Inj.ReplaceWord(rec.fixed, rec.old); err != nil {
		d.logf("undo inject failed: %v", err)
		return false
	}
	d.resetFixRun()
	if rec.switched {
		if _, err := d.Niri.SwitchToLayoutOf(rec.old); err != nil {
			d.logf("undo layout switch failed: %v", err)
		}
	}
	// The phrase memory holds the fixed text (bare word); after the
	// revert it holds what was typed again.
	if n := len(d.tail); n > 0 && d.tail[n-1].appText == strings.TrimSuffix(rec.fixed, " ") {
		d.tail[n-1].appText = strings.TrimSuffix(rec.old, " ")
	}
	d.logf("undo: %q → %q", rec.fixed, rec.old)
	d.FB.Fire(rec.fixed, rec.old)
	return true
}

// undoSocketPath mirrors lockSingleton's directory: the socket lives in
// XDG_RUNTIME_DIR and dies with the session.
func undoSocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, undoSocket)
}

// serveUndo answers the one-shot fix CLI: an "undo" line reverts the
// last auto-fix when it is still fresh ("ok") or is declined ("no"), in
// which case the CLI does its normal toggle.
func (d *Daemon) serveUndo(ctx context.Context) {
	path := undoSocketPath()
	os.Remove(path) // a stale socket file from a crashed instance
	ln, err := net.Listen("unix", path)
	if err != nil {
		d.logf("undo socket unavailable: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(path)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logf("undo accept: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		go d.handleUndoConn(conn)
	}
}

func (d *Daemon) handleUndoConn(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	cmd := strings.TrimSpace(string(buf[:n]))
	if err != nil {
		return
	}
	reply := "no\n"
	switch cmd {
	case "undo":
		if d.Undo() {
			reply = "ok\n"
		}
	case "tail":
		if d.FixTail() {
			reply = "ok\n"
		}
	default:
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte(reply))
}

// resetFixRun drops the consecutive-fix run. Caller must hold d.mu.
func (d *Daemon) resetFixRun() {
	d.fixRunCount = 0
}

// tapWindow is the current double-tap window.
func (d *Daemon) tapWindow() time.Duration {
	if ms := d.Cfg.Daemon.DoubleAltWindowMs; ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return maxDoubleTapGap
}

// altDown / altUp implement the double-tap-of-left-Alt detector: two
// taps within doubleTapWindow with no other key in between flip the
// buffered word and move the layout along with it.
func (d *Daemon) altDown() {
	now := time.Now()
	d.logf("alt down (wait=%v, since last up=%v)", d.altTapWait, now.Sub(d.altLastUp))
	if d.altTapWait && now.Sub(d.altLastUp) <= d.tapWindow() {
		d.altTapWait = false
		d.altDouble = true
	}
}

func (d *Daemon) altUp() {
	d.logf("alt up (double=%v)", d.altDouble)
	if d.altDouble {
		d.altDouble = false
		d.flipBufferWord()
		return
	}
	d.altTapWait = true
	d.altLastUp = time.Now()
}

// flipBufferWord flips the buffered word to the other layout and moves
// the layout along with it — the explicit "typed in the wrong layout,
// fix it now" action. The word must still be in the buffer (no
// separator pressed yet). Caller must hold d.mu.
func (d *Daemon) flipBufferWord() {
	if len(d.buf) == 0 {
		return
	}
	presses := d.buf
	d.clearBuf()
	// The double-Alt is a pure toggle: flip the buffered presses
	// positionally, unconditionally — even gibberish and even correct
	// words. The dictionary-gated fixing is the auto-fix path.
	cur := d.cur
	typed := d.reading(presses, cur)
	flipped := d.reading(presses, layout.Other(cur))
	if flipped == typed {
		d.logf("double-alt: %q maps to itself, nothing to flip", typed)
		return
	}
	if err := d.Inj.ReplaceWord(typed, flipped); err != nil {
		d.logf("double-alt inject failed: %v", err)
		return
	}
	d.logf("double-alt: %q → %q", typed, flipped)
	if _, err := d.Niri.SwitchToLayoutOf(flipped); err != nil {
		d.logf("double-alt layout switch failed: %v", err)
	}
}

// restoreAppLayout switches the focused window to the language last
// used in this application, when it differs from the current one.
func (d *Daemon) restoreAppLayout() {
	if !d.Cfg.Daemon.RememberWindowLayout {
		return
	}
	d.mu.Lock()
	app := d.appID
	remembered, known := d.appLayouts[app]
	cur := d.cur
	d.mu.Unlock()
	if app == "" {
		return
	}
	d.logf("focus %q: remembered=%v cur=%v", app, remembered, cur)
	if niri.AppIDIn(app, d.Cfg.Daemon.ExcludeAppIDs) {
		return
	}

	target, known := remembered, known
	usedDefault := false
	if !known {
		// Unknown application: the configured default language applies.
		target, known = defaultLayoutTarget(d.Cfg.Daemon.DefaultLayout)
		usedDefault = true
	}
	if !known || target == cur {
		return
	}
	ls, err := d.Niri.KeyboardLayouts()
	if err != nil {
		return
	}
	if idx := niri.LayoutIndex(ls.Names, target); idx >= 0 && idx != ls.CurrentIdx {
		if usedDefault {
			d.logf("new app %q: applying default layout %q", app, target)
		} else {
			d.logf("restoring %q layout for app %q", target, app)
		}
		if err := d.Niri.SwitchLayout(idx); err != nil {
			d.logf("layout restore failed: %v", err)
		}
	}
}

// defaultLayoutTarget parses the daemon.default_layout setting.
func defaultLayoutTarget(s string) (layout.Layout, bool) {
	switch strings.ToLower(s) {
	case "en":
		return layout.LayoutEN, true
	case "ru":
		return layout.LayoutRU, true
	}
	return layout.LayoutEN, false
}

// DefaultLayout returns the configured default language for unknown
// applications: "", "en" or "ru".
func (d *Daemon) DefaultLayout() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Cfg.Daemon.DefaultLayout
}

// SetDefaultLayout changes the default language for unknown applications
// ("" = off) and persists the setting to ConfigPath when set.
func (d *Daemon) SetDefaultLayout(v string) error {
	d.mu.Lock()
	d.Cfg.Daemon.DefaultLayout = v
	d.mu.Unlock()
	if d.ConfigPath == "" {
		return nil
	}
	return config.Save(d.ConfigPath, d.Cfg)
}

// RememberLayout reports whether per-application layout memory is on.
func (d *Daemon) RememberLayout() bool {
	return d.Cfg.Daemon.RememberWindowLayout
}

// SetRememberLayout toggles per-application layout memory and persists
// the setting to ConfigPath when set.
func (d *Daemon) SetRememberLayout(on bool) error {
	d.mu.Lock()
	d.Cfg.Daemon.RememberWindowLayout = on
	d.mu.Unlock()
	if d.ConfigPath == "" {
		return nil
	}
	return config.Save(d.ConfigPath, d.Cfg)
}

// TogglePause flips the paused state.
func (d *Daemon) TogglePause() {
	d.SetPaused(!d.Paused())
}

// Paused reports whether auto-fixing is currently paused.
func (d *Daemon) Paused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

// SetPaused turns daemon auto-fixing on/off.
func (d *Daemon) SetPaused(p bool) {
	d.mu.Lock()
	d.paused = p
	d.mu.Unlock()
	if d.OnPauseChange != nil {
		d.OnPauseChange(p)
	}
}

// CurrentLayout returns the active layout the daemon translates
// keystrokes with.
func (d *Daemon) CurrentLayout() layout.Layout {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur
}

// Feedback returns the current feedback settings.
func (d *Daemon) Feedback() (notify bool, sound string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Cfg.Feedback.Notify, d.Cfg.Feedback.Sound
}

// SetFeedback flips feedback settings: applies them to the running
// daemon immediately and persists them to ConfigPath when set.
func (d *Daemon) SetFeedback(notify bool, sound string) error {
	d.mu.Lock()
	d.Cfg.Feedback.Notify = notify
	d.Cfg.Feedback.Sound = sound
	if d.FB != nil {
		d.FB.Set(notify, sound)
	}
	d.mu.Unlock()
	if d.ConfigPath == "" {
		return nil
	}
	return config.Save(d.ConfigPath, d.Cfg)
}
