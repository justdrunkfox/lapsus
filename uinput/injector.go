package uinput

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/keymap"
	"github.com/voev/lapsus/layout"
)

const (
	keyLeftShift = 42
	keyBackspace = 14
	defaultGap   = 8 * time.Millisecond
)

// TapDevice is the subset of Keyboard the injector drives. Tests can
// replace it with a recorder.
type TapDevice interface {
	Tap(code uint16, gap time.Duration) error
	Hold(code uint16) error
	Release(code uint16) error
}

// Injector types corrected text through a uinput keyboard, switching
// the compositor layout per character so every character lands as
// itself (a latin char moves the layout to EN, a cyrillic one to RU).
type Injector struct {
	Dev TapDevice
	// EnsureLayout makes the compositor layout match the target before
	// typing; it must switch the layout when needed.
	EnsureLayout func(layout.Layout) error
	// Gap between keystrokes; 0 means a small default.
	Gap time.Duration
}

func (i *Injector) gap() time.Duration {
	if i.Gap > 0 {
		return i.Gap
	}
	return defaultGap
}

// ReplaceWord erases old (rune count backspaces) and types corrected,
// ensuring the compositor layout matches the corrected text's language
// first. The trailing separator, when present in old, must be present
// in corrected too (it is re-typed as itself).
func (i *Injector) ReplaceWord(old, corrected string) error {
	target := analyze.GuessLayout(corrected)
	if i.EnsureLayout != nil {
		if err := i.EnsureLayout(target); err != nil {
			return fmt.Errorf("ensure layout: %w", err)
		}
	}
	if err := i.Backspace(utf8.RuneCountInString(old)); err != nil {
		return err
	}
	return i.TypeText(corrected)
}

// Backspace deletes n characters before the caret.
func (i *Injector) Backspace(n int) error {
	for ; n > 0; n-- {
		if err := i.Dev.Tap(keyBackspace, i.gap()); err != nil {
			return err
		}
	}
	return nil
}

// TypeText types text, switching layouts per character so mixed-script
// text lands correctly. Characters not present on any supported layout
// are an error.
func (i *Injector) TypeText(text string) error {
	var active layout.Layout
	have := false
	for _, r := range text {
		code, shift, target, ok := keymap.CodeForAny(r)
		if !ok {
			return fmt.Errorf("character %q is not typable", r)
		}
		if !have || target != active {
			if i.EnsureLayout != nil {
				if err := i.EnsureLayout(target); err != nil {
					return err
				}
			}
			active = target
			have = true
		}
		if shift {
			if err := i.Dev.Hold(keyLeftShift); err != nil {
				return err
			}
		}
		if err := i.Dev.Tap(code, i.gap()); err != nil {
			return err
		}
		if shift {
			if err := i.Dev.Release(keyLeftShift); err != nil {
				return err
			}
		}
	}
	return nil
}
