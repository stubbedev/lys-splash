package asynclog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type gated struct {
	slog.Handler
	mu      *sync.Mutex
	out     *bytes.Buffer
	entered chan struct{}
	release chan struct{}
}

func (g gated) Handle(ctx context.Context, r slog.Record) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Handler.Handle(ctx, r)
}

func (g gated) WithAttrs(attrs []slog.Attr) slog.Handler {
	g.Handler = g.Handler.WithAttrs(attrs)
	return g
}

func newGated() (gated, func() string) {
	var mu sync.Mutex
	var out bytes.Buffer
	g := gated{Handler: slog.NewTextHandler(&out, nil), mu: &mu, out: &out, entered: make(chan struct{}, 1), release: make(chan struct{})}
	return g, func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
}

func TestABlockedLogHoldsNoCaller(t *testing.T) {
	inner, written := newGated()
	h := New(inner)
	log := slog.New(h)
	log.Warn("first")
	select {
	case <-inner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never started")
	}
	const records = 10 * queueLength
	for range records {
		log.Warn("flood")
	}
	close(inner.release)
	h.Flush(time.After(5 * time.Second))
	out := written()
	if !strings.Contains(out, "first") {
		t.Errorf("the first record was lost: %s", out)
	}
	if !strings.Contains(out, "dropped records") {
		t.Errorf("records dropped without saying so: %s", out)
	}
	if got := strings.Count(out, "flood"); got > queueLength {
		t.Errorf("%d of a queue of %d records written", got, queueLength)
	}
}

func TestRecordsKeepTheirOrderAttrsAndContents(t *testing.T) {
	inner, written := newGated()
	close(inner.release)
	h := New(inner)
	log := slog.New(h).With("card", "card0")
	for _, m := range []string{"one", "two", "three"} {
		log.Info(m)
	}
	h.Flush(time.After(5 * time.Second))
	out := written()
	if i, j, k := strings.Index(out, "one"), strings.Index(out, "two"), strings.Index(out, "three"); i < 0 || i >= j || j >= k {
		t.Errorf("records out of order: %s", out)
	}
	if strings.Count(out, "card=card0") != 3 {
		t.Errorf("attrs lost: %s", out)
	}
}

func TestAFlushGivesUpOnALogThatNeverReturns(t *testing.T) {
	inner, _ := newGated()
	defer close(inner.release)
	h := New(inner)
	slog.New(h).Warn("stuck")
	timeout := make(chan time.Time)
	close(timeout)
	h.Flush(timeout)
}
