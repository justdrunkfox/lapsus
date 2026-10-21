// Package niri wraps the compositor CLI lapsus talks to for focused
// window, keyboard layouts and layout switching. Two backends share the
// client: niri (`niri msg`) and umbriel (`umbriel msg`/`subscribe`) —
// their IPC is shape-compatible but differs in commands and field names,
// and the client translates umbriel's event stream into the niri event
// JSON the rest of lapsus understands (plan M4: not tied to one WM).
package niri

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/layout"
)

// commandTimeout bounds every compositor invocation; the compositor
// answers instantly, so a hang means something is deeply wrong.
const commandTimeout = 3 * time.Second

// Runner executes a command and returns its stdout. Args includes the
// binary name as args[0].
type Runner func(name string, args ...string) ([]byte, error)

// Backend selects the compositor CLI: "niri", "umbriel" or "" (auto —
// niri when NIRI_SOCKET is set, else umbriel when its binary exists).
type Backend string

const (
	BackendNiri    Backend = "niri"
	BackendUmbriel Backend = "umbriel"
)

// Client talks to the compositor via its CLI.
type Client struct {
	// Bin overrides the binary name; defaults to the backend's.
	Bin string
	// Backend pins the compositor; "" auto-detects (see backend()).
	Backend Backend
	// Run overrides command execution (for tests).
	Run Runner

	resolved Backend
	once     sync.Once
}

// backend resolves the pinned-or-auto backend once. "auto" (and "")
// picks niri when NIRI_SOCKET is set, else umbriel when its binary
// exists, else niri. With an injected Runner (tests) and no explicit
// pin it resolves to niri — auto-detect would otherwise follow the
// machine's session and break hermetic tests (the tests' fake speaks
// the niri dialect).
func (c *Client) backend() Backend {
	c.once.Do(func() {
		switch c.Backend {
		case BackendNiri, BackendUmbriel:
			c.resolved = c.Backend
		default: // "" or "auto"
			switch {
			case c.Run != nil:
				c.resolved = BackendNiri
			case os.Getenv("NIRI_SOCKET") != "":
				c.resolved = BackendNiri
			case func() bool { _, err := exec.LookPath("umbriel"); return err == nil }():
				c.resolved = BackendUmbriel
			default:
				c.resolved = BackendNiri
			}
		}
	})
	return c.resolved
}

func (c *Client) binary() string {
	if c.Bin != "" {
		return c.Bin
	}
	if c.backend() == BackendUmbriel {
		return "umbriel"
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

// Window is the focused-window reply. app_id and title may be null or
// missing (e.g. XWayland windows). ID is backend-specific (niri: a
// number; umbriel: unused, 0).
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

// KeyboardLayouts is the keyboard-layouts reply: the configured layout
// names and the active index. niri names the field current_idx, umbriel
// current_index — both parse into CurrentIdx.
type KeyboardLayouts struct {
	Names      []string `json:"names"`
	CurrentIdx int      `json:"current_idx"`
}

type rawLayouts struct {
	Names        []string `json:"names"`
	CurrentIdx   *int     `json:"current_idx"`
	CurrentIndex *int     `json:"current_index"`
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
		hasToken(n, "us")
	if target == layout.LayoutRU {
		return isRussian
	}
	return isEnglish && !isRussian
}

// hasToken reports whether the name contains word as a whole token
// ("English (US)" → tokens "english", "us"). Substring matching would
// count the "us" in "Belarusian" as English.
func hasToken(name, word string) bool {
	for _, t := range strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if t == word {
			return true
		}
	}
	return false
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
	var out []byte
	var err error
	if c.backend() == BackendUmbriel {
		out, err = c.run("keyboard-layouts", "--json")
	} else {
		out, err = c.run("msg", "--json", "keyboard-layouts")
	}
	if err != nil {
		return nil, fmt.Errorf("%s keyboard-layouts: %w", c.backend(), err)
	}
	var raw rawLayouts
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse keyboard-layouts: %w", err)
	}
	ls := &KeyboardLayouts{Names: raw.Names}
	switch {
	case raw.CurrentIdx != nil:
		ls.CurrentIdx = *raw.CurrentIdx
	case raw.CurrentIndex != nil:
		ls.CurrentIdx = *raw.CurrentIndex
	}
	if len(ls.Names) == 0 {
		return nil, errors.New("compositor reports no keyboard layouts")
	}
	if ls.CurrentIdx < 0 || ls.CurrentIdx >= len(ls.Names) {
		return nil, fmt.Errorf("current layout index %d out of range (%d layouts)", ls.CurrentIdx, len(ls.Names))
	}
	return ls, nil
}

// SwitchLayout switches to the layout at the given 0-based index.
// umbriel has no set-by-index action, only keyboard-layout-next — the
// switch spins the wheel the short way around.
func (c *Client) SwitchLayout(index int) error {
	if c.backend() == BackendUmbriel {
		ls, err := c.KeyboardLayouts()
		if err != nil {
			return err
		}
		n := len(ls.Names)
		steps := (index - ls.CurrentIdx + n) % n
		for i := 0; i < steps; i++ {
			if _, err := c.run("msg", "keyboard-layout-next"); err != nil {
				return fmt.Errorf("umbriel msg keyboard-layout-next: %w", err)
			}
		}
		return nil
	}
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

// FocusedWindow returns the currently focused window.
func (c *Client) FocusedWindow() (*Window, error) {
	if c.backend() == BackendUmbriel {
		out, err := c.run("windows", "--json")
		if err != nil {
			return nil, fmt.Errorf("umbriel windows: %w", err)
		}
		return parseUmbrielFocused(out)
	}
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

// parseUmbrielFocused picks the globally focused window from a
// `umbriel windows --json` reply: `active` marks the one window on the
// whole desktop, `focused` is per-output (several windows carry it).
func parseUmbrielFocused(out []byte) (*Window, error) {
	var wins []struct {
		AppID   string `json:"app_id"`
		Title   string `json:"title"`
		PID     uint32 `json:"pid"`
		Active  bool   `json:"active"`
		Focused bool   `json:"focused"`
	}
	if err := json.Unmarshal(out, &wins); err != nil {
		return nil, fmt.Errorf("parse umbriel windows: %w", err)
	}
	best := -1
	for i, w := range wins {
		if w.Active {
			best = i
			break
		}
		if w.Focused && best < 0 {
			best = i
		}
	}
	if best < 0 {
		return &Window{}, nil
	}
	w := &Window{PID: wins[best].PID, IsFocused: true, Title: &wins[best].Title}
	if wins[best].AppID != "" {
		w.AppID = &wins[best].AppID
	}
	return w, nil
}

// EventStream starts the compositor's event stream and returns its
// stdout as newline-delimited JSON events in the niri event format (the
// umbriel stream is translated on the fly), plus a wait func that reaps
// the exited child (skipping Wait leaves a zombie process per
// reconnect). The process is killed when ctx is cancelled. Not
// available with an injected Runner (tests drive the daemon's state
// machine directly instead).
func (c *Client) EventStream(ctx context.Context) (io.ReadCloser, func() error, error) {
	if c.Run != nil {
		return nil, nil, errors.New("EventStream is not available with an injected runner")
	}
	if c.backend() == BackendUmbriel {
		return c.eventStreamUmbriel(ctx)
	}
	cmd := exec.CommandContext(ctx, c.binary(), "msg", "--json", "event-stream")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start niri event-stream: %w", err)
	}
	return stdout, cmd.Wait, nil
}

// eventStreamUmbriel runs `umbriel subscribe` and translates its lines
// into niri event JSON on a pipe: keyboard_layout becomes
// KeyboardLayoutsChanged (plus KeyboardLayoutSwitched on index change,
// which drives per-app layout learning), a change of the active window
// in windows becomes WindowFocusChanged (the daemon refetches the
// window itself).
func (c *Client) eventStreamUmbriel(ctx context.Context) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, c.binary(), "subscribe", "keyboard_layout,windows")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start umbriel subscribe: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(stdout)
		prevIdx, prevApp := -1, "\x00" // sentinel: no observation yet
		for sc.Scan() {
			for _, line := range translateUmbrielLine(sc.Text(), &prevIdx, &prevApp) {
				if _, err := pw.Write([]byte(line + "\n")); err != nil {
					break
				}
			}
		}
		pw.CloseWithError(sc.Err())
	}()
	return pr, cmd.Wait, nil
}

// translateUmbrielLine converts one `umbriel subscribe` line into zero
// or more niri event lines. prevIdx/prevApp carry state between lines.
func translateUmbrielLine(line string, prevIdx *int, prevApp *string) []string {
	var ev struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	switch ev.Event {
	case "keyboard_layout":
		var ls rawLayouts
		if err := json.Unmarshal(ev.Data, &ls); err != nil {
			return nil
		}
		idx := 0
		switch {
		case ls.CurrentIdx != nil:
			idx = *ls.CurrentIdx
		case ls.CurrentIndex != nil:
			idx = *ls.CurrentIndex
		}
		names, err := json.Marshal(ls.Names)
		if err != nil {
			return nil
		}
		out := []string{fmt.Sprintf(
			`{"KeyboardLayoutsChanged":{"keyboard_layouts":{"names":%s,"current_idx":%d}}}`, names, idx)}
		if *prevIdx >= 0 && idx != *prevIdx {
			out = append(out, fmt.Sprintf(`{"KeyboardLayoutSwitched":{"idx":%d}}`, idx))
		}
		*prevIdx = idx
		return out
	case "windows":
		var wins []struct {
			AppID   string `json:"app_id"`
			Active  bool   `json:"active"`
			Focused bool   `json:"focused"`
		}
		if err := json.Unmarshal(ev.Data, &wins); err != nil {
			return nil
		}
		app := ""
		best := -1
		for i, w := range wins {
			if w.Active {
				best = i
				break
			}
			if w.Focused && best < 0 {
				best = i
			}
		}
		if best >= 0 {
			app = wins[best].AppID
		}
		if *prevApp == "\x00" {
			// First observation: the daemon already fetched the state at
			// startup; just remember it.
			*prevApp = app
			return nil
		}
		if app == *prevApp {
			return nil
		}
		*prevApp = app
		return []string{`{"WindowFocusChanged":{"id":0}}`}
	}
	return nil
}
