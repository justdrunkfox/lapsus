package feedback

import (
	"strings"
	"testing"
)

type capture struct {
	calls []string
}

func (c *capture) run(name string, args []string) ([]byte, error) {
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func (c *capture) has(substr string) bool {
	for _, call := range c.calls {
		if strings.Contains(call, substr) {
			return true
		}
	}
	return false
}

func TestFireSendsNotification(t *testing.T) {
	cap := &capture{}
	f := &F{Notify: true, Run: cap.run}
	f.Fire("ghbdtn", "привет")
	if !cap.has("notify-send") || !cap.has("ghbdtn → привет") {
		t.Errorf("notification expected, calls: %v", cap.calls)
	}
	if len(cap.calls) != 1 {
		t.Errorf("sound must be off by default, calls: %v", cap.calls)
	}
}

func TestFirePlaysBellSound(t *testing.T) {
	cap := &capture{}
	f := &F{Sound: "bell", Run: cap.run}
	f.Fire("руддщ", "hello")
	if !cap.has(bellPath) {
		t.Errorf("bell sound expected, calls: %v", cap.calls)
	}
	if !cap.has("pw-play") && !cap.has("paplay") {
		t.Errorf("expected an audio player call, calls: %v", cap.calls)
	}
	if cap.has("notify-send") {
		t.Errorf("notification must be off when Notify=false, calls: %v", cap.calls)
	}
}

func TestFireCustomSoundPath(t *testing.T) {
	cap := &capture{}
	f := &F{Sound: "/tmp/my.wav", Run: cap.run}
	f.Fire("a", "b")
	if !cap.has("/tmp/my.wav") {
		t.Errorf("custom sound path expected, calls: %v", cap.calls)
	}
}

func TestFireDisabled(t *testing.T) {
	cap := &capture{}
	f := &F{Notify: false, Sound: "", Run: cap.run}
	f.Fire("a", "b")
	if len(cap.calls) != 0 {
		t.Errorf("no feedback expected, calls: %v", cap.calls)
	}
}

func TestFireNilReceiver(t *testing.T) {
	var f *F
	f.Fire("a", "b") // must not panic
}

func TestTruncate(t *testing.T) {
	if got := truncate(strings.Repeat("ж", 100), 80); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %d runes, want 80 with ellipsis", len([]rune(got)))
	}
	if got := truncate("short", 80); got != "short" {
		t.Errorf("truncate(short) = %q", got)
	}
}
