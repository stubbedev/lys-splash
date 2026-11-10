// Package server serves plymouthd's socket protocol: it accepts connections
// on the abstract socket, decodes their requests and hands each one to a
// Handler, which answers it at once or later.
package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/peercred"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

const (
	writeTimeout = 5 * time.Second
	replyQueue   = 16
)

// ErrRunning is what Listen fails with when another daemon already listens
// on the socket.
var ErrRunning = errors.New("server: another daemon is already listening")

// Handler receives a server's requests. Both methods are called from the
// connection's own goroutine, in the order the client sent its requests,
// and must not block.
type Handler interface {
	// Request is a request from a permitted peer. The handler answers it
	// with Call.Reply, now or later; a request whose connection hangs up
	// first needs no answer.
	Request(c *Call)
	// HangUp reports that c is gone: its pending calls will never be
	// answered on the wire and can be dropped. No Request on c follows it.
	HangUp(c *Conn)
}

// wire is the part of a connection its writer uses.
type wire interface {
	SetWriteDeadline(t time.Time) error
	Write(b []byte) (int, error)
}

// Conn is one client connection. Replies go through a bounded queue to the
// connection's own writer, so a client that stops reading costs only itself:
// its connection is dropped once the queue overflows or a write times out.
type Conn struct {
	c    *net.UnixConn
	w    wire
	peer peercred.Credentials
	log  *slog.Logger

	mu      sync.Mutex
	gone    bool
	queue   chan []byte
	drained chan struct{}
}

func newConn(c *net.UnixConn, w wire, peer peercred.Credentials, log *slog.Logger) *Conn {
	return &Conn{c: c, w: w, peer: peer, log: log, queue: make(chan []byte, replyQueue), drained: make(chan struct{})}
}

// Peer is the process that opened the connection, as SO_PEERCRED reports
// it when the connection is accepted.
func (c *Conn) Peer() peercred.Credentials {
	return c.peer
}

// Call is one request awaiting its answer.
type Call struct {
	Request protocol.Request
	conn    *Conn

	mu       sync.Mutex
	answered bool
}

// Conn is the connection the request came in on.
func (c *Call) Conn() *Conn {
	return c.conn
}

// Reply answers the call without waiting for the client: the answer joins the
// connection's queue. A call is answered once; a second Reply fails. When the
// client has already hung up the answer is dropped without error. The encoded
// bytes are zeroed once written or dropped, since an answer may be a secret.
func (c *Call) Reply(r protocol.Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answered {
		return fmt.Errorf("server: %q from pid %d is already answered", c.Request.Command(), c.conn.peer.PID)
	}
	c.answered = true
	return c.conn.send(r)
}

func (c *Conn) send(r protocol.Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil
	}
	out, err := protocol.AppendResponse(nil, r)
	if err != nil {
		clear(out)
		return fmt.Errorf("server: encode a %#02x response for pid %d: %w", byte(r.Type()), c.peer.PID, err)
	}
	select {
	case c.queue <- out:
		return nil
	default:
	}
	clear(out)
	c.drop()
	return fmt.Errorf("server: pid %d stopped reading its %d queued replies: dropped a %#02x response and the connection", c.peer.PID, replyQueue, byte(r.Type()))
}

// drop ends the connection from the writing side: nothing more is queued, and
// the reader sees its end of the socket close and reports the hang-up.
func (c *Conn) drop() {
	c.hangUp()
	if err := c.c.CloseRead(); err != nil && !errors.Is(err, net.ErrClosed) {
		c.log.Warn("hanging up a client that stopped reading", "pid", c.peer.PID, "err", err)
	}
}

func (c *Conn) hangUp() {
	if !c.gone {
		c.gone = true
		close(c.queue)
	}
}

// writeReplies writes what is queued until the connection ends. After a
// failed write it writes nothing more, only zeroing what is left.
func (c *Conn) writeReplies() {
	defer close(c.drained)
	broken := false
	for out := range c.queue {
		if !broken {
			broken = c.write(out) != nil
		}
		clear(out)
	}
}

func (c *Conn) write(out []byte) error {
	err := c.w.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err == nil {
		_, err = c.w.Write(out)
	}
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		c.log.Warn("dropping a client whose reply could not be written", "pid", c.peer.PID, "err", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.gone {
		c.drop()
	}
	return err
}

// finish stops the queue, waits for the writer and so flushes the replies a
// client that is still reading has yet to get.
func (c *Conn) finish() {
	c.mu.Lock()
	c.hangUp()
	c.mu.Unlock()
	<-c.drained
}

// Server accepts connections and dispatches their requests.
type Server struct {
	ln      *net.UnixListener
	wire    func(*net.UnixConn) wire
	owner   uint32
	handler Handler
	log     *slog.Logger

	mu     sync.Mutex
	conns  map[*Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Listen listens on the Unix socket name, "@" leading an abstract one as in
// net.Listen. Only requests from processes running as uid owner reach the
// handler Serve is given; every other peer is answered NAK, as plymouthd
// answers non-root peers. A name another process listens on fails with
// ErrRunning.
func Listen(name string, owner uint32, log *slog.Logger) (*Server, error) {
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("%w on %s: %w", ErrRunning, name, err)
	}
	if err != nil {
		return nil, fmt.Errorf("server: listen on %s: %w", name, err)
	}
	return &Server{ln: ln, wire: func(c *net.UnixConn) wire { return c }, owner: owner, log: log, conns: map[*Conn]struct{}{}}, nil
}

// Serve hands every request to h and accepts connections until Close, then
// returns nil.
func (s *Server) Serve(h Handler) error {
	s.handler = h
	for {
		c, err := s.ln.AcceptUnix()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("server: accept on %s: %w", s.ln.Addr(), err)
		}
		peer, err := peercred.Of(c)
		if err != nil {
			s.log.Warn("dropping a connection whose peer is unknown", "err", err)
			s.close(c, 0)
			continue
		}
		conn := newConn(c, s.wire(c), peer, s.log)
		if !s.track(conn) {
			s.close(c, peer.PID)
			return nil
		}
		go conn.writeReplies()
		go s.serve(conn)
	}
}

func (s *Server) track(c *Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *Server) serve(c *Conn) {
	defer s.wg.Done()
	defer func() {
		c.finish()
		s.close(c.c, c.peer.PID)
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.handler.HangUp(c)
	}()
	d := protocol.NewDecoder(c.c)
	for {
		req, err := d.Decode()
		var de *protocol.DecodeError
		switch {
		case err == nil:
		case errors.As(err, &de) && !de.Fatal():
			s.log.Warn("refusing a request", "pid", c.peer.PID, "err", err)
			s.nak(c)
			continue
		case errors.Is(err, io.EOF), errors.Is(err, unix.ECONNRESET):
			return
		default:
			s.log.Warn("dropping a connection", "pid", c.peer.PID, "err", err)
			return
		}
		if c.peer.UID != s.owner {
			s.log.Warn("refusing a request from a foreign user", "pid", c.peer.PID, "uid", c.peer.UID, "command", string(rune(req.Command())))
			s.nak(c)
			continue
		}
		s.handler.Request(&Call{Request: req, conn: c})
	}
}

func (s *Server) close(c *net.UnixConn, pid int32) {
	if err := c.Close(); err != nil {
		s.log.Warn("closing a connection", "pid", pid, "err", err)
	}
}

func (s *Server) nak(c *Conn) {
	if err := c.send(protocol.Nak{}); err != nil {
		s.log.Warn("answering NAK", "pid", c.peer.PID, "err", err)
	}
}

// Close stops accepting, hangs up every connection and waits until each
// connection's HangUp has been delivered. Closing again does nothing.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	var errs []error
	if err := s.ln.Close(); err != nil {
		errs = append(errs, fmt.Errorf("server: close %s: %w", s.ln.Addr(), err))
	}
	for c := range s.conns {
		if err := c.c.CloseRead(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("server: hang up pid %d: %w", c.peer.PID, err))
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	return errors.Join(errs...)
}
