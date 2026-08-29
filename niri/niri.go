// Package niri wraps the `niri` CLI, which talks to the compositor over
// NIRI_SOCKET. Used to detect the focused window and to switch layouts.
package niri

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/layout"
)

// commandTimeout bounds every niri invocation; the compositor answers
// instantly, so a hang means something is deeply wrong.
const commandTimeout = 3 * time.Second

// Runner executes a command and returns its stdout. Args includes the
// binary name as args[0].
type Runner func(name string, args ...string) ([]byte, error)

// Client talks to niri via the `niri` CLI.
type Client struct {
	// Bin is the niri binary name; defaults to "niri".
	Bin string
	// Run overrides command execution (for tests).
	Run Runner
}

func (c *Client) binary() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "niri"
}

func (c *Client) run(args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(c.binary(), args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Window is the focused-window reply of `niri msg --json focused-window`.
// app_id and title may be null (e.g. XWayland windows).
type Window struct {
	ID          uint64  `json:"id"`
	Title       *string `json:"title"`
	AppID       *string `json:"app_id"`
	PID         uint32  `json:"pid"`
	WorkspaceID uint64  `json:"workspace_id"`
	IsFocused   bool    `json:"is_focused"`
}

// AppIDOr returns the window's app_id or the fallback if it is null.
func (w *Window) AppIDOr(fallback string) string {
	if w.AppID != nil {
		return *w.AppID
	}
	return fallback
}

// FocusedWindow returns the currently focused window.
func (c *Client) FocusedWindow() (*Window, error) {
	out, err := c.run("msg", "--json", "focused-window")
	if err != nil {
		return nil, fmt.Errorf("niri msg focused-window: %w", err)
	}
	var w Window
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, fmt.Errorf("parse focused-window: %w", err)
	}
	return &w, nil
}

// KeyboardLayouts is the reply of `niri msg --json keyboard-layouts`.
type KeyboardLayouts struct {
	Names      []string `json:"names"`
	CurrentIdx int      `json:"current_idx"`
}

// Current maps the active layout to a lapsus Layout by matching the
// configured layout name. ok=false when the names are unrecognized.
func (ls *KeyboardLayouts) Current() (l layout.Layout, ok bool) {
	if ls.CurrentIdx < 0 || ls.CurrentIdx >= len(ls.Names) {
		return layout.LayoutEN, false
	}
	if MatchLayoutName(ls.Names[ls.CurrentIdx], layout.LayoutRU) {
		return layout.LayoutRU, true
	}
	if MatchLayoutName(ls.Names[ls.CurrentIdx], layout.LayoutEN) {
		return layout.LayoutEN, true
	}
	return layout.LayoutEN, false
}

// MatchLayoutName matches a configured layout name against a target
// layout. Russian is checked first: "russian" contains "us", which would
// otherwise trip the English heuristics.
func MatchLayoutName(name string, target layout.Layout) bool {
	n := strings.ToLower(name)
	isRussian := strings.Contains(n, "russ") || strings.Contains(n, "рус") ||
		n == "ru" || strings.HasPrefix(n, "ru-") || strings.HasPrefix(n, "ru_")
	isEnglish := strings.Contains(n, "engl") || strings.Contains(n, "англ") ||
		n == "en" || strings.HasPrefix(n, "en-") || strings.HasPrefix(n, "en_") ||
		strings.Contains(n, "us")
	if target == layout.LayoutRU {
		return isRussian
	}
	return isEnglish && !isRussian
}

// LayoutIndex maps a target layout to its position among the configured
// layout names (e.g. "English (US)", "Russian"), or -1 if unrecognized.
func LayoutIndex(names []string, target layout.Layout) int {
	for i, name := range names {
		if MatchLayoutName(name, target) {
			return i
		}
	}
	return -1
}

// KeyboardLayouts returns the configured layouts and the active index.
func (c *Client) KeyboardLayouts() (*KeyboardLayouts, error) {
	out, err := c.run("msg", "--json", "keyboard-layouts")
	if err != nil {
		return nil, fmt.Errorf("niri msg keyboard-layouts: %w", err)
	}
	var ls KeyboardLayouts
	if err := json.Unmarshal(out, &ls); err != nil {
		return nil, fmt.Errorf("parse keyboard-layouts: %w", err)
	}
	if len(ls.Names) == 0 {
		return nil, errors.New("niri reports no keyboard layouts")
	}
	if ls.CurrentIdx < 0 || ls.CurrentIdx >= len(ls.Names) {
		return nil, fmt.Errorf("niri current layout index %d out of range (%d layouts)", ls.CurrentIdx, len(ls.Names))
	}
	return &ls, nil
}

// SwitchLayout switches to the layout at the given 0-based index.
func (c *Client) SwitchLayout(index int) error {
	if _, err := c.run("msg", "action", "switch-layout", fmt.Sprint(index)); err != nil {
		return fmt.Errorf("niri msg action switch-layout %d: %w", index, err)
	}
	return nil
}

// SwitchToLayoutOf switches to the layout the text belongs to (by
// script), so the user continues typing in the layout they meant. Must
// be called after text injection (niri#3568: virtual keyboard events
// can reset the layout group). It is a no-op when the target layout is
// already active or its name is not recognizable.
func (c *Client) SwitchToLayoutOf(text string) (switched bool, err error) {
	target := analyze.GuessLayout(text)
	ls, err := c.KeyboardLayouts()
	if err != nil {
		return false, fmt.Errorf("query layouts: %w", err)
	}
	idx := LayoutIndex(ls.Names, target)
	if idx < 0 {
		// Layout names unrecognized: with exactly two layouts the target
		// is unambiguously the other one.
		if len(ls.Names) == 2 {
			idx = 1 - ls.CurrentIdx
		} else {
			return false, nil
		}
	}
	if idx == ls.CurrentIdx {
		return false, nil
	}
	return true, c.SwitchLayout(idx)
}

// AppIDIn reports whether the app_id matches one of the configured list
// entries (terminal detection, per-app exclusions). Matching is
// case-insensitive equality: substring matching would make short ids
// like "st" match "ghostty" or unrelated apps.
func AppIDIn(appID string, list []string) bool {
	if appID == "" {
		return false
	}
	for _, t := range list {
		if strings.EqualFold(appID, t) {
			return true
		}
	}
	return false
}

// EventStream starts `niri msg --json event-stream` and returns its
// stdout as newline-delimited JSON events. The process is killed when
// ctx is cancelled. Not available with an injected Runner (tests drive
// the daemon's state machine directly instead).
func (c *Client) EventStream(ctx context.Context) (io.ReadCloser, error) {
	if c.Run != nil {
		return nil, errors.New("EventStream is not available with an injected runner")
	}
	cmd := exec.CommandContext(ctx, c.binary(), "msg", "--json", "event-stream")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start niri event-stream: %w", err)
	}
	return stdout, nil
}
