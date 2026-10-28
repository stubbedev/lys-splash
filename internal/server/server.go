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

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

const writeTimeout = 5 * time.Second

// Peer is the process at the other end of a connection, as SO_PEERCRED
// reports it when the connection is accepted.
type Peer struct {
	PID int32
	UID uint32
}

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

// Conn is one client connection.
type Conn struct {
	c    *net.UnixConn
	peer Peer

	mu   sync.Mutex
	gone bool
	out  []byte
}

// Peer is the process that opened the connection.
func (c *Conn) Peer() Peer {
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

// Reply answers the call. A call is answered once; a second Reply fails.
// When the client has already hung up the answer is dropped without error.
// The encoded bytes are zeroed once written, since an answer may be a secret.
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
	out, err := protocol.AppendResponse(c.out[:0], r)
	defer clear(out)
	c.out = out
	if err != nil {
		return fmt.Errorf("server: encode a %#02x response for pid %d: %w", byte(r.Type()), c.peer.PID, err)
	}
	if err := c.c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("server: set the write deadline for pid %d: %w", c.peer.PID, err)
	}
	if _, err := c.c.Write(out); err != nil {
		if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
			c.gone = true
			return nil
		}
		return fmt.Errorf("server: write a %#02x response to pid %d: %w", byte(r.Type()), c.peer.PID, err)
	}
	return nil
}

func (c *Conn) hangUp() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gone = true
	clear(c.out)
}

// Server accepts connections and dispatches their requests.
type Server struct {
	ln      *net.UnixListener
	owner   uint32
	handler Handler
	log     *slog.Logger

	mu     sync.Mutex
	conns  map[*Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Listen listens on the Unix socket name, "@" leading an abstract one as in
// net.Listen. Only requests from processes running as uid owner reach h;
// every other peer is answered NAK, as plymouthd answers non-root peers.
func Listen(name string, owner uint32, h Handler, log *slog.Logger) (*Server, error) {
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("server: listen on %s: %w", name, err)
	}
	return &Server{ln: ln, owner: owner, handler: h, log: log, conns: map[*Conn]struct{}{}}, nil
}

// Serve accepts connections until Close, then returns nil.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.AcceptUnix()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("server: accept on %s: %w", s.ln.Addr(), err)
		}
		peer, err := peerOf(c)
		if err != nil {
			s.log.Warn("dropping a connection whose peer is unknown", "err", err)
			s.close(c, 0)
			continue
		}
		conn := &Conn{c: c, peer: peer}
		if !s.track(conn) {
			s.close(c, peer.PID)
			return nil
		}
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

func peerOf(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, fmt.Errorf("server: the raw connection: %w", err)
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, fmt.Errorf("server: reach the socket: %w", err)
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("server: SO_PEERCRED: %w", credErr)
	}
	return Peer{PID: cred.Pid, UID: cred.Uid}, nil
}

func (s *Server) serve(c *Conn) {
	defer s.wg.Done()
	defer func() {
		c.hangUp()
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
