package server

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

type event struct {
	call *Call
	hung *Conn
}

type recorder struct {
	events chan event
	answer func(*Call) (protocol.Response, bool)
}

func (r *recorder) Request(c *Call) {
	if resp, ok := r.answer(c); ok {
		if err := c.Reply(resp); err != nil {
			panic(err)
		}
	}
	r.events <- event{call: c}
}

func (r *recorder) HangUp(c *Conn) {
	r.events <- event{hung: c}
}

func (r *recorder) next(t *testing.T) event {
	t.Helper()
	select {
	case e := <-r.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no request or hang-up within 5s")
		return event{}
	}
}

func (r *recorder) request(t *testing.T) *Call {
	t.Helper()
	e := r.next(t)
	if e.call == nil {
		t.Fatal("got a hang-up, want a request")
	}
	return e.call
}

func (r *recorder) hangUp(t *testing.T) *Conn {
	t.Helper()
	e := r.next(t)
	if e.hung == nil {
		t.Fatalf("got %#v, want a hang-up", e.call.Request)
	}
	return e.hung
}

func ackAll(c *Call) (protocol.Response, bool) {
	switch c.Request.(type) {
	case protocol.Password, protocol.Question, protocol.WatchKeystroke:
		return nil, false
	}
	return protocol.Ack{}, true
}

func serve(t *testing.T, name string, owner uint32, answer func(*Call) (protocol.Response, bool)) *recorder {
	t.Helper()
	r := &recorder{events: make(chan event, 16), answer: answer}
	s, err := Listen(name, owner, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(r) }()
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return r
}

func socketName() string {
	return "@lys-splash-test-" + rand.Text()
}

func uid() uint32 {
	return uint32(os.Getuid())
}

func dial(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, c *net.UnixConn, wire string) {
	t.Helper()
	if _, err := c.Write([]byte(wire)); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, c *net.UnixConn, want protocol.Response) {
	t.Helper()
	got, err := protocol.ReadResponse(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestPing(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid(), ackAll)
	c := dial(t, name)
	send(t, c, "P\x00")
	expect(t, c, protocol.Ack{})
	call := r.request(t)
	if call.Request != (protocol.Ping{}) {
		t.Errorf("request = %#v, want a ping", call.Request)
	}
	if p := call.Conn().Peer(); p.UID != uid() || int(p.PID) != os.Getpid() {
		t.Errorf("peer = %+v, want this process", p)
	}
}

func TestMessageThenPauseOnOneConnection(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid(), ackAll)
	c := dial(t, name)
	send(t, c, "M\x02\x06hello\x00A\x00")
	c.Close()
	if got := r.request(t).Request; got != (protocol.ShowMessage{Text: "hello"}) {
		t.Errorf("first request = %#v", got)
	}
	if got := r.request(t).Request; got != (protocol.PauseProgress{}) {
		t.Errorf("second request = %#v", got)
	}
	r.hangUp(t)
}

func TestPasswordAnsweredLater(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid(), ackAll)
	c := dial(t, name)
	send(t, c, "*\x02\x06Disk:\x00P\x00")
	pending := r.request(t)
	if pending.Request != (protocol.Password{Prompt: "Disk:"}) {
		t.Fatalf("request = %#v", pending.Request)
	}
	expect(t, c, protocol.Ack{})
	r.request(t)
	if err := pending.Reply(protocol.Answer{Data: []byte("hunter2")}); err != nil {
		t.Fatal(err)
	}
	expect(t, c, protocol.Answer{Data: []byte("hunter2")})
	if err := pending.Reply(protocol.NoAnswer{}); err == nil {
		t.Error("a second Reply succeeded")
	}
}

func TestHangUpWhilePending(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid(), ackAll)
	c := dial(t, name)
	send(t, c, "*\x02\x06Disk:\x00")
	pending := r.request(t)
	c.Close()
	if got := r.hangUp(t); got != pending.Conn() {
		t.Error("the hang-up names another connection")
	}
	if err := pending.Reply(protocol.Answer{Data: []byte("late")}); err != nil {
		t.Errorf("Reply after the hang-up: %v", err)
	}
}

func TestForeignUserGetsNak(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid()+1, ackAll)
	c := dial(t, name)
	send(t, c, "P\x00Q\x02\x01\x00")
	expect(t, c, protocol.Nak{})
	expect(t, c, protocol.Nak{})
	c.Close()
	r.hangUp(t)
}

func TestUnknownCommandGetsNakAndContinues(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := serve(t, name, uid(), ackAll)
	c := dial(t, name)
	send(t, c, "z\x00u\x02\x04abc\x00P\x00")
	expect(t, c, protocol.Nak{})
	expect(t, c, protocol.Nak{})
	expect(t, c, protocol.Ack{})
	if got := r.request(t).Request; got != (protocol.Ping{}) {
		t.Errorf("request = %#v, want only the ping", got)
	}
}

func TestMalformedDropsConnection(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{"M\x02\x00", "M\x02\x03abc", "P\x07", "M\x02\x05ab"} {
		name := socketName()
		r := serve(t, name, uid(), ackAll)
		c := dial(t, name)
		send(t, c, wire)
		if wire == "M\x02\x05ab" {
			if err := c.CloseWrite(); err != nil {
				t.Fatal(err)
			}
		}
		r.hangUp(t)
		if n, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Errorf("after %q the client read %d bytes, %v; want EOF", wire, n, err)
		}
	}
}

func TestCloseHangsUpPendingConnections(t *testing.T) {
	t.Parallel()
	name := socketName()
	r := &recorder{events: make(chan event, 4), answer: ackAll}
	s, err := Listen(name, uid(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(r) }()
	c := dial(t, name)
	send(t, c, "*\x00")
	r.request(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r.hangUp(t)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"}); err == nil {
		t.Error("dialing a closed server succeeded")
	}
}

func TestListenTwiceFails(t *testing.T) {
	t.Parallel()
	name := socketName()
	serve(t, name, uid(), ackAll)
	if _, err := Listen(name, uid(), slog.New(slog.DiscardHandler)); !errors.Is(err, ErrRunning) {
		t.Errorf("a second Listen on the same name = %v, want ErrRunning", err)
	}
}

func TestPlymouthClient(t *testing.T) {
	plymouth := os.Getenv("LYS_SPLASH_PLYMOUTH")
	if plymouth == "" {
		t.Skip("LYS_SPLASH_PLYMOUTH names no plymouth client; the plymouth-client check sets it")
	}
	r := serve(t, protocol.SocketName, uid(), func(c *Call) (protocol.Response, bool) {
		if _, ok := c.Request.(protocol.Password); ok {
			return protocol.Answer{Data: []byte("hunter2")}, true
		}
		return protocol.Ack{}, true
	})
	tests := []struct {
		args []string
		want []protocol.Request
		out  string
	}{
		{[]string{"--ping"}, []protocol.Request{protocol.Ping{}}, ""},
		{[]string{"display-message", "--text=Unlocking"}, []protocol.Request{protocol.ShowMessage{Text: "Unlocking"}}, ""},
		{[]string{"ask-for-password", "--prompt=Disk password"}, []protocol.Request{protocol.PauseProgress{}, protocol.Password{Prompt: "Disk password"}, protocol.ResumeProgress{}}, "hunter2"},
		{[]string{"quit"}, []protocol.Request{protocol.Quit{}}, ""},
		{[]string{"quit", "--retain-splash"}, []protocol.Request{protocol.Quit{Retain: true}}, ""},
	}
	for _, tc := range tests {
		out, err := exec.CommandContext(t.Context(), plymouth, tc.args...).Output()
		if err != nil {
			t.Fatalf("plymouth %v: %v", tc.args, err)
		}
		if string(out) != tc.out {
			t.Errorf("plymouth %v printed %q, want %q", tc.args, out, tc.out)
		}
		var got []protocol.Request
		for {
			e := r.next(t)
			if e.hung != nil {
				break
			}
			got = append(got, e.call.Request)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("plymouth %v sent %#v, want %#v", tc.args, got, tc.want)
		}
	}
}

// stalled is a client's socket as a writer sees it once the client stops
// reading: a write blocks until the test decides how it ends.
type stalled struct {
	*net.UnixConn
	loop    uint64
	mu      sync.Mutex
	writes  [][]byte
	onLoop  int
	entered chan struct{}
	end     chan error
}

func goroutineID() uint64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	id, _, _ := strings.Cut(strings.TrimPrefix(string(buf[:n]), "goroutine "), " ")
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}

func (s *stalled) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.writes = append(s.writes, b)
	if goroutineID() == s.loop {
		s.onLoop++
	}
	s.mu.Unlock()
	s.entered <- struct{}{}
	if err := <-s.end; err != nil {
		return 0, err
	}
	return len(b), nil
}

func (s *stalled) counts() (writes, onLoop int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes), s.onLoop
}

func serveStalled(t *testing.T) (*recorder, *stalled, *net.UnixConn) {
	t.Helper()
	name := socketName()
	st := &stalled{loop: goroutineID(), entered: make(chan struct{}, 64), end: make(chan error, 64)}
	r := &recorder{events: make(chan event, 64), answer: func(*Call) (protocol.Response, bool) { return nil, false }}
	s, err := Listen(name, uid(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s.wire = func(c *net.UnixConn) wire {
		st.UnixConn = c
		return st
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(r) }()
	t.Cleanup(func() {
		for range 4 {
			select {
			case st.end <- os.ErrDeadlineExceeded:
			default:
			}
		}
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return r, st, dial(t, name)
}

func entered(t *testing.T, st *stalled) {
	t.Helper()
	select {
	case <-st.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never wrote")
	}
}

func TestAClientThatStopsReadingCostsTheLoopNoWrites(t *testing.T) {
	t.Parallel()
	r, st, c := serveStalled(t)
	send(t, c, "P\x00")
	call := r.request(t)
	if err := call.Reply(protocol.Ack{}); err != nil {
		t.Fatal(err)
	}
	entered(t, st)
	passes := 0
	for range replyQueue {
		send(t, c, "P\x00")
		if err := r.request(t).Reply(protocol.Ack{}); err != nil {
			t.Fatal(err)
		}
		passes++
	}
	if writes, onLoop := st.counts(); writes != 1 || onLoop != 0 {
		t.Errorf("%d socket writes, %d of them on the loop; want the writer's one and none", writes, onLoop)
	}
	if passes != replyQueue {
		t.Errorf("%d loop passes during the stall, want %d", passes, replyQueue)
	}
	send(t, c, "P\x00")
	if err := r.request(t).Reply(protocol.Ack{}); err == nil {
		t.Error("an overflowing reply was dropped silently")
	}
	st.end <- os.ErrDeadlineExceeded
	if got := r.hangUp(t); got != call.Conn() {
		t.Errorf("hang-up of %v, want the stalled connection", got)
	}
	if writes, onLoop := st.counts(); writes != 1 || onLoop != 0 {
		t.Errorf("after the drop: %d writes, %d on the loop", writes, onLoop)
	}
}

func TestATimedOutWriteDropsTheConnection(t *testing.T) {
	t.Parallel()
	r, st, c := serveStalled(t)
	send(t, c, "P\x00")
	call := r.request(t)
	st.end <- os.ErrDeadlineExceeded
	if err := call.Reply(protocol.Ack{}); err != nil {
		t.Fatal(err)
	}
	entered(t, st)
	r.hangUp(t)
	if err := (&Call{Request: protocol.Ping{}, conn: call.Conn()}).Reply(protocol.Ack{}); err != nil {
		t.Errorf("a reply after the drop: %v", err)
	}
	if writes, _ := st.counts(); writes != 1 {
		t.Errorf("%d writes to a client whose write timed out, want 1", writes)
	}
}

func TestQueuedAnswersAreZeroedWrittenOrDropped(t *testing.T) {
	t.Parallel()
	r, st, c := serveStalled(t)
	send(t, c, "P\x00")
	call := r.request(t)
	secret := protocol.Answer{Data: []byte("hunter2")}
	if err := call.Reply(secret); err != nil {
		t.Fatal(err)
	}
	entered(t, st)
	var held [][]byte
	conn := call.Conn()
	for range 3 {
		if err := (&Call{Request: protocol.Ping{}, conn: conn}).Reply(secret); err != nil {
			t.Fatal(err)
		}
	}
	for range len(conn.queue) {
		b := <-conn.queue
		held = append(held, b)
		conn.queue <- b
	}
	st.mu.Lock()
	held = append(held, st.writes...)
	st.mu.Unlock()
	if len(held) != 4 || !bytes.Contains(held[0], secret.Data) {
		t.Fatalf("held %q, want four encoded answers", held)
	}
	st.end <- os.ErrDeadlineExceeded
	select {
	case <-conn.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never drained")
	}
	for i, b := range held {
		if slices.ContainsFunc(b, func(v byte) bool { return v != 0 }) {
			t.Errorf("reply %d is left in memory: %q", i, b)
		}
	}
}
