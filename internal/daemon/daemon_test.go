package daemon

import (
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/drm"

	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/theme"
)

type fakeScreen struct {
	frame    kms.Frame
	mu       sync.Mutex
	presents []image.Rectangle
}

func newScreen(size image.Point) *fakeScreen {
	return &fakeScreen{frame: kms.Frame{Pix: make([]byte, size.X*size.Y*4), Stride: size.X * 4, Size: size}}
}

func (s *fakeScreen) Frame() kms.Frame { return s.frame }
func (s *fakeScreen) Scale() int       { return 1 }

func (s *fakeScreen) Present(r image.Rectangle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presents = append(s.presents, r)
	return nil
}

type fakeDisplay struct {
	mu          sync.Mutex
	screens     []Screen
	closed      bool
	retained    bool
	deactivated bool
	closeErr    error
	onDeactive  func()
}

func (d *fakeDisplay) Events() <-chan kms.Event { return nil }
func (d *fakeDisplay) Handle(kms.Event) error   { return nil }

func (d *fakeDisplay) Screens() []Screen {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.screens
}

func (d *fakeDisplay) Deactivate() error {
	if d.onDeactive != nil {
		d.onDeactive()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deactivated = true
	return nil
}

func (d *fakeDisplay) Reactivate() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deactivated = false
	return nil
}

func (d *fakeDisplay) Close(retain bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed, d.retained = true, retain
	return d.closeErr
}

type fakeVT struct {
	input    chan []byte
	reading  chan struct{}
	mu       sync.Mutex
	graphics bool
	restored string
	reclaims int
	out      strings.Builder
}

func newVT() *fakeVT {
	return &fakeVT{input: make(chan []byte), reading: make(chan struct{}, 1)}
}

func (v *fakeVT) Read(b []byte) (int, error) {
	select {
	case v.reading <- struct{}{}:
	default:
	}
	in, ok := <-v.input
	if !ok {
		return 0, os.ErrClosed
	}
	return copy(b, in), nil
}

func (v *fakeVT) Write(b []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.out.Write(b)
}

func (v *fakeVT) Rows() (int, error) { return 3, nil }

func (v *fakeVT) Reclaim() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.reclaims++
	v.graphics = false
	return nil
}

func (v *fakeVT) reclaimed() (int, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.reclaims, v.graphics
}

func (v *fakeVT) Graphics() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.graphics = true
	return nil
}

func (v *fakeVT) Text() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.graphics = false
	return nil
}

func (v *fakeVT) release(how string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.restored == "" {
		v.restored = how
		close(v.input)
	}
	return nil
}

func (v *fakeVT) Restore() error { return v.release("restore") }
func (v *fakeVT) Leave() error   { return v.release("leave") }

func (v *fakeVT) how() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.restored
}

type run struct {
	socket   string
	display  *fakeDisplay
	vt       *fakeVT
	cancel   context.CancelFunc
	done     chan error
	roots    chan string
	duration string
	console  *fakeConsole
}

type setup struct {
	mode     Mode
	noVT     bool
	screens  int
	chroot   func(string) error
	duration string
	log      *slog.Logger
}

func start(t *testing.T, s setup) *run {
	t.Helper()
	r := &run{socket: "@lys-splash-daemon-test-" + rand.Text(), display: &fakeDisplay{}, done: make(chan error, 1), roots: make(chan string, 4), duration: s.duration, console: newConsole()}
	if r.duration == "" {
		r.duration = filepath.Join(t.TempDir(), "boot-duration")
	}
	for range s.screens {
		r.display.screens = append(r.display.screens, newScreen(image.Pt(320, 240)))
	}
	var vt Terminal
	if !s.noVT {
		r.vt = newVT()
		vt = r.vt
	}
	chroot := s.chroot
	if chroot == nil {
		chroot = func(dir string) error {
			r.roots <- dir
			return nil
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	r.cancel = cancel
	log := s.log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cfg := Config{Mode: s.mode, Socket: r.socket, Owner: uint32(os.Getuid()), Chroot: chroot, BootDuration: r.duration, Log: log}
	go func() {
		r.done <- Run(ctx, cfg, theme.Default(), nil, Devices{Display: r.display, VT: vt, Console: r.console})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after its context ended")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("unix", r.socket)
		if err == nil {
			c.Close()
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never listened: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *run) typeKeys(s string) {
	<-r.vt.reading
	r.vt.input <- []byte(s)
	<-r.vt.reading
	r.vt.reading <- struct{}{}
}

func (r *run) dial(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", r.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *run) send(t *testing.T, c net.Conn, reqs ...protocol.Request) {
	t.Helper()
	var wire []byte
	for _, req := range reqs {
		var err error
		if wire, err = protocol.AppendRequest(wire, req); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Write(wire); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, c net.Conn, want protocol.Response) {
	t.Helper()
	got, err := protocol.ReadResponse(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response %#v, want %#v", got, want)
	}
}

func (r *run) call(t *testing.T, req protocol.Request, want protocol.Response) {
	t.Helper()
	c := r.dial(t)
	r.send(t, c, req)
	expect(t, c, want)
}

func (r *run) finished(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestPingAndHasActiveVT(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.call(t, protocol.Ping{}, protocol.Ack{})
	r.call(t, protocol.HasActiveVT{}, protocol.Ack{})
	noVT := start(t, setup{noVT: true})
	noVT.call(t, protocol.HasActiveVT{}, protocol.Nak{})
}

func TestPasswordTypedThenCached(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	r.typeKeys("hunter2\r")
	expect(t, c, protocol.Answer{Data: []byte("hunter2")})
	r.call(t, protocol.CachedPassword{}, protocol.MultipleAnswers{Answers: [][]byte{[]byte("hunter2")}})
}

func TestNoKeyboardMeansNoAnswer(t *testing.T) {
	t.Parallel()
	r := start(t, setup{noVT: true, screens: 1})
	r.call(t, protocol.Password{Prompt: "Disk:"}, protocol.NoAnswer{})
	r.call(t, protocol.Question{Prompt: "Go?"}, protocol.NoAnswer{})
	r.call(t, protocol.WatchKeystroke{}, protocol.NoAnswer{})
}

func TestDeactivateDropsKeys(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Deactivate{})
	expect(t, c, protocol.Ack{})
	r.typeKeys("lost\r")
	r.call(t, protocol.Reactivate{}, protocol.Ack{})
	r.typeKeys("kept\r")
	expect(t, c, protocol.Answer{Data: []byte("kept")})
	r.display.mu.Lock()
	defer r.display.mu.Unlock()
	if r.display.deactivated {
		t.Error("the display is still deactivated")
	}
}

func TestMessageRedrawsOnlyItsLine(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.call(t, protocol.PauseProgress{}, protocol.Ack{})
	s := r.display.screens[0].(*fakeScreen)
	s.mu.Lock()
	before := len(s.presents)
	first := s.presents[0]
	s.mu.Unlock()
	if first != (image.Rectangle{Max: image.Pt(320, 240)}) {
		t.Errorf("the first frame presented %v, want all of it", first)
	}
	r.call(t, protocol.ShowMessage{Text: "Unlocking"}, protocol.Ack{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.presents) != before+1 {
		t.Fatalf("%d presents after the message, want one more than %d", len(s.presents), before)
	}
	if last := s.presents[len(s.presents)-1]; last.Dy() >= 240 || last.Empty() {
		t.Errorf("the message presented %v, want its line only", last)
	}
	r.vt.mu.Lock()
	defer r.vt.mu.Unlock()
	if !r.vt.graphics {
		t.Error("the VT is not in graphics mode under a graphical splash")
	}
}

func TestTextFallback(t *testing.T) {
	t.Parallel()
	r := start(t, setup{})
	r.call(t, protocol.ShowMessage{Text: "Starting"}, protocol.Ack{})
	c := r.dial(t)
	r.send(t, c, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
	expect(t, c, protocol.Ack{})
	r.typeKeys("pw\r")
	expect(t, c, protocol.Answer{Data: []byte("pw")})
	r.vt.mu.Lock()
	out := r.vt.out.String()
	r.vt.mu.Unlock()
	if !strings.Contains(out, "Starting\r\n") || !strings.Contains(out, "Disk: ") || strings.Contains(out, "pw") {
		t.Errorf("the text splash wrote %q", out)
	}
}

func TestNewRoot(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.call(t, protocol.NewRoot{Dir: "/sysroot"}, protocol.Ack{})
	if got := <-r.roots; got != "/sysroot" {
		t.Errorf("switched into %q", got)
	}
	failing := start(t, setup{screens: 1, chroot: func(string) error { return os.ErrPermission }})
	failing.call(t, protocol.NewRoot{Dir: "/sysroot"}, protocol.Nak{})
}

func TestQuitPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    Mode
		quit    *protocol.Quit
		screens int
		retain  bool
		vt      string
		console string
	}{
		{"quit", Boot, &protocol.Quit{}, 1, false, "restore", "restore"},
		{"quit retaining", Boot, &protocol.Quit{Retain: true}, 1, true, "leave", "leave"},
		{"quit retaining in text", Boot, &protocol.Quit{Retain: true}, 0, true, "restore", "leave"},
		{"SIGTERM at boot", Boot, nil, 1, false, "restore", "restore"},
		{"SIGTERM at reboot", Reboot, nil, 1, true, "leave", "leave"},
		{"SIGTERM at shutdown", Shutdown, nil, 1, true, "leave", "leave"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := start(t, setup{mode: tc.mode, screens: tc.screens})
			pending := r.dial(t)
			r.send(t, pending, protocol.Password{Prompt: "Disk:"}, protocol.Ping{})
			expect(t, pending, protocol.Ack{})
			if tc.quit != nil {
				r.call(t, *tc.quit, protocol.Ack{})
			} else {
				r.cancel()
			}
			if err := r.finished(t); err != nil {
				t.Fatal(err)
			}
			expect(t, pending, protocol.NoAnswer{})
			if !r.display.closed || r.display.retained != tc.retain {
				t.Errorf("display closed %v retained %v, want retained %v", r.display.closed, r.display.retained, tc.retain)
			}
			if got := r.vt.how(); got != tc.vt {
				t.Errorf("the VT was given back by %q, want %q", got, tc.vt)
			}
			if got, _ := r.console.released(); got != tc.console {
				t.Errorf("the console was given back by %q, want %q", got, tc.console)
			}
			if _, err := net.Dial("unix", r.socket); err == nil {
				t.Error("the socket still listens after the quit")
			}
		})
	}
}

func TestQuitWithoutCloseFB(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.display.mu.Lock()
	r.display.closeErr = fmt.Errorf("kms: release: %w", drm.ErrNoCloseFB)
	r.display.mu.Unlock()
	r.call(t, protocol.Quit{Retain: true}, protocol.Ack{})
	if err := r.finished(t); err != nil {
		t.Errorf("a kernel without CLOSEFB failed the quit: %v", err)
	}
}

func TestPanicRestoresTheVT(t *testing.T) {
	t.Parallel()
	display := &fakeDisplay{onDeactive: func() { panic("handler bug") }}
	display.screens = []Screen{newScreen(image.Pt(64, 64))}
	vt := newVT()
	socket := "@lys-splash-daemon-test-" + rand.Text()
	cfg := Config{Socket: socket, Owner: uint32(os.Getuid()), Chroot: func(string) error { return nil }, Log: slog.New(slog.DiscardHandler)}
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_ = Run(t.Context(), cfg, theme.Default(), nil, Devices{Display: display, VT: vt})
	}()
	var c net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		if c, err = net.Dial("unix", socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer c.Close()
	wire, _ := protocol.AppendRequest(nil, protocol.Deactivate{})
	if _, err := c.Write(wire); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-panicked:
		if p == nil {
			t.Fatal("Run returned without the panic")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panic never surfaced")
	}
	if vt.how() != "restore" || !display.closed || display.retained {
		t.Errorf("after a panic: VT %q, display closed %v retained %v", vt.how(), display.closed, display.retained)
	}
}

func TestListenFailureGivesTheVTBack(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	vt := newVT()
	display := &fakeDisplay{}
	cfg := Config{Socket: r.socket, Owner: uint32(os.Getuid()), Log: slog.New(slog.DiscardHandler)}
	if err := Run(t.Context(), cfg, theme.Default(), nil, Devices{Display: display, VT: vt}); err == nil {
		t.Fatal("Run on a taken socket succeeded")
	}
	if vt.how() != "restore" || !display.closed {
		t.Errorf("a failed start left VT %q, display closed %v", vt.how(), display.closed)
	}
}

func TestNotifyReady(t *testing.T) {
	t.Parallel()
	addr := "@lys-splash-notify-" + rand.Text()
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := Notify(addr, "READY=1"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if err := ln.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := ln.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Errorf("the supervisor read %q, %v", buf[:n], err)
	}
	if err := Notify("", "READY=1"); err != nil {
		t.Errorf("Notify without a supervisor: %v", err)
	}
	if err := Notify("@lys-splash-nobody-"+rand.Text(), "READY=1"); err == nil {
		t.Error("Notify to a missing socket succeeded")
	}
}

func TestNeedsReexec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode   Mode
		argv0  string
		initrd bool
		want   bool
	}{
		{Boot, "/bin/lys-splash", true, true},
		{Boot, "@/bin/lys-splash", true, false},
		{Boot, "/bin/lys-splash", false, false},
		{Updates, "/bin/lys-splash", true, false},
		{Shutdown, "/bin/lys-splash", false, true},
		{Reboot, "/bin/lys-splash", false, true},
		{Kexec, "/bin/lys-splash", true, true},
		{Reboot, "@/bin/lys-splash", false, false},
	}
	for _, tc := range tests {
		if got := NeedsReexec(tc.mode, tc.argv0, tc.initrd); got != tc.want {
			t.Errorf("NeedsReexec(%v, %q, %v) = %v", tc.mode, tc.argv0, tc.initrd, got)
		}
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for _, m := range []Mode{Boot, Shutdown, Reboot, Kexec, Updates} {
		if got, err := ParseMode(m.String()); err != nil || got != m {
			t.Errorf("ParseMode(%q) = %v, %v", m, got, err)
		}
	}
	if _, err := ParseMode("suspend"); err == nil {
		t.Error("an unknown mode parsed")
	}
}

func TestTheVTIsReclaimedOnceTheRootIsWritableAndOnReactivate(t *testing.T) {
	t.Parallel()
	r := start(t, setup{screens: 1})
	r.call(t, protocol.SystemInitialized{}, protocol.Ack{})
	if n, graphics := r.vt.reclaimed(); n != 1 || !graphics {
		t.Errorf("after the root became writable the VT was reclaimed %d times, graphics %v; want once, in graphics", n, graphics)
	}
	r.call(t, protocol.Deactivate{}, protocol.Ack{})
	r.call(t, protocol.Reactivate{}, protocol.Ack{})
	if n, graphics := r.vt.reclaimed(); n != 2 || !graphics {
		t.Errorf("after reactivate the VT was reclaimed %d times, graphics %v; want twice, in graphics", n, graphics)
	}
}
