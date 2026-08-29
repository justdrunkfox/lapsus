// Package niri wraps the `niri` CLI, which talks to the compositor over
// NIRI_SOCKET. Used to detect the focused window and to switch layouts.
package niri

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// IsTerminalApp reports whether the app_id matches one of the configured
// terminal app_ids. Matching is case-insensitive equality: substring
// matching would make short ids like "st" match "ghostty" or unrelated apps.
func IsTerminalApp(appID string, terminals []string) bool {
	if appID == "" {
		return false
	}
	for _, t := range terminals {
		if strings.EqualFold(appID, t) {
			return true
		}
	}
	return false
}
