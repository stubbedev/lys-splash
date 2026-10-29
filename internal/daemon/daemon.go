// Package daemon is the splash process: it serves plymouthd's protocol,
// draws the theme on every output or as text on the VT, answers prompts from
// the keyboard and follows the protocol's lifecycle from boot to quit.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
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
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Restore() error
	Leave() error
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
	// Socket and Owner are where the protocol is served and which uid may
	// use it: protocol.SocketName and root.
	Socket string
	Owner  uint32
	// Notify is the systemd notify socket, empty without a supervisor.
	Notify string
	// Chroot switches into the new root, as `R` asks.
	Chroot func(dir string) error
	Log    *slog.Logger
}

type event struct {
	call   *server.Call
	hangup *server.Conn
	keys   []key.Key
}

type daemon struct {
	cfg      Config
	display  Display
	vt       Terminal
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
}

// Run serves the splash until a quit request or ctx ends. ctx ending is a
// SIGTERM: it quits as a quit request without retain would, except in the
// shutdown, reboot and kexec modes, where the frame stays until the power
// goes. Whatever ends Run, a panic in a handler included, gives the VT back
// and releases the display. vt is nil on a kernel without VTs.
func Run(ctx context.Context, cfg Config, t theme.Theme, bgrt *theme.BGRT, display Display, vt Terminal) (err error) {
	r, err := theme.NewRenderer(t, bgrt)
	if err != nil {
		return err
	}
	d := &daemon{
		cfg:      cfg,
		display:  display,
		vt:       vt,
		bgrt:     bgrt,
		renderer: r,
		units:    t.Units,
		drawn:    map[Screen]*theme.State{},
		inbox:    make(chan event, 16),
		done:     make(chan struct{}),
		state:    theme.State{Updating: cfg.Mode == Updates},
	}
	defer func() {
		if !d.quitted {
			err = errors.Join(err, d.quit(false))
		}
	}()
	srv, err := server.Listen(cfg.Socket, cfg.Owner, d, cfg.Log)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()
	defer func() {
		close(d.done)
		err = errors.Join(err, srv.Close(), <-served)
	}()
	if vt != nil {
		go d.readKeys()
	}
	if err := d.redraw(); err != nil {
		cfg.Log.Warn("drawing the first frame", "err", err)
	}
	if err := Notify(cfg.Notify, "READY=1"); err != nil {
		return err
	}
	return d.loop(ctx)
}

func (d *daemon) loop(ctx context.Context) error {
	tick := time.NewTicker(FrameInterval)
	defer tick.Stop()
	for {
		var reply func() error
		var err error
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
			default:
				err = d.typed(e.keys)
			}
		case e := <-d.display.Events():
			err = d.display.Handle(e)
		case <-tick.C:
			if !d.animating() {
				continue
			}
			d.state.Phase = (d.state.Phase + 1) % theme.Frames
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
	buf := make([]byte, 256)
	defer clear(buf)
	for {
		n, err := d.vt.Read(buf)
		if err != nil {
			return
		}
		keys := dec.Decode(nil, buf[:n])
		clear(buf[:n])
		d.post(event{keys: keys})
	}
}

func (d *daemon) typed(keys []key.Key) error {
	if d.inactive {
		return nil
	}
	var errs []error
	for _, k := range keys {
		errs = append(errs, d.asker.Key(k))
	}
	return errors.Join(errs...)
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
		return d.display.Deactivate()
	case protocol.Reactivate:
		d.inactive = false
		clear(d.drawn)
		return d.display.Reactivate()
	case protocol.IgnoreKeystroke:
		return d.asker.Ignore(r.Keys)
	case protocol.Reload:
		return d.reload()
	case protocol.Error:
		d.cfg.Log.Warn("the boot reported an error")
	case protocol.Ping, protocol.SystemInitialized, protocol.Quit, protocol.Password, protocol.Question,
		protocol.CachedPassword, protocol.WatchKeystroke, protocol.HasActiveVT, protocol.NewRoot:
	}
	return nil
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
	if len(screens) == 0 {
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
	errs = append(errs, console.TextView{W: d.vt}.Show(d.shown, d.state))
	state := d.state
	d.shown = &state
	return errors.Join(errs...)
}

func (d *daemon) quit(retain bool) error {
	d.quitted = true
	errs := []error{d.asker.Close()}
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
	return errors.Join(errs...)
}
