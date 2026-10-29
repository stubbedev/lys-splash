package daemon

import (
	"image"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-splash/internal/console"
	"github.com/stubbedev/lys-splash/internal/protocol"
)

type fakeConsole struct {
	output  chan []byte
	reading chan struct{}
	mu      sync.Mutex
	how     string
	lines   []string
}

func newConsole() *fakeConsole {
	return &fakeConsole{output: make(chan []byte), reading: make(chan struct{}, 1)}
}

func (c *fakeConsole) Read(b []byte) (int, error) {
	select {
	case c.reading <- struct{}{}:
	default:
	}
	out, ok := <-c.output
	if !ok {
		return 0, os.ErrClosed
	}
	return copy(b, out), nil
}

func (c *fakeConsole) release(how string, lines []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.how == "" {
		c.how, c.lines = how, lines
		close(c.output)
	}
	return nil
}

func (c *fakeConsole) Restore(lines []string) error { return c.release("restore", lines) }
func (c *fakeConsole) Leave() error                 { return c.release("leave", nil) }

func (c *fakeConsole) released() (string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.how, c.lines
}

// write hands s to the daemon as console output and returns once the
// daemon has taken it; a request sent afterwards is handled after it.
func (r *run) write(s string) {
	<-r.console.reading
	r.console.output <- []byte(s)
	<-r.console.reading
	r.console.reading <- struct{}{}
}

// handled returns once every key and console output handed over so far has
// been handled.
func (r *run) handled(t *testing.T) {
	t.Helper()
	r.call(t, protocol.Ping{}, protocol.Ack{})
}

func (r *run) state(t *testing.T) (deactivated, graphics bool, out string) {
	t.Helper()
	r.display.mu.Lock()
	deactivated = r.display.deactivated
	r.display.mu.Unlock()
	r.vt.mu.Lock()
	defer r.vt.mu.Unlock()
	return deactivated, r.vt.graphics, r.vt.out.String()
}

func TestEscapeTogglesTheBootMessages(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.write("[  OK  ] Started a.\r\n\x1b[0;32mStarted\x1b[0m b.\r\n")
	pending := r.dial(t)
	r.send(t, pending, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, pending, protocol.Ack{})
	r.typeKeys("p\x1b")
	r.handled(t)
	deactivated, graphics, out := r.state(t)
	if !deactivated || graphics {
		t.Fatalf("after Esc the display is deactivated %v and the VT in graphics %v, want the console shown", deactivated, graphics)
	}
	want := console.ClearScreen + "[  OK  ] Started a.\r\n"
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "Started b.\r\n") || !strings.HasSuffix(out, "Disk: ") {
		t.Errorf("the boot messages were written as %q, want %q, the second line and the prompt", out, want)
	}
	r.write("Started c.\r\n")
	r.handled(t)
	if _, _, out := r.state(t); !strings.HasSuffix(out, "Started c.\r\n") {
		t.Errorf("console output while the messages show reached the VT as %q", out)
	}
	s := r.display.screens[0].(*fakeScreen)
	s.mu.Lock()
	before := len(s.presents)
	s.mu.Unlock()
	r.typeKeys("w\x1b")
	r.handled(t)
	if deactivated, graphics, _ := r.state(t); deactivated || !graphics {
		t.Fatalf("after Esc again the display is deactivated %v and the VT in graphics %v, want the splash back", deactivated, graphics)
	}
	s.mu.Lock()
	presents := slices.Clone(s.presents[before:])
	s.mu.Unlock()
	if !slices.Contains(presents, image.Rectangle{Max: image.Pt(320, 240)}) {
		t.Errorf("the splash came back presenting %v, want the whole frame", presents)
	}
	r.typeKeys("\r")
	expect(t, pending, protocol.Answer{Data: []byte("pw")})
	r.call(t, protocol.Quit{}, protocol.Ack{})
	if err := r.finished(t); err != nil {
		t.Fatal(err)
	}
	how, lines := r.console.released()
	if want := []string{"[  OK  ] Started a.", "Started b.", "Started c."}; how != "restore" || !slices.Equal(lines, want) {
		t.Errorf("the console was given back by %q with %q, want restore with %q", how, lines, want)
	}
}

func TestEscapeInTheTextFallback(t *testing.T) {
	t.Parallel()
	r := start(t, setup{})
	r.call(t, protocol.ShowMessage{Text: "Unlocking"}, protocol.Ack{})
	r.write("Started a.\r\n")
	r.typeKeys("\x1b")
	r.handled(t)
	_, _, out := r.state(t)
	shown := console.ClearScreen + "Started a.\r\nUnlocking\r\n"
	if !strings.HasSuffix(out, shown) {
		t.Fatalf("Esc in text wrote %q, want it to end in %q", out, shown)
	}
	r.typeKeys("\x1b")
	r.handled(t)
	if _, _, out := r.state(t); !strings.HasSuffix(out, shown+console.ClearScreen+"Unlocking\r\n") {
		t.Errorf("Esc again in text wrote %q, want the screen cleared and the splash's text again", out)
	}
}

func TestDeactivateWhileTheBootMessagesShow(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.typeKeys("\x1b")
	r.call(t, protocol.Deactivate{}, protocol.Ack{})
	if deactivated, _, _ := r.state(t); !deactivated {
		t.Fatal("the display is active after deactivate")
	}
	r.call(t, protocol.Reactivate{}, protocol.Ack{})
	if deactivated, graphics, _ := r.state(t); deactivated || !graphics {
		t.Errorf("after reactivate the display is deactivated %v and the VT in graphics %v, want the splash", deactivated, graphics)
	}
}
