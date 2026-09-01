package units

import (
	"strings"
	"testing"
)

func TestUTF16UnitsMatchesServerFixtures(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{name: "ASCII", text: "Hello, world!", want: 13},
		{name: "composed Unicode", text: "é", want: 1},
		{name: "decomposed Unicode", text: "e\u0301", want: 2},
		{name: "CJK", text: "你好世界", want: 4},
		{name: "emoji", text: "😀", want: 2},
		{name: "surrogate pair", text: "𐐷", want: 2},
		{name: "flag", text: "🇨🇦", want: 4},
		{name: "newlines", text: "one\ntwo\r\nthree", want: 14},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := UTF16Units(tt.text); got != tt.want {
				t.Errorf("UTF16Units(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestUTF16UnitsCountsCallerTrimmedInput(t *testing.T) {
	text := "  😀  "
	if got := UTF16Units(text); got != 6 {
		t.Errorf("UTF16Units(%q) = %d, want 6", text, got)
	}
	if got := UTF16Units(strings.TrimSpace(text)); got != 2 {
		t.Errorf("UTF16Units(trimmed %q) = %d, want 2", text, got)
	}
}
