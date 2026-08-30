package tray

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/voev/lapsus/layout"
)

func TestFlagPNGDecodes(t *testing.T) {
	for _, l := range []layout.Layout{layout.LayoutEN, layout.LayoutRU} {
		for _, dim := range []bool{false, true} {
			raw := flagPNG(l, dim)
			if len(raw) == 0 {
				t.Fatalf("flagPNG(%v, %v) is empty", l, dim)
			}
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("flagPNG(%v, %v) is not a valid PNG: %v", l, dim, err)
			}
			if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
				t.Errorf("icon size = %v, want 32x32", img.Bounds())
			}
		}
	}
}

func TestFlagPNGDimsWhenPaused(t *testing.T) {
	bright := flagPNG(layout.LayoutRU, false)
	dim := flagPNG(layout.LayoutRU, true)
	if bytes.Equal(bright, dim) {
		t.Error("dimmed icon must differ from the normal one")
	}
}

func TestFlagPNGDiffersPerLayout(t *testing.T) {
	en := flagPNG(layout.LayoutEN, false)
	ru := flagPNG(layout.LayoutRU, false)
	if bytes.Equal(en, ru) {
		t.Error("EN and RU flags must differ")
	}
}
