package daemon

import (
	"errors"
	"image"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/theme"
)

func keymapOf(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func (r *run) await(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("not within 5s: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *run) polled(t *testing.T, n int) {
	t.Helper()
	from := r.vt.reads()
	r.await(t, "the Caps Lock polls", func() bool { return r.vt.reads() >= from+n })
}

func (r *run) settled(t *testing.T) int {
	t.Helper()
	for {
		before := r.presents()
		r.polled(t, 2)
		if r.presents() == before {
			return before
		}
	}
}

func (r *run) lastPresent() image.Rectangle {
	screen := r.display.screens[0].(*fakeScreen)
	screen.mu.Lock()
	defer screen.mu.Unlock()
	return screen.presents[len(screen.presents)-1]
}

func (r *run) out() string {
	r.vt.mu.Lock()
	defer r.vt.mu.Unlock()
	return r.vt.out.String()
}

func TestABootWithoutAPromptReadsNoLockFlags(t *testing.T) {
	t.Parallel()
	r := start(t, setup{mode: Boot, screens: 1, keymap: keymapOf("de")})
	r.call(t, protocol.ShowMessage{Text: "Starting"}, protocol.Ack{})
	r.typeKeys("abc")
	r.call(t, protocol.SystemUpdate{Progress: 10}, protocol.Ack{})
	r.vtSettled(t)
	if n := r.vt.reads(); n != 0 {
		t.Errorf("%d lock flag reads without a prompt, want none", n)
	}
}

func hintDamage(t *testing.T, size image.Point, prev, cur theme.State) image.Rectangle {
	t.Helper()
	rd, err := theme.NewRenderer(theme.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}
	if _, err := rd.Draw(f, 1, nil, prev); err != nil {
		t.Fatal(err)
	}
	d, err := rd.Draw(f, 1, &prev, cur)
	if err != nil {
		t.Fatal(err)
	}
	var b image.Rectangle
	for _, r := range d.Rects() {
		b = b.Union(r)
	}
	return b
}

func TestACapsLockChangePresentsOnlyTheHintOnce(t *testing.T) {
	t.Parallel()
	size := image.Pt(800, 600)
	r := start(t, setup{screens: 1, size: size, keymap: keymapOf("de")})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	before := r.settled(t)

	off := theme.State{Prompt: theme.Prompt{Kind: theme.PasswordPrompt, Text: "Disk:"}, Hints: theme.Hints{Keymap: "de"}}
	on := off
	on.Hints.CapsLock = true
	for i, step := range []struct {
		caps     bool
		from, to theme.State
	}{{true, off, on}, {false, on, off}} {
		r.vt.setCaps(step.caps)
		r.await(t, "a present for the Caps Lock change", func() bool { return r.presents() > before })
		if got := r.settled(t); got != before+1 {
			t.Fatalf("change %d: %d presents, want one", i, got-before)
		}
		before++
		if got, want := r.lastPresent(), hintDamage(t, size, step.from, step.to); got != want || got.Empty() {
			t.Errorf("change %d presented %v, want the hint's %v alone", i, got, want)
		}
	}
	r.typeKeys("x\r")
	expect(t, c, protocol.Answer{Data: []byte("x")})
	reads := r.vt.reads()
	r.call(t, protocol.Ping{}, protocol.Ack{})
	r.vtSettled(t)
	if n := r.vt.reads(); n != reads {
		t.Errorf("%d lock flag reads after the prompt ended, want none", n-reads)
	}
}

func TestTheTextFallbackShowsTheHints(t *testing.T) {
	t.Parallel()
	r := start(t, setup{keymap: keymapOf("de")})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	r.await(t, "the keymap on the prompt line", func() bool { return strings.HasSuffix(r.out(), "Disk: [de] ") })
	r.vt.setCaps(true)
	r.await(t, "Caps Lock on the prompt line", func() bool { return strings.HasSuffix(r.out(), "Disk: [de] ("+theme.CapsLockHint+") ") })
	r.typeKeys("p")
	r.vt.setCaps(false)
	r.await(t, "Caps Lock off the prompt line", func() bool { return strings.HasSuffix(r.out(), "Disk: [de] ") })
}

func TestTheNewRootsKeymapIsShown(t *testing.T) {
	t.Parallel()
	var loads atomic.Int32
	keymap := func() (string, error) {
		if loads.Add(1) == 1 {
			return "us", nil
		}
		return "de", nil
	}
	r := start(t, setup{keymap: keymap})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	r.await(t, "the initrd's keymap", func() bool { return strings.HasSuffix(r.out(), "Disk: [us] ") })
	r.call(t, protocol.NewRoot{Dir: "/sysroot"}, protocol.Ack{})
	r.await(t, "the real root's keymap", func() bool { return strings.HasSuffix(r.out(), "Disk: [de] ") })
}

func TestPromptHintsOff(t *testing.T) {
	t.Parallel()
	th := theme.Default()
	th.PromptHints = false
	r := start(t, setup{keymap: keymapOf("de"), theme: &th})
	r.vt.setCaps(true)
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	r.typeKeys("ab")
	r.vtSettled(t)
	if n := r.vt.reads(); n != 0 {
		t.Errorf("%d lock flag reads with prompt-hints off, want none", n)
	}
	if out := r.out(); !strings.HasSuffix(out, "Disk: ") {
		t.Errorf("the prompt line is %q, want no hints", out)
	}
}

func TestAKeymapLoadKeepsWhatTheKernelHasLoaded(t *testing.T) {
	t.Parallel()
	d := &daemon{hints: true, cfg: Config{Log: slog.New(slog.DiscardHandler)}}
	load := func(name string, err error) {
		d.keymaps++
		d.keymapLoaded(&keymapLoad{serial: d.keymaps, name: name, err: err})
	}
	load("us", nil)
	load("", nil)
	if got := d.state.Hints.Keymap; got != "us" {
		t.Errorf("after a root without a keymap the hint is %q, want the loaded us", got)
	}
	load("de", nil)
	d.keymapLoaded(&keymapLoad{serial: d.keymaps - 1, name: "fr"})
	if got := d.state.Hints.Keymap; got != "de" {
		t.Errorf("after a stale load the hint is %q, want de", got)
	}
	load("", errors.New("unreadable"))
	if got := d.state.Hints.Keymap; got != "" {
		t.Errorf("after a failed load the hint is %q, want none", got)
	}
}
