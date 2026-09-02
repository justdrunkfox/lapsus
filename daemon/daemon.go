// Package daemon implements the lapsus auto-fix daemon (M2): it reads
// keystrokes from evdev devices (without grabbing them), tracks the word
// being typed, and when the word ends in a way that is safe to fix — a
// space or punctuation boundary, or an idle pause — replaces it with the
// corrected version if the dictionaries are confident it was typed in
// the wrong layout.
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
	"os"
	"os/signal"
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
	// for missed KeyboardLayoutSwitched events).
	layoutPoll = 300 * time.Millisecond
	// maxDoubleTapGap bounds doubleTapWindow when the config value is 0.
	maxDoubleTapGap = 5 * time.Second
)

// Daemon is the running auto-fix instance.
// WordInjector is the injection surface the daemon needs: replace the
// word before the caret (including its trailing separator, when there
// was one) with the corrected text.
type WordInjector interface {
	ReplaceWord(old, corrected string) error
}

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
	ctrlHeld   bool
	altHeld    bool
	altTapWait bool
	altDouble  bool
	altLastUp  time.Time
	// fix-run tracking: consecutive fixes to the same language move the
	// layout once the run reaches switch_after_words.
	fixRunLang  layout.Layout
	fixRunCount int
	shift       bool
	buf         []rune
	gen         uint64
	timer       *time.Timer
	cur         layout.Layout
	appID       string
	paused      bool
	warned      bool
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

// setLayouts updates the active layout and clears the word buffer:
// characters from different layouts do not mix into a fixable word.
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
		// A real layout change: mixed-layout characters are garbage, and
		// the fix-run counter belongs to the old layout.
		d.buf = nil
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
		stream, err := d.Niri.EventStream(ctx)
		if err != nil {
			d.logf("event stream unavailable: %v", err)
		} else {
			sc := bufio.NewScanner(stream)
			for sc.Scan() {
				if ctx.Err() != nil {
					stream.Close()
					return
				}
				d.handleStreamEvent(sc.Text())
			}
			stream.Close()
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
				d.clearBuf() // the half-typed word belongs to the old window
			}
			d.mu.Unlock()
		}
		if ls, err := d.Niri.KeyboardLayouts(); err == nil {
			d.setLayouts(ls)
		}
		d.restoreAppLayout()
	}
}

// superviseDevices periodically rescans input devices, opening new
// keyboards and retiring dead readers (hotplug).
func (d *Daemon) superviseDevices(ctx context.Context) {
	for ctx.Err() == nil {
		if devs, err := evdev.Discover(); err == nil {
			for _, dev := range devs {
				if strings.Contains(strings.ToLower(dev.Name), "lapsus") {
					// Our own uinput keyboard: reading it would feed
					// our injections back into the word buffer.
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
		// word; tracking resumes with the next one.
		d.clearBuf()
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
			if d.Cfg.Daemon.DoubleAltFlip {
				d.altDown()
			} else {
				d.clearBuf() // trigger off: an Alt press is a combo start
			}
		} else if ev.Value == evdev.ValKeyUp {
			d.altHeld = false
			if d.Cfg.Daemon.DoubleAltFlip {
				d.altUp()
			}
		}
		return
	case evdev.KeyRightAlt:
		// Right Alt belongs to the compositor hotkey (Multi_key);
		// the daemon only tracks its held state.
		if ev.Value == evdev.ValKeyDown {
			d.altHeld = true
		} else if ev.Value == evdev.ValKeyUp {
			d.altHeld = false
		}
		return
	}
	if ev.Value == evdev.ValKeyDown && ev.Code != evdev.KeyLeftAlt {
		// Any other key between the taps cancels the double-tap.
		d.altTapWait = false
		d.altDouble = false
	}
	if d.ctrlHeld || d.altHeld {
		// A combo is in progress (Alt+letter, Alt+Enter...): drop the
		// word, it is not going to end in a fixable way.
		d.clearBuf()
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
		}
		return
	case evdev.KeyDelete, evdev.KeyEnter, evdev.KeyTab, evdev.KeyEsc,
		evdev.KeyCapslock, evdev.KeyUp, evdev.KeyDown, evdev.KeyLeft, evdev.KeyRight,
		evdev.KeyHome, evdev.KeyEnd, evdev.KeyPageUp, evdev.KeyPageDown:
		// The word ends in a way that is not safe to fix after: text may
		// be committed (enter), completed (tab) or the caret moved.
		d.clearBuf()
		return
	}

	ch, ok := keymap.CharFor(ev.Code, d.shift, d.cur)
	if !ok {
		d.clearBuf() // F-keys, numpad, media keys, anything untranslatable
		return
	}
	d.logf("key %d → %q (layout %v)", ev.Code, ch, d.cur)
	switch {
	case keymap.IsWordChar(ch):
		d.buf = append(d.buf, ch)
		d.armPauseTimer()
	default:
		// Any printable separator (space, punctuation, quotes, brackets)
		// completes the word: it is typed as normal text and the word
		// before it is definitely finished. The separator itself is
		// passed along so the fix can restore it.
		d.finishWord(ch)
	}
}

// clearBuf drops the buffered word and invalidates the pause timer.
func (d *Daemon) clearBuf() {
	d.buf = nil
	d.gen++
}

// armPauseTimer (re)starts the idle timer that treats the buffered word
// as finished. It is only armed when the pause boundary is enabled:
// a non-zero boundary_pause_ms. A too-short pause splits slow-typed
// words into fragments, so the default is 0 (separators only); the
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
// separator character that completed the word (0 for the pause boundary):
// it has already reached the application and sits between the word and
// the caret, so the fix must delete and re-type it as well. Caller must
// hold d.mu.
func (d *Daemon) finishWord(sep rune) {
	if len(d.buf) == 0 {
		return
	}
	word := string(d.buf)
	d.clearBuf()
	if sep != 0 {
		d.logf("word %q complete (sep %q)", word, sep)
	} else {
		d.logf("word %q complete (pause)", word)
	}
	d.maybeFix(word, sep)
}

// maybeFix decides whether the word must be corrected and injects the
// fix. Caller must hold d.mu.
func (d *Daemon) maybeFix(word string, sep rune) {
	if d.paused {
		d.logf("paused, skipping %q", word)
		return
	}
	if niri.AppIDIn(d.appID, d.Cfg.Daemon.ExcludeAppIDs) {
		d.logf("app %q excluded, skipping %q", d.appID, word)
		return
	}
	// Short words are risky to fix automatically: a mid-word pause can
	// split a word into fragments, and single letters map to real
	// one-letter Russian words ("c" → "с"). The manual hotkey path has
	// no such limit — there the intent is explicit.
	if utf8.RuneCountInString(word) < d.Cfg.Daemon.MinWordLen {
		d.logf("word %q shorter than min_word_len=%d, skipping", word, d.Cfg.Daemon.MinWordLen)
		return
	}
	corrected, needsFix := d.Ana.Analyze(word, d.cur)
	if !needsFix {
		d.resetFixRun()
		d.logf("no fix needed for %q", word)
		return
	}
	if d.DryRun {
		d.logf("dry run: would replace %q with %q", word, corrected)
		return
	}
	// The separator sits between the word and the caret: delete it with
	// the word and type it back after the fix — flipped to the corrected
	// word's layout, same as the word itself. The user pressed the same
	// physical keys while intending the other layout: RU-typed "руддщ,"
	// (Shift+/ → «,») must become "hello?" — the comma was what the wrong
	// layout produced, "?" is what the user meant.
	old, fixed := word, corrected
	if sep != 0 {
		target := analyze.GuessLayout(corrected)
		flipped := layout.Map(string(sep), d.cur, target)
		old += string(sep)
		fixed += flipped
	}
	if err := d.Inj.ReplaceWord(old, fixed); err != nil {
		d.logf("inject failed: %v", err)
		return
	}
	d.logf("fixed %q → %q", word, corrected)
	d.FB.Fire(word, corrected)
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
			if _, err := d.Niri.SwitchToLayoutOf(corrected); err != nil {
				d.logf("cannot switch layout: %v", err)
			}
		}
	}
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
	if d.altTapWait && now.Sub(d.altLastUp) <= d.tapWindow() {
		d.altTapWait = false
		d.altDouble = true
	}
}

func (d *Daemon) altUp() {
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
	word := string(d.buf)
	if word == "" {
		return
	}
	cur := d.cur
	target := layout.Other(cur)
	flipped := layout.Map(word, cur, target)
	if flipped == word {
		d.clearBuf()
		return
	}
	d.clearBuf()
	if err := d.Inj.ReplaceWord(word, flipped); err != nil {
		d.logf("double-alt inject failed: %v", err)
		return
	}
	d.logf("double-alt: %q → %q", word, flipped)
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
		d.FB.Notify = notify
		d.FB.Sound = sound
	}
	d.mu.Unlock()
	if d.ConfigPath == "" {
		return nil
	}
	return config.Save(d.ConfigPath, d.Cfg)
}
