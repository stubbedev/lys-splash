// Package kmsg logs to the kernel's log buffer, the one place a daemon with no
// terminal and no journald yet can reach from the initramfs on. journald reads
// the buffer once it runs, under the identifier the records carry.
package kmsg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"unicode/utf8"
)

// Path is the device the kernel takes log records from.
const Path = "/dev/kmsg"

const (
	identifier     = "lys-splash"
	facilityDaemon = 3 << 3
	maxRecord      = 976

	severityError = 3
	severityWarn  = 4
	severityInfo  = 6
	severityDebug = 7
)

type sink struct {
	mu     sync.Mutex
	w      io.Writer
	record bytes.Buffer
	prefix string
}

// Handler writes each slog record as one kernel log record, with its severity
// in the record's priority and without the time the kernel stamps on it.
type Handler struct {
	text slog.Handler
	sink *sink
}

// NewHandler logs at level and above to w, naming pid as the logging process.
// Each record reaches w in a single Write, as /dev/kmsg needs.
func NewHandler(w io.Writer, pid int, level slog.Leveler) *Handler {
	s := &sink{w: w, prefix: fmt.Sprintf("%s[%d]: ", identifier, pid)}
	return &Handler{
		sink: s,
		text: slog.NewTextHandler(&s.record, &slog.HandlerOptions{Level: level, ReplaceAttr: dropTime}),
	}
}

func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func severity(l slog.Level) int {
	switch {
	case l >= slog.LevelError:
		return severityError
	case l >= slog.LevelWarn:
		return severityWarn
	case l >= slog.LevelInfo:
		return severityInfo
	}
	return severityDebug
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.text.Enabled(ctx, l)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.record.Reset()
	fmt.Fprintf(&h.sink.record, "<%d>%s", facilityDaemon|severity(r.Level), h.sink.prefix)
	if err := h.text.Handle(ctx, r); err != nil {
		return err
	}
	_, err := h.sink.w.Write(fitRecord(h.sink.record.Bytes()))
	return err
}

func fitRecord(b []byte) []byte {
	if len(b) <= maxRecord {
		return b
	}
	end := maxRecord - 1
	for end > 0 && !utf8.RuneStart(b[end]) {
		end--
	}
	b = b[:end+1]
	b[end] = '\n'
	return b
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{text: h.text.WithAttrs(attrs), sink: h.sink}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{text: h.text.WithGroup(name), sink: h.sink}
}
