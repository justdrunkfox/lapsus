package layout

import "testing"

func TestMapRUToEN(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"йцукен", "qwerty"},
		{"привет", "ghbdtn"},
		{"гоьы", "ujms"},
		{"ж", ";"},
		{"я", "z"},
		{"ё", "`"},
		{"Ю", ">"},
		{"Б", "<"},
		{"Ж", ":"},
		{"Э", "\""},
		{"№", "#"},
		{"@", "@"},
		{"1", "1"},
	}
	for _, tt := range tests {
		result := Map(tt.input, LayoutRU, LayoutEN)
		if result != tt.expected {
			t.Errorf("Map(%q, RU, EN) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestMapENToRU(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"qwerty", "йцукен"},
		{"asdf", "фыва"},
		{"z", "я"},
		{";", "ж"},
		{"`", "ё"},
		{"~", "Ё"},
		{"<", "Б"},
		{">", "Ю"},
		{"?", ","},
		{"@", "\""},
		{"1", "1"},
	}
	for _, tt := range tests {
		result := Map(tt.input, LayoutEN, LayoutRU)
		if result != tt.expected {
			t.Errorf("Map(%q, EN, RU) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestMapShiftedDigitRow(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"@", `"`},
		{"#", "№"},
		{"$", ";"},
		{"^", ":"},
		{"&", "?"},
	}
	for _, tt := range tests {
		result := Map(tt.input, LayoutEN, LayoutRU)
		if result != tt.expected {
			t.Errorf("Map(%q, EN, RU) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestMapPhrase(t *testing.T) {
	// Position-faithful: '?' sits on Shift+/ which types ',' on ЙЦУКЕН.
	if got := Map("Ghbdtn? vbh!", LayoutEN, LayoutRU); got != "Привет, мир!" {
		t.Errorf("Map phrase EN→RU = %q, want %q", got, "Привет, мир!")
	}
	if got := Map("Привет, мир!", LayoutRU, LayoutEN); got != "Ghbdtn? vbh!" {
		t.Errorf("Map phrase RU→EN = %q, want %q", got, "Ghbdtn? vbh!")
	}
}

func TestNormalizeLower(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello", "hello"},
		{"ПРИВЕТ", "привет"},
		{"Привет", "привет"},
		{"ЁЖА", "ёжа"},
		{"ABC", "abc"},
		{"abc", "abc"},
	}
	for _, tt := range tests {
		result := NormalizeLower(tt.input)
		if result != tt.expected {
			t.Errorf("NormalizeLower(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestMapUppercase(t *testing.T) {
	tests := []struct {
		input    string
		from, to Layout
		expected string
	}{
		{"ПРИВЕТ", LayoutRU, LayoutEN, "GHBDTN"},
		{"Привет", LayoutRU, LayoutEN, "Ghbdtn"},
		{"GHBDTN", LayoutEN, LayoutRU, "ПРИВЕТ"},
		{"Ghbdtn", LayoutEN, LayoutRU, "Привет"},
	}
	for _, tt := range tests {
		result := Map(tt.input, tt.from, tt.to)
		if result != tt.expected {
			t.Errorf("Map(%q, %v, %v) = %q, want %q", tt.input, tt.from, tt.to, result, tt.expected)
		}
	}
}

func TestMapRoundTrip(t *testing.T) {
	for _, w := range []string{"hello", "world", "test", "Hello, world!"} {
		lower := NormalizeLower(w)
		mappedRU := Map(lower, LayoutEN, LayoutRU)
		back := Map(mappedRU, LayoutRU, LayoutEN)
		if back != lower {
			t.Errorf("EN round trip %q -> %q -> %q, want %q", lower, mappedRU, back, lower)
		}
	}
	for _, w := range []string{"привет", "мир", "программа", "Привет, мир!"} {
		mappedEN := Map(w, LayoutRU, LayoutEN)
		back := Map(mappedEN, LayoutEN, LayoutRU)
		if back != w {
			t.Errorf("RU round trip %q -> %q -> %q, want %q", w, mappedEN, back, w)
		}
	}
}
