// Package feedback fires best-effort desktop notifications and sounds
// after a successful flip. It never fails the pipeline: every step is
// optional, bounded by a timeout and silently skipped when the tool is
// missing.
package feedback

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// bellPath is the freedesktop sound used when the sound setting is "bell".
const bellPath = "/usr/share/sounds/freedesktop/stereo/bell.oga"

const commandTimeout = 2 * time.Second

// Runner executes a command (for tests). When set on F, commands run
// synchronously so tests can observe them.
type Runner func(name string, args []string) ([]byte, error)

// F is the feedback sender. A nil *F is a no-op. All methods are safe
// for concurrent use: Fire may run in a goroutine while Set updates the
// settings from the daemon.
type F struct {
	// Notify sends a desktop notification via notify-send (libnotify-bin).
	Notify bool
	// Sound is a path to an audio file or "bell" for the freedesktop
	// bell; "" disables sound.
	Sound string
	// Run overrides command execution (for tests).
	Run Runner

	mu sync.Mutex
}

// Set flips the settings (safe concurrently with Fire).
func (f *F) Set(notify bool, sound string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Notify, f.Sound = notify, sound
}

// Fire reports that from was flipped to to. Safe to call with a nil
// receiver.
func (f *F) Fire(from, to string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	notify, sound, run := f.Notify, f.Sound, f.Run
	f.mu.Unlock()

	msg := truncate(from+" → "+to, 80)
	if notify {
		// -t: auto-expire; without it the notification daemon stacks
		// every flip forever (DMS keeps them in the center).
		f.spawn(run, "notify-send", []string{"-a", "lapsus", "-t", "2500", "lapsus", msg})
	}
	if path := soundPath(sound); path != "" {
		if player := soundPlayer(); player != "" {
			f.spawn(run, player, []string{path})
		}
	}
}

// spawn runs one feedback command detached: sh backgrounds the command
// and exits immediately. This is race-free for the one-shot fix (a bare
// goroutine could lose the race against process exit and never start
// the child) and zombie-free for the daemon (the re-parented player is
// sh's child, not ours).
func (f *F) spawn(run Runner, name string, args []string) {
	if run != nil {
		run(name, args)
		return
	}
	argv := append([]string{"-c", `"$@" >/dev/null 2>&1 &`, "_", name}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "sh", argv...).Run()
}

// soundPath resolves the sound setting to a file path, "" for off.
func soundPath(sound string) string {
	switch sound {
	case "", "-":
		return ""
	case "bell":
		return bellPath
	default:
		return sound
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
