// Package key is the keyboard input the splash acts on: characters already
// mapped by the console's keymap, and the few keys that edit or answer a
// prompt.
package key

import "unicode/utf8"

// Kind is what a key does.
type Kind uint8

// The key kinds.
const (
	// Char types a character.
	Char Kind = iota
	// Enter answers a prompt.
	Enter
	// Backspace deletes the last character.
	Backspace
	// Escape toggles the boot messages.
	Escape
	// Interrupt is Ctrl-C or Ctrl-D, which abandon a prompt.
	Interrupt
)

// Key is one key press.
type Key struct {
	Kind Kind
	Rune rune
}

// Decoder turns bytes read from a terminal in non-canonical mode into keys,
// keeping a multibyte character split across reads until it is whole. The
// bytes it keeps or joins may be part of a password, so it zeroes them once
// decoded, and Forget zeroes what it still keeps.
type Decoder struct {
	partial []byte
}

const (
	esc       = 0x1b
	del       = 0x7f
	ctrlC     = 0x03
	ctrlD     = 0x04
	ctrlH     = 0x08
	csiFinalL = 0x40
	csiFinalH = 0x7e
)

// Decode appends the keys in one read's bytes to keys. An escape byte alone
// at the end of a read is the Escape key; one followed by '[' or 'O' starts
// a terminal escape sequence, such as an arrow key's, which is dropped.
// Other control characters and invalid UTF-8 are dropped.
func (d *Decoder) Decode(keys []Key, b []byte) []Key {
	if len(d.partial) > 0 {
		joined := append(d.partial, b...)
		d.partial = nil
		defer clear(joined)
		b = joined
	}
	for len(b) > 0 {
		c := b[0]
		switch {
		case c == '\r' || c == '\n':
			keys = append(keys, Key{Kind: Enter})
		case c == del || c == ctrlH:
			keys = append(keys, Key{Kind: Backspace})
		case c == ctrlC || c == ctrlD:
			keys = append(keys, Key{Kind: Interrupt})
		case c == esc:
			if len(b) > 1 && (b[1] == '[' || b[1] == 'O') {
				b = b[sequenceLen(b):]
				continue
			}
			keys = append(keys, Key{Kind: Escape})
		case c < ' ':
		case c < utf8.RuneSelf:
			keys = append(keys, Key{Kind: Char, Rune: rune(c)})
		default:
			if !utf8.FullRune(b) {
				d.partial = append(d.partial[:0], b...)
				return keys
			}
			r, n := utf8.DecodeRune(b)
			if r != utf8.RuneError {
				keys = append(keys, Key{Kind: Char, Rune: r})
			}
			b = b[n:]
			continue
		}
		b = b[1:]
	}
	return keys
}

// Forget zeroes and drops the start of a character still waiting for the
// rest of its bytes.
func (d *Decoder) Forget() {
	clear(d.partial)
	d.partial = nil
}

func sequenceLen(b []byte) int {
	for i := 2; i < len(b); i++ {
		if b[i] >= csiFinalL && b[i] <= csiFinalH {
			return i + 1
		}
	}
	return len(b)
}
