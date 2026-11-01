package kms

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"
)

// firmwareDrivers are the platform drivers of the DRM drivers that show the
// firmware's framebuffer until the native driver takes the GPU over:
// simpledrm, efidrm, vesadrm and ofdrm.
var firmwareDrivers = []string{"simple-framebuffer", "efi-framebuffer", "vesa-framebuffer", "of-display"}

// A card another process is master of is tried again this often for this
// long: a display manager's service can stop before the compositor in its
// session lets the card go.
const (
	busyRetryEvery = 20 * time.Millisecond
	busyRetryFor   = 10 * time.Second
)

// Paths are where the display finds card nodes and their sysfs entries.
type Paths struct {
	Dev   string
	Sysfs string
}

// SystemPaths are the kernel's: /dev/dri and /sys/class/drm.
var SystemPaths = Paths{Dev: "/dev/dri", Sysfs: "/sys/class/drm"}

// Event is something that happened to the display: a card's page flip, a
// uevent, a reader failing, or the time to try a busy card again.
// Display.Handle applies it.
type Event struct {
	card   *Card
	flip   drm.FlipEvent
	uevent []byte
	err    error
	busy   string
	until  time.Time
}

// Display is every DRM card's outputs. It is not safe for concurrent use:
// one goroutine opens it, handles its events and draws.
type Display struct {
	paths   Paths
	open    func(path string) (device, error)
	log     *slog.Logger
	scaler  Scaler
	cards   map[string]*Card
	readers map[*Card]*os.File
	events  chan Event
	done    chan struct{}
	wg      sync.WaitGroup
	uevents *ueventSocket
	paused  bool
}

// Open opens every card under paths and lights its outputs, then follows
// cards appearing and disappearing. A card that fails to open is logged and
// left out, unless another process is master of it, when it is tried again
// for a while; with none at all, Outputs is empty and the splash has no
// graphical output.
func Open(paths Paths, log *slog.Logger) (*Display, error) {
	return open(paths, func(path string) (device, error) { return drm.Open(path) }, true, log)
}

func open(paths Paths, opener func(string) (device, error), uevents bool, log *slog.Logger) (*Display, error) {
	d := &Display{
		paths:   paths,
		open:    opener,
		log:     log,
		cards:   map[string]*Card{},
		readers: map[*Card]*os.File{},
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
			d.add(e.Name())
		}
	}
	return d, nil
}

func (d *Display) firmware(name string) (bool, error) {
	link, err := os.Readlink(filepath.Join(d.paths.Sysfs, name, "device", "driver"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("kms: the driver of %s: %w", name, err)
	}
	return slices.Contains(firmwareDrivers, filepath.Base(link)), nil
}

func (d *Display) add(name string) {
	d.light(name, time.Now().Add(busyRetryFor))
}

func (d *Display) light(name string, until time.Time) {
	if _, ok := d.cards[name]; ok {
		return
	}
	err := d.tryAdd(name)
	if err == nil {
		return
	}
	if errors.Is(err, unix.EBUSY) && !d.paused && time.Now().Before(until) {
		d.wg.Go(func() { d.retryBusy(name, until) })
		return
	}
	d.log.Warn("lighting a card", "card", name, "err", err)
}

func (d *Display) retryBusy(name string, until time.Time) {
	select {
	case <-time.After(busyRetryEvery):
		d.send(Event{busy: name, until: until})
	case <-d.done:
	}
}

func (d *Display) tryAdd(name string) error {
	firmware, err := d.firmware(name)
	if err != nil {
		return err
	}
	dev, err := d.open(filepath.Join(d.paths.Dev, name))
	if err != nil {
		return err
	}
	c, err := openCard(dev, name, firmware, &d.scaler, d.log)
	if err != nil {
		return errors.Join(err, dev.Close())
	}
	r, err := eventReader(dev.FD(), dev.Path())
	if err != nil {
		return errors.Join(err, dev.Close())
	}
	d.cards[name] = c
	d.readers[c] = r
	d.wg.Go(func() { d.readFlips(c, r) })
	if d.paused {
		return c.Deactivate()
	}
	return c.Rescan()
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
		if !d.send(Event{uevent: slices.Clone(msg)}) {
			return
		}
	}
}

// Events delivers what Handle applies.
func (d *Display) Events() <-chan Event {
	return d.events
}

// Handle applies e: a flip lets its output show the next frame, a card that
// appears is opened and lit, one that disappears is dropped, a hotplug
// rescans its card's connectors and a busy card is tried again.
func (d *Display) Handle(e Event) error {
	switch {
	case e.err != nil:
		return e.err
	case e.uevent != nil:
		return d.uevent(e.uevent)
	case e.busy != "":
		d.light(e.busy, e.until)
		return nil
	case e.card != nil:
		if d.cards[e.card.name] != e.card {
			return nil
		}
		return e.card.Flipped(e.flip)
	}
	return nil
}

func (d *Display) uevent(msg []byte) error {
	u, err := ParseUevent(msg)
	if errors.Is(err, errUdevMessage) {
		return nil
	}
	if err != nil {
		return err
	}
	name, ok := u.Card()
	if !ok {
		return nil
	}
	switch u.Action {
	case Add:
		d.add(name)
	case Remove:
		return d.remove(name)
	case Change:
		if c, ok := d.cards[name]; ok && u.Hotplug && !d.paused {
			return c.Rescan()
		}
	}
	return nil
}

func (d *Display) remove(name string) error {
	c, ok := d.cards[name]
	if !ok {
		return nil
	}
	delete(d.cards, name)
	d.log.Info("a card went away", "card", name)
	return d.closeCard(c, false)
}

func (d *Display) closeCard(c *Card, retain bool) error {
	r := d.readers[c]
	delete(d.readers, c)
	return errors.Join(r.Close(), c.Release(retain), c.dev.Close())
}

// Outputs are every lit output, ordered by card and connector.
func (d *Display) Outputs() []*Output {
	var out []*Output
	for _, c := range d.sortedCards() {
		out = append(out, c.Outputs()...)
	}
	return out
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
	var errs []error
	for _, c := range d.sortedCards() {
		errs = append(errs, c.Deactivate())
	}
	return errors.Join(errs...)
}

// Reactivate takes DRM master back on every card and shows the splash again.
func (d *Display) Reactivate() error {
	d.paused = false
	var errs []error
	for _, c := range d.sortedCards() {
		errs = append(errs, c.Reactivate(), c.Rescan())
	}
	return errors.Join(errs...)
}

// Close releases every card and stops following uevents. With retain, the
// frames on screen stay there after the process exits (see Card.Release).
func (d *Display) Close(retain bool) error {
	close(d.done)
	var errs []error
	if d.uevents != nil {
		errs = append(errs, d.uevents.close())
	}
	for _, c := range d.sortedCards() {
		errs = append(errs, d.closeCard(c, retain))
	}
	clear(d.cards)
	d.wg.Wait()
	return errors.Join(errs...)
}
