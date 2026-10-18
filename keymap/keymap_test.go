package keymap

import (
	"testing"

	"github.com/voev/lapsus/layout"
)

// Translation tests ported from the Langswitcher-reverse groundwork
// (daemon/translate.go there duplicated this table; the package is the
// single home — these cases pin it down).

func TestCharForEN(t *testing.T) {
	cases := []struct {
		code  uint16
		shift bool
		want  rune
	}{
		{18, false, 'e'},
		{18, true, 'E'},
		{35, false, 'h'},
		{13, true, '+'},
		{57, false, ' '},
		{39, false, ';'},
		{53, true, '?'},
	}
	for _, c := range cases {
		got, ok := CharFor(c.code, c.shift, layout.LayoutEN)
		if !ok || got != c.want {
			t.Errorf("CharFor(%d, shift=%v, EN) = (%q, %v), want %q", c.code, c.shift, got, ok, c.want)
		}
	}
}

func TestCharForRU(t *testing.T) {
	cases := []struct {
		code  uint16
		shift bool
		want  rune
	}{
		// Positional ЙЦУКЕН: same physical keys.
		{16, false, 'й'},
		{16, true, 'Й'},
		{35, false, 'р'},
		{38, false, 'д'},
		{24, false, 'щ'},
		{39, false, 'ж'}, // ';' key is 'ж' in Russian
		{40, false, 'э'},
		{26, false, 'х'},
		{51, false, 'б'},
		{52, false, 'ю'},
		{53, true, ','}, // '?' key is ',' in Russian
		{2, false, '1'}, // digits pass through
	}
	for _, c := range cases {
		got, ok := CharFor(c.code, c.shift, layout.LayoutRU)
		if !ok || got != c.want {
			t.Errorf("CharFor(%d, shift=%v, RU) = (%q, %v), want %q", c.code, c.shift, got, ok, c.want)
		}
	}
}

func TestCharForNonCharacterKeys(t *testing.T) {
	for _, code := range []uint16{1, 28, 29, 42, 56, 58, 59, 87, 88, 103, 105, 106, 108, 111} {
		if _, ok := CharFor(code, false, layout.LayoutEN); ok {
			t.Errorf("keycode %d should not translate to a character", code)
		}
	}
}

func TestCodeForRoundTrip(t *testing.T) {
	// Every character either round-trips through CodeFor or is absent
	// from the tables; a mismatch would corrupt uinput injection.
	// Shift-invariant characters (space: {' ',' '}) round-trip to the
	// base state — CodeFor returns the canonical press.
	for _, l := range []layout.Layout{layout.LayoutEN, layout.LayoutRU} {
		for code := uint16(1); code < 120; code++ {
			for _, shift := range []bool{false, true} {
				ch, ok := CharFor(code, shift, l)
				if !ok {
					continue
				}
				base, _ := CharFor(code, false, l)
				gotCode, gotShift, ok := CodeFor(ch, l)
				wantShift := shift
				if base == ch {
					wantShift = false
				}
				if !ok || gotCode != code || gotShift != wantShift {
					t.Errorf("CodeFor(CharFor(%d,%v,%v)=%q) = (%d,%v,%v)", code, shift, l, ch, gotCode, gotShift, ok)
				}
			}
		}
	}
}
