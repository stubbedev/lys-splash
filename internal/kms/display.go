package kms

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"
)

// firmwareDrivers are the platform drivers of the DRM drivers that show the
// firmware's framebuffer until the native driver takes the GPU over:
// simpledrm, efidrm, vesadrm and ofdrm.
var firmwareDrivers = []string{"simple-framebuffer", "efi-framebuffer", "vesa-framebuffer", "of-display"}

// driversRemovedWithClients are the drivers whose PCI shutdown removes the DRM
// device and frees its state, so a client that closes its card afterwards
// oopses the kernel and the machine never reaches the reset.
var driversRemovedWithClients = []string{"nvidia"}

// Paths are where the display finds card nodes and their sysfs entries.
type Paths struct {
	Dev   string
	Sysfs string
}

// SystemPaths are the kernel's: /dev/dri and /sys/class/drm.
var SystemPaths = Paths{Dev: "/dev/dri", Sysfs: "/sys/class/drm"}

// Event is something that happened to the display: a card's page flip, a
// uevent, something failing off the loop, or a card worker's result.
// Display.Handle applies it.
type Event struct {
	card   *Card
	flip   drm.FlipEvent
	uevent []byte
	err    error
	lit    *litCard
	scan   *scanResult
}

// Display is every DRM card's outputs. It is not safe for concurrent use:
// one goroutine opens it, handles its events and draws, while a worker per card
// does whatever could keep that goroutine waiting.
type Display struct {
	paths       Paths
	open        func(path string) (device, error)
	log         *slog.Logger
	clock       clock
	scaler      Scaler
	cards       map[string]*Card
	workers     map[string]*cardWorker
	events      chan Event
	done        chan struct{}
	wg          sync.WaitGroup
	workersDone sync.WaitGroup
	uevents     *ueventSocket
	paused      bool
	outs        []*Output
}

// Open opens every card under paths and lights its outputs, then follows
// cards appearing and disappearing. A card that fails to open is logged and
// left out, unless another process is master of it, when it is tried again
// for a while; with none at all, Outputs is empty and the splash has no
// graphical output.
func Open(paths Paths, log *slog.Logger) (*Display, error) {
	return open(paths, func(path string) (device, error) { return drm.Open(path) }, true, systemClock, log)
}

func open(paths Paths, opener func(string) (device, error), uevents bool, clk clock, log *slog.Logger) (*Display, error) {
	d := &Display{
		paths:   paths,
		open:    opener,
		log:     log,
		clock:   clk,
		cards:   map[string]*Card{},
		workers: map[string]*cardWorker{},
		events:  make(chan Event, 16),
		done:    make(chan struct{}),
	}
	if uevents {
		s, err := openUevents()
		if err != nil {
			return nil, err
		}
		d.uevents = s
		d.wg.Go(d.readUevents)
	}
	entries, err := os.ReadDir(paths.Dev)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.Join(fmt.Errorf("kms: list %s: %w", paths.Dev, err), d.Close(false))
	}
	for _, e := range entries {
		if isCard(e.Name()) {
			d.lightNow(e.Name())
		}
	}
	return d, nil
}

func (p Paths) driver(name string) (string, error) {
	link, err := os.Readlink(filepath.Join(p.Sysfs, name, "device", "driver"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kms: the driver of %s: %w", name, err)
	}
	return filepath.Base(link), nil
}

func (d *Display) firmware(name string) (bool, error) {
	driver, err := d.paths.driver(name)
	if err != nil {
		return false, err
	}
	return slices.Contains(firmwareDrivers, driver), nil
}

func (p Paths) ClientsMustCloseBeforeRemoval() (bool, error) {
	entries, err := os.ReadDir(p.Sysfs)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("kms: list %s: %w", p.Sysfs, err)
	}
	for _, e := range entries {
		if !isCard(e.Name()) {
			continue
		}
		driver, err := p.driver(e.Name())
		if err != nil {
			return false, err
		}
		if slices.Contains(driversRemovedWithClients, driver) {
			return true, nil
		}
	}
	return false, nil
}

// lightNow lights a card before the loop runs, so the first frame shows on it:
// nothing is waiting yet. A card another master holds goes on in its worker.
func (d *Display) lightNow(name string) {
	w := d.newWorker(name)
	w.lighting, w.until, w.wait = true, d.clock.now().Add(busyRetryFor), firstBusyRetry
	lit := w.attempt(false, 0)
	d.workersDone.Go(w.run)
	if lit == nil {
		return
	}
	if err := d.take(lit); err != nil {
		d.log.Warn("lighting a card", "card", name, "err", err)
	}
}

// take brings a card its worker opened into the display.
func (d *Display) take(lit *litCard) error {
	c := lit.card
	d.cards[c.name] = c
	d.wg.Go(func() { d.readFlips(c, lit.reader) })
	c.apply(lit.scan)
	d.outs = nil
	if d.paused {
		return errors.Join(lit.scan.err, c.Deactivate())
	}
	return lit.scan.err
}

func eventReader(fd int, path string) (*os.File, error) {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("kms: duplicate the fd of %s: %w", path, err)
	}
	if err := unix.SetNonblock(dup, true); err != nil {
		return nil, errors.Join(fmt.Errorf("kms: make %s non-blocking: %w", path, err), unix.Close(dup))
	}
	return os.NewFile(uintptr(dup), path), nil
}

func (d *Display) send(e Event) bool {
	select {
	case d.events <- e:
		return true
	case <-d.done:
		return false
	}
}

func (d *Display) readFlips(c *Card, r *os.File) {
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if errors.Is(err, os.ErrClosed) {
			return
		}
		if err != nil {
			d.send(Event{card: c, err: fmt.Errorf("kms: read the events of %s: %w", r.Name(), err)})
			return
		}
		if err := drm.ParseEvents(buf[:n], func(e drm.FlipEvent) error {
			if !d.send(Event{card: c, flip: e}) {
				return os.ErrClosed
			}
			return nil
		}); err != nil {
			d.send(Event{card: c, err: fmt.Errorf("kms: the events of %s: %w", r.Name(), err)})
			return
		}
	}
}

func (d *Display) readUevents() {
	for {
		msg, err := d.uevents.read()
		if errors.Is(err, os.ErrClosed) {
			return
		}
		if err != nil {
			d.send(Event{err: err})
			return
		}
		if !aboutDRM(msg) {
			continue
		}
		if !d.send(Event{uevent: slices.Clone(msg)}) {
			return
		}
	}
}

// Events delivers what Handle applies.
func (d *Display) Events() <-chan Event {
	return d.events
}

// Handle applies e and says whether Outputs changed: a flip lets its output
// show the next frame, a card that disappears is dropped at once, and what takes
// time, a card appearing or a hotplug, goes to the card's worker, whose result
// is another Event.
func (d *Display) Handle(e Event) (bool, error) {
	switch {
	case e.err != nil:
		return false, e.err
	case e.uevent != nil:
		return d.uevent(e.uevent)
	case e.lit != nil:
		if e.lit.gen != d.workers[e.lit.card.name].gen {
			return false, nil
		}
		return true, d.take(e.lit)
	case e.scan != nil:
		return d.scanned(e.scan)
	case e.card != nil:
		if d.cards[e.card.name] != e.card {
			return false, nil
		}
		return false, e.card.Flipped(e.flip)
	}
	return false, nil
}

func (d *Display) scanned(r *scanResult) (bool, error) {
	if d.cards[r.card.name] != r.card {
		return false, nil
	}
	r.card.apply(*r)
	d.outs = nil
	if len(r.dropped) > 0 {
		dropped := r.dropped
		d.workers[r.card.name].ask(func(j *cardJobs) { j.retired = append(j.retired, dropped...) })
	}
	return len(r.added)+len(r.dropped) > 0, r.err
}

func (d *Display) uevent(msg []byte) (bool, error) {
	u, err := ParseUevent(msg)
	if errors.Is(err, errUdevMessage) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	name, ok := u.Card()
	if !ok {
		return false, nil
	}
	switch u.Action {
	case Add:
		until := d.clock.now().Add(busyRetryFor)
		d.worker(name).ask(func(j *cardJobs) { j.open, j.until = true, until })
	case Remove:
		return d.remove(name), nil
	case Change:
		if _, ok := d.cards[name]; ok && u.Hotplug && !d.paused {
			d.worker(name).ask(func(j *cardJobs) { j.rescan = true })
		}
	}
	return false, nil
}

// remove forgets a card at once, so nothing more is drawn on it, and leaves its
// worker to free it. A card still being lit is told apart by the generation.
func (d *Display) remove(name string) bool {
	d.worker(name).removed()
	if _, ok := d.cards[name]; !ok {
		return false
	}
	delete(d.cards, name)
	d.outs = nil
	d.log.Info("a card went away", "card", name)
	return true
}

// Outputs are every lit output, ordered by card and connector. The list is
// built once per change, and a change gives a new list.
func (d *Display) Outputs() []*Output {
	if d.outs != nil {
		return d.outs
	}
	d.outs = []*Output{}
	for _, c := range d.sortedCards() {
		d.outs = append(d.outs, c.outputs...)
	}
	return d.outs
}

func (d *Display) sortedCards() []*Card {
	cards := make([]*Card, 0, len(d.cards))
	for _, c := range d.cards {
		cards = append(cards, c)
	}
	slices.SortFunc(cards, func(a, b *Card) int { return cmp.Compare(a.name, b.name) })
	return cards
}

// Deactivate drops DRM master on every card, leaving the frames on screen;
// cards that appear meanwhile are opened deactivated.
func (d *Display) Deactivate() error {
	d.paused = true
	for _, w := range d.workers {
		w.setPaused(true)
	}
	var errs []error
	for _, c := range d.sortedCards() {
		errs = append(errs, c.Deactivate())
	}
	return errors.Join(errs...)
}

// Reactivate takes DRM master back on every card and shows the splash again.
func (d *Display) Reactivate() error {
	d.paused = false
	for _, w := range d.workers {
		w.setPaused(false)
	}
	var errs []error
	for _, c := range d.sortedCards() {
		errs = append(errs, c.Reactivate())
	}
	for _, w := range d.workers {
		w.ask(func(j *cardJobs) { j.rescan = true })
	}
	return errors.Join(errs...)
}

// Close releases every card and stops following uevents. With retain, the
// frames on screen stay there after the process exits (see Card.Release).
func (d *Display) Close(retain bool) error {
	close(d.done)
	d.workersDone.Wait()
	var errs []error
	if d.uevents != nil {
		errs = append(errs, d.uevents.close())
	}
	names := slices.Sorted(maps.Keys(d.workers))
	for _, name := range names {
		errs = append(errs, d.workers[name].closeCard(retain))
	}
	clear(d.cards)
	d.outs = nil
	d.wg.Wait()
	return errors.Join(errs...)
}
