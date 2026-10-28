package console

import (
	"fmt"
	"io"
	"strconv"

	"github.com/stubbedev/lys-splash/internal/theme"
)

const clearLine = "\r\x1b[K"

// TextView shows the splash as text on a terminal, for a machine without
// graphical output: each message on a line of its own, the update progress,
// and the prompt on the last line, a question's answer echoed and a
// password's not.
type TextView struct {
	W io.Writer
}

// Show writes what changed from prev to cur; with prev nil, everything.
func (t TextView) Show(prev *theme.State, cur theme.State) error {
	var before theme.State
	if prev != nil {
		before = *prev
	}
	var out []byte
	open := before.Prompt.Kind != theme.NoPrompt
	if open && (cur.Message != before.Message || cur.Updating && cur.Progress != before.Progress) {
		out = append(out, clearLine...)
	}
	if cur.Message != before.Message && cur.Message != "" {
		out = append(append(out, cur.Message...), "\r\n"...)
	}
	if cur.Updating && (!before.Updating || cur.Progress != before.Progress) {
		out = append(append(out, strconv.Itoa(int(cur.Progress))...), "%\r\n"...)
	}
	switch {
	case cur.Prompt.Kind == theme.NoPrompt && open:
		out = append(out, "\r\n"...)
	case cur.Prompt.Kind != theme.NoPrompt && (cur.Prompt != before.Prompt || len(out) > 0):
		out = append(append(out, clearLine...), cur.Prompt.Text...)
		out = append(out, ' ')
		if cur.Prompt.Kind == theme.QuestionPrompt {
			out = append(out, cur.Prompt.Shown...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if _, err := t.W.Write(out); err != nil {
		return fmt.Errorf("console: show the splash as text: %w", err)
	}
	return nil
}
