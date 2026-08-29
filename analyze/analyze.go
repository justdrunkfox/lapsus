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
func (a *Analyzer) Analyze(word string, currentLayout layout.Layout) (corrected string, needsFix bool) {
	// Strip trailing/leading punctuation, preserve it
	leading, core, trailing := stripPunctuation(word)

	// Map the core word to the other layout
	otherLayout := layout.LayoutEN
	if currentLayout == layout.LayoutEN {
		otherLayout = layout.LayoutRU
	}
	mapped := layout.Map(core, currentLayout, otherLayout)

	// If the mapped version is the same as the original, no layout fix possible.
	if mapped == core {
		return word, false
	}

	// Score original in current layout dict
	origScore := a.dict.Score(core, currentLayout)
	// Score mapped version in the OTHER layout dict (where it would be a real word)
	mappedScore := a.dict.Score(mapped, otherLayout)

	// If the mapped version is a known word in the other layout, and the
	// original is NOT a known word in the current layout, it's likely wrong.
	if mappedScore > 0 && origScore == 0 {
		return leading + mapped + trailing, true
	}

	// If both are known words, prefer the higher score.
	// Threshold: mapped must score at least 2x the original to override.
	if mappedScore > 0 && origScore > 0 && mappedScore >= origScore*2 {
		return leading + mapped + trailing, true
	}

	return word, false
}

// stripPunctuation separates leading/trailing non-alphanumeric characters.
func stripPunctuation(word string) (leading, core, trailing string) {
	runes := []rune(word)

	// Find start of alphanumeric core
	start := 0
	for start < len(runes) {
		r := runes[start]
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '\u0430' && r <= '\u044f') || r == '\u0451' || r == '\u0401' ||
			(r >= '0' && r <= '9') {
			break
		}
		start++
	}

	// Find end of alphanumeric core
	end := len(runes) - 1
	for end >= start {
		r := runes[end]
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '\u0430' && r <= '\u044f') || r == '\u0451' || r == '\u0401' ||
			(r >= '0' && r <= '9') {
			break
		}
		end--
	}
	end++

	if start > 0 {
		leading = string(runes[:start])
	}
	if end < len(runes) {
		trailing = string(runes[end:])
	}
	core = string(runes[start:end])

	return leading, core, trailing
}
