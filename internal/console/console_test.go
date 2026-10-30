package console

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-splash/internal/theme"
)

type fakeTTY struct {
	mode, kbMode int
	termios      unix.Termios
	closed       bool
	failSet      map[string]error
	calls        []string
	out          bytes.Buffer
}

func newFakeTTY() *fakeTTY {
	t := &fakeTTY{mode: kdText, kbMode: 0x01, failSet: map[string]error{}}
	t.termios.Lflag = unix.ICANON | unix.ECHO | unix.ISIG
	t.termios.Iflag = unix.IXON | unix.ICRNL
	return t
}

func (f *fakeTTY) Name() string                { return "/dev/tty1" }
func (f *fakeTTY) Read([]byte) (int, error)    { return 0, os.ErrClosed }
func (f *fakeTTY) Write(b []byte) (int, error) { return f.out.Write(b) }
func (f *fakeTTY) Mode() (int, error)          { return f.mode, nil }
func (f *fakeTTY) KeyboardMode() (int, error)  { return f.kbMode, nil }
func (f *fakeTTY) Winsize() (unix.Winsize, error) {
	return unix.Winsize{Row: 25, Col: 80}, nil
}

func (f *fakeTTY) Termios() (unix.Termios, error) {
	return f.termios, nil
}

func (f *fakeTTY) Close() error {
	f.calls = append(f.calls, "close")
	f.closed = true
	return nil
}

func (f *fakeTTY) set(name string) error {
	f.calls = append(f.calls, name)
	return f.failSet[name]
}

func (f *fakeTTY) SetMode(m int) error {
	if err := f.set("mode"); err != nil {
		return err
	}
	f.mode = m
	return nil
}

func (f *fakeTTY) SetKeyboardMode(m int) error {
	if err := f.set("kbmode"); err != nil {
		return err
	}
	f.kbMode = m
	return nil
}

func (f *fakeTTY) SetTermios(t unix.Termios) error {
	if err := f.set("termios"); err != nil {
		return err
	}
	f.termios = t
	return nil
}

func TestTakeOverAndRestore(t *testing.T) {
	t.Parallel()
	f := newFakeTTY()
	before := f.termios
	v, err := takeOver(f)
	if err != nil {
		t.Fatal(err)
	}
	if f.kbMode != kUnicode || f.termios.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 || f.termios.Cc[unix.VMIN] != 1 {
		t.Fatalf("after takeOver: kbmode %d, lflag %#x", f.kbMode, f.termios.Lflag)
	}
	if err := v.Graphics(); err != nil || f.mode != kdGraphics {
		t.Fatalf("Graphics: mode %d, %v", f.mode, err)
	}
	if err := v.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.mode != kdText || f.kbMode != 0x01 || f.termios != before || !f.closed {
		t.Errorf("after Restore: mode %d, kbmode %d, termios restored %v, closed %v", f.mode, f.kbMode, f.termios == before, f.closed)
	}
	calls := len(f.calls)
	if err := v.Restore(); err != nil || len(f.calls) != calls {
		t.Errorf("a second Restore acted: %v, calls %v", err, f.calls[calls:])
	}
}

func TestReclaimAfterAReset(t *testing.T) {
	t.Parallel()
	f := newFakeTTY()
	v, err := takeOver(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, kb := f.termios, f.kbMode
	f.termios.Lflag |= unix.ICANON | unix.ECHO
	f.kbMode = 0x01
	if err := v.Reclaim(); err != nil {
		t.Fatal(err)
	}
	if f.termios != raw || f.kbMode != kb {
		t.Errorf("after Reclaim: lflag %#x, kbmode %d; want %#x and %d", f.termios.Lflag, f.kbMode, raw.Lflag, kb)
	}
	f.failSet["termios"] = unix.EIO
	if err := v.Reclaim(); !errors.Is(err, unix.EIO) || !strings.Contains(err.Error(), "/dev/tty1") {
		t.Errorf("a failed Reclaim = %v, want EIO naming /dev/tty1", err)
	}
}

func TestLeaveKeepsGraphics(t *testing.T) {
	t.Parallel()
	f := newFakeTTY()
	v, err := takeOver(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Graphics(); err != nil {
		t.Fatal(err)
	}
	if err := v.Leave(); err != nil {
		t.Fatal(err)
	}
	if f.mode != kdGraphics || f.kbMode != 0x01 || !f.closed {
		t.Errorf("after Leave: mode %d, kbmode %d, closed %v", f.mode, f.kbMode, f.closed)
	}
	if err := v.Restore(); err != nil || f.mode != kdGraphics {
		t.Errorf("Restore after Leave acted: mode %d, %v", f.mode, err)
	}
}

func TestFailedRestoreNamesTheVT(t *testing.T) {
	t.Parallel()
	f := newFakeTTY()
	v, err := takeOver(f)
	if err != nil {
		t.Fatal(err)
	}
	f.failSet["mode"] = unix.EIO
	f.failSet["kbmode"] = unix.EIO
	err = v.Restore()
	if !errors.Is(err, unix.EIO) || !strings.Contains(err.Error(), "/dev/tty1") {
		t.Fatalf("Restore = %v, want EIO naming /dev/tty1", err)
	}
	if !f.closed || f.calls[len(f.calls)-1] != "close" {
		t.Errorf("a failed restore did not go on to close: %v", f.calls)
	}
}

func TestTakeOverUndoesWhenTheKeyboardRefuses(t *testing.T) {
	t.Parallel()
	f := newFakeTTY()
	before := f.termios
	f.failSet["kbmode"] = unix.EPERM
	if _, err := takeOver(f); !errors.Is(err, unix.EPERM) {
		t.Fatalf("takeOver = %v, want EPERM", err)
	}
	if f.termios != before || !f.closed {
		t.Errorf("a failed takeOver left the terminal raw (%v) or open (%v)", f.termios != before, !f.closed)
	}
}

func TestOpenActiveWithoutVTs(t *testing.T) {
	t.Parallel()
	if _, err := OpenActive(Paths{Sysfs: t.TempDir(), Dev: t.TempDir()}); !errors.Is(err, ErrNoVT) {
		t.Errorf("OpenActive without tty0 = %v, want ErrNoVT", err)
	}
	sys := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "tty0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "tty0", "active"), []byte("../../etc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenActive(Paths{Sysfs: sys, Dev: t.TempDir()}); err == nil || errors.Is(err, ErrNoVT) {
		t.Errorf("OpenActive of a bogus active VT = %v", err)
	}
}

func TestTextView(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	v := TextView{W: &out}
	steps := []struct {
		state theme.State
		want  string
	}{
		{theme.State{Message: "Starting"}, "Starting\r\n"},
		{theme.State{Message: "Starting", Phase: 4}, ""},
		{theme.State{Message: "Starting", Booting: true, Progress: 10}, ""},
		{theme.State{Message: "Starting", Booting: true, Progress: 11}, ""},
		{theme.State{Message: "Starting", Prompt: theme.Prompt{Kind: theme.PasswordPrompt, Text: "Disk:", Shown: "••"}}, clearLine + "Disk: "},
		{theme.State{Message: "Starting", Prompt: theme.Prompt{Kind: theme.PasswordPrompt, Text: "Disk:", Shown: "•••"}}, clearLine + "Disk: "},
		{theme.State{Message: "Unlocking", Prompt: theme.Prompt{Kind: theme.PasswordPrompt, Text: "Disk:", Shown: "•••"}}, clearLine + "Unlocking\r\n" + clearLine + "Disk: "},
		{theme.State{Message: "Unlocking"}, "\r\n"},
		{theme.State{Message: "Unlocking", Prompt: theme.Prompt{Kind: theme.QuestionPrompt, Text: "Go?", Shown: "ye"}}, clearLine + "Go? ye"},
		{theme.State{Message: "Unlocking", Updating: true, Progress: 40, Prompt: theme.Prompt{Kind: theme.QuestionPrompt, Text: "Go?", Shown: "ye"}}, clearLine + "40%\r\n" + clearLine + "Go? ye"},
	}
	var prev *theme.State
	for i, s := range steps {
		out.Reset()
		if err := v.Show(prev, s.state); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != s.want {
			t.Errorf("step %d wrote %q, want %q", i, got, s.want)
		}
		prev = &steps[i].state
	}
	if strings.Contains(out.String(), "•") {
		t.Error("the text view echoed password bullets")
	}
}
