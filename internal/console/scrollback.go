package console

import "unicode/utf8"

// Scrollback is the console's output as the lines a terminal would show: a
// carriage return goes back to the start of the line, a backspace back one
// character and an erase-line sequence clears from the cursor on; other
// escape sequences and control characters are dropped. It keeps the last
// MaxLines lines, each cut at MaxLineWidth characters.
type Scrollback struct {
	lines   []string
	first   int
	cur     []rune
	col     int
	esc     escape
	params  []byte
	partial []byte
}

// The bounds of a Scrollback.
const (
	MaxLines     = 1000
	MaxLineWidth = 512
)

// ClearScreen clears a terminal and puts the cursor top left.
const ClearScreen = "\x1b[H\x1b[2J"

type escape uint8

const (
	plain escape = iota
	escaped
	csi
	osc
	oscEscaped
)

const (
	bel           = 0x07
	backspace     = 0x08
	tab           = 0x09
	esc           = 0x1b
	del           = 0x7f
	csiFinalFirst = 0x40
	csiFinalLast  = 0x7e
)

// Write takes the next of the console's output; it never fails.
func (s *Scrollback) Write(b []byte) (int, error) {
	n := len(b)
	if len(s.partial) > 0 {
		b = append(s.partial, b...)
		s.partial = nil
	}
	for len(b) > 0 {
		c := b[0]
		if s.esc != plain || c < utf8.RuneSelf {
			s.byte(c)
			b = b[1:]
			continue
		}
		if !utf8.FullRune(b) {
			s.partial = append(s.partial[:0], b...)
			break
		}
		r, size := utf8.DecodeRune(b)
		s.put(r)
		b = b[size:]
	}
	return n, nil
}

func (s *Scrollback) byte(c byte) {
	switch s.esc {
	case escaped:
		switch c {
		case '[':
			s.esc, s.params = csi, s.params[:0]
		case ']':
			s.esc = osc
		default:
			s.esc = plain
		}
		return
	case csi:
		if c >= csiFinalFirst && c <= csiFinalLast {
			s.esc = plain
			if c == 'K' {
				s.eraseLine(string(s.params))
			}
			return
		}
		s.params = append(s.params, c)
		return
	case osc:
		switch c {
		case bel:
			s.esc = plain
		case esc:
			s.esc = oscEscaped
		}
		return
	case oscEscaped:
		s.esc = plain
		return
	case plain:
	}
	switch {
	case c == '\n':
		s.newline()
	case c == '\r':
		s.col = 0
	case c == backspace:
		s.col = max(s.col-1, 0)
	case c == esc:
		s.esc = escaped
	case c == tab || c >= ' ' && c != del:
		s.put(rune(c))
	}
}

func (s *Scrollback) put(r rune) {
	if s.col >= MaxLineWidth {
		return
	}
	for len(s.cur) < s.col {
		s.cur = append(s.cur, ' ')
	}
	if s.col < len(s.cur) {
		s.cur[s.col] = r
	} else {
		s.cur = append(s.cur, r)
	}
	s.col++
}

func (s *Scrollback) eraseLine(params string) {
	switch params {
	case "", "0":
		s.cur = s.cur[:min(s.col, len(s.cur))]
	case "1":
		for i := range min(s.col+1, len(s.cur)) {
			s.cur[i] = ' '
		}
	case "2":
		s.cur = s.cur[:0]
	}
}

func (s *Scrollback) newline() {
	line := string(s.cur)
	s.cur, s.col = s.cur[:0], 0
	if len(s.lines) < MaxLines {
		s.lines = append(s.lines, line)
		return
	}
	s.lines[s.first] = line
	s.first = (s.first + 1) % MaxLines
}

// Lines are the lines kept, oldest first, the one still being written last
// when it has anything on it.
func (s *Scrollback) Lines() []string {
	out := make([]string, 0, len(s.lines)+1)
	out = append(out, s.lines[s.first:]...)
	out = append(out, s.lines[:s.first]...)
	if len(s.cur) > 0 {
		out = append(out, string(s.cur))
	}
	return out
}

// Screen is what a terminal of rows lines shows of the scrollback: the
// screen cleared, then the last lines, leaving the bottom row free.
func (s *Scrollback) Screen(rows int) []byte {
	lines := s.Lines()
	lines = lines[max(len(lines)-max(rows-1, 0), 0):]
	out := []byte(ClearScreen)
	for _, l := range lines {
		out = append(append(out, l...), "\r\n"...)
	}
	return out
}
