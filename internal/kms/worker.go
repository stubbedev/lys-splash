package kms

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// A card another process is master of is tried again for this long, at first
// soon and then less and less often: a display manager's service can stop
// before the compositor in its session lets the card go.
const (
	firstBusyRetry = 20 * time.Millisecond
	lastBusyRetry  = 250 * time.Millisecond
	busyRetryFor   = 10 * time.Second
)

// clock is where a card's worker reads the time and waits.
type clock struct {
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

var systemClock = clock{now: time.Now, after: time.After}

// litCard is a card its worker opened, for the loop to take up, with the outputs
// lit on it unless it was opened deactivated.
type litCard struct {
	card   *Card
	reader *os.File
	scan   scanResult
	gen    uint64
}

// cardWorker does the slow half of one card node off the frame loop: opening it,
// probing its connectors, which reads EDIDs, and building and freeing outputs'
// buffers. Results reach the loop as Events and only the loop changes what the
// display shows. It owns the card's connector, CRTC and plane bookkeeping; the
// loop owns the lit outputs and asks by setting the fields under mu.
type cardWorker struct {
	d    *Display
	name string
	wake chan struct{}

	// gen is the loop's: it counts removals, so a card lit before one is told
	// from a card lit after it.
	gen uint64

	mu      sync.Mutex
	want    cardJobs
	paused  bool
	current uint64

	card     *Card
	reader   *os.File
	lighting bool
	until    time.Time
	wait     time.Duration
	retry    <-chan time.Time
}

type cardJobs struct {
	open    bool
	until   time.Time
	remove  bool
	rescan  bool
	retired []*Output
}

func (d *Display) worker(name string) *cardWorker {
	w, ok := d.workers[name]
	if !ok {
		w = d.newWorker(name)
		d.workersDone.Go(w.run)
	}
	return w
}

func (d *Display) newWorker(name string) *cardWorker {
	w := &cardWorker{d: d, name: name, wake: make(chan struct{}, 1), paused: d.paused}
	d.workers[name] = w
	return w
}

func (w *cardWorker) ask(set func(*cardJobs)) {
	w.mu.Lock()
	set(&w.want)
	w.mu.Unlock()
	w.poke()
}

func (w *cardWorker) setPaused(paused bool) {
	w.mu.Lock()
	w.paused = paused
	w.mu.Unlock()
	w.poke()
}

func (w *cardWorker) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// removed tells the worker the card went away: what it was asked before does not
// matter any more, and a card it is lighting now belongs to an older generation.
func (w *cardWorker) removed() {
	w.gen++
	w.mu.Lock()
	w.current = w.gen
	w.want.open, w.want.rescan, w.want.remove = false, false, true
	w.mu.Unlock()
	w.poke()
}

func (w *cardWorker) take() (cardJobs, bool, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	jobs := w.want
	w.want = cardJobs{}
	return jobs, w.paused, w.current
}

func (w *cardWorker) run() {
	for {
		attempt := false
		select {
		case <-w.d.done:
			return
		case <-w.wake:
		case <-w.retry:
			w.retry, attempt = nil, true
		}
		jobs, paused, gen := w.take()
		w.work(jobs, paused, gen, attempt)
	}
}

func (w *cardWorker) work(jobs cardJobs, paused bool, gen uint64, attempt bool) {
	if jobs.remove {
		w.lighting, w.retry = false, nil
		if err := w.closeCard(false); err != nil {
			w.d.send(Event{err: err})
		}
	}
	if w.card != nil {
		if err := w.card.releaseRetired(jobs.retired); err != nil {
			w.d.send(Event{err: err})
		}
	}
	if paused {
		w.lighting, w.retry = false, nil
	}
	if jobs.open {
		w.lighting, w.until, w.wait, attempt = true, jobs.until, firstBusyRetry, true
	}
	if w.lighting && attempt {
		if lit := w.attempt(paused, gen); lit != nil {
			w.d.send(Event{lit: lit})
			return
		}
	}
	if jobs.rescan && w.card != nil && !paused {
		scan := w.card.probe()
		w.d.send(Event{scan: &scan})
	}
}

// attempt tries the card once and returns it if it lit. A card another master
// holds is tried again later, unless the display is deactivated or it has been
// held for too long; anything else gives up on it.
func (w *cardWorker) attempt(paused bool, gen uint64) *litCard {
	if w.card != nil {
		w.lighting = false
		return nil
	}
	c, r, err := w.d.openNode(w.name)
	if err == nil {
		w.lighting, w.card, w.reader = false, c, r
		lit := &litCard{card: c, reader: r, gen: gen}
		if !paused {
			lit.scan = c.probe()
		}
		return lit
	}
	now := w.d.clock.now()
	if !errors.Is(err, unix.EBUSY) || paused || !now.Before(w.until) {
		w.lighting = false
		w.d.log.Warn("lighting a card", "card", w.name, "err", err)
		return nil
	}
	w.retry = w.d.clock.after(w.wait)
	w.wait = min(2*w.wait, lastBusyRetry)
	return nil
}

func (w *cardWorker) closeCard(retain bool) error {
	if w.card == nil {
		return nil
	}
	c, r := w.card, w.reader
	w.card, w.reader = nil, nil
	return errors.Join(r.Close(), c.Release(retain), c.dev.Close())
}

// openNode opens the card node, takes master and readies its page-flip reader.
func (d *Display) openNode(name string) (*Card, *os.File, error) {
	firmware, err := d.firmware(name)
	if err != nil {
		return nil, nil, err
	}
	dev, err := d.open(filepath.Join(d.paths.Dev, name))
	if err != nil {
		return nil, nil, err
	}
	c, err := openCard(dev, name, firmware, &d.scaler, d.log)
	if err != nil {
		return nil, nil, errors.Join(err, dev.Close())
	}
	r, err := eventReader(dev.FD(), dev.Path())
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("kms: read the events of %s: %w", dev.Path(), err), dev.Close())
	}
	return c, r, nil
}
