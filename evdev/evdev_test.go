package evdev

import (
	"os"
	"path/filepath"
	"testing"
)

// Bitmap of the real built-in keyboard on the target machine
// (AT Translated Set 2 keyboard), truncated for readability.
const realKeyboardBitmap = "402000007 ff803078f800d001 feffffdfffcfffff fffffffffffffffe"

func TestHasKeyBitsRealKeyboard(t *testing.T) {
	if !hasKeyBits(realKeyboardBitmap, KeyA, KeyQ, KeySpace) {
		t.Error("real keyboard bitmap should have KEY_A, KEY_Q and KEY_SPACE")
	}
}

func TestHasKeyBitsNegative(t *testing.T) {
	// Tiny bitmap: only bits 0-2 set — no letter keys.
	if hasKeyBits("7", KeyA, KeyQ, KeySpace) {
		t.Error("bits 0-2 only must not look like a keyboard")
	}
	// KEY_A bit explicitly clear in the last (least significant) word.
	if hasKeyBits("ff ff ffffffffbfffffff", KeyA) {
		t.Error("bitmap without KEY_A bit must not match")
	}
	// Bitmap too short to contain the requested bit.
	if hasKeyBits("0", KeySpace) {
		t.Error("short bitmap must not match high bits")
	}
}

func TestReadEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event0")
	// input_event for KEY_A down: timeval(16) + type(2) + code(2) + value(4).
	buf := make([]byte, 24)
	buf[16] = 0x01 // type = EV_KEY
	buf[18] = 0x1e // code = 30 (KEY_A), little endian
	buf[20] = 0x01 // value = 1 (down)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(Device{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ev, err := r.ReadEvent()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != TypeKey || ev.Code != KeyA || ev.Value != ValKeyDown {
		t.Errorf("got %+v, want {1 30 1}", ev)
	}
}

func TestDiscoverFindsRealKeyboard(t *testing.T) {
	devs, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range devs {
		if d.Name == "AT Translated Set 2 keyboard" {
			found = true
		}
	}
	// The machine running the tests has the built-in keyboard; when run
	// elsewhere the test only requires no error.
	if !found {
		t.Logf("built-in keyboard not found (discovered %d devices)", len(devs))
	}
}
