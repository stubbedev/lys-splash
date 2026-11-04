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

// A card another process is master of is tried again for this long. No event
// says master was dropped, but a compositor that exits closes the card, which
// inotify reports at once; the retries that back off cover a master dropped
// without a close: a display manager's service can stop before the compositor
// in its session lets the card go.
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
	// waiting is the node opened while another process is master of it, kept
	// open so that only a master letting go or a timer costs an ioctl, and so
	// that trying again closes nothing the watch would report.
	waiting  device
	firmware bool
	closes   *closeWatch
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
		case <-w.closes.events():
			attempt = true
		}
		jobs, paused, gen := w.take()
		w.work(jobs, paused, gen, attempt)
	}
}

func (w *cardWorker) work(jobs cardJobs, paused bool, gen uint64, attempt bool) {
	if jobs.remove {
		w.stopLighting()
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
		w.stopLighting()
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

// stopLighting gives up on a card it was waiting to light.
func (w *cardWorker) stopLighting() {
	w.lighting, w.retry = false, nil
	w.closes.stop()
	w.closes = nil
	if w.waiting != nil {
		if err := w.waiting.Close(); err != nil {
			w.d.log.Warn("closing a busy card", "card", w.name, "err", err)
		}
		w.waiting = nil
	}
}

// attempt tries the card once and returns it if it lit. A card another master
// holds is tried again later, unless the display is deactivated or it has been
// held for too long; anything else gives up on it.
func (w *cardWorker) attempt(paused bool, gen uint64) *litCard {
	if w.card != nil {
		w.stopLighting()
		return nil
	}
	c, r, err := w.openNode()
	if err == nil {
		w.waiting = nil
		w.stopLighting()
		w.card, w.reader = c, r
		lit := &litCard{card: c, reader: r, gen: gen}
		if !paused {
			lit.scan = c.probe()
		}
		return lit
	}
	if !errors.Is(err, unix.EBUSY) || paused || !w.d.clock.now().Before(w.until) {
		w.stopLighting()
		w.d.log.Warn("lighting a card", "card", w.name, "err", err)
		return nil
	}
	w.watchCloses()
	w.retry = w.d.clock.after(w.wait)
	w.wait = min(2*w.wait, lastBusyRetry)
	return nil
}

func (w *cardWorker) watchCloses() {
	if w.closes != nil {
		return
	}
	watch, err := newCloseWatch(filepath.Join(w.d.paths.Dev, w.name))
	if err != nil {
		w.d.log.Info("a busy card is only retried on a timer", "card", w.name, "err", err)
		return
	}
	w.closes = watch
}

func (w *cardWorker) closeCard(retain bool) error {
	w.stopLighting()
	if w.card == nil {
		return nil
	}
	c, r := w.card, w.reader
	w.card, w.reader = nil, nil
	return errors.Join(r.Close(), c.Release(retain), c.dev.Close())
}

// openNode opens the card node unless it is waiting on it, takes master and
// readies its page-flip reader. A node another process is master of stays open
// in waiting, and the error says EBUSY.
func (w *cardWorker) openNode() (*Card, *os.File, error) {
	if w.waiting == nil {
		dev, firmware, err := w.d.prepareNode(w.name)
		if err != nil {
			return nil, nil, err
		}
		w.waiting, w.firmware = dev, firmware
	}
	dev := w.waiting
	if err := takeMaster(dev); err != nil {
		return nil, nil, err
	}
	c, err := newCard(dev, w.name, w.firmware, &w.d.scaler, w.d.log)
	if err != nil {
		return nil, nil, err
	}
	r, err := eventReader(dev.FD(), dev.Path())
	if err != nil {
		return nil, nil, fmt.Errorf("kms: read the events of %s: %w", dev.Path(), err)
	}
	return c, r, nil
}

func (d *Display) prepareNode(name string) (device, bool, error) {
	firmware, err := d.firmware(name)
	if err != nil {
		return nil, false, err
	}
	dev, err := d.open(filepath.Join(d.paths.Dev, name))
	if err != nil {
		return nil, false, err
	}
	if err := atomicCaps(dev); err != nil {
		return nil, false, errors.Join(err, dev.Close())
	}
	return dev, firmware, nil
}

// closeWatch reports someone closing a file opened for writing, which is how a
// master that exits lets go of a card.
type closeWatch struct {
	f     *os.File
	fired chan struct{}
	ended chan struct{}
}

func newCloseWatch(path string) (*closeWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("kms: inotify for %s: %w", path, err)
	}
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_CLOSE_WRITE); err != nil {
		return nil, errors.Join(fmt.Errorf("kms: watch %s: %w", path, err), unix.Close(fd))
	}
	c := &closeWatch{f: os.NewFile(uintptr(fd), path), fired: make(chan struct{}, 1), ended: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *closeWatch) read() {
	defer close(c.ended)
	buf := make([]byte, 4096)
	for {
		if _, err := c.f.Read(buf); err != nil {
			return
		}
		select {
		case c.fired <- struct{}{}:
		default:
		}
	}
}

// events is nil, which never delivers, without a watch.
func (c *closeWatch) events() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.fired
}

func (c *closeWatch) stop() {
	if c == nil {
		return
	}
	_ = c.f.Close()
	<-c.ended
}
