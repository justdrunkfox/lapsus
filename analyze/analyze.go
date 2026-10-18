// Package analyze detects and corrects words typed in the wrong
// keyboard layout, using frequency dictionaries.
package analyze

import (
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/layout"
)

// Analyzer detects and corrects words typed in the wrong keyboard layout.
type Analyzer struct {
	dict *dict.Dict
}

// New creates an analyzer with the given dictionary.
func New(d *dict.Dict) *Analyzer {
	return &Analyzer{dict: d}
}

// GuessLayout guesses which layout the text was typed in, by script:
// more Cyrillic than Latin runes means RU, anything else (including ties
// and digits-only text) means EN.
func GuessLayout(text string) layout.Layout {
	cyr, lat := 0, 0
	for _, r := range text {
		switch {
		case (r >= '\u0410' && r <= '\u044f') || r == '\u0401' || r == '\u0451':
			cyr++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			lat++
		}
	}
	if cyr > lat {
		return layout.LayoutRU
	}
	return layout.LayoutEN
}

// Analyze checks if a word was typed in the wrong layout and returns the
// corrected version. needsFix is true if the word should be replaced.
//
// The word is whatever the daemon buffered between spaces: letters,
// digits and punctuation-key characters (the Russian б ж э ю х ъ sit on
// , . ; ' [ ]). The whole string flips positionally — case comes along
// naturally via the layout table ("Руддщ" → "Hello") — and must read as
// a single dictionary word in the other layout: "hs,f" → "рыба" fixes,
// while "ghbdtn," (→ "приветб", not a word) is left untouched.
func (a *Analyzer) Analyze(word string, currentLayout layout.Layout) (corrected string, needsFix bool) {
	otherLayout := layout.Other(currentLayout)
	mapped := layout.Map(word, currentLayout, otherLayout)

	// Nothing maps (digits, same-key symbols): a layout fix is impossible.
	if mapped == word {
		return word, false
	}

	// Fix only on clear evidence: the flipped reading is a known word and
	// the original is not. If both are known, the flipped one must win by
	// a wide margin (conservative threshold).
	origScore := a.dict.Score(word, currentLayout)
	mappedScore := a.dict.Score(mapped, otherLayout)

	if mappedScore > 0 && origScore == 0 {
		return mapped, true
	}
	if mappedScore > 0 && origScore > 0 && mappedScore >= origScore*2 {
		return mapped, true
	}

	return word, false
}
