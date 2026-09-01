package uinput

import (
	"testing"
	"time"

	"github.com/voev/lapsus/layout"
)

type fakeDev struct {
	taps  []uint16
	holds []uint16
	rel   []uint16
}

func (f *fakeDev) hasTap(code uint16) bool {
	for _, c := range f.taps {
		if c == code {
			return true
		}
	}
	return false
}

func (f *fakeDev) Tap(code uint16, gap time.Duration) error {
	f.taps = append(f.taps, code)
	return nil
}

func (f *fakeDev) Hold(code uint16) error {
	f.holds = append(f.holds, code)
	return nil
}

func (f *fakeDev) Release(code uint16) error {
	f.rel = append(f.rel, code)
	return nil
}

func TestInjectorReplaceWordTypedCodes(t *testing.T) {
	dev := &fakeDev{}
	ensured := []layout.Layout{}
	inj := &Injector{
		Dev:          dev,
		EnsureLayout: func(l layout.Layout) error { ensured = append(ensured, l); return nil },
	}

	if err := inj.ReplaceWord("руддщ ", "hello "); err != nil {
		t.Fatalf("ReplaceWord: %v", err)
	}
	// 6 backspaces (5 letters + trailing space) come first
	if len(dev.taps) < 6 || dev.taps[0] != 14 {
		t.Errorf("expected leading backspaces, taps: %v", dev.taps)
	}
	// Corrected text typed in EN layout: h e l l o space
	want := []uint16{35, 18, 38, 38, 24, 57}
	got := dev.taps[len(dev.taps)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("typed codes = %v, want tail %v", got, want)
			break
		}
	}
	if len(ensured) != 2 || ensured[0] != layout.LayoutEN || ensured[1] != layout.LayoutEN {
		t.Errorf("EnsureLayout should be EN twice (word + TypeText), got %v", ensured)
	}
}

func TestInjectorMixedScriptSwitchesLayouts(t *testing.T) {
	dev := &fakeDev{}
	ensured := []layout.Layout{}
	inj := &Injector{
		Dev:          dev,
		EnsureLayout: func(l layout.Layout) error { ensured = append(ensured, l); return nil },
	}

	// hi привет: EN then RU — the injector must switch layouts between.
	if err := inj.TypeText("hi привет"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	switched := 0
	for i := 1; i < len(ensured); i++ {
		if ensured[i] != ensured[i-1] {
			switched++
		}
	}
	if len(ensured) == 0 || ensured[0] != layout.LayoutEN || ensured[len(ensured)-1] != layout.LayoutRU {
		t.Errorf("expected EN then RU layout ensures, got %v", ensured)
	}
	_ = switched
}

func TestInjectorUnmappableCharIsError(t *testing.T) {
	dev := &fakeDev{}
	inj := &Injector{Dev: dev}
	if err := inj.TypeText("привет 😀"); err == nil {
		t.Error("emoji is not typable — expected an error")
	}
}

func TestInjectorShiftedCharacters(t *testing.T) {
	dev := &fakeDev{}
	inj := &Injector{Dev: dev, EnsureLayout: func(layout.Layout) error { return nil }}

	if err := inj.TypeText("Ghbdtn"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	// Capital G: Shift is held (42), KEY_G tapped, Shift released.
	if len(dev.holds) != 1 || dev.holds[0] != 42 {
		t.Errorf("expected Shift hold for capital G, holds: %v", dev.holds)
	}
	if len(dev.rel) != 1 || dev.rel[0] != 42 {
		t.Errorf("expected Shift release, rel: %v", dev.rel)
	}
	if !dev.hasTap(34) {
		t.Errorf("expected KEY_G tap, taps: %v", dev.taps)
	}
}
