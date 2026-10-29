package console

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestScrollbackLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{"lf", []string{"one\ntwo\n"}, []string{"one", "two"}},
		{"crlf", []string{"one\r\ntwo\r\n"}, []string{"one", "two"}},
		{"partial last line", []string{"one\ntw", "o"}, []string{"one", "two"}},
		{"carriage return overwrites", []string{"abcdef\rXY\n"}, []string{"XYcdef"}},
		{"systemd's status line", []string{"\r[   *  ] A start job is running\x1b[K\r[  OK  ] Started x.\x1b[K\n"}, []string{"[  OK  ] Started x."}},
		{"colours dropped", []string{"[\x1b[0;32m  OK  \x1b[0m] Reached target \x1b[0;1;39mBasic System\x1b[0m.\r\n"}, []string{"[  OK  ] Reached target Basic System."}},
		{"erase whole line", []string{"abc\x1b[2Kd\n"}, []string{"   d"}},
		{"erase to the cursor", []string{"abcdef\b\b\b\x1b[1K\n"}, []string{"    ef"}},
		{"backspace", []string{"ab\bc\n"}, []string{"ac"}},
		{"title sequence dropped", []string{"\x1b]0;title\x07a\x1b]2;x\x1b\\b\n"}, []string{"ab"}},
		{"controls dropped", []string{"a\x00\x01\x7fb\n"}, []string{"ab"}},
		{"multibyte", []string{"Grüße €\n"}, []string{"Grüße €"}},
		{"multibyte split across writes", []string{"Gr\xc3", "\xbc\xc3", "\x9f\xe2\x82", "\xac\n"}, []string{"Grüß€"}},
		{"overwrite a multibyte character", []string{"äöü\rx\n"}, []string{"xöü"}},
		{"empty lines kept", []string{"a\n\nb\n"}, []string{"a", "", "b"}},
	}
	for _, tc := range tests {
		var s Scrollback
		for _, w := range tc.writes {
			if n, err := s.Write([]byte(w)); n != len(w) || err != nil {
				t.Fatalf("%s: Write = %d, %v", tc.name, n, err)
			}
		}
		if got := s.Lines(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Lines = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScrollbackBound(t *testing.T) {
	t.Parallel()
	var s Scrollback
	for i := range MaxLines + 250 {
		fmt.Fprintf(&s, "line %d\n", i)
	}
	got := s.Lines()
	if len(got) != MaxLines {
		t.Fatalf("kept %d lines, want %d", len(got), MaxLines)
	}
	for i, l := range got {
		if want := "line " + strconv.Itoa(250+i); l != want {
			t.Fatalf("line %d is %q, want %q", i, l, want)
		}
	}
	fmt.Fprint(&s, strings.Repeat("x", 3*MaxLineWidth), "\n")
	if last := s.Lines()[MaxLines-1]; len(last) != MaxLineWidth {
		t.Errorf("a long line kept %d characters, want %d", len(last), MaxLineWidth)
	}
}

func TestScrollbackScreen(t *testing.T) {
	t.Parallel()
	var s Scrollback
	fmt.Fprint(&s, "a\nb\nc\nd")
	tests := []struct {
		rows int
		want string
	}{
		{3, ClearScreen + "c\r\nd\r\n"},
		{10, ClearScreen + "a\r\nb\r\nc\r\nd\r\n"},
		{1, ClearScreen},
		{0, ClearScreen},
	}
	for _, tc := range tests {
		if got := string(s.Screen(tc.rows)); got != tc.want {
			t.Errorf("Screen(%d) = %q, want %q", tc.rows, got, tc.want)
		}
	}
}
