package dict

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voev/lapsus/layout"
)

func TestContains(t *testing.T) {
	d := New()
	if !d.Contains("hello", layout.LayoutEN) {
		t.Error("expected 'hello' to be in EN dictionary")
	}
	if !d.Contains("the", layout.LayoutEN) {
		t.Error("expected 'the' to be in EN dictionary")
	}
	if !d.Contains("мир", layout.LayoutRU) {
		t.Error("expected 'мир' to be in RU dictionary")
	}
	if !d.Contains("привет", layout.LayoutRU) {
		t.Error("expected 'привет' to be in RU dictionary")
	}
	if d.Contains("xkqzjwep", layout.LayoutEN) {
		t.Error("expected 'xkqzjwep' to NOT be in dictionary")
	}
	if d.Contains("абвгдеёж", layout.LayoutRU) {
		t.Error("expected 'абвгдеёж' to NOT be in RU dictionary")
	}
}

func TestScore(t *testing.T) {
	d := New()
	helloScore := d.Score("hello", layout.LayoutEN)
	if helloScore <= 0 {
		t.Errorf("expected 'hello' score > 0, got %d", helloScore)
	}
	unknownScore := d.Score("xkqzjwep", layout.LayoutEN)
	if unknownScore != 0 {
		t.Errorf("expected unknown word score = 0, got %d", unknownScore)
	}
}

func TestCount(t *testing.T) {
	d := New()
	if d.Count(layout.LayoutEN) < 10000 {
		t.Errorf("EN dictionary suspiciously small: %d words", d.Count(layout.LayoutEN))
	}
	if d.Count(layout.LayoutRU) < 10000 {
		t.Errorf("RU dictionary suspiciously small: %d words", d.Count(layout.LayoutRU))
	}
}

func TestUserDictMerge(t *testing.T) {
	d := New()
	if d.Contains("customword", layout.LayoutEN) {
		t.Error("'customword' should not exist before loading user dict")
	}
	// Create a temp dir with a user dict file
	tmpDir, err := os.MkdirTemp("", "userdict")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Write an EN freq file
	err = os.WriteFile(filepath.Join(tmpDir, "en_freq.txt"), []byte("customword 9999\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	err = d.LoadUserDict(tmpDir)
	if err != nil {
		t.Fatalf("LoadUserDict failed: %v", err)
	}
	if !d.Contains("customword", layout.LayoutEN) {
		t.Error("expected 'customword' to be in dictionary after loading user dict")
	}
	score := d.Score("customword", layout.LayoutEN)
	if score != 9999 {
		t.Errorf("expected 'customword' score = 9999, got %d", score)
	}
}

func TestLoadUserDictNonexistent(t *testing.T) {
	d := New()
	err := d.LoadUserDict("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("LoadUserDict should return nil for nonexistent dir, got: %v", err)
	}
}
