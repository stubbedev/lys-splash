package ask

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stubbedev/lys-splash/internal/key"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/theme"
)

type reply struct {
	got []protocol.Response
	err error
}

func (r *reply) Reply(resp protocol.Response) error {
	switch v := resp.(type) {
	case protocol.Answer:
		resp = protocol.Answer{Data: bytes.Clone(v.Data)}
	case protocol.MultipleAnswers:
		answers := make([][]byte, len(v.Answers))
		for i, a := range v.Answers {
			answers[i] = bytes.Clone(a)
		}
		resp = protocol.MultipleAnswers{Answers: answers}
	}
	r.got = append(r.got, resp)
	return r.err
}

func (r *reply) only(t *testing.T, want protocol.Response) {
	t.Helper()
	if len(r.got) != 1 || !reflect.DeepEqual(r.got[0], want) {
		t.Fatalf("replies = %#v, want only %#v", r.got, want)
	}
}

func (r *reply) none(t *testing.T) {
	t.Helper()
	if len(r.got) != 0 {
		t.Fatalf("replies = %#v, want none yet", r.got)
	}
}

func typeText(t *testing.T, a *Asker, s string) {
	t.Helper()
	for _, c := range s {
		if err := a.Key(key.Key{Kind: key.Char, Rune: c}); err != nil {
			t.Fatal(err)
		}
	}
}

func press(t *testing.T, a *Asker, k key.Kind) {
	t.Helper()
	if err := a.Key(key.Key{Kind: k}); err != nil {
		t.Fatal(err)
	}
}

func answer(s string) protocol.Answer {
	return protocol.Answer{Data: []byte(s)}
}

func TestOnePromptAnswered(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Password(r, 1, "Disk:")
	typeText(t, &a, "pä")
	if got := a.Prompt(); got != (theme.Prompt{Kind: theme.PasswordPrompt, Text: "Disk:", Shown: "••"}) {
		t.Errorf("Prompt = %+v, want two bullets", got)
	}
	r.none(t)
	press(t, &a, key.Enter)
	r.only(t, answer("pä"))
	if got := a.Prompt(); got != (theme.Prompt{}) {
		t.Errorf("Prompt after the answer = %+v", got)
	}
}

func TestQuestionEchoes(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Question(r, 1, "Continue?")
	typeText(t, &a, "yes")
	if got := a.Prompt(); got.Kind != theme.QuestionPrompt || got.Shown != "yes" {
		t.Errorf("Prompt = %+v, want the echo", got)
	}
	press(t, &a, key.Enter)
	r.only(t, answer("yes"))
	c := &reply{}
	if err := a.Cached(c); err != nil {
		t.Fatal(err)
	}
	c.only(t, protocol.NoAnswer{})
}

func TestTwoRequestsQueueInOrder(t *testing.T) {
	t.Parallel()
	var a Asker
	first, second := &reply{}, &reply{}
	a.Password(first, 1, "root:")
	a.Password(second, 2, "home:")
	if got := a.Prompt().Text; got != "root:" {
		t.Fatalf("the prompt showing is %q, want the oldest", got)
	}
	typeText(t, &a, "one")
	press(t, &a, key.Enter)
	first.only(t, answer("one"))
	second.none(t)
	if got := a.Prompt(); got.Text != "home:" || got.Shown != "" {
		t.Fatalf("the next prompt = %+v, want home: empty", got)
	}
	typeText(t, &a, "two")
	press(t, &a, key.Enter)
	second.only(t, answer("two"))
}

func TestHangUpWhileShowing(t *testing.T) {
	t.Parallel()
	var a Asker
	gone, next := &reply{}, &reply{}
	a.Password(gone, 1, "root:")
	a.Password(next, 2, "home:")
	typeText(t, &a, "sec")
	typed := a.typed[:cap(a.typed)]
	a.HangUp(1)
	gone.none(t)
	if !zero(typed) {
		t.Errorf("the text typed for a client that hung up is not zeroed: %q", typed)
	}
	if got := a.Prompt(); got.Text != "home:" || got.Shown != "" {
		t.Fatalf("after the hang-up the prompt is %+v", got)
	}
	typeText(t, &a, "x")
	press(t, &a, key.Enter)
	next.only(t, answer("x"))
}

func TestHangUpWhileQueued(t *testing.T) {
	t.Parallel()
	var a Asker
	showing, queued := &reply{}, &reply{}
	a.Password(showing, 1, "root:")
	a.Password(queued, 2, "home:")
	typeText(t, &a, "ab")
	a.HangUp(2)
	if got := a.Prompt(); got.Text != "root:" || got.Shown != "••" {
		t.Errorf("the showing prompt changed: %+v", got)
	}
	press(t, &a, key.Enter)
	showing.only(t, answer("ab"))
	queued.none(t)
	if got := a.Prompt(); got != (theme.Prompt{}) {
		t.Errorf("the hung-up request is still queued: %+v", got)
	}
}

func TestBackspace(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Password(r, 1, "p:")
	press(t, &a, key.Backspace)
	typeText(t, &a, "aé€")
	press(t, &a, key.Backspace)
	if got := a.Prompt().Shown; got != "••" {
		t.Errorf("after Backspace over a multibyte character: %q", got)
	}
	press(t, &a, key.Backspace)
	press(t, &a, key.Backspace)
	press(t, &a, key.Backspace)
	typeText(t, &a, "b")
	press(t, &a, key.Enter)
	r.only(t, answer("b"))
}

func TestEscapeKeepsTheText(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Password(r, 1, "p:")
	typeText(t, &a, "ab")
	press(t, &a, key.Escape)
	typeText(t, &a, "c")
	press(t, &a, key.Enter)
	r.only(t, answer("abc"))
}

func TestInterruptAbandonsWithoutCaching(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Password(r, 1, "p:")
	typeText(t, &a, "partial")
	press(t, &a, key.Interrupt)
	r.only(t, answer("\x03"))
	c := &reply{}
	if err := a.Cached(c); err != nil {
		t.Fatal(err)
	}
	c.only(t, protocol.NoAnswer{})
}

func TestCachedPasswords(t *testing.T) {
	t.Parallel()
	var a Asker
	none := &reply{}
	if err := a.Cached(none); err != nil {
		t.Fatal(err)
	}
	none.only(t, protocol.NoAnswer{})
	for i, pw := range []string{"first", "second"} {
		r := &reply{}
		a.Password(r, i, "p:")
		typeText(t, &a, pw)
		press(t, &a, key.Enter)
		c := &reply{}
		if err := a.Cached(c); err != nil {
			t.Fatal(err)
		}
		want := [][]byte{[]byte("first"), []byte("second")}[:i+1]
		c.only(t, protocol.MultipleAnswers{Answers: want})
	}
}

func TestKeystrokeWatch(t *testing.T) {
	t.Parallel()
	var a Asker
	yn, anyKey := &reply{}, &reply{}
	a.Watch(yn, 1, "yn")
	typeText(t, &a, "x")
	yn.none(t)
	typeText(t, &a, "n")
	yn.only(t, answer("n"))
	a.Watch(anyKey, 1, "")
	press(t, &a, key.Enter)
	anyKey.only(t, answer("\n"))
}

func TestKeystrokeWaitsBehindAPrompt(t *testing.T) {
	t.Parallel()
	var a Asker
	w, p := &reply{}, &reply{}
	a.Watch(w, 1, "")
	a.Password(p, 2, "p:")
	typeText(t, &a, "y")
	w.none(t)
	press(t, &a, key.Enter)
	p.only(t, answer("y"))
	typeText(t, &a, "z")
	w.only(t, answer("z"))
}

func TestIgnoreKeystroke(t *testing.T) {
	t.Parallel()
	var a Asker
	yn, other := &reply{}, &reply{}
	a.Watch(yn, 1, "yn")
	a.Watch(other, 1, "q")
	if err := a.Ignore("yn"); err != nil {
		t.Fatal(err)
	}
	yn.only(t, protocol.NoAnswer{})
	typeText(t, &a, "y")
	other.none(t)
	typeText(t, &a, "q")
	other.only(t, answer("q"))
	if err := a.Ignore("q"); err != nil {
		t.Fatal(err)
	}
}

func TestHangUpDropsWatches(t *testing.T) {
	t.Parallel()
	var a Asker
	w := &reply{}
	a.Watch(w, 1, "")
	a.HangUp(1)
	typeText(t, &a, "k")
	w.none(t)
}

func zero(b []byte) bool {
	return bytes.Count(b, []byte{0}) == len(b)
}

func TestSecretsAreZeroed(t *testing.T) {
	t.Parallel()
	var a Asker
	r := &reply{}
	a.Password(r, 1, "p:")
	typeText(t, &a, strings.Repeat("s", typedChunk*3))
	answered := a.typed[:cap(a.typed)]
	press(t, &a, key.Enter)
	if !zero(answered) {
		t.Error("the answered text is not zeroed")
	}
	a.Password(&reply{}, 2, "p:")
	typeText(t, &a, "short")
	cancelled := a.typed[:cap(a.typed)]
	a.HangUp(2)
	if !zero(cancelled) {
		t.Error("the cancelled text is not zeroed")
	}
	pending := &reply{}
	a.Password(pending, 3, "p:")
	typeText(t, &a, "pending")
	quitting := a.typed[:cap(a.typed)]
	cached := a.cache[0]
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	pending.only(t, protocol.NoAnswer{})
	if !zero(quitting) || !zero(cached) {
		t.Error("Close left typed text or a cached password in memory")
	}
}

func TestGrowingZeroesTheOldBuffer(t *testing.T) {
	t.Parallel()
	var a Asker
	a.Password(&reply{}, 1, "p:")
	typeText(t, &a, "abc")
	old := a.typed[:cap(a.typed)]
	typeText(t, &a, strings.Repeat("x", 2*typedChunk))
	if &old[0] == &a.typed[0] {
		t.Fatal("the buffer never grew")
	}
	if !zero(old) {
		t.Errorf("the outgrown buffer still holds %q", old)
	}
}

func TestSecretNeverPrints(t *testing.T) {
	t.Parallel()
	s := Secret("hunter2")
	for _, verb := range []string{"%v", "%s", "%x", "%X", "%q", "%d", "%#v", "%+v"} {
		if got := fmt.Sprintf(verb, s); strings.Contains(got, "hunter2") || strings.Contains(got, "68756e") || strings.Contains(got, "104") {
			t.Errorf("%s prints %q", verb, got)
		}
	}
	if got := fmt.Sprintf("%v", struct{ S Secret }{s}); strings.Contains(got, "hunter2") {
		t.Errorf("a struct holding a secret prints %q", got)
	}
}

func TestReplyErrorsSurface(t *testing.T) {
	t.Parallel()
	var a Asker
	broken := &reply{err: errors.New("gone")}
	a.Password(broken, 1, "p:")
	if err := a.Key(key.Key{Kind: key.Enter}); err == nil {
		t.Error("a failed reply was swallowed")
	}
	if got := a.Prompt(); got != (theme.Prompt{}) {
		t.Errorf("a failed reply left the prompt %+v", got)
	}
}
