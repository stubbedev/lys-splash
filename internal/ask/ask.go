// Package ask answers the protocol's password, question, cached-password and
// keystroke requests from what is typed: prompts queue and show one at a
// time, oldest first, every password typed is cached for the next disk, and
// every buffer that held a secret is zeroed once it is no longer needed.
package ask

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stubbedev/lys-splash/internal/key"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/theme"
)

// Replier answers one request; *server.Call is one.
type Replier interface {
	Reply(r protocol.Response) error
}

// Conn identifies the connection a request came in on, so a hang-up drops
// every request of that connection.
type Conn any

// Secret is bytes that must not be printed: every fmt verb shows it
// redacted.
type Secret []byte

// Format prints a redaction in place of the secret, whatever the verb.
func (Secret) Format(f fmt.State, _ rune) {
	fmt.Fprint(f, "<secret>")
}

// GoString prints a redaction in place of the secret for %#v.
func (Secret) GoString() string {
	return "<secret>"
}

// interrupted is what plymouth answers a prompt abandoned with Ctrl-C or
// Ctrl-D.
const interrupted = "\x03"

const bullet = "•"

const typedChunk = 64

type entry struct {
	kind   theme.PromptKind
	prompt string
	reply  Replier
	conn   Conn
}

type watch struct {
	keys  string
	reply Replier
	conn  Conn
}

// Asker is the queue of prompts, the text typed into the one showing, the
// cached passwords and the keystroke watches. It is not safe for concurrent
// use.
type Asker struct {
	queue   []entry
	typed   Secret
	cache   []Secret
	watches []watch
}

// Password queues a password prompt answered by r.
func (a *Asker) Password(r Replier, c Conn, prompt string) {
	a.queue = append(a.queue, entry{kind: theme.PasswordPrompt, prompt: prompt, reply: r, conn: c})
}

// Question queues a question, whose answer is echoed, answered by r.
func (a *Asker) Question(r Replier, c Conn, prompt string) {
	a.queue = append(a.queue, entry{kind: theme.QuestionPrompt, prompt: prompt, reply: r, conn: c})
}

// Cached answers r with every password typed so far, or NO_ANSWER when
// there is none.
func (a *Asker) Cached(r Replier) error {
	if len(a.cache) == 0 {
		return r.Reply(protocol.NoAnswer{})
	}
	answers := make([][]byte, len(a.cache))
	for i, s := range a.cache {
		answers[i] = s
	}
	return r.Reply(protocol.MultipleAnswers{Answers: answers})
}

// Watch waits for one of keys, any key when keys is empty, while no prompt
// shows, and answers r with it.
func (a *Asker) Watch(r Replier, c Conn, keys string) {
	a.watches = append(a.watches, watch{keys: keys, reply: r, conn: c})
}

// Ignore drops the watches for exactly keys, answering each NO_ANSWER.
func (a *Asker) Ignore(keys string) error {
	var errs []error
	a.watches = slices.DeleteFunc(a.watches, func(w watch) bool {
		if w.keys != keys {
			return false
		}
		errs = append(errs, w.reply.Reply(protocol.NoAnswer{}))
		return true
	})
	return errors.Join(errs...)
}

// HangUp drops every prompt and watch of c, whose client is gone. A prompt
// showing closes and what was typed into it is zeroed.
func (a *Asker) HangUp(c Conn) {
	if len(a.queue) > 0 && a.queue[0].conn == c {
		a.forget()
	}
	a.queue = slices.DeleteFunc(a.queue, func(e entry) bool { return e.conn == c })
	a.watches = slices.DeleteFunc(a.watches, func(w watch) bool { return w.conn == c })
}

func (a *Asker) forget() {
	clear(a.typed)
	a.typed = a.typed[:0]
}

// Key applies a key press: to the prompt showing, or else to the first
// watch it matches. Escape belongs to the boot messages and is ignored here.
func (a *Asker) Key(k key.Key) error {
	if len(a.queue) == 0 {
		return a.watched(k)
	}
	switch k.Kind {
	case key.Char:
		a.typeRune(k.Rune)
	case key.Backspace:
		if _, n := utf8.DecodeLastRune(a.typed); n > 0 {
			clear(a.typed[len(a.typed)-n:])
			a.typed = a.typed[:len(a.typed)-n]
		}
	case key.Enter:
		return a.answer(a.typed, true)
	case key.Interrupt:
		return a.answer([]byte(interrupted), false)
	case key.Escape:
	}
	return nil
}

func (a *Asker) typeRune(r rune) {
	if cap(a.typed)-len(a.typed) < utf8.UTFMax {
		grown := make(Secret, len(a.typed), 2*cap(a.typed)+typedChunk)
		copy(grown, a.typed)
		clear(a.typed[:cap(a.typed)])
		a.typed = grown
	}
	a.typed = utf8.AppendRune(a.typed, r)
}

func (a *Asker) answer(text []byte, cache bool) error {
	e := a.queue[0]
	a.queue = a.queue[1:]
	if cache && e.kind == theme.PasswordPrompt {
		a.cache = append(a.cache, Secret(slices.Clone(text)))
	}
	err := e.reply.Reply(protocol.Answer{Data: text})
	a.forget()
	return err
}

func (a *Asker) watched(k key.Key) error {
	var s string
	switch k.Kind {
	case key.Char:
		s = string(k.Rune)
	case key.Enter:
		s = "\n"
	default:
		return nil
	}
	for i, w := range a.watches {
		if w.keys == "" || strings.Contains(w.keys, s) {
			a.watches = slices.Delete(a.watches, i, i+1)
			return w.reply.Reply(protocol.Answer{Data: []byte(s)})
		}
	}
	return nil
}

// Prompt is what the splash shows of the prompt in front: its question and a
// bullet per character typed for a password, or the text for a question.
func (a *Asker) Prompt() theme.Prompt {
	if len(a.queue) == 0 {
		return theme.Prompt{}
	}
	e := a.queue[0]
	p := theme.Prompt{Kind: e.kind, Text: e.prompt}
	if e.kind == theme.PasswordPrompt {
		p.Shown = strings.Repeat(bullet, utf8.RuneCount(a.typed))
	} else {
		p.Shown = string(a.typed)
	}
	return p
}

// Close answers every prompt and watch still waiting NO_ANSWER and zeroes
// the text typed and every cached password, as the daemon quits.
func (a *Asker) Close() error {
	var errs []error
	for _, e := range a.queue {
		errs = append(errs, e.reply.Reply(protocol.NoAnswer{}))
	}
	for _, w := range a.watches {
		errs = append(errs, w.reply.Reply(protocol.NoAnswer{}))
	}
	a.queue, a.watches = nil, nil
	a.forget()
	for _, s := range a.cache {
		clear(s)
	}
	a.cache = nil
	return errors.Join(errs...)
}
