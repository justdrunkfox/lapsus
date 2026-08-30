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
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/evdev"
	"github.com/voev/lapsus/feedback"
	"github.com/voev/lapsus/layout"
	"github.com/voev/lapsus/niri"
	"github.com/voev/lapsus/wayland"
)

const (
	// deviceRescan is the hotplug polling interval: new keyboards are
	// picked up (and dead ones retired) without restarts.
	deviceRescan = 3 * time.Second
	// niriRetry is the reconnect delay for the event stream watcher.
	niriRetry = 3 * time.Second
)

// Daemon is the running auto-fix instance.
type Daemon struct {
	Cfg     *config.Config
	Ana     *analyze.Analyzer
	Niri    *niri.Client
	Way     *wayland.Tools
	FB      *feedback.F
	Verbose bool
	DryRun  bool

	mu      sync.Mutex
	devices map[string]bool
	shift   bool
	buf     []rune
	gen     uint64
	timer   *time.Timer
	cur     layout.Layout
	appID   string
	paused  bool
	warned  bool
}

// New builds a Daemon; Run blocks until its context is cancelled.
func New(cfg *config.Config, ana *analyze.Analyzer, nir *niri.Client, way *wayland.Tools, verbose, dryRun bool) *Daemon {
	return &Daemon{
		Cfg:  cfg,
		Ana:  ana,
		Niri: nir,
		Way:  way,
		FB: &feedback.F{
			Notify: cfg.Feedback.Notify,
			Sound:  cfg.Feedback.Sound,
			Reap:   true,
		},
		Verbose: verbose,
		DryRun:  dryRun,
		devices: map[string]bool{},
		cur:     layout.LayoutEN,
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
			d.mu.Lock()
			state := "running"
			if d.paused {
				state = "paused"
			}
			d.mu.Unlock()
			fmt.Fprintf(os.Stderr, "lapsus daemon: %s\n", state)
		}
	}()

	go d.watchNiri(ctx)
	go d.superviseDevices(ctx)

	<-ctx.Done()
	return nil
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
	defer d.mu.Unlock()
	if l, ok := ls.Current(); ok {
		d.cur = l
		d.warned = false
	} else if !d.warned {
		d.warned = true
		d.logf("unrecognized layout names %v, assuming EN", ls.Names)
	}
	d.buf = nil
	d.gen++
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
	case ev.WindowFocusChanged != nil:
		// track-layout "window": the focused window brings its own
		// layout, and app exclusions follow the focus too.
		if win, err := d.Niri.FocusedWindow(); err == nil {
			d.mu.Lock()
			d.appID = win.AppIDOr("")
			d.mu.Unlock()
		}
		if ls, err := d.Niri.KeyboardLayouts(); err == nil {
			d.setLayouts(ls)
		}
	}
}

// superviseDevices periodically rescans input devices, opening new
// keyboards and retiring dead readers (hotplug).
func (d *Daemon) superviseDevices(ctx context.Context) {
	for ctx.Err() == nil {
		if devs, err := evdev.Discover(); err == nil {
			for _, dev := range devs {
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
	case evdev.KeyLeftCtrl, evdev.KeyRightCtrl, evdev.KeyLeftAlt, evdev.KeyRightAlt:
		if ev.Value == evdev.ValKeyDown {
			// A shortcut is starting: the buffered word is not going to
			// end in a fixable way.
			d.clearBuf()
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

	ch, ok := charFor(ev.Code, d.shift, d.cur)
	if !ok {
		d.clearBuf() // F-keys, numpad, media keys, anything untranslatable
		return
	}
	switch {
	case isWordChar(ch):
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
	if err := d.Way.ReplaceWord(old, fixed); err != nil {
		d.logf("inject failed: %v", err)
		return
	}
	d.logf("fixed %q → %q", word, corrected)
	d.FB.Fire(word, corrected)
	if d.Cfg.Daemon.SwitchLayout {
		if _, err := d.Niri.SwitchToLayoutOf(corrected); err != nil {
			d.logf("cannot switch layout: %v", err)
		}
	}
}

// TogglePause flips the paused state.
func (d *Daemon) TogglePause() {
	d.mu.Lock()
	d.paused = !d.paused
	d.mu.Unlock()
}
