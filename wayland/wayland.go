// Package wayland wraps the external tools used for text injection and
// selection access: wtype (virtual keyboard) and wl-clipboard.
//
// wtype sends keysyms, so it types Cyrillic regardless of the active
// layout — that is what makes the fix pipeline layout-independent.
package wayland

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// commandTimeout bounds every tool invocation. wl-copy is expected to
// fork into the background; wtype and wl-paste return immediately.
const commandTimeout = 3 * time.Second

// Runner executes a command with stdin and returns its stdout.
type Runner func(name string, args []string, stdin []byte) ([]byte, error)

// Tools runs the Wayland helper commands. Pause is inserted after
// synthetic key events to give the focused application time to react
// before the next step (see fix.pause_ms in the config).
type Tools struct {
	Pause time.Duration
	Run   Runner
}

func (t *Tools) run(name string, args []string, stdin []byte) ([]byte, error) {
	if t.Run != nil {
		return t.Run(name, args, stdin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.Bytes(), err
}

// combo presses a key with modifiers held: -M m1 -k key -m m1.
func (t *Tools) combo(key string, mods ...string) error {
	var args []string
	for _, m := range mods {
		args = append(args, "-M", m)
	}
	args = append(args, "-k", key)
	for i := len(mods) - 1; i >= 0; i-- {
		args = append(args, "-m", mods[i])
	}
	if _, err := t.run("wtype", args, nil); err != nil {
		return fmt.Errorf("wtype %s: %w", strings.Join(args, " "), err)
	}
	t.pause()
	return nil
}

func (t *Tools) pause() {
	if t.Pause > 0 {
		time.Sleep(t.Pause)
	}
}

// SelectPrevWord selects the word left of the caret (Ctrl+Shift+Left).
func (t *Tools) SelectPrevWord() error {
	return t.combo("Left", "ctrl", "shift")
}

// Cut cuts the selection (Ctrl+X).
func (t *Tools) Cut() error {
	return t.combo("x", "ctrl")
}

// Paste pastes the clipboard (Ctrl+V).
func (t *Tools) Paste() error {
	return t.combo("v", "ctrl")
}

// PasteTerminal pastes the clipboard in terminals (Ctrl+Shift+V).
func (t *Tools) PasteTerminal() error {
	return t.combo("v", "ctrl", "shift")
}

// CollapseSelection presses Right, collapsing the selection back to a
// caret at the original position.
func (t *Tools) CollapseSelection() error {
	return t.combo("Right")
}

// TypeText types text as keysyms; the text lands regardless of the
// active layout. If a selection is active, typing replaces it.
func (t *Tools) TypeText(text string) error {
	if _, err := t.run("wtype", []string{"--", text}, nil); err != nil {
		return fmt.Errorf("wtype --: %w", err)
	}
	t.pause()
	return nil
}

// DeleteBack presses BackSpace n times in a single wtype invocation.
func (t *Tools) DeleteBack(n int) error {
	if n <= 0 {
		return nil
	}
	args := make([]string, 0, 2*n)
	for i := 0; i < n; i++ {
		args = append(args, "-k", "BackSpace")
	}
	if _, err := t.run("wtype", args, nil); err != nil {
		return fmt.Errorf("wtype backspace x%d: %w", n, err)
	}
	t.pause()
	return nil
}

// ReplaceWord replaces the word the caret currently sits right after
// with corrected: BackSpace × rune-count of old, then type the fix.
// This is the unified replacement for both the daemon and the terminal
// path of `lapsus fix`; it requires the caret to be directly after the
// old word (which is guaranteed when the word was just typed).
func (t *Tools) ReplaceWord(old, corrected string) error {
	n := utf8.RuneCountInString(old)
	if err := t.DeleteBack(n); err != nil {
		return err
	}
	return t.TypeText(corrected)
}

// ReadPrimary returns the primary selection, or "" if it is empty or
// unset (wl-paste exits non-zero when no selection exists — that is
// normal, not an error).
func (t *Tools) ReadPrimary() string {
	out, err := t.run("wl-paste", []string{"--primary", "--no-newline"}, nil)
	if err != nil {
		return ""
	}
	return string(out)
}

// ReadClipboard returns the regular clipboard, or "" if unavailable.
func (t *Tools) ReadClipboard() string {
	out, err := t.run("wl-paste", []string{"--no-newline"}, nil)
	if err != nil {
		return ""
	}
	return string(out)
}

// CopyClipboard copies text to the clipboard. wl-copy forks into the
// background to keep serving the selection.
func (t *Tools) CopyClipboard(text string) error {
	if _, err := t.run("wl-copy", nil, []byte(text)); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	t.pause()
	return nil
}

// ClearPrimary empties the primary selection so a stale selection can
// not leak into the next read.
func (t *Tools) ClearPrimary() error {
	if _, err := t.run("wl-copy", []string{"--primary", "--clear"}, nil); err != nil {
		return fmt.Errorf("wl-copy --primary --clear: %w", err)
	}
	t.pause()
	return nil
}

// ClearClipboard empties the regular clipboard (used after the cut path,
// so the cut-out original does not linger in the clipboard).
func (t *Tools) ClearClipboard() error {
	if _, err := t.run("wl-copy", []string{"--clear"}, nil); err != nil {
		return fmt.Errorf("wl-copy --clear: %w", err)
	}
	t.pause()
	return nil
}

// ReadPrimaryWithRetries reads the primary selection, retrying while it
// stays empty: some applications update the selection a beat after the
// synthetic keypress. Retries only when the first read is empty.
func (t *Tools) ReadPrimaryWithRetries(attempts int) string {
	sel := t.ReadPrimary()
	for attempt := 1; sel == "" && attempt < attempts; attempt++ {
		t.pause()
		sel = t.ReadPrimary()
	}
	return sel
}
