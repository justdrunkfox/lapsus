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

func TestCorrectPunctuation(t *testing.T) {
	a := New(newTestDict())

	result, needsFix := a.Analyze("ghbdtn!", layout.LayoutEN)
	if !needsFix {
		t.Error("expected correction")
	}
	if result != "привет!" {
		t.Errorf("got %q, want %q", result, "привет!")
	}

	result, needsFix = a.Analyze("(руддщ)", layout.LayoutRU)
	if !needsFix {
		t.Error("expected correction")
	}
	if result != "(hello)" {
		t.Errorf("got %q, want %q", result, "(hello)")
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
	// RU-typed Shift+/ produces ',' — the user meant the EN '?'.
	corrected, needs := a.Analyze("руддщ,", layout.LayoutRU)
	if !needs || corrected != "hello?" {
		t.Errorf("Analyze(руддщ,, RU) = %q, %v; want %q, true", corrected, needs, "hello?")
	}
	// Dots, parens and spaces that are the same in both layouts survive.
	corrected, needs = a.Analyze("(ghbdtn)", layout.LayoutEN)
	if !needs || corrected != "(привет)" {
		t.Errorf("Analyze((ghbdtn), EN) = %q, %v; want %q, true", corrected, needs, "(привет)")
	}
}
