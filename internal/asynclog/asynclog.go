// Package asynclog gives a slog.Handler to code that must not wait for where
// the log goes: /dev/kmsg blocks while the kernel console flushes, and a pipe
// while its reader sleeps. Records go through a bounded queue to one goroutine;
// a full queue drops records and the next one written says how many.
package asynclog

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const queueLength = 256

type job struct {
	handler slog.Handler
	record  slog.Record
	flushed chan struct{}
}

type queue struct {
	jobs    chan job
	dropped atomic.Uint64
	base    slog.Handler
}

// Handler hands records to the handler it wraps, on another goroutine.
type Handler struct {
	inner slog.Handler
	q     *queue
}

// New starts the goroutine that writes to inner for the life of the process.
func New(inner slog.Handler) *Handler {
	q := &queue{jobs: make(chan job, queueLength), base: inner}
	go q.run()
	return &Handler{inner: inner, q: q}
}

func (q *queue) run() {
	ctx := context.Background()
	for j := range q.jobs {
		if n := q.dropped.Swap(0); n > 0 {
			lost := slog.NewRecord(time.Now(), slog.LevelWarn, "the log fell behind and dropped records", 0)
			lost.AddAttrs(slog.Uint64("records", n))
			_ = q.base.Handle(ctx, lost)
		}
		if j.flushed != nil {
			close(j.flushed)
			continue
		}
		_ = j.handler.Handle(ctx, j.record)
	}
}

// Enabled asks the wrapped handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle queues r and never waits; a full queue drops it.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.q.jobs <- job{handler: h.inner, record: r.Clone()}:
	default:
		h.q.dropped.Add(1)
	}
	return nil
}

// WithAttrs is the wrapped handler with attrs, on the same queue.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), q: h.q}
}

// WithGroup is the wrapped handler in a group, on the same queue.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), q: h.q}
}

// Flush waits until what was queued before it is written, or timeout fires.
func (h *Handler) Flush(timeout <-chan time.Time) {
	flushed := make(chan struct{})
	select {
	case h.q.jobs <- job{flushed: flushed}:
	case <-timeout:
		return
	}
	select {
	case <-flushed:
	case <-timeout:
	}
}
