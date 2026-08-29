// Package evdev reads Linux input devices without grabbing them: device
// discovery goes through /sys/class/input capabilities, event reading is
// raw fixed-size input_event records from /dev/input/eventX. Reading
// never blocks the compositor: evdev devices can have many readers.
package evdev

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Event is a stripped-down linux input_event (24 bytes on 64-bit):
// type, code, value; the timestamp is not needed for keystroke tracking.
type Event struct {
	Type  uint16
	Code  uint16
	Value int32
}

// Event types and values used for keystroke tracking.
const (
	TypeKey      = 0x01
	ValKeyDown   = 1
	ValKeyRepeat = 2
	ValKeyUp     = 0
	keyCapFile   = "capabilities/key"
	inputClass   = "/sys/class/input"
	inputDevice  = "/dev/input"
)

// Well-known keycodes (linux/input-event-codes.h) used by lapsus.
const (
	KeyEsc        = 1
	Key1          = 2
	Key0          = 11
	KeyMinus      = 12
	KeyEqual      = 13
	KeyBackspace  = 14
	KeyTab        = 15
	KeyQ          = 16
	KeyW          = 17
	KeyE          = 18
	KeyR          = 19
	KeyT          = 20
	KeyY          = 21
	KeyU          = 22
	KeyI          = 23
	KeyO          = 24
	KeyP          = 25
	KeyLeftBrace  = 26
	KeyRightBrace = 27
	KeyEnter      = 28
	KeyLeftCtrl   = 29
	KeyA          = 30
	KeyS          = 31
	KeyD          = 32
	KeyF          = 33
	KeyG          = 34
	KeyH          = 35
	KeyJ          = 36
	KeyK          = 37
	KeyL          = 38
	KeySemicolon  = 39
	KeyApostrophe = 40
	KeyGrave      = 41
	KeyLeftShift  = 42
	KeyBackslash  = 43
	KeyZ          = 44
	KeyX          = 45
	KeyC          = 46
	KeyV          = 47
	KeyB          = 48
	KeyN          = 49
	KeyM          = 50
	KeyComma      = 51
	KeyDot        = 52
	KeySlash      = 53
	KeyRightShift = 54
	KeyKPMinus    = 74
	KeyLeftAlt    = 56
	KeySpace      = 57
	KeyCapslock   = 58
	KeyF1         = 59
	KeyF10        = 68
	KeyNumlock    = 69
	KeyScrolllock = 71 // = KEY_KP7 with numlock on
	KeyKP7        = 71
	KeyKP8        = 72
	KeyKP9        = 73
	KeyKPPlus     = 78
	KeyKP1        = 79
	KeyKP2        = 80
	KeyKP3        = 81
	KeyKP0        = 82
	KeyKPDot      = 83
	KeyF11        = 87
	KeyF12        = 88
	KeyKP4        = 75
	KeyKP5        = 76
	KeyKP6        = 77
	KeyKPEnter    = 96
	KeyRightCtrl  = 97
	KeyKPSlash    = 98
	KeyRightAlt   = 100
	KeyHome       = 102
	KeyUp         = 103
	KeyPageUp     = 104
	KeyLeft       = 105
	KeyRight      = 106
	KeyEnd        = 107
	KeyDown       = 108
	KeyPageDown   = 109
	KeyDelete     = 111
)

// Device identifies a readable evdev device node.
type Device struct {
	Path string // /dev/input/eventX
	Name string // human-readable name from /sys
}

// String implements fmt.Stringer for logging.
func (d Device) String() string { return fmt.Sprintf("%s (%s)", d.Path, d.Name) }

// Discover lists input devices that look like keyboards: their `key`
// capability bitmap has letter keys and Space. Other devices (mice,
// buttons, LEDs, media keys) are skipped.
func Discover() ([]Device, error) {
	inputs, err := filepath.Glob(filepath.Join(inputClass, "input*"))
	if err != nil {
		return nil, err
	}
	var devices []Device
	for _, input := range inputs {
		raw, err := os.ReadFile(filepath.Join(input, keyCapFile))
		if err != nil {
			continue // no key capabilities — not a keyboard
		}
		if !hasKeyBits(string(raw), KeyA, KeyQ, KeySpace) {
			continue
		}
		nameB, err := os.ReadFile(filepath.Join(input, "name"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameB))
		// event node: /sys/class/input/inputX -> /dev/input/eventY via
		// the device's event*/ directory entry.
		matches, err := filepath.Glob(filepath.Join(input, "event*"))
		if err != nil || len(matches) == 0 {
			continue
		}
		eventDir := filepath.Base(matches[0])
		devices = append(devices, Device{
			Path: filepath.Join(inputDevice, eventDir),
			Name: name,
		})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Path < devices[j].Path })
	return devices, nil
}

// hasKeyBits parses a /sys capabilities/key bitmap (space-separated hex
// words, most significant word first) and reports whether every listed
// keycode bit is set.
func hasKeyBits(bitmap string, keys ...uint16) bool {
	words := strings.Fields(strings.TrimSpace(bitmap))
	if len(words) == 0 {
		return false
	}
	for _, k := range keys {
		// word index from the right: k/64; the last field is word 0.
		idx := len(words) - 1 - int(k)/64
		if idx < 0 {
			return false
		}
		var word uint64
		if _, err := fmt.Sscanf(words[idx], "%x", &word); err != nil {
			return false
		}
		if word&(1<<(k%64)) == 0 {
			return false
		}
	}
	return true
}

// Reader reads events from an opened device.
type Reader struct {
	f *os.File
}

// Open opens the device for reading. No grab is performed: the
// compositor keeps receiving its events; membership in the `input`
// group is required for the open to succeed.
func Open(d Device) (*Reader, error) {
	f, err := os.Open(d.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.Path, err)
	}
	return &Reader{f: f}, nil
}

// ReadEvent blocks until the next full input_event record. EOF means
// the device was unplugged.
func (r *Reader) ReadEvent() (Event, error) {
	var buf [24]byte
	if _, err := io.ReadFull(r.f, buf[:]); err != nil {
		return Event{}, err
	}
	return Event{
		Type:  binary.LittleEndian.Uint16(buf[16:18]),
		Code:  binary.LittleEndian.Uint16(buf[18:20]),
		Value: int32(binary.LittleEndian.Uint32(buf[20:24])),
	}, nil
}

// Close releases the device.
func (r *Reader) Close() error { return r.f.Close() }
