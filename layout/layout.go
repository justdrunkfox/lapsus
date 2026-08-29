// Package layout converts text between US QWERTY and Russian ЙЦУКЕН
// keyboard layouts by physical key position.
package layout

import (
	"fmt"
	"unicode/utf8"
)

// Layout identifies a keyboard layout.
type Layout int

const (
	LayoutEN Layout = iota
	LayoutRU
)

// enToRu maps US QWERTY characters (lowercase, uppercase and shifted
// symbols) to the characters produced by the same physical keys on the
// ЙЦУКЕН layout. Punctuation is position-faithful: '?' (Shift+/) maps to
// ',' because Shift+/ types ',' on ЙЦУКЕН, and so on.
// Table proven in practice by the wksw prototype.
var enToRu = map[rune]rune{
	// letters
	'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н',
	'u': 'г', 'i': 'ш', 'o': 'щ', 'p': 'з', '[': 'х', ']': 'ъ',
	'a': 'ф', 's': 'ы', 'd': 'в', 'f': 'а', 'g': 'п', 'h': 'р',
	'j': 'о', 'k': 'л', 'l': 'д', ';': 'ж', '\'': 'э',
	'z': 'я', 'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и', 'n': 'т', 'm': 'ь',
	',': 'б', '.': 'ю', '/': '.', '`': 'ё',
	// uppercase letters (Shift + same position)
	'Q': 'Й', 'W': 'Ц', 'E': 'У', 'R': 'К', 'T': 'Е', 'Y': 'Н',
	'U': 'Г', 'I': 'Ш', 'O': 'Щ', 'P': 'З', '{': 'Х', '}': 'Ъ',
	'A': 'Ф', 'S': 'Ы', 'D': 'В', 'F': 'А', 'G': 'П', 'H': 'Р',
	'J': 'О', 'K': 'Л', 'L': 'Д', ':': 'Ж', '"': 'Э',
	'Z': 'Я', 'X': 'Ч', 'C': 'С', 'V': 'М', 'B': 'И', 'N': 'Т', 'M': 'Ь',
	'<': 'Б', '>': 'Ю', '?': ',', '~': 'Ё',
	// shifted digit row (Shift + same position)
	'@': '"', '#': '№', '$': ';', '^': ':', '&': '?',
}

// ruToEn is the inverse of enToRu.
var ruToEn = map[rune]rune{}

func init() {
	for en, ru := range enToRu {
		if prev, dup := ruToEn[ru]; dup {
			panic(fmt.Sprintf("layout table conflict: %q and %q both map to %q", prev, en, ru))
		}
		ruToEn[ru] = en
	}
}

// Other returns the counterpart layout of a two-layout setup.
func Other(l Layout) Layout {
	if l == LayoutRU {
		return LayoutEN
	}
	return LayoutRU
}

// Map converts text from one layout to the other by physical key position.
// Characters without a mapping (digits, most symbols, other scripts) pass
// through unchanged. Case is handled by the table itself.
func Map(text string, from, to Layout) string {
	if from == to {
		return text
	}
	var m map[rune]rune
	switch {
	case from == LayoutEN && to == LayoutRU:
		m = enToRu
	case from == LayoutRU && to == LayoutEN:
		m = ruToEn
	default:
		return text
	}
	runes := []rune(text)
	for i, r := range runes {
		if mapped, ok := m[r]; ok {
			runes[i] = mapped
		}
	}
	return string(runes)
}

// NormalizeLower converts a word to lowercase for dictionary lookup
// normalization. Not a general-purpose lowercasing function — only handles
// ASCII and Cyrillic uppercase letters needed for layout mapping.
func NormalizeLower(word string) string {
	runes := make([]rune, utf8.RuneCountInString(word))
	i := 0
	for _, r := range word {
		runes[i] = toLower(r)
		i++
	}
	return string(runes)
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	if r >= 'А' && r <= 'Я' {
		return r + 32
	}
	if r == 'Ё' {
		return 'ё'
	}
	return r
}
