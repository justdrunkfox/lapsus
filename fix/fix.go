// Package fix implements the one-shot "fix the last typed word" pipeline
// triggered by an niri hotkey: select the word, read it back, analyze,
// inject the corrected word, then switch the layout (always after the
// injection — niri#3568: virtual keyboard events can reset the layout
// group).
package fix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/niri"
	"github.com/voev/lapsus/wayland"
)

// maxWordLen bounds the selection we are willing to replace. Ctrl+Shift+Left
// normally selects one word; a bigger selection means something unexpected
// is selected and replacing it would destroy text.
const maxWordLen = 64

// primaryReadAttempts is how many times the primary selection is read
// before giving up on it being empty.
const primaryReadAttempts = 3

// ErrBusy is returned when another fix instance is already running
// (hotkey pressed twice quickly). The caller should exit silently.
var ErrBusy = errors.New("another fix is already running")

// Options control a single fix run.
type Options struct {
	// DryRun: do everything except injecting text and switching layout.
	DryRun bool
	// Verbose: log each step to stderr.
	Verbose bool
}

// Fixer carries the fix pipeline dependencies.
type Fixer struct {
	Cfg  *config.Config
	Dict *dict.Dict
	Ana  *analyze.Analyzer
	Niri *niri.Client
	Way  *wayland.Tools
}

// Run executes one fix attempt against the focused window.
func (f *Fixer) Run(opts Options) error {
	lock, err := acquireLock()
	if err != nil {
		return ErrBusy
	}
	defer lock.close()

	win, err := f.Niri.FocusedWindow()
	if err != nil {
		return err
	}
	appID := win.AppIDOr("")
	terminal := niri.AppIDIn(appID, f.Cfg.Hotkey.Terminals)
	f.logf(opts, "focused window %d app_id=%q terminal=%v", win.ID, appID, terminal)

	if terminal {
		return f.fixTerminal(opts)
	}
	return f.fixGUI(opts)
}

// fixGUI is the path for regular GUI text fields: select the last word,
// read the primary selection, analyze, type the fix over the selection.
func (f *Fixer) fixGUI(opts Options) error {
	if f.Cfg.Capture.Method == "cut" {
		return f.fixGUICut(opts)
	}

	f.Way.ClearPrimary()
	if err := f.Way.SelectPrevWord(); err != nil {
		return err
	}
	sel := f.Way.ReadPrimaryWithRetries(primaryReadAttempts)

	word, ok := sanitizeSelection(sel)
	if !ok {
		if sel != "" {
			// A selection exists but is not usable; collapse it so the
			// caret and document are left untouched.
			f.Way.CollapseSelection()
		}
		return fmt.Errorf("no single-word selection after Ctrl+Shift+Left (got %d bytes)", len(sel))
	}
	f.logf(opts, "captured %q", word)

	corrected, needsFix := f.decide(word)
	if !needsFix {
		f.Way.CollapseSelection()
		f.logf(opts, "no fix needed for %q", word)
		return nil
	}
	if opts.DryRun {
		f.Way.CollapseSelection()
		f.logf(opts, "dry run: would replace %q with %q", word, corrected)
		return nil
	}

	if err := f.Way.TypeText(corrected); err != nil {
		return err
	}
	f.Way.ClearPrimary()
	return f.switchLayoutAfter(corrected, opts)
}

// fixGUICut is the fallback for applications that do not update the
// primary selection: cut the word into the clipboard, read it from there.
// The clipboard ends up cleared (its old content is lost — unavoidable
// without a clipboard manager).
func (f *Fixer) fixGUICut(opts Options) error {
	if err := f.Way.SelectPrevWord(); err != nil {
		return err
	}
	if err := f.Way.Cut(); err != nil {
		return err
	}
	word, ok := sanitizeSelection(f.Way.ReadClipboard())
	if !ok {
		// Nothing was cut (e.g. empty document) — paste back whatever the
		// cut removed, if anything.
		f.Way.Paste()
		return errors.New("no single-word selection after cut")
	}
	f.logf(opts, "captured %q", word)

	corrected, needsFix := f.decide(word)
	if !needsFix {
		f.Way.Paste()
		f.logf(opts, "no fix needed for %q", word)
		return nil
	}
	if opts.DryRun {
		f.Way.Paste()
		f.logf(opts, "dry run: would replace %q with %q", word, corrected)
		return nil
	}

	if err := f.Way.TypeText(corrected); err != nil {
		return err
	}
	f.Way.ClearClipboard()
	return f.switchLayoutAfter(corrected, opts)
}

// fixTerminal is the path for terminals: they do not update the primary
// selection on Ctrl+Shift+Left, so the word must be selected with the
// mouse beforehand. The fix replaces the word by BackSpace × word length
// plus typing — this assumes the caret sits right after the word, which
// is the normal case of fixing the word you have just typed.
func (f *Fixer) fixTerminal(opts Options) error {
	word, ok := sanitizeSelection(f.Way.ReadPrimary())
	if !ok {
		return errors.New("terminal mode: select the text with the mouse first")
	}
	f.logf(opts, "captured %q", word)

	corrected, needsFix := f.decide(word)
	if !needsFix {
		f.logf(opts, "no fix needed for %q", word)
		return nil
	}
	if opts.DryRun {
		f.logf(opts, "dry run: would replace %q with %q", word, corrected)
		return nil
	}

	if err := f.Way.ReplaceWord(word, corrected); err != nil {
		return err
	}
	f.Way.ClearPrimary()
	return f.switchLayoutAfter(corrected, opts)
}

// decide analyzes the word and returns the corrected form, or needsFix=false
// when the dictionaries are not confident enough.
func (f *Fixer) decide(word string) (corrected string, needsFix bool) {
	current := analyze.GuessLayout(word)
	return f.Ana.Analyze(word, current)
}

// switchLayoutAfter switches to the layout of the corrected text, so the
// user continues typing in the layout they actually meant. Must be called
// after the text injection (niri#3568).
func (f *Fixer) switchLayoutAfter(corrected string, opts Options) error {
	if !f.Cfg.Fix.SwitchLayout {
		return nil
	}
	switched, err := f.Niri.SwitchToLayoutOf(corrected)
	if err != nil {
		// The word is already fixed; the layout switch is best-effort
		// (older niri versions have no keyboard-layouts IPC).
		f.logf(opts, "cannot switch layout: %v", err)
		return nil
	}
	if switched {
		f.logf(opts, "layout switched to match %q", corrected)
	}
	return nil
}

// sanitizeSelection extracts a single-word candidate from a selection:
// trailing whitespace stripped, within sane length, no control characters,
// and no internal line breaks. ok=false means the selection is empty,
// multi-line, oversized, or binary.
func sanitizeSelection(sel string) (word string, ok bool) {
	if sel == "" {
		return "", false
	}
	// A trailing newline (or spaces) after the word is normal; an internal
	// one means a multi-line selection — replacing it with one word would
	// destroy text, so refuse.
	trimmed := strings.TrimRight(sel, " \t\n\r")
	if strings.ContainsRune(trimmed, '\n') {
		return "", false
	}
	word = strings.TrimSpace(trimmed)
	if word == "" || len(word) > maxWordLen {
		return "", false
	}
	for _, r := range word {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return word, true
}

func (f *Fixer) logf(opts Options, format string, args ...any) {
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "lapsus fix: "+format+"\n", args...)
	}
}

// flockFile holds the singleton lock for the duration of a fix run.
type flockFile struct {
	f *os.File
}

// acquireLock takes a non-blocking exclusive lock so a double hotkey
// press does not run two pipelines at once.
func acquireLock() (*flockFile, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	path := filepath.Join(dir, "lapsus.fix.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return &flockFile{f: file}, nil
}

func (l *flockFile) close() {
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
