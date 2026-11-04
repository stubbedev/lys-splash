// Package daemon is the splash process: it serves plymouthd's protocol,
// draws the theme on every output or as text on the VT, answers prompts from
// the keyboard and follows the protocol's lifecycle from boot to quit.
package daemon

import (
	"context"
	"errors"
	"fmt"
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

// FrameInterval is how long one of the throbber's phases lasts.
const FrameInterval = time.Second / 30

// A screen whose frame fails to reach the display is tried again after a wait
// that doubles from the first to the last, so a lost master or a broken driver
// costs neither a commit per frame nor a log record per frame.
const (
	firstPresentRetry = 50 * time.Millisecond
	lastPresentRetry  = 2 * time.Second
)

// Screen is one output the splash draws on.
type Screen interface {
	Frame() kms.Frame
	Scale() int
	Present(damage kms.Damage) error
}

// Display is every graphical output; with no Screens the splash is text on
// the VT.
type Display interface {
	Events() <-chan kms.Event
	// Handle applies e and says whether the set of screens changed.
	Handle(e kms.Event) (bool, error)
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
type KMS struct {
	*kms.Display
	outputs []*kms.Output
	screens []Screen
}

// Screens are the display's lit outputs; the list is built again only when
// they change, and a change gives a new list rather than altering this one.
func (k *KMS) Screens() []Screen {
	outs := k.Outputs()
	if slices.Equal(outs, k.outputs) {
		return k.screens
	}
	s := make([]Screen, len(outs))
	for i, o := range outs {
		s[i] = o
	}
	k.outputs, k.screens = outs, s
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
	// from, for its progress, and records its own in if it came through the
	// switch to a real root that became writable: BootDurationFile.
	BootDuration     string
	LoadBootDuration func(path string) (time.Duration, error)
	SaveBootDuration func(path string, d time.Duration) error
	// LoadTheme reads the theme Reload shows.
	LoadTheme func(path string) (theme.Theme, error)
	// RasterizeLogo draws the logo for a scale off the loop.
	RasterizeLogo func(scale int) (*theme.LogoRaster, error)
	Clock         Clock
	Log           *slog.Logger
	// started hands a test the daemon as Run makes it.
	started func(*daemon)
}

// previousBoot is what reading the previous boot's duration found.
type previousBoot struct {
	total time.Duration
	err   error
}

// rasterizedLogo is the logo for a scale, ready or failed.
type rasterizedLogo struct {
	scale int
	logo  *theme.LogoRaster
	err   error
}

// themeLoad is what reading the theme found, for the Reload calls waiting on it.
type themeLoad struct {
	calls    []*server.Call
	renderer *theme.Renderer
	units    bool
	err      error
}

// drawnScreen is what a screen shows and how its failing frames are retried: a
// zero wait means its last frame reached the display.
type drawnScreen struct {
	state   theme.State
	wait    time.Duration
	retryAt time.Time
}

type event struct {
	call     *server.Call
	hangup   *server.Conn
	keys     []key.Key
	output   []byte
	previous *previousBoot
	theme    *themeLoad
	logo     *rasterizedLogo
}

type daemon struct {
	cfg      Config
	display  Display
	vt       Terminal
	vtq      *vtQueue
	console  Console
	messages console.Scrollback
	details  bool
	bgrt     *theme.BGRT
	renderer *theme.Renderer
	units    bool
	state    theme.State
	screens  []Screen
	drawn    map[Screen]drawnScreen
	settled  theme.State
	stale    bool
	shown    *theme.State
	inText   theme.State
	graphics bool
	paused   bool
	inactive bool
	asker    ask.Asker
	inbox    chan event
	done     chan struct{}
	quitted  bool
	started  time.Time
	previous time.Duration
	switched bool
	writable bool
	// reloads wait for the next theme load, reloading says one is running.
	reloads   []*server.Call
	reloading bool
	// looping says the frame loop runs, so a logo is rasterized off it; logos
	// are the scales asked for since the renderer changed, true while one is on
	// its way and false once it failed.
	looping bool
	logos   map[int]bool
	// The timer is armed for the next change of the picture, counted from
	// phaseAt, the start of the phase showing, or for retryAt, the first retry
	// of a failing screen. ticking says the animation is running and rearm that
	// the deadline has to be worked out again.
	timer   Timer
	phaseAt time.Time
	retryAt time.Time
	ticking bool
	rearm   bool
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
		drawn:    map[Screen]drawnScreen{},
		inbox:    make(chan event, 16),
		done:     make(chan struct{}),
		state:    theme.State{Updating: cfg.Mode == Updates},
		started:  cfg.Clock.Now(),
		timer:    cfg.Clock.NewTimer(),
	}
	if cfg.started != nil {
		cfg.started(d)
	}
	if cfg.Mode == Boot {
		d.loadPrevious()
	}
	defer func() {
		if !d.quitted {
			err = errors.Join(err, d.quit(false), d.recordDuration())
		}
	}()
	served := make(chan error, 1)
	go func() { served <- cfg.Server.Serve(d) }()
	defer func() {
		close(d.done)
		err = errors.Join(err, cfg.Server.Close(), <-served)
	}()
	if d.vt != nil {
		d.vtq = newVTQueue(d.vt, cfg.Log)
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

// loadPrevious reads the previous boot's duration off the loop, since the
// disk may stall; previousLoaded applies it.
func (d *daemon) loadPrevious() {
	go func() {
		total, err := d.cfg.LoadBootDuration(d.cfg.BootDuration)
		d.post(event{previous: &previousBoot{total: total, err: err}})
	}()
}

func (d *daemon) previousLoaded(p *previousBoot) {
	if d.previous > 0 {
		return
	}
	switch err := p.err; {
	case errors.Is(err, fs.ErrNotExist):
		d.cfg.Log.Info("no previous boot to measure progress by", "file", d.cfg.BootDuration)
	case err != nil:
		d.cfg.Log.Warn("showing no boot progress", "err", err)
	default:
		d.previous, d.state.Booting, d.rearm = p.total, true, true
	}
}

// tick is the channel of the next change of the animation or retry of a failing
// screen, nil with neither: the timer only ever wakes the loop for a picture
// that differs or a frame still to show.
func (d *daemon) tick() <-chan time.Time {
	animating := d.animating()
	if animating != d.ticking {
		d.ticking, d.rearm = animating, true
		if animating {
			d.phaseAt = d.cfg.Clock.Now()
		}
	}
	if !animating && d.retryAt.IsZero() {
		if d.rearm {
			d.rearm = false
			d.timer.Stop()
		}
		return nil
	}
	if d.rearm {
		d.rearm = false
		d.timer.Reset(d.untilNextChange())
	}
	return d.timer.C()
}

func (d *daemon) untilNextChange() time.Duration {
	next := d.retryAt
	if d.ticking {
		next = earliest(next, d.phaseAt.Add(time.Duration(d.renderer.PhasesToNextPicture(d.state.Phase))*FrameInterval))
		if d.previous > 0 && d.state.Progress < protocol.MaxPercent-1 {
			next = earliest(next, d.started.Add(nextPercentAt(d.state.Progress, d.previous)))
		}
	}
	return max(next.Sub(d.cfg.Clock.Now()), 0)
}

// earliest is the sooner of a and b, a being zero when there is none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func (d *daemon) step() {
	now := d.cfg.Clock.Now()
	d.rearm = true
	if !d.retryAt.IsZero() && !now.Before(d.retryAt) {
		d.stale = true
	}
	if !d.ticking {
		return
	}
	phases := now.Sub(d.phaseAt) / FrameInterval
	d.state.Phase = (d.state.Phase + int(phases%theme.Frames)) % theme.Frames
	d.phaseAt = d.phaseAt.Add(phases * FrameInterval)
	if d.previous > 0 {
		d.state.Progress = bootProgress(now.Sub(d.started), d.previous)
	}
}

func (d *daemon) loop(ctx context.Context) error {
	defer d.timer.Stop()
	d.looping = true
	for {
		var reply func() error
		var err error
		select {
		case <-ctx.Done():
			return errors.Join(d.quit(d.cfg.Mode.stopping()), d.recordDuration())
		case e := <-d.inbox:
			switch {
			case e.call != nil:
				if q, ok := e.call.Request.(protocol.Quit); ok {
					return errors.Join(d.quit(q.Retain), e.call.Reply(protocol.Ack{}), d.recordDuration())
				}
				reply, err = d.request(e.call)
			case e.hangup != nil:
				d.asker.HangUp(e.hangup)
			case e.output != nil:
				err = d.output(e.output)
			case e.previous != nil:
				d.previousLoaded(e.previous)
			case e.theme != nil:
				reply, err = d.themeLoaded(e.theme)
			case e.logo != nil:
				err = d.logoRasterized(e.logo)
			default:
				err = d.typed(e.keys)
			}
			d.state.Prompt = d.asker.Prompt()
		case e := <-d.display.Events():
			var changed bool
			changed, err = d.display.Handle(e)
			d.stale = d.stale || changed
			if flip, ok := errors.AsType[*kms.FlipError](err); ok {
				d.presentFailed(flip.Output, d.drawn[flip.Output])
				d.nextRetry()
			}
		case <-d.tick():
			d.step()
		}
		if d.stale || d.state != d.settled {
			err = errors.Join(err, d.redraw())
		}
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
	_, err := d.vtq.Write(b)
	return err
}

func (d *daemon) typed(keys []key.Key) error {
	defer clear(keys)
	if d.inactive {
		return nil
	}
	var errs []error
	for _, k := range keys {
		var err error
		if k.Kind == key.Escape {
			err = d.toggleDetails()
		} else {
			err = d.asker.Key(k)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *daemon) toggleDetails() error {
	d.details = !d.details
	d.shown, d.stale = nil, true
	if len(d.display.Screens()) == 0 {
		if d.details {
			return nil
		}
		_, err := d.vtq.Write([]byte(console.ClearScreen))
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
	return !d.paused && !d.inactive && !d.state.Updating && !d.state.Hidden && d.state.Prompt.Kind == theme.NoPrompt && d.graphics
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
	case protocol.Reload:
		d.reload(c)
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
		d.switched = true
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
			d.details, d.stale = false, true
			return nil
		}
		return d.display.Deactivate()
	case protocol.Reactivate:
		d.inactive, d.details, d.stale = false, false, true
		clear(d.drawn)
		return errors.Join(d.display.Reactivate(), d.reclaim())
	case protocol.IgnoreKeystroke:
		return d.asker.Ignore(r.Keys)
	case protocol.SystemInitialized:
		d.writable = true
		return d.reclaim()
	case protocol.Error:
		d.cfg.Log.Warn("the boot reported an error")
	case protocol.Ping, protocol.Quit, protocol.Password, protocol.Question,
		protocol.CachedPassword, protocol.WatchKeystroke, protocol.HasActiveVT, protocol.NewRoot, protocol.Reload:
	}
	return nil
}

// reclaim takes the VT back as the splash left it, after systemd reset the
// console in the real root or a display manager had it.
func (d *daemon) reclaim() error {
	if d.vt == nil {
		return nil
	}
	graphics := d.graphics
	d.vtq.do(func(vt Terminal) error {
		err := vt.Reclaim()
		if graphics {
			err = errors.Join(err, vt.Graphics())
		}
		return err
	})
	return nil
}

// reload answers c once the theme is read again, which happens off the loop
// and one load at a time: calls that arrive during a load wait for the next,
// so the theme they see was read after they asked.
func (d *daemon) reload(c *server.Call) {
	d.reloads = append(d.reloads, c)
	if !d.reloading {
		d.loadTheme()
	}
}

func (d *daemon) loadTheme() {
	calls := d.reloads
	d.reloads, d.reloading = nil, true
	scales := d.scales()
	go func() {
		t, err := d.cfg.LoadTheme(d.cfg.ThemePath)
		r, rerr := theme.NewRenderer(t, d.bgrt)
		if rerr == nil {
			for _, scale := range scales {
				if !r.NeedsLogo(scale) {
					continue
				}
				logo, lerr := d.cfg.RasterizeLogo(scale)
				if lerr != nil {
					err = errors.Join(err, lerr)
					continue
				}
				r.AddLogo(logo)
			}
		}
		d.post(event{theme: &themeLoad{calls: calls, renderer: r, units: t.Units, err: errors.Join(err, rerr)}})
	}()
}

// scales are the scales of the screens now.
func (d *daemon) scales() []int {
	var scales []int
	for _, s := range d.display.Screens() {
		if scale := s.Scale(); !slices.Contains(scales, scale) {
			scales = append(scales, scale)
		}
	}
	return scales
}

// logoReady says a screen at scale can be drawn whole now. Otherwise the logo is
// on its way, and the screen shows nothing rather than half a frame until it comes,
// milliseconds later: a screen just lit has shown nothing yet. A logo that failed
// is left to Draw, which reports it.
func (d *daemon) logoReady(scale int) bool {
	if !d.looping || !d.renderer.NeedsLogo(scale) {
		return true
	}
	pending, asked := d.logos[scale]
	if asked {
		return !pending
	}
	if d.logos == nil {
		d.logos = map[int]bool{}
	}
	d.logos[scale] = true
	go func() {
		logo, err := d.cfg.RasterizeLogo(scale)
		d.post(event{logo: &rasterizedLogo{scale: scale, logo: logo, err: err}})
	}()
	return false
}

func (d *daemon) logoRasterized(l *rasterizedLogo) error {
	d.stale = true
	if l.err != nil {
		d.logos[l.scale] = false
		return l.err
	}
	delete(d.logos, l.scale)
	d.renderer.AddLogo(l.logo)
	return nil
}

func (d *daemon) themeLoaded(l *themeLoad) (func() error, error) {
	d.reloading = false
	if len(d.reloads) > 0 {
		d.loadTheme()
	}
	if l.renderer != nil {
		d.renderer, d.units, d.rearm = l.renderer, l.units, true
		clear(d.drawn)
		d.shown, d.stale = nil, true
	}
	return func() error {
		var errs []error
		for _, c := range l.calls {
			errs = append(errs, c.Reply(protocol.Ack{}))
		}
		return errors.Join(errs...)
	}, l.err
}

// redraw brings every screen to the state. A screen that fails to draw stays
// at the state it shows, so the next redraw takes it the whole way.
func (d *daemon) redraw() error {
	screens := d.display.Screens()
	if len(screens) == 0 || d.details {
		d.stale, d.settled = false, d.state
		return d.redrawText()
	}
	var errs []error
	if d.vt != nil && !d.graphics {
		d.vtq.do(Terminal.Graphics)
	}
	d.graphics, d.shown = true, nil
	if !slices.Equal(screens, d.screens) {
		d.screens = screens
		for s := range d.drawn {
			if !slices.Contains(screens, s) {
				delete(d.drawn, s)
			}
		}
	}
	d.stale, d.settled = false, d.state
	for _, s := range screens {
		if err := d.drawScreen(s); err != nil {
			errs = append(errs, err)
		}
	}
	d.nextRetry()
	return errors.Join(errs...)
}

func (d *daemon) drawScreen(s Screen) error {
	if !d.logoReady(s.Scale()) {
		return nil
	}
	shown, known := d.drawn[s]
	if shown.wait > 0 && d.cfg.Clock.Now().Before(shown.retryAt) {
		return nil
	}
	from := &shown.state
	if !known {
		from = nil
	}
	damage, err := d.renderer.Draw(s.Frame(), s.Scale(), from, d.state)
	if err != nil {
		d.stale = true
		return err
	}
	shown.state = d.state
	if known && shown.wait == 0 && damage.Empty() {
		d.drawn[s] = shown
		return nil
	}
	err = s.Present(damage)
	if err == nil {
		shown.wait = 0
		d.drawn[s] = shown
		return nil
	}
	if d.presentFailed(s, shown) {
		return err
	}
	return nil
}

// presentFailed puts off s's next frame and says whether this failure is news:
// only the first of a run of them is worth a log record.
func (d *daemon) presentFailed(s Screen, shown drawnScreen) bool {
	first := shown.wait == 0
	shown.wait = min(max(2*shown.wait, firstPresentRetry), lastPresentRetry)
	shown.retryAt = d.cfg.Clock.Now().Add(shown.wait)
	d.drawn[s] = shown
	return first
}

func (d *daemon) nextRetry() {
	var next time.Time
	for _, s := range d.screens {
		if shown := d.drawn[s]; shown.wait > 0 {
			next = earliest(next, shown.retryAt)
		}
	}
	if !next.Equal(d.retryAt) {
		d.retryAt, d.rearm = next, true
	}
}

func (d *daemon) redrawText() error {
	if d.vt == nil {
		return nil
	}
	if d.graphics {
		d.vtq.do(Terminal.Text)
		d.graphics, d.shown = false, nil
	}
	clear(d.drawn)
	if d.details && d.shown == nil {
		d.showMessages()
	}
	err := console.TextView{W: d.vtq}.Show(d.shown, d.state)
	d.inText = d.state
	d.shown = &d.inText
	return err
}

// showMessages writes the end of the scrollback as it is now, once the queue
// has asked the VT how much of it fits.
func (d *daemon) showMessages() {
	lines := d.messages.Lines()
	d.vtq.do(func(vt Terminal) error {
		rows, err := vt.Rows()
		if err != nil {
			return err
		}
		_, err = vt.Write(console.Screen(lines, rows))
		return err
	})
}

func (d *daemon) quit(retain bool) error {
	d.quitted = true
	d.cfg.Log.Info("quitting", "mode", d.cfg.Mode, "retain", retain)
	errs := []error{d.asker.Close()}
	for _, c := range d.reloads {
		errs = append(errs, c.Reply(protocol.Nak{}))
	}
	if err := d.display.Close(retain); errors.Is(err, drm.ErrNoCloseFB) {
		d.cfg.Log.Warn("this kernel has no CLOSEFB, so the splash's last frame goes away with it", "err", err)
	} else {
		errs = append(errs, err)
	}
	if d.vt != nil {
		if d.vtq != nil {
			d.vtq.drain(time.After(vtDrainWait))
		}
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

func (d *daemon) recordDuration() error {
	if d.cfg.Mode != Boot || !d.switched || !d.writable {
		return nil
	}
	return d.cfg.SaveBootDuration(d.cfg.BootDuration, d.cfg.Clock.Now().Sub(d.started))
}
