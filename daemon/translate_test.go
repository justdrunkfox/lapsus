package daemon

import (
	"testing"

	"github.com/voev/lapsus/layout"
)

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
		got, ok := charFor(c.code, c.shift, layout.LayoutEN)
		if !ok || got != c.want {
			t.Errorf("charFor(%d, shift=%v, EN) = (%q, %v), want %q", c.code, c.shift, got, ok, c.want)
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
		got, ok := charFor(c.code, c.shift, layout.LayoutRU)
		if !ok || got != c.want {
			t.Errorf("charFor(%d, shift=%v, RU) = (%q, %v), want %q", c.code, c.shift, got, ok, c.want)
		}
	}
}

func TestCharForNonCharacterKeys(t *testing.T) {
	for _, code := range []uint16{1, 28, 29, 42, 56, 58, 59, 87, 88, 103, 105, 106, 108, 111} {
		if _, ok := charFor(code, false, layout.LayoutEN); ok {
			t.Errorf("keycode %d should not translate to a character", code)
		}
	}
}

func TestIsFixBoundary(t *testing.T) {
	for _, r := range []rune{' ', ',', '.', ';', '/', '?', '!', ':'} {
		if !isFixBoundary(r) {
			t.Errorf("%q should be a fix boundary", r)
		}
	}
	for _, r := range []rune{'-', '=', '[', ']', '\'', '\\', 'a', 'ф'} {
		if isFixBoundary(r) {
			t.Errorf("%q must not be a fix boundary", r)
		}
	}
}
