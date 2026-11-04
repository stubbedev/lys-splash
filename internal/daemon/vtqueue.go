package daemon

import (
	"log/slog"
	"sync"
	"time"
)

const (
	// maxPendingVTBytes bounds the text waiting for a VT that stopped taking any,
	// as one held with Ctrl-S or scroll lock does.
	maxPendingVTBytes = 256 << 10
	// maxPendingVTOps bounds what else waits, which only keys and requests add.
	maxPendingVTOps = 1024
	vtDrainWait     = 2 * time.Second
)

// vtQueue runs everything the loop does to the VT in order on one goroutine: a
// write blocks on a stopped terminal, and setting its mode waits for the console
// lock while the kernel prints to a slow serial console. It drops text past its
// bound, since the scrollback keeps it and Esc shows it again, and says so.
type vtQueue struct {
	vt  Terminal
	log *slog.Logger

	mu      sync.Mutex
	ops     []func(Terminal) error
	bytes   int
	dropped int
	closed  bool

	wake chan struct{}
	done chan struct{}
}

func newVTQueue(vt Terminal, log *slog.Logger) *vtQueue {
	q := &vtQueue{vt: vt, log: log, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go q.run()
	return q
}

// do queues op, or drops it past the bound.
func (q *vtQueue) do(op func(Terminal) error) {
	q.mu.Lock()
	if q.closed || len(q.ops) >= maxPendingVTOps {
		q.dropped++
		q.mu.Unlock()
		return
	}
	q.ops = append(q.ops, op)
	q.mu.Unlock()
	q.poke()
}

// Write queues a copy of b; it never waits and never fails.
func (q *vtQueue) Write(b []byte) (int, error) {
	n := len(b)
	q.mu.Lock()
	if q.closed || q.bytes+n > maxPendingVTBytes || len(q.ops) >= maxPendingVTOps {
		q.dropped++
		q.mu.Unlock()
		return n, nil
	}
	own := append([]byte(nil), b...)
	q.bytes += n
	q.ops = append(q.ops, func(vt Terminal) error {
		q.mu.Lock()
		q.bytes -= n
		q.mu.Unlock()
		_, err := vt.Write(own)
		return err
	})
	q.mu.Unlock()
	q.poke()
	return n, nil
}

func (q *vtQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *vtQueue) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		ops, dropped, closed := q.ops, q.dropped, q.closed
		q.ops, q.dropped = nil, 0
		q.mu.Unlock()
		if dropped > 0 {
			q.log.Warn("the VT stopped taking what it was sent, so some of it was dropped", "writes", dropped)
		}
		for _, op := range ops {
			if err := op(q.vt); err != nil {
				q.log.Warn("using the VT", "err", err)
			}
		}
		if len(ops) > 0 {
			continue
		}
		if closed {
			return
		}
		<-q.wake
	}
}

// drain runs what is queued and ends the queue, giving up when timeout fires so
// a stopped terminal cannot hold the quit.
func (q *vtQueue) drain(timeout <-chan time.Time) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.poke()
	select {
	case <-q.done:
	case <-timeout:
		q.log.Warn("the VT stopped taking what it was sent, so the quit goes on without it")
	}
}
