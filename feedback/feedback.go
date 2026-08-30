// Package feedback fires best-effort desktop notifications and sounds
// after a successful flip. It never fails the pipeline: every step is
// optional, bounded by a timeout and silently skipped when the tool is
// missing.
package feedback

import (
	"context"
	"os/exec"
	"time"
)

// bellPath is the freedesktop sound used when the sound setting is "bell".
const bellPath = "/usr/share/sounds/freedesktop/stereo/bell.oga"

const commandTimeout = 2 * time.Second

// Runner executes a command (for tests). When set on F, commands run
// synchronously so tests can observe them.
type Runner func(name string, args []string) ([]byte, error)

// F is the feedback sender. A nil *F is a no-op.
type F struct {
	// Notify sends a desktop notification via notify-send (libnotify-bin).
	Notify bool
	// Sound is a path to an audio file or "bell" for the freedesktop
	// bell; "" disables sound.
	Sound string
	// Run overrides command execution (for tests).
	Run Runner
}

// Fire reports that from was flipped to to. Safe to call with a nil
// receiver.
func (f *F) Fire(from, to string) {
	if f == nil {
		return
	}
	msg := truncate(from+" → "+to, 80)
	if f.Notify {
		f.spawn("notify-send", []string{"-a", "lapsus", "lapsus", msg})
	}
	if path := f.soundPath(); path != "" {
		if player := soundPlayer(); player != "" {
			f.spawn(player, []string{path})
		}
	}
}

// spawn runs one feedback command detached: sh backgrounds the command
// and exits immediately. This is race-free for the one-shot fix (a bare
// goroutine could lose the race against process exit and never start
// the child) and zombie-free for the daemon (the re-parented player is
// sh's child, not ours).
func (f *F) spawn(name string, args []string) {
	if f.Run != nil {
		f.Run(name, args)
		return
	}
	argv := append([]string{"-c", `"$@" >/dev/null 2>&1 &`, "_", name}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "sh", argv...).Run()
}

// soundPath resolves the sound setting to a file path, "" for off.
func (f *F) soundPath() string {
	switch f.Sound {
	case "", "-":
		return ""
	case "bell":
		return bellPath
	default:
		return f.Sound
	}
}

// soundPlayer picks an available audio player, PipeWire-native first.
func soundPlayer() string {
	for _, p := range []string{"pw-play", "paplay"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
