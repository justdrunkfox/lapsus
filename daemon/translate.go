package daemon

import (
	"unicode"

	"github.com/voev/lapsus/layout"
)

// keyChar maps evdev keycodes to the (base, shifted) characters they
// produce on US QWERTY. Russian characters are derived from these via
// the position-faithful layout table, so both layouts stay in sync.
var keyChar = map[uint16][2]rune{
	2:  {'1', '!'},
	3:  {'2', '@'},
	4:  {'3', '#'},
	5:  {'4', '$'},
	6:  {'5', '%'},
	7:  {'6', '^'},
	8:  {'7', '&'},
	9:  {'8', '*'},
	10: {'9', '('},
	11: {'0', ')'},
	12: {'-', '_'},
	13: {'=', '+'},
	16: {'q', 'Q'},
	17: {'w', 'W'},
	18: {'e', 'E'},
	19: {'r', 'R'},
	20: {'t', 'T'},
	21: {'y', 'Y'},
	22: {'u', 'U'},
	23: {'i', 'I'},
	24: {'o', 'O'},
	25: {'p', 'P'},
	26: {'[', '{'},
	27: {']', '}'},
	30: {'a', 'A'},
	31: {'s', 'S'},
	32: {'d', 'D'},
	33: {'f', 'F'},
	34: {'g', 'G'},
	35: {'h', 'H'},
	36: {'j', 'J'},
	37: {'k', 'K'},
	38: {'l', 'L'},
	39: {';', ':'},
	40: {'\'', '"'},
	41: {'`', '~'},
	43: {'\\', '|'},
	44: {'z', 'Z'},
	45: {'x', 'X'},
	46: {'c', 'C'},
	47: {'v', 'V'},
	48: {'b', 'B'},
	49: {'n', 'N'},
	50: {'m', 'M'},
	51: {',', '<'},
	52: {'.', '>'},
	53: {'/', '?'},
	57: {' ', ' '},
}

// charsByLayout holds per-layout keycode → (base, shifted) tables. The
// RU table is derived from the EN one through the position-faithful
// layout mapping (e.g. KEY_R → 'к' / 'К', KEY_SEMICOLON → 'ж' / 'Ж').
var charsByLayout = map[layout.Layout]map[uint16][2]rune{
	layout.LayoutEN: keyChar,
	layout.LayoutRU: buildRuTable(),
}

func buildRuTable() map[uint16][2]rune {
	ru := make(map[uint16][2]rune, len(keyChar))
	for code, pair := range keyChar {
		ru[code] = [2]rune{
			mapOne(layout.Map(string(pair[0]), layout.LayoutEN, layout.LayoutRU)),
			mapOne(layout.Map(string(pair[1]), layout.LayoutEN, layout.LayoutRU)),
		}
	}
	return ru
}

func mapOne(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// charFor translates a keycode into the character it produces under the
// given layout and shift state. ok=false for keys that produce no
// character (modifiers, arrows, F-keys, numpad...).
func charFor(code uint16, shift bool, l layout.Layout) (rune, bool) {
	table, ok := charsByLayout[l]
	if !ok {
		table = keyChar
	}
	pair, ok := table[code]
	if !ok {
		return 0, false
	}
	if shift {
		return pair[1], true
	}
	return pair[0], true
}

// isWordChar reports whether the character participates in the word
// buffer: letters and digits only. Every other produced character is a
// word separator: it completes the buffer (space, punctuation, quotes,
// brackets — anything typed as normal text).
func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
