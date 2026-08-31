// Package uinput creates a virtual keyboard at the kernel level and
// injects raw key events through it.
//
// This is the uinput-injection experiment (branch uinput-injection).
// Unlike wtype, which asks the compositor to type keysyms, uinput
// injects raw keycodes: the compositor translates them through its XKB
// map exactly like physical keystrokes. That makes injection work on
// any compositor and in raw-input applications — but the correct
// layout must be ensured before injection, because keycodes are
// interpreted by the compositor's layout, not by us.
//
// Requires write access to /dev/uinput: root, or a udev rule granting
// the session access (see deploy/60-lapsus-uinput.rules).
package uinput

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	devPath = "/dev/uinput"
	name    = "lapsus-virtual-keyboard"

	evSyn = 0x00
	evKey = 0x01

	// ioctl numbers for /dev/uinput (linux/uinput.h).
	uiSetEvbit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeybit  = 0x40045565 // _IOW('U', 101, int)
	uiDevCreate  = 0x5501     // _IO('U', 1)
	uiDevDestroy = 0x5502     // _IO('U', 2)

	keyRelease = 0
	keyPress   = 1

	// maxKeyCode is the highest keycode we register. The positional
	// tables only need codes up to the media/function keys.
	maxKeyCode = 120
)

// Keyboard is a created virtual keyboard device.
type Keyboard struct {
	f *os.File
}

func ioctl(f *os.File, req, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func (k *Keyboard) setBit(req uintptr, code uint16) error {
	v := uint32(code)
	return ioctl(k.f, req, uintptr(unsafe.Pointer(&v)))
}

// Open creates the virtual keyboard device. The caller must Close it
// (otherwise the device lingers until the process exits).
func Open() (*Keyboard, error) {
	f, err := os.OpenFile(devPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/uinput: %w (нужны права: скопируй deploy/60-lapsus-uinput.rules в /etc/udev/rules.d/ и выполни sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=uinput)", err)
	}
	k := &Keyboard{f: f}

	if err := k.setBit(uiSetEvbit, evKey); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	if err := k.setBit(uiSetEvbit, evSyn); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_SET_EVBIT(SYN): %w", err)
	}
	for code := uint16(1); code <= maxKeyCode; code++ {
		if err := k.setBit(uiSetKeybit, code); err != nil {
			f.Close()
			return nil, fmt.Errorf("UI_SET_KEYBIT(%d): %w", code, err)
		}
	}
	if err := ioctl(f, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return k, nil
}

// inputEvent mirrors linux/input_event on 64-bit platforms.
type inputEvent struct {
	Sec, Usec int64
	Type      uint16
	Code      uint16
	Value     int32
}

func eventBytes(typ, code uint16, value int32) []byte {
	now := time.Now()
	var buf [24]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(now.Unix()))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(now.UnixNano()%1_000_000_000))
	binary.LittleEndian.PutUint16(buf[16:18], typ)
	binary.LittleEndian.PutUint16(buf[18:20], code)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(value))
	return buf[:]
}

func (k *Keyboard) emit(typ, code uint16, value int32) error {
	_, err := k.f.Write(eventBytes(typ, code, value))
	return err
}

// Tap presses and releases one keycode with a small gap so that
// compositors and applications register both edges.
func (k *Keyboard) Tap(code uint16, gap time.Duration) error {
	if err := k.emit(evKey, code, keyPress); err != nil {
		return err
	}
	if err := k.emit(evSyn, 0, 0); err != nil {
		return err
	}
	time.Sleep(gap)
	if err := k.emit(evKey, code, keyRelease); err != nil {
		return err
	}
	return k.emit(evSyn, 0, 0)
}

// TypeSequence types the keycodes in order with a per-key gap.
func (k *Keyboard) TypeSequence(codes []uint16, gap time.Duration) error {
	for _, code := range codes {
		if err := k.Tap(code, gap); err != nil {
			return err
		}
		time.Sleep(gap)
	}
	return nil
}

// Close destroys the virtual device and releases the fd.
func (k *Keyboard) Close() error {
	err := ioctl(k.f, uiDevDestroy, 0)
	if cerr := k.f.Close(); err == nil {
		err = cerr
	}
	return err
}
