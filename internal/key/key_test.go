package key

import (
	"slices"
	"testing"
)

func chars(s string) []Key {
	var out []Key
	for _, r := range s {
		out = append(out, Key{Kind: Char, Rune: r})
	}
	return out
}

func TestDecode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reads []string
		want  []Key
	}{
		{"ascii", []string{"abc"}, chars("abc")},
		{"enter", []string{"a\r", "\n"}, append(chars("a"), Key{Kind: Enter}, Key{Kind: Enter})},
		{"backspace", []string{"\x7f\x08"}, []Key{{Kind: Backspace}, {Kind: Backspace}}},
		{"interrupt", []string{"\x03\x04"}, []Key{{Kind: Interrupt}, {Kind: Interrupt}}},
		{"escape alone", []string{"\x1b"}, []Key{{Kind: Escape}}},
		{"escape then text", []string{"\x1bx"}, append([]Key{{Kind: Escape}}, chars("x")...)},
		{"arrow key", []string{"a\x1b[Ab"}, chars("ab")},
		{"ss3 key", []string{"\x1bOP"}, nil},
		{"modified arrow", []string{"\x1b[1;5Cz"}, chars("z")},
		{"multibyte", []string{"äö€😀"}, chars("äö€😀")},
		{"split multibyte", []string{"\xc3", "\xa4", "\xe2\x82", "\xac"}, chars("ä€")},
		{"german keymap", []string{"ß", "ü", "@"}, chars("ßü@")},
		{"invalid utf-8", []string{"\xff\xfea"}, chars("a")},
		{"other controls", []string{"\x01\x00\x1fa"}, chars("a")},
	}
	for _, tc := range tests {
		var d Decoder
		var got []Key
		for _, r := range tc.reads {
			got = d.Decode(got, []byte(r))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: Decode = %v, want %v", tc.name, got, tc.want)
		}
	}
}
