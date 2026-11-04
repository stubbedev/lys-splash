package daemon

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/theme"
)

type quietScreen struct {
	frame    kms.Frame
	frames   int
	presents int
}

func (s *quietScreen) Frame() kms.Frame {
	s.frames++
	return s.frame
}

func (s *quietScreen) Scale() int { return 1 }

func (s *quietScreen) Present(kms.Damage) error {
	s.presents++
	return nil
}

func newQuietDaemon(t *testing.T, th theme.Theme) (*daemon, *quietScreen) {
	t.Helper()
	size := image.Pt(1920, 1080)
	s := &quietScreen{frame: kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}}
	r, err := theme.NewRenderer(th, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{
		cfg:      Config{Clock: SystemClock{}, Log: slog.New(slog.DiscardHandler)},
		display:  &fakeDisplay{screens: []Screen{s}},
		renderer: r,
		drawn:    map[Screen]drawnScreen{},
	}
	if err := d.redraw(); err != nil {
		t.Fatal(err)
	}
	return d, s
}

func TestARedrawAllocatesNothing(t *testing.T) {
	for _, throbber := range []theme.Throbber{theme.Spinner, theme.Dots} {
		th := theme.Default()
		th.Throbber = throbber
		d, _ := newQuietDaemon(t, th)
		step := func() {
			d.state.Phase = (d.state.Phase + 1) % theme.Frames
			if err := d.redraw(); err != nil {
				t.Fatal(err)
			}
		}
		for range theme.Frames {
			step()
		}
		if n := testing.AllocsPerRun(theme.Frames, step); n != 0 {
			t.Errorf("throbber %v: %v allocations per tick", throbber, n)
		}
		if n := testing.AllocsPerRun(100, func() {
			if err := d.redraw(); err != nil {
				t.Fatal(err)
			}
		}); n != 0 {
			t.Errorf("throbber %v: %v allocations per redraw of an unchanged state", throbber, n)
		}
	}
}

type flippingDisplay struct {
	fakeDisplay
	events  chan kms.Event
	handled chan struct{}
	changed atomic.Bool
}

func (d *flippingDisplay) Events() <-chan kms.Event { return d.events }

func (d *flippingDisplay) Handle(kms.Event) (bool, error) {
	d.handled <- struct{}{}
	return d.changed.Load(), nil
}

func TestAFlipDrawsNothing(t *testing.T) {
	size := image.Pt(320, 240)
	s := &quietScreen{frame: kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}}
	display := &flippingDisplay{events: make(chan kms.Event, 1), handled: make(chan struct{}, 1)}
	display.screens = []Screen{s}
	r, err := theme.NewRenderer(theme.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{
		cfg:      Config{Clock: SystemClock{}, Log: slog.New(slog.DiscardHandler)},
		display:  display,
		renderer: r,
		drawn:    map[Screen]drawnScreen{},
		inbox:    make(chan event, 1),
		done:     make(chan struct{}),
		paused:   true,
		timer:    SystemClock{}.NewTimer(),
	}
	if err := d.redraw(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.loop(ctx) }()
	await := func() {
		t.Helper()
		select {
		case <-display.handled:
		case <-time.After(5 * time.Second):
			t.Fatal("the loop did not handle the event")
		}
	}
	for range 100 {
		display.events <- kms.Event{}
		await()
	}
	display.changed.Store(true)
	display.events <- kms.Event{}
	await()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop")
	}
	if s.frames != 2 {
		t.Errorf("%d draws: the first frame, nothing for 100 flips and one for the change is 2", s.frames)
	}
}

type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	armed chan time.Duration
	c     chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1000, 0), armed: make(chan time.Duration, 1), c: make(chan time.Time, 1)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer() Timer { return c }

func (c *fakeClock) C() <-chan time.Time { return c.c }

func (c *fakeClock) Reset(d time.Duration) { c.armed <- d }

func (c *fakeClock) Stop() {}

func (c *fakeClock) fire(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.armed:
		c.mu.Lock()
		c.now = c.now.Add(d)
		now := c.now
		c.mu.Unlock()
		c.c <- now
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the loop armed no timer")
		return 0
	}
}

func TestTheLoopWakesOnlyForAPictureThatChanges(t *testing.T) {
	const turn = theme.Frames * FrameInterval
	tests := []struct {
		name     string
		throbber theme.Throbber
		previous time.Duration
		wakes    int
	}{
		{"dots", theme.Dots, 0, 3},
		{"spinner", theme.Spinner, 0, theme.Frames},
		{"dots with no percent in the turn", theme.Dots, 100*turn + 50*FrameInterval, 3},
		{"dots with four percents in the turn", theme.Dots, 20*turn + 10*FrameInterval, 3 + 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th := theme.Default()
			th.Throbber = tc.throbber
			clock := newFakeClock()
			r, err := theme.NewRenderer(th, nil)
			if err != nil {
				t.Fatal(err)
			}
			size := image.Pt(320, 240)
			s := &quietScreen{frame: kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}}
			d := &daemon{
				cfg:      Config{Clock: clock, Log: slog.New(slog.DiscardHandler)},
				display:  &fakeDisplay{screens: []Screen{s}},
				renderer: r,
				drawn:    map[Screen]drawnScreen{},
				inbox:    make(chan event, 1),
				done:     make(chan struct{}),
				timer:    clock,
				started:  clock.Now(),
				previous: tc.previous,
			}
			d.state.Booting = tc.previous > 0
			if err := d.redraw(); err != nil {
				t.Fatal(err)
			}
			first := s.presents
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- d.loop(ctx) }()
			wakes := 0
			for elapsed := time.Duration(0); elapsed < turn; wakes++ {
				elapsed += clock.fire(t)
			}
			<-clock.armed
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop did not stop")
			}
			if wakes != tc.wakes {
				t.Errorf("%d wakes in a turn, want %d", wakes, tc.wakes)
			}
			if got := s.presents - first; got != wakes {
				t.Errorf("%d presents for %d wakes: a wake without a new picture is wasted", got, wakes)
			}
		})
	}
}

type failingScreen struct {
	quietScreen
	err error
}

func (s *failingScreen) Present(kms.Damage) error {
	s.presents++
	return s.err
}

type countingHandler struct {
	slog.Handler
	records *atomic.Int32
}

func (countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h countingHandler) Handle(context.Context, slog.Record) error {
	h.records.Add(1)
	return nil
}

func TestAFailingScreenBacksOffAndLogsOnce(t *testing.T) {
	for name, paused := range map[string]bool{"animating": false, "still": true} {
		t.Run(name, func(t *testing.T) {
			th := theme.Default()
			clock := newFakeClock()
			r, err := theme.NewRenderer(th, nil)
			if err != nil {
				t.Fatal(err)
			}
			size := image.Pt(320, 240)
			s := &failingScreen{err: errors.New("commit refused")}
			s.frame = kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}
			var records atomic.Int32
			d := &daemon{
				cfg:      Config{Clock: clock, Log: slog.New(countingHandler{slog.DiscardHandler, &records})},
				display:  &fakeDisplay{screens: []Screen{s}},
				renderer: r,
				drawn:    map[Screen]drawnScreen{},
				inbox:    make(chan event, 1),
				done:     make(chan struct{}),
				timer:    clock,
				started:  clock.Now(),
				paused:   paused,
			}
			d.stale = true
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- d.loop(ctx) }()
			d.post(event{previous: &previousBoot{err: errors.New("first event")}})
			const minute = time.Minute
			for elapsed := time.Duration(0); elapsed < minute; {
				elapsed += clock.fire(t)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop did not stop")
			}
			if got := records.Load(); got != 2 {
				t.Errorf("%d log records in a minute of failing commits, want the failure once beside the first event", got)
			}
			wantAtMost := 7 + int(minute/lastPresentRetry)
			if s.presents > wantAtMost || s.presents < int(minute/lastPresentRetry) {
				t.Errorf("%d commits tried in a minute, want about one per %v and at most %d", s.presents, lastPresentRetry, wantAtMost)
			}
		})
	}
}

func TestAFrameBackOffEndsWhenAPresentSucceeds(t *testing.T) {
	d, s := newQuietDaemon(t, theme.Default())
	failing := &failingScreen{err: errors.New("commit refused")}
	failing.frame = s.frame
	clock := newFakeClock()
	d.cfg.Clock = clock
	d.display = &fakeDisplay{screens: []Screen{failing}}
	if err := d.redraw(); err == nil {
		t.Fatal("the first failure was not reported")
	}
	first := failing.presents
	if d.retryAt.IsZero() {
		t.Fatal("a failing screen has no retry")
	}
	if err := d.redraw(); err != nil || failing.presents != first {
		t.Errorf("before its retry: %v, %d presents after %d", err, failing.presents, first)
	}
	clock.mu.Lock()
	clock.now = d.retryAt
	clock.mu.Unlock()
	if err := d.redraw(); err != nil || failing.presents != first+1 {
		t.Errorf("a repeated failure: %v, %d presents after %d", err, failing.presents, first)
	}
	clock.mu.Lock()
	clock.now = d.retryAt
	clock.mu.Unlock()
	failing.err = nil
	if err := d.redraw(); err != nil || failing.presents != first+2 {
		t.Errorf("the retry that works: %v, %d presents after %d", err, failing.presents, first)
	}
	if !d.retryAt.IsZero() {
		t.Error("a screen that works still has a retry")
	}
	if err := d.redraw(); err != nil || failing.presents != first+2 {
		t.Errorf("an unchanged state after recovering: %v, %d presents", err, failing.presents)
	}
}
