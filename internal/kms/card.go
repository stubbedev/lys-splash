// Package kms lights every output of every DRM card on two linear XRGB8888
// dumb buffers and flips frames to them with atomic commits, one per vblank,
// copying only what changed. It follows cards appearing and disappearing
// through kernel uevents, from a firmware framebuffer to the native driver.
package kms

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"slices"
	"strings"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"golang.org/x/sys/unix"
)

const scanoutFormat = fourcc.XRGB8888

type device interface {
	Path() string
	FD() int
	Close() error
	SetClientCap(capability, value uint64) error
	SetMaster() error
	DropMaster() error
	Resources() (drm.Resources, error)
	Connector(id uint32) (drm.Connector, error)
	Encoder(id uint32) (drm.Encoder, error)
	Planes() ([]uint32, error)
	Plane(id uint32) (drm.Plane, error)
	Properties(objectID, objectType uint32) (map[string]drm.Property, error)
	PropertyInfo(id uint32) (drm.PropertyInfo, error)
	Blob(id uint32) ([]byte, error)
	CreateBlob(data []byte) (uint32, error)
	DestroyBlob(id uint32) error
	CreateDumb(size image.Point) (drm.Dumb, error)
	DestroyDumb(b drm.Dumb) error
	AddFB2(fb drm.FB) (uint32, error)
	RemoveFB(id uint32) error
	CloseFB(id uint32) error
	Commit(r *drm.Request, flags uint32, userData uint64) error
}

// propIDs looks an object's property IDs up once, so that a commit touches no
// map and a property the atomic API promises fails when the card opens.
type propIDs struct {
	object  string
	props   map[string]drm.Property
	missing []string
}

func (p *propIDs) id(name string) uint32 {
	prop, ok := p.props[name]
	if !ok {
		p.missing = append(p.missing, name)
	}
	return prop.ID
}

func (p *propIDs) err() error {
	if len(p.missing) == 0 {
		return nil
	}
	return fmt.Errorf("kms: %s has no %s property", p.object, strings.Join(p.missing, ", "))
}

type connector struct {
	id   uint32
	crtc uint32
}

func newConnector(id uint32, props map[string]drm.Property) (connector, error) {
	p := propIDs{object: fmt.Sprintf("connector %d", id), props: props}
	c := connector{id: id, crtc: p.id("CRTC_ID")}
	return c, p.err()
}

type crtc struct {
	id     uint32
	mode   uint32
	active uint32
	index  int
	taken  bool
}

func newCRTC(id uint32, index int, props map[string]drm.Property) (*crtc, error) {
	p := propIDs{object: fmt.Sprintf("CRTC %d", id), props: props}
	c := &crtc{id: id, mode: p.id("MODE_ID"), active: p.id("ACTIVE"), index: index}
	return c, p.err()
}

type plane struct {
	id                         uint32
	fb, crtc                   uint32
	srcX, srcY, srcW, srcH     uint32
	crtcX, crtcY, crtcW, crtcH uint32
	damageClips                uint32
	possible                   uint32
	taken                      bool
}

func newPlane(id, possible uint32, props map[string]drm.Property) (*plane, error) {
	p := propIDs{object: fmt.Sprintf("plane %d", id), props: props}
	pl := &plane{
		id: id, possible: possible,
		fb: p.id("FB_ID"), crtc: p.id("CRTC_ID"),
		srcX: p.id("SRC_X"), srcY: p.id("SRC_Y"), srcW: p.id("SRC_W"), srcH: p.id("SRC_H"),
		crtcX: p.id("CRTC_X"), crtcY: p.id("CRTC_Y"), crtcW: p.id("CRTC_W"), crtcH: p.id("CRTC_H"),
		damageClips: props["FB_DAMAGE_CLIPS"].ID,
	}
	return pl, p.err()
}

// Card is one open DRM card and the outputs lit on it.
type Card struct {
	dev      device
	name     string
	firmware bool
	scaler   *Scaler
	log      *slog.Logger
	crtcs    []*crtc
	planes   []*plane
	outputs  []*Output
	req      drm.Request
	paused   bool
}

func openCard(dev device, name string, firmware bool, scaler *Scaler, log *slog.Logger) (*Card, error) {
	c := &Card{dev: dev, name: name, firmware: firmware, scaler: scaler, log: log.With("card", name)}
	for _, capability := range []uint64{drm.ClientCapUniversal, drm.ClientCapAtomic} {
		if err := dev.SetClientCap(capability, 1); err != nil {
			return nil, fmt.Errorf("kms: %s: client capability %d: %w", dev.Path(), capability, err)
		}
	}
	if err := dev.SetMaster(); err != nil {
		return nil, fmt.Errorf("kms: %s: become DRM master: %w", dev.Path(), err)
	}
	res, err := dev.Resources()
	if err != nil {
		return nil, err
	}
	for i, id := range res.CRTCs {
		props, err := dev.Properties(id, drm.ObjectCRTC)
		if err != nil {
			return nil, err
		}
		cr, err := newCRTC(id, i, props)
		if err != nil {
			return nil, err
		}
		c.crtcs = append(c.crtcs, cr)
	}
	planes, err := dev.Planes()
	if err != nil {
		return nil, err
	}
	for _, id := range planes {
		p, err := c.primary(id)
		if err != nil {
			return nil, err
		}
		if p != nil {
			c.planes = append(c.planes, p)
		}
	}
	return c, nil
}

func (c *Card) primary(id uint32) (*plane, error) {
	info, err := c.dev.Plane(id)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.Formats, scanoutFormat) {
		return nil, nil
	}
	props, err := c.dev.Properties(id, drm.ObjectPlane)
	if err != nil {
		return nil, err
	}
	kind, ok := props["type"]
	if !ok {
		return nil, fmt.Errorf("kms: %s: plane %d has no type property", c.dev.Path(), id)
	}
	enum, err := c.dev.PropertyInfo(kind.ID)
	if err != nil {
		return nil, err
	}
	if primary, ok := enum.Enums["Primary"]; !ok || kind.Value != primary {
		return nil, nil
	}
	return newPlane(id, info.PossibleCRTCs, props)
}

// Name is the card node's name, such as "card0".
func (c *Card) Name() string {
	return c.name
}

// Rescan lights every connected connector not lit yet and drops the outputs
// whose connector went away, as after a hotplug. A connector that fails to
// light is reported and the others are lit regardless.
func (c *Card) Rescan() error {
	res, err := c.dev.Resources()
	if err != nil {
		return err
	}
	return c.scan(res.Connectors)
}

func (c *Card) scan(connectors []uint32) error {
	var errs []error
	for _, o := range slices.Clone(c.outputs) {
		conn, err := c.dev.Connector(o.connector.id)
		if !slices.Contains(connectors, o.connector.id) || errors.Is(err, unix.ENOENT) || err == nil && conn.Connection != drm.Connected {
			errs = append(errs, c.drop(o))
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, id := range connectors {
		if slices.ContainsFunc(c.outputs, func(o *Output) bool { return o.connector.id == id }) {
			continue
		}
		conn, err := c.dev.Connector(id)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if conn.Connection != drm.Connected || len(conn.Modes) == 0 {
			continue
		}
		if err := c.add(conn); err != nil {
			errs = append(errs, fmt.Errorf("kms: %s: light %s: %w", c.dev.Path(), conn.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func (c *Card) drop(o *Output) error {
	c.outputs = slices.DeleteFunc(c.outputs, func(x *Output) bool { return x == o })
	return o.release(false)
}

func (c *Card) pick(conn drm.Connector, routed uint32) (*crtc, *plane, error) {
	var fallback *crtc
	for _, encID := range conn.Encoders {
		enc, err := c.dev.Encoder(encID)
		if err != nil {
			return nil, nil, err
		}
		for _, cr := range c.crtcs {
			if cr.taken || enc.PossibleCRTCs&(1<<cr.index) == 0 || c.freePlane(cr) == nil {
				continue
			}
			if cr.id == routed {
				return cr, c.freePlane(cr), nil
			}
			if fallback == nil {
				fallback = cr
			}
		}
	}
	if fallback == nil {
		return nil, nil, errors.New("no free CRTC with an XRGB8888 primary plane")
	}
	return fallback, c.freePlane(fallback), nil
}

func (c *Card) freePlane(cr *crtc) *plane {
	for _, p := range c.planes {
		if !p.taken && p.possible&(1<<cr.index) != 0 {
			return p
		}
	}
	return nil
}

func (c *Card) litMode(cr *crtc, routed uint32) (*drm.Mode, error) {
	if routed != cr.id {
		return nil, nil
	}
	props, err := c.dev.Properties(cr.id, drm.ObjectCRTC)
	if err != nil {
		return nil, err
	}
	blob := props["MODE_ID"].Value
	if props["ACTIVE"].Value != 1 || blob == 0 {
		return nil, nil
	}
	id, err := intconv.Exact[uint32](blob)
	if err != nil {
		return nil, fmt.Errorf("MODE_ID blob %d of CRTC %d: %w", blob, cr.id, err)
	}
	data, err := c.dev.Blob(id)
	if err != nil {
		return nil, err
	}
	m, err := drm.ParseMode(data)
	if err != nil {
		return nil, fmt.Errorf("the mode CRTC %d shows: %w", cr.id, err)
	}
	return &m, nil
}

func (c *Card) orientation(props map[string]drm.Property) (Rotation, error) {
	p, ok := props["panel orientation"]
	if !ok {
		return Upright, nil
	}
	info, err := c.dev.PropertyInfo(p.ID)
	if err != nil {
		return Upright, err
	}
	for name, v := range info.Enums {
		if v == p.Value {
			return rotationOf(name)
		}
	}
	return Upright, fmt.Errorf("kms: panel orientation %d is none of %v", p.Value, info.Enums)
}

func (c *Card) add(conn drm.Connector) error {
	props, err := c.dev.Properties(conn.ID, drm.ObjectConnector)
	if err != nil {
		return err
	}
	connected, err := newConnector(conn.ID, props)
	if err != nil {
		return err
	}
	routed, err := intconv.Exact[uint32](props["CRTC_ID"].Value)
	if err != nil {
		return fmt.Errorf("CRTC_ID: %w", err)
	}
	cr, pl, err := c.pick(conn, routed)
	if err != nil {
		return err
	}
	lit, err := c.litMode(cr, routed)
	if err != nil {
		return err
	}
	mode, err := chooseMode(conn.Modes, lit)
	if err != nil {
		return err
	}
	size := modeSize(mode)
	if c.firmware && placeholder(size) {
		c.log.Info("waiting for the native driver instead of the firmware's placeholder mode", "connector", conn.Name(), "size", size)
		return nil
	}
	rot, err := c.orientation(props)
	if err != nil {
		return err
	}
	o := &Output{
		card:      c,
		name:      conn.Name(),
		connector: connected,
		crtc:      cr,
		plane:     pl,
		mode:      mode,
		rotation:  rot,
		scale:     c.scaler.Scale(size, conn.PhysicalMM, c.firmware),
		modeset:   lit == nil,
		inherited: lit != nil,
	}
	cr.taken, pl.taken = true, true
	if err := o.allocate(); err != nil {
		return errors.Join(err, o.release(false))
	}
	c.outputs = append(c.outputs, o)
	c.log.Info("lit an output", "connector", o.name, "size", size, "scale", o.scale, "rotation", rot, "kept the lit mode", lit != nil)
	return nil
}

// Outputs are the card's lit outputs.
func (c *Card) Outputs() []*Output {
	return slices.Clone(c.outputs)
}

// Flipped hands the card a page-flip event read from its fd.
func (c *Card) Flipped(e drm.FlipEvent) error {
	for _, o := range c.outputs {
		if uint64(o.crtc.id) == e.UserData {
			return o.flipped()
		}
	}
	return nil
}

// Deactivate drops DRM master and stops committing, leaving the last frame
// on screen for the next master.
func (c *Card) Deactivate() error {
	if c.paused {
		return nil
	}
	c.paused = true
	if err := c.dev.DropMaster(); err != nil {
		return fmt.Errorf("kms: %s: drop DRM master: %w", c.dev.Path(), err)
	}
	return nil
}

// Reactivate takes DRM master back and shows the splash again with a
// modeset, since the master in between may have changed the mode.
func (c *Card) Reactivate() error {
	if !c.paused {
		return nil
	}
	if err := c.dev.SetMaster(); err != nil {
		return fmt.Errorf("kms: %s: take DRM master back: %w", c.dev.Path(), err)
	}
	c.paused = false
	var errs []error
	for _, o := range c.outputs {
		o.modeset, o.pending = true, true
		if !o.flipping {
			errs = append(errs, o.flip())
		}
	}
	return errors.Join(errs...)
}

// Release frees every output's buffers. With retain, the framebuffer on
// screen is closed with CLOSEFB so the frame outlives the process; an error
// wrapping drm.ErrNoCloseFB says the kernel could not keep it.
func (c *Card) Release(retain bool) error {
	var errs []error
	for _, o := range c.outputs {
		errs = append(errs, o.release(retain))
	}
	c.outputs = nil
	return errors.Join(errs...)
}

// Output is one connector lit on a CRTC and its primary plane, drawn into an
// upright frame in memory and copied, turned, into two scanout buffers.
type Output struct {
	card      *Card
	name      string
	connector connector
	crtc      *crtc
	plane     *plane
	mode      drm.Mode
	modeBlob  uint32
	rotation  Rotation
	scale     int
	frame     Frame
	bufs      [2]buffer
	back      int
	committed bool
	modeset   bool
	inherited bool
	flipping  bool
	pending   bool
	damage    Damage
	clips     [damageBlobs]damageBlob
	commits   uint64
}

type buffer struct {
	dumb  drm.Dumb
	fb    uint32
	stale Damage
}

// damageBlobs is how many sets of damage rectangles an output keeps a blob
// for: the dots step through three, and a blob costs two ioctls to replace.
const damageBlobs = 4

type damageBlob struct {
	id   uint32
	of   Damage
	used uint64
}

func (o *Output) allocate() error {
	var err error
	if o.modeBlob, err = o.card.dev.CreateBlob(o.mode.Bytes()); err != nil {
		return err
	}
	size := modeSize(o.mode)
	for i := range o.bufs {
		b := &o.bufs[i]
		if b.dumb, err = o.card.dev.CreateDumb(size); err != nil {
			return err
		}
		fb := drm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: scanoutFormat} //nolint:gosec // mode sizes are 16-bit
		fb.Handles[0], fb.Pitches[0] = b.dumb.Handle, b.dumb.Pitch
		if b.fb, err = o.card.dev.AddFB2(fb); err != nil {
			return err
		}
	}
	view := o.rotation.View(size)
	stride := view.X * BytesPerPixel
	o.frame = Frame{Pix: make([]byte, stride*view.Y), Stride: stride, Size: view}
	return nil
}

func (o *Output) release(retain bool) error {
	dev := o.card.dev
	var errs []error
	front := 1 - o.back
	for i := range o.bufs {
		b := &o.bufs[i]
		if b.fb != 0 {
			if retain && o.committed && i == front {
				errs = append(errs, dev.CloseFB(b.fb))
			} else {
				errs = append(errs, dev.RemoveFB(b.fb))
			}
			b.fb = 0
		}
		if b.dumb.Pix != nil {
			errs = append(errs, dev.DestroyDumb(b.dumb))
			b.dumb = drm.Dumb{}
		}
	}
	if o.modeBlob != 0 {
		errs = append(errs, dev.DestroyBlob(o.modeBlob))
		o.modeBlob = 0
	}
	for i := range o.clips {
		if id := o.clips[i].id; id != 0 {
			errs = append(errs, dev.DestroyBlob(id))
			o.clips[i] = damageBlob{}
		}
	}
	o.crtc.taken, o.plane.taken = false, false
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("kms: %s: release %s: %w", dev.Path(), o.name, err)
	}
	return nil
}

// Name is the connector's name, such as "eDP-1".
func (o *Output) Name() string {
	return o.name
}

// Scale is the output's scale, 1 or 2.
func (o *Output) Scale() int {
	return o.scale
}

// Rotation is how the output's frame is turned onto its panel.
func (o *Output) Rotation() Rotation {
	return o.rotation
}

// Frame is the upright frame to draw into, kept between frames so that a
// redraw only touches what changed. Present shows it.
func (o *Output) Frame() Frame {
	return o.frame
}

// Present shows the frame, of which only damage changed since the last
// Present. A frame presented while the previous one still waits for its
// vblank goes out on that vblank's flip event, so frames flip at most once
// per vblank.
func (o *Output) Present(damage Damage) error {
	damage.Intersect(image.Rectangle{Max: o.frame.Size})
	for i := range o.bufs {
		o.bufs[i].stale.Merge(&damage)
	}
	o.damage.Merge(&damage)
	o.pending = true
	if o.flipping || o.card.paused {
		return nil
	}
	return o.flip()
}

func (o *Output) flipped() error {
	o.flipping = false
	if !o.pending || o.card.paused {
		return nil
	}
	return o.flip()
}

func (o *Output) flip() error {
	b := &o.bufs[o.back]
	scanout := Frame{Pix: b.dumb.Pix, Stride: int(b.dumb.Pitch), Size: modeSize(o.mode)}
	for _, r := range b.stale.Rects() {
		Blit(scanout, o.frame, r, o.rotation)
	}
	b.stale = Damage{}
	clips, err := o.damageClips()
	if err != nil {
		return &FlipError{Output: o, Err: err}
	}
	if o.inherited && !o.committed {
		o.request(b.fb, clips)
		if err := o.card.dev.Commit(&o.card.req, drm.AtomicTestOnly, 0); err != nil {
			o.card.log.Info("the lit mode needs a modeset after all", "connector", o.name, "err", err)
			o.modeset = true
		}
	}
	o.request(b.fb, clips)
	flags := uint32(drm.AtomicNonblock | drm.AtomicPageFlipEvent)
	if o.modeset {
		flags |= drm.AtomicAllowModeset
	}
	if err := o.card.dev.Commit(&o.card.req, flags, uint64(o.crtc.id)); err != nil {
		return &FlipError{Output: o, Err: fmt.Errorf("kms: %s: commit a frame to %s: %w", o.card.dev.Path(), o.name, err)}
	}
	o.committed, o.modeset, o.flipping, o.pending = true, false, true, false
	o.damage = Damage{}
	o.back = 1 - o.back
	return nil
}

// FlipError is a frame of Output that did not reach the display. The output
// keeps it, and its next Present sends it.
type FlipError struct {
	Output *Output
	Err    error
}

func (e *FlipError) Error() string {
	return e.Err.Error()
}

func (e *FlipError) Unwrap() error {
	return e.Err
}

// damageClips is the blob of the damage rectangles for this commit, kept for
// the next time the damage is the same: an animation damages the same few sets
// of rectangles over and over.
func (o *Output) damageClips() (uint32, error) {
	if o.plane.damageClips == 0 || o.damage.Empty() || o.modeset {
		return 0, nil
	}
	var device Damage
	for _, r := range o.damage.Rects() {
		device.Add(o.rotation.Device(o.frame.Size, r))
	}
	o.commits++
	oldest := 0
	for i := range o.clips {
		c := &o.clips[i]
		if c.id != 0 && c.of == device {
			c.used = o.commits
			return c.id, nil
		}
		if c.used < o.clips[oldest].used {
			oldest = i
		}
	}
	return o.keepClips(oldest, &device)
}

func (o *Output) keepClips(slot int, device *Damage) (uint32, error) {
	rects := device.Rects()
	data := make([]byte, 0, len(rects)*16)
	for _, r := range rects {
		for _, v := range []int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y} {
			data = binary.NativeEndian.AppendUint32(data, intconv.Uint32Bits(intconv.Must[int32](v)))
		}
	}
	id, err := o.card.dev.CreateBlob(data)
	if err != nil {
		return 0, fmt.Errorf("kms: %s: damage clips for %s: %w", o.card.dev.Path(), o.name, err)
	}
	c := &o.clips[slot]
	if c.id != 0 {
		if derr := o.card.dev.DestroyBlob(c.id); derr != nil {
			o.card.log.Warn("destroying the damage clips", "connector", o.name, "err", derr)
		}
	}
	*c = damageBlob{id: id, of: *device, used: o.commits}
	return id, nil
}

func (o *Output) request(fb, clips uint32) {
	r := &o.card.req
	r.Reset()
	if o.modeset {
		r.Add(o.connector.id, o.connector.crtc, uint64(o.crtc.id))
		r.Add(o.crtc.id, o.crtc.mode, uint64(o.modeBlob))
		r.Add(o.crtc.id, o.crtc.active, 1)
	}
	size := modeSize(o.mode)
	w, h := uint64(size.X), uint64(size.Y) //nolint:gosec // mode sizes are 16-bit
	p := o.plane
	r.Add(p.id, p.fb, uint64(fb))
	r.Add(p.id, p.crtc, uint64(o.crtc.id))
	r.Add(p.id, p.srcX, 0)
	r.Add(p.id, p.srcY, 0)
	r.Add(p.id, p.srcW, w<<16)
	r.Add(p.id, p.srcH, h<<16)
	r.Add(p.id, p.crtcX, 0)
	r.Add(p.id, p.crtcY, 0)
	r.Add(p.id, p.crtcW, w)
	r.Add(p.id, p.crtcH, h)
	if clips != 0 {
		r.Add(p.id, p.damageClips, uint64(clips))
	}
}
