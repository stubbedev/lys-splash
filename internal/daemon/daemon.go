// Package daemon is the splash process: it serves plymouthd's protocol,
// draws the theme on every output or as text on the VT, answers prompts from
// the keyboard and follows the protocol's lifecycle from boot to quit.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-splash/internal/ask"
	"github.com/stubbedev/lys-splash/internal/console"
	"github.com/stubbedev/lys-splash/internal/key"
	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/server"
	"github.com/stubbedev/lys-splash/internal/theme"
)

// FrameInterval is how often the throbber steps.
const FrameInterval = time.Second / 30

// Screen is one output the splash draws on.
type Screen interface {
	Frame() kms.Frame
	Scale() int
	Present(damage image.Rectangle) error
}

// Display is every graphical output; with no Screens the splash is text on
// the VT.
type Display interface {
	Events() <-chan kms.Event
	Handle(e kms.Event) error
	Screens() []Screen
	Deactivate() error
	Reactivate() error
	Close(retain bool) error
}

// Terminal is the VT the splash took over.
type Terminal interface {
	Graphics() error
	Text() error
	Rows() (int, error)
	Reclaim() error
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Restore() error
	Leave() error
}

// Console is the console's output, captured while the splash shows so the
// kernel console does not draw over it, and shown as the boot messages.
type Console interface {
	Read(b []byte) (int, error)
	// Restore gives the console back and writes lines to it.
	Restore(lines []string) error
	// Leave gives the console back without writing to it.
	Leave() error
}

// Devices are what the splash shows on and reads from. VT is nil on a
// kernel without VTs, and Console nil when the console's output could not
// be captured.
type Devices struct {
	Display Display
	VT      Terminal
	Console Console
}

// KMS adapts a kms.Display to Display.
type KMS struct{ *kms.Display }

// Screens are the display's lit outputs.
func (k KMS) Screens() []Screen {
	outs := k.Outputs()
	s := make([]Screen, len(outs))
	for i, o := range outs {
		s[i] = o
	}
	return s
}

// SwitchRoot chroots into dir and changes to its root directory, so that
// after the switch out of the initrd the splash's paths resolve in the real
// root rather than the freed initramfs.
func SwitchRoot(dir string) error {
	if err := unix.Chroot(dir); err != nil {
		return fmt.Errorf("daemon: chroot into %s: %w", dir, err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("daemon: change to / in %s: %w", dir, err)
	}
	return nil
}

// Config is how the daemon runs.
type Config struct {
	Mode Mode
	// ThemePath is the theme.yaml that Reload reads again.
	ThemePath string
	// Server is where the protocol is served, listening already; Run
	// serves it and closes it.
	Server *server.Server
	// Notify is the systemd notify socket, empty without a supervisor.
	Notify string
	// Chroot switches into the new root, as `R` asks.
	Chroot func(dir string) error
	// BootDuration is the file a boot reads the previous boot's duration
	// from, for its progress, and records its own in once the root is
	// writable: BootDurationFile.
	BootDuration string
	Log          *slog.Logger
}

type event struct {
	call   *server.Call
	hangup *server.Conn
	keys   []key.Key
	output []byte
}

type daemon struct {
	cfg      Config
	display  Display
	vt       Terminal
	console  Console
	messages console.Scrollback
	details  bool
	bgrt     *theme.BGRT
	renderer *theme.Renderer
	units    bool
	state    theme.State
	drawn    map[Screen]*theme.State
	shown    *theme.State
	graphics bool
	paused   bool
	inactive bool
	asker    ask.Asker
	inbox    chan event
	done     chan struct{}
	quitted  bool
	started  time.Time
	previous time.Duration
	writable bool
}

// Run serves the splash until a quit request or ctx ends. ctx ending is a
// SIGTERM: it quits as a quit request without retain would, except in the
// shutdown, reboot and kexec modes, where the frame stays until the power
// goes. Whatever ends Run, a panic in a handler included, gives the VT back
// and releases the display and the console.
func Run(ctx context.Context, cfg Config, t theme.Theme, bgrt *theme.BGRT, dev Devices) (err error) {
	r, err := theme.NewRenderer(t, bgrt)
	if err != nil {
		return errors.Join(err, cfg.Server.Close())
	}
	d := &daemon{
		cfg:      cfg,
		display:  dev.Display,
		vt:       dev.VT,
		console:  dev.Console,
		bgrt:     bgrt,
		renderer: r,
		units:    t.Units,
		drawn:    map[Screen]*theme.State{},
		inbox:    make(chan event, 16),
		done:     make(chan struct{}),
		state:    theme.State{Updating: cfg.Mode == Updates},
		started:  time.Now(),
	}
	if cfg.Mode == Boot {
		d.loadPrevious()
	}
	defer func() {
		if !d.quitted {
			err = errors.Join(err, d.quit(false))
		}
	}()
	served := make(chan error, 1)
	go func() { served <- cfg.Server.Serve(d) }()
	defer func() {
		close(d.done)
		err = errors.Join(err, cfg.Server.Close(), <-served)
	}()
	if d.vt != nil {
		go d.readKeys()
	}
	if d.console != nil {
		go d.readConsole()
	}
	if err := d.redraw(); err != nil {
		cfg.Log.Warn("drawing the first frame", "err", err)
	}
	if err := Notify(cfg.Notify, "READY=1"); err != nil {
		return err
	}
	cfg.Log.Info("showing the splash", "mode", cfg.Mode, "pid", os.Getpid())
	return d.loop(ctx)
}

func (d *daemon) loadPrevious() {
	total, err := LoadBootDuration(d.cfg.BootDuration)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d.cfg.Log.Info("no previous boot to measure progress by", "file", d.cfg.BootDuration)
	case err != nil:
		d.cfg.Log.Warn("showing no boot progress", "err", err)
	default:
		d.previous, d.state.Booting = total, true
	}
}

func (d *daemon) loop(ctx context.Context) error {
	ticker := time.NewTicker(FrameInterval)
	defer ticker.Stop()
	for {
		var reply func() error
		var err error
		var tick <-chan time.Time
		if d.animating() {
			tick = ticker.C
		}
		select {
		case <-ctx.Done():
			return d.quit(d.cfg.Mode.stopping())
		case e := <-d.inbox:
			switch {
			case e.call != nil:
				if q, ok := e.call.Request.(protocol.Quit); ok {
					return errors.Join(d.quit(q.Retain), e.call.Reply(protocol.Ack{}))
				}
				reply, err = d.request(e.call)
			case e.hangup != nil:
				d.asker.HangUp(e.hangup)
			case e.output != nil:
				err = d.output(e.output)
			default:
				err = d.typed(e.keys)
			}
		case e := <-d.display.Events():
			err = d.display.Handle(e)
		case <-tick:
			d.state.Phase = (d.state.Phase + 1) % theme.Frames
			if d.previous > 0 {
				d.state.Progress = bootProgress(time.Since(d.started), d.previous)
			}
		}
		d.state.Prompt = d.asker.Prompt()
		err = errors.Join(err, d.redraw())
		if reply != nil {
			err = errors.Join(err, reply())
		}
		if err != nil {
			d.cfg.Log.Warn("handling an event", "err", err)
		}
	}
}

func (d *daemon) post(e event) {
	select {
	case d.inbox <- e:
	case <-d.done:
	}
}

// Request implements server.Handler, handing the call to the loop.
func (d *daemon) Request(c *server.Call) {
	d.post(event{call: c})
}

// HangUp implements server.Handler, handing the hang-up to the loop.
func (d *daemon) HangUp(c *server.Conn) {
	d.post(event{hangup: c})
}

func (d *daemon) readKeys() {
	var dec key.Decoder
	defer dec.Forget()
	buf := make([]byte, 256)
	defer clear(buf)
	for {
		n, err := d.vt.Read(buf)
		if err != nil {
			d.stopped("reading the keyboard", err)
			return
		}
		keys := dec.Decode(nil, buf[:n])
		clear(buf[:n])
		d.post(event{keys: keys})
	}
}

func (d *daemon) readConsole() {
	buf := make([]byte, 4096)
	for {
		n, err := d.console.Read(buf)
		if err != nil {
			d.stopped("capturing the console", err)
			return
		}
		d.post(event{output: slices.Clone(buf[:n])})
	}
}

func (d *daemon) stopped(what string, err error) {
	select {
	case <-d.done:
	default:
		if !errors.Is(err, os.ErrClosed) {
			d.cfg.Log.Error("stopped "+what, "err", err)
		}
	}
}

func (d *daemon) output(b []byte) error {
	if _, err := d.messages.Write(b); err != nil {
		return err
	}
	if !d.details || d.vt == nil {
		return nil
	}
	_, err := d.vt.Write(b)
	return err
}

func (d *daemon) typed(keys []key.Key) error {
	defer clear(keys)
	if d.inactive {
		return nil
	}
	var errs []error
	for _, k := range keys {
		if k.Kind == key.Escape {
			errs = append(errs, d.toggleDetails())
			continue
		}
		errs = append(errs, d.asker.Key(k))
	}
	return errors.Join(errs...)
}

func (d *daemon) toggleDetails() error {
	d.details = !d.details
	d.shown = nil
	if len(d.display.Screens()) == 0 {
		if d.details {
			return nil
		}
		_, err := d.vt.Write([]byte(console.ClearScreen))
		return err
	}
	if d.details {
		d.cfg.Log.Info("showing the boot messages")
		return d.display.Deactivate()
	}
	d.cfg.Log.Info("showing the splash again")
	clear(d.drawn)
	return d.display.Reactivate()
}

func (d *daemon) animating() bool {
	return !d.paused && !d.inactive && !d.state.Updating && !d.state.Hidden && d.graphics
}

func (d *daemon) canAsk() bool {
	return d.vt != nil
}

func (d *daemon) request(c *server.Call) (func() error, error) {
	answer := func(r protocol.Response) func() error {
		return func() error { return c.Reply(r) }
	}
	switch r := c.Request.(type) {
	case protocol.Password:
		if !d.canAsk() {
			return answer(protocol.NoAnswer{}), nil
		}
		d.asker.Password(c, c.Conn(), r.Prompt)
		return nil, nil
	case protocol.Question:
		if !d.canAsk() {
			return answer(protocol.NoAnswer{}), nil
		}
		d.asker.Question(c, c.Conn(), r.Prompt)
		return nil, nil
	case protocol.CachedPassword:
		return func() error { return d.asker.Cached(c) }, nil
	case protocol.WatchKeystroke:
		if !d.canAsk() {
			return answer(protocol.NoAnswer{}), nil
		}
		d.asker.Watch(c, c.Conn(), r.Keys)
		return nil, nil
	case protocol.HasActiveVT:
		if d.vt == nil {
			return answer(protocol.Nak{}), nil
		}
		return answer(protocol.Ack{}), nil
	case protocol.NewRoot:
		if err := d.cfg.Chroot(r.Dir); err != nil {
			return answer(protocol.Nak{}), fmt.Errorf("daemon: switch to the new root %s: %w", r.Dir, err)
		}
		if d.cfg.Mode == Boot && d.previous == 0 {
			d.loadPrevious()
		}
		return answer(protocol.Ack{}), nil
	}
	return answer(protocol.Ack{}), d.apply(c.Request)
}

func (d *daemon) apply(req protocol.Request) error {
	switch r := req.(type) {
	case protocol.Update:
		if d.units {
			d.state.Message = r.Status
		}
	case protocol.ChangeMode:
		d.state.Updating = slices.Contains(updatingModes, r.Mode)
	case protocol.SystemUpdate:
		d.state.Updating, d.state.Progress = true, r.Progress
	case protocol.ShowMessage:
		d.state.Message = r.Text
	case protocol.HideMessage:
		if d.state.Message == r.Text {
			d.state.Message = ""
		}
	case protocol.PauseProgress:
		d.paused = true
	case protocol.ResumeProgress:
		d.paused = false
	case protocol.ShowSplash:
		d.state.Hidden = false
	case protocol.HideSplash:
		d.state.Hidden = true
	case protocol.Deactivate:
		d.inactive = true
		if d.details {
			d.details = false
			return nil
		}
		return d.display.Deactivate()
	case protocol.Reactivate:
		d.inactive, d.details = false, false
		clear(d.drawn)
		return errors.Join(d.display.Reactivate(), d.reclaim())
	case protocol.IgnoreKeystroke:
		return d.asker.Ignore(r.Keys)
	case protocol.Reload:
		return d.reload()
	case protocol.SystemInitialized:
		d.writable = true
		return d.reclaim()
	case protocol.Error:
		d.cfg.Log.Warn("the boot reported an error")
	case protocol.Ping, protocol.Quit, protocol.Password, protocol.Question,
		protocol.CachedPassword, protocol.WatchKeystroke, protocol.HasActiveVT, protocol.NewRoot:
	}
	return nil
}

// reclaim takes the VT back as the splash left it, after systemd reset the
// console in the real root or a display manager had it.
func (d *daemon) reclaim() error {
	if d.vt == nil {
		return nil
	}
	err := d.vt.Reclaim()
	if d.graphics {
		err = errors.Join(err, d.vt.Graphics())
	}
	return err
}

func (d *daemon) reload() error {
	t, err := theme.Load(d.cfg.ThemePath)
	r, rerr := theme.NewRenderer(t, d.bgrt)
	if rerr != nil {
		return errors.Join(err, rerr)
	}
	d.renderer, d.units = r, t.Units
	clear(d.drawn)
	d.shown = nil
	return err
}

func (d *daemon) redraw() error {
	screens := d.display.Screens()
	if len(screens) == 0 || d.details {
		return d.redrawText()
	}
	var errs []error
	if d.vt != nil && !d.graphics {
		errs = append(errs, d.vt.Graphics())
	}
	d.graphics, d.shown = true, nil
	for s := range d.drawn {
		if !slices.Contains(screens, s) {
			delete(d.drawn, s)
		}
	}
	for _, s := range screens {
		prev := d.drawn[s]
		damage, err := d.renderer.Draw(s.Frame(), s.Scale(), prev, d.state)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		state := d.state
		d.drawn[s] = &state
		if prev == nil || !damage.Empty() {
			errs = append(errs, s.Present(damage))
		}
	}
	return errors.Join(errs...)
}

func (d *daemon) redrawText() error {
	if d.vt == nil {
		return nil
	}
	var errs []error
	if d.graphics {
		errs = append(errs, d.vt.Text())
		d.graphics, d.shown = false, nil
	}
	clear(d.drawn)
	if d.details && d.shown == nil {
		errs = append(errs, d.showMessages())
	}
	errs = append(errs, console.TextView{W: d.vt}.Show(d.shown, d.state))
	state := d.state
	d.shown = &state
	return errors.Join(errs...)
}

func (d *daemon) showMessages() error {
	rows, err := d.vt.Rows()
	if err != nil {
		return err
	}
	_, err = d.vt.Write(d.messages.Screen(rows))
	return err
}

func (d *daemon) quit(retain bool) error {
	d.quitted = true
	d.cfg.Log.Info("quitting", "mode", d.cfg.Mode, "retain", retain)
	errs := []error{d.asker.Close()}
	if d.cfg.Mode == Boot && d.writable {
		errs = append(errs, SaveBootDuration(d.cfg.BootDuration, time.Since(d.started)))
	}
	if err := d.display.Close(retain); errors.Is(err, drm.ErrNoCloseFB) {
		d.cfg.Log.Warn("this kernel has no CLOSEFB, so the splash's last frame goes away with it", "err", err)
	} else {
		errs = append(errs, err)
	}
	if d.vt != nil {
		if retain && d.graphics {
			errs = append(errs, d.vt.Leave())
		} else {
			errs = append(errs, d.vt.Restore())
		}
	}
	if d.console != nil {
		if retain {
			errs = append(errs, d.console.Leave())
		} else {
			errs = append(errs, d.console.Restore(d.messages.Lines()))
		}
	}
	return errors.Join(errs...)
}
