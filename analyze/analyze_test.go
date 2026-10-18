package analyze

import (
	"testing"

	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/layout"
)

func newTestDict() *dict.Dict {
	return dict.New()
}

func TestCorrectWrongLayout(t *testing.T) {
	a := New(newTestDict())

	// "ghbdtn" is "привет" typed with EN layout active but fingers on RU
	result, needsFix := a.Analyze("ghbdtn", layout.LayoutEN)
	if !needsFix {
		t.Error("expected ghbdtn to need correction")
	}
	if result != "привет" {
		t.Errorf("got %q, want %q", result, "привет")
	}

	// The reverse: "пздрвдштп" etc. — "ghbdtn" covered above; check RU→EN
	result, needsFix = a.Analyze("руддщ", layout.LayoutRU)
	if !needsFix {
		t.Error("expected руддщ (hello in RU layout) to need correction")
	}
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestCorrectAlreadyCorrect(t *testing.T) {
	a := New(newTestDict())

	// "привет" in RU layout is correct
	_, needsFix := a.Analyze("привет", layout.LayoutRU)
	if needsFix {
		t.Error("привет in RU layout should NOT need correction")
	}

	// "hello" in EN layout is correct
	_, needsFix = a.Analyze("hello", layout.LayoutEN)
	if needsFix {
		t.Error("hello in EN layout should NOT need correction")
	}
}

func TestCorrectUnknownWord(t *testing.T) {
	a := New(newTestDict())

	// Random gibberish — no correction
	_, needsFix := a.Analyze("xkqzjwep", layout.LayoutEN)
	if needsFix {
		t.Error("unknown word should NOT be corrected")
	}
}

func TestCapsCyrillic(t *testing.T) {
	a := New(newTestDict())

	// Regression: uppercase Cyrillic used to be stripped off as "edge
	// punctuation", so sentence-start words were silently skipped and
	// some cases corrupted ("руддЩ" → "hellO" via a mangled core).
	result, needsFix := a.Analyze("Руддщ", layout.LayoutRU)
	if !needsFix || result != "Hello" {
		t.Errorf("Analyze(Руддщ, RU) = %q, %v; want Hello, true", result, needsFix)
	}
	result, needsFix = a.Analyze("руддЩ", layout.LayoutRU)
	if !needsFix || result != "hellO" {
		t.Errorf("Analyze(руддЩ, RU) = %q, %v; want hellO, true", result, needsFix)
	}
}

func TestPunctuationInsideWord(t *testing.T) {
	a := New(newTestDict())

	// "hs,f" is "рыба" with EN active: б sits on the comma key. The
	// whole flipped reading is a dictionary word → fix.
	result, needsFix := a.Analyze("hs,f", layout.LayoutEN)
	if !needsFix || result != "рыба" {
		t.Errorf("Analyze(hs,f, EN) = %q, %v; want рыба, true", result, needsFix)
	}

	// Real punctuation never survives the dictionary gate: the flipped
	// reading with the sign attached is not a word.
	if _, needsFix := a.Analyze("руддщ,", layout.LayoutRU); needsFix {
		t.Error("руддщ, → hello? is not a word, must not be auto-fixed")
	}
	if _, needsFix := a.Analyze("ghbdtn!", layout.LayoutEN); needsFix {
		t.Error("ghbdtn! → привет! is not a word, must not be auto-fixed")
	}
	if _, needsFix := a.Analyze("ghbdtn,", layout.LayoutEN); needsFix {
		t.Error("ghbdtn, → приветб is not a word, must not be auto-fixed")
	}
}

func TestGuessLayout(t *testing.T) {
	cases := []struct {
		text string
		want layout.Layout
	}{
		{"ghbdtn", layout.LayoutEN},
		{"привет", layout.LayoutRU},
		{"руддщ", layout.LayoutRU},
		{"Ghbdtn!", layout.LayoutEN},
		{"123", layout.LayoutEN},     // digits only → default EN
		{"", layout.LayoutEN},        // empty → default EN
		{"a абвг", layout.LayoutRU},  // more cyrillic
		{"abcd ab", layout.LayoutEN}, // more latin
		{"abc абв", layout.LayoutEN}, // tie → default EN
	}
	for _, c := range cases {
		if got := GuessLayout(c.text); got != c.want {
			t.Errorf("GuessLayout(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestFlipsTrailingPunctuation(t *testing.T) {
	a := New(newTestDict())
	// Punctuation handling was removed from Analyze (the daemon only ever
	// delivers bare words); punctuation-bearing input must stay untouched.
	corrected, needs := a.Analyze("руддщ,", layout.LayoutRU)
	if needs || corrected != "руддщ," {
		t.Errorf("Analyze(руддщ,, RU) = %q, %v; want unchanged, false", corrected, needs)
	}
	corrected, needs = a.Analyze("(ghbdtn)", layout.LayoutEN)
	if needs || corrected != "(ghbdtn)" {
		t.Errorf("Analyze((ghbdtn), EN) = %q, %v; want unchanged, false", corrected, needs)
	}
}
