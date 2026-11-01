package kms

import (
	"encoding/binary"
	"errors"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"
)

var quiet = slog.New(slog.DiscardHandler)

func TestChooseMode(t *testing.T) {
	t.Parallel()
	small, big, other := mode(1280, 720, false), mode(1920, 1080, true), mode(1024, 768, false)
	tests := []struct {
		name  string
		modes []drm.Mode
		lit   *drm.Mode
		want  drm.Mode
	}{
		{"keeps the lit mode", []drm.Mode{small, big}, &other, other},
		{"preferred", []drm.Mode{small, big}, nil, big},
		{"first without a preferred one", []drm.Mode{small, other}, nil, small},
	}
	for _, tc := range tests {
		got, err := chooseMode(tc.modes, tc.lit)
		if err != nil || got != tc.want {
			t.Errorf("%s: chooseMode = %v, %v; want %v", tc.name, modeSize(got), err, modeSize(tc.want))
		}
	}
	if _, err := chooseMode(nil, nil); !errors.Is(err, errNoMode) {
		t.Errorf("chooseMode of no modes = %v, want errNoMode", err)
	}
}

func TestPlaceholder(t *testing.T) {
	t.Parallel()
	for _, s := range []image.Point{{640, 480}, {800, 600}, {1024, 768}} {
		if !placeholder(s) {
			t.Errorf("%v is not a placeholder", s)
		}
	}
	for _, s := range []image.Point{{1920, 1080}, {1280, 800}, {768, 1024}} {
		if placeholder(s) {
			t.Errorf("%v is a placeholder", s)
		}
	}
}

func TestScale(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size image.Point
		mm   [2]uint32
		want int
	}{
		{"13 inch 2560x1600 laptop", image.Pt(2560, 1600), [2]uint32{286, 179}, 2},
		{"24 inch 1080p monitor", image.Pt(1920, 1080), [2]uint32{531, 299}, 1},
		{"27 inch 4K monitor", image.Pt(3840, 2160), [2]uint32{597, 336}, 1},
		{"15.6 inch 4K laptop", image.Pt(3840, 2160), [2]uint32{344, 194}, 2},
		{"65 inch 4K television", image.Pt(3840, 2160), [2]uint32{1428, 803}, 1},
		{"aspect ratio for a size", image.Pt(3840, 2160), [2]uint32{160, 90}, 1},
		{"no size", image.Pt(3840, 2160), [2]uint32{0, 0}, 1},
	}
	for _, tc := range tests {
		var s Scaler
		if got := s.Scale(tc.size, tc.mm, false); got != tc.want {
			t.Errorf("%s: Scale = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestFirmwareGuessKeepsTheScale(t *testing.T) {
	t.Parallel()
	tv := [2]uint32{1428, 803}
	var s Scaler
	if got := s.Scale(image.Pt(3840, 2160), [2]uint32{1015, 571}, true); got != 2 {
		t.Fatalf("simpledrm at 96 dpi guessed scale %d, want 2", got)
	}
	if got := s.Scale(image.Pt(3840, 2160), tv, false); got != 2 {
		t.Errorf("the native driver after a guess chose %d, want the guessed 2", got)
	}
	var fresh Scaler
	if got := fresh.Scale(image.Pt(3840, 2160), tv, false); got != 1 {
		t.Errorf("the native driver without a guess chose %d, want 1", got)
	}
	var known Scaler
	if got := known.Scale(image.Pt(2560, 1600), [2]uint32{286, 179}, true); got != 2 || known.guessed != 0 {
		t.Errorf("simpledrm knowing its size chose %d (guessed %d), want 2 from the size", got, known.guessed)
	}
	if got := s.Scale(image.Pt(3000, 2000), [2]uint32{0, 0}, false); got != 2 {
		t.Errorf("a 3:2 guess chose %d, want 2", got)
	}
}

func TestFirmwareGuessKeepsTheValueAcrossAModeChange(t *testing.T) {
	t.Parallel()
	var s Scaler
	if got := s.Scale(image.Pt(1280, 800), [2]uint32{339, 212}, true); got != 1 {
		t.Fatalf("simpledrm at 1280x800 guessed scale %d, want 1", got)
	}
	if got := s.Scale(image.Pt(1800, 1200), [2]uint32{457, 305}, false); got != 1 {
		t.Errorf("the native driver at 1800x1200 chose %d, want simpledrm's 1", got)
	}
	var hidpi Scaler
	if got := hidpi.Scale(image.Pt(3840, 2160), [2]uint32{1015, 571}, true); got != 2 {
		t.Fatalf("simpledrm at 3840x2160 guessed scale %d, want 2", got)
	}
	if got := hidpi.Scale(image.Pt(1920, 1080), [2]uint32{0, 0}, false); got != 2 {
		t.Errorf("the native driver at 1920x1080 chose %d, want simpledrm's 2", got)
	}
}

func TestParseUevent(t *testing.T) {
	t.Parallel()
	add := "add@/devices/pci0000:00/0000:00:02.0/drm/card1\x00ACTION=add\x00DEVPATH=/devices/pci0000:00/0000:00:02.0/drm/card1\x00SUBSYSTEM=drm\x00MAJOR=226\x00MINOR=1\x00DEVNAME=dri/card1\x00DEVTYPE=drm_minor\x00SEQNUM=2345\x00"
	u, err := ParseUevent([]byte(add))
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := u.Card(); u.Action != Add || !ok || name != "card1" || u.Hotplug {
		t.Errorf("ParseUevent(add) = %+v, card %q %v", u, name, ok)
	}
	hotplug := "change@/devices/pci0000:00/0000:00:02.0/drm/card1\x00ACTION=change\x00DEVPATH=/devices/pci0000:00/0000:00:02.0/drm/card1\x00SUBSYSTEM=drm\x00HOTPLUG=1\x00DEVNAME=dri/card1\x00"
	if u, err := ParseUevent([]byte(hotplug)); err != nil || u.Action != Change || !u.Hotplug {
		t.Errorf("ParseUevent(hotplug) = %+v, %v", u, err)
	}
	render := "add@/devices/x/drm/renderD128\x00ACTION=add\x00SUBSYSTEM=drm\x00DEVNAME=dri/renderD128\x00"
	if u, err := ParseUevent([]byte(render)); err != nil {
		t.Error(err)
	} else if _, ok := u.Card(); ok {
		t.Error("a render node counts as a card")
	}
	input := "add@/devices/x/input/input3\x00ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=dri/card9\x00"
	if u, err := ParseUevent([]byte(input)); err != nil {
		t.Error(err)
	} else if _, ok := u.Card(); ok {
		t.Error("a non-drm device counts as a card")
	}
	bad := map[string]string{
		"no header":       "ACTION=add\x00SUBSYSTEM=drm\x00",
		"empty action":    "@/devices/x\x00",
		"action mismatch": "add@/devices/x\x00ACTION=remove\x00",
		"path mismatch":   "add@/devices/x\x00DEVPATH=/devices/y\x00",
		"bare field":      "add@/devices/x\x00SUBSYSTEM\x00",
	}
	for name, msg := range bad {
		if u, err := ParseUevent([]byte(msg)); err == nil {
			t.Errorf("%s: ParseUevent = %+v, want an error", name, u)
		}
	}
	if _, err := ParseUevent([]byte("libudev\x00\xfe\xed\xca\xfe")); !errors.Is(err, errUdevMessage) {
		t.Errorf("a udev message parsed as %v", err)
	}
}

func TestIsCard(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{"card0": true, "card12": true, "card": false, "card0-eDP-1": false, "renderD128": false, "cardx": false} {
		if got := isCard(name); got != want {
			t.Errorf("isCard(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRotationMapsPixelsAndRectangles(t *testing.T) {
	t.Parallel()
	view := image.Pt(5, 3)
	for _, rot := range []Rotation{Upright, UpsideDown, Clockwise, CounterClockwise} {
		dev := rot.View(view)
		if rot.sideways() != (dev != view) {
			t.Errorf("%v: device size %v for view %v", rot, dev, view)
		}
		for y := range view.Y {
			for x := range view.X {
				px, py := rot.pixel(view, x, y)
				want := image.Rect(px, py, px+1, py+1)
				if got := rot.Device(view, image.Rect(x, y, x+1, y+1)); got != want {
					t.Errorf("%v: pixel (%d,%d) lands on %v, its rectangle on %v", rot, x, y, want, got)
				}
				if !want.In(image.Rectangle{Max: dev}) {
					t.Errorf("%v: pixel (%d,%d) lands outside the device at %v", rot, x, y, want)
				}
			}
		}
		if got := rot.Device(view, image.Rect(1, 0, 3, 2)); got.Dx()*got.Dy() != 4 || !got.In(image.Rectangle{Max: dev}) {
			t.Errorf("%v: a 2x2 rectangle maps to %v", rot, got)
		}
	}
}

func TestRotationOf(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]Rotation{"Normal": Upright, "Upside Down": UpsideDown, "Left Side Up": CounterClockwise, "Right Side Up": Clockwise} {
		if got, err := rotationOf(name); err != nil || got != want {
			t.Errorf("rotationOf(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	if _, err := rotationOf("Sideways"); err == nil {
		t.Error("an unknown orientation parsed")
	}
}

func frameOf(size image.Point) Frame {
	return Frame{Pix: make([]byte, size.X*size.Y*4), Stride: size.X * 4, Size: size}
}

func pixelAt(f Frame, x, y int) uint32 {
	return binary.LittleEndian.Uint32(f.Pix[y*f.Stride+x*4:])
}

func setPixel(f Frame, x, y int, v uint32) {
	binary.LittleEndian.PutUint32(f.Pix[y*f.Stride+x*4:], v)
}

func TestBlitCopiesOnlyTheRectangleTurned(t *testing.T) {
	t.Parallel()
	view := image.Pt(4, 3)
	for _, rot := range []Rotation{Upright, UpsideDown, Clockwise, CounterClockwise} {
		src := frameOf(view)
		for y := range view.Y {
			for x := range view.X {
				setPixel(src, x, y, uint32(1+y*view.X+x))
			}
		}
		dst := frameOf(rot.View(view))
		r := image.Rect(1, 1, 3, 3)
		Blit(dst, src, r, rot)
		for y := range view.Y {
			for x := range view.X {
				dx, dy := rot.pixel(view, x, y)
				want := uint32(0)
				if image.Pt(x, y).In(r) {
					want = pixelAt(src, x, y)
				}
				if got := pixelAt(dst, dx, dy); got != want {
					t.Errorf("%v: device (%d,%d) = %d, want %d", rot, dx, dy, got, want)
				}
			}
		}
	}
}

func openFake(t *testing.T, d *fakeDevice, firmware bool, scaler *Scaler) *Card {
	t.Helper()
	c, err := openCard(d, "card0", firmware, scaler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Rescan(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUnlitConnectorModesetsThePreferredMode(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{531, 299}, mode(1280, 720, false), mode(1920, 1080, true))
	c := openFake(t, d, false, &Scaler{})
	outs := c.Outputs()
	if len(outs) != 1 {
		t.Fatalf("%d outputs, want 1", len(outs))
	}
	o := outs[0]
	if o.Frame().Size != image.Pt(1920, 1080) || o.Scale() != 1 || o.Name() != "HDMI-A-40" {
		t.Errorf("output %s: frame %v, scale %d", o.Name(), o.Frame().Size, o.Scale())
	}
	if len(d.commits) != 0 {
		t.Fatalf("%d commits before the first Present", len(d.commits))
	}
	if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
		t.Fatal(err)
	}
	cs := d.real()
	if len(cs) != 1 || cs[0].flags&drm.AtomicAllowModeset == 0 || cs[0].user != 10 {
		t.Fatalf("commits = %+v, want one modeset for CRTC 10", cs)
	}
	if got := cs[0].values[40]["CRTC_ID"]; got != 10 {
		t.Errorf("connector routed to CRTC %d", got)
	}
	if m, err := drm.ParseMode(d.blobs[uint32(cs[0].values[10]["MODE_ID"])]); err != nil || modeSize(m) != image.Pt(1920, 1080) {
		t.Errorf("modeset to %v, %v", modeSize(m), err)
	}
	if _, ok := cs[0].values[20]["FB_DAMAGE_CLIPS"]; ok {
		t.Error("a modeset carries damage clips")
	}
}

func TestLitCRTCKeepsItsModeWithoutModeset(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 2)
	firmware := mode(1366, 768, false)
	d.connect(40, [2]uint32{344, 194}, mode(1920, 1080, true))
	d.light(40, 11, firmware)
	c := openFake(t, d, false, &Scaler{})
	o := c.Outputs()[0]
	if o.Frame().Size != image.Pt(1366, 768) {
		t.Errorf("frame %v, want the lit 1366x768", o.Frame().Size)
	}
	if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
		t.Fatal(err)
	}
	cs := d.real()
	if len(cs) != 1 || cs[0].flags&drm.AtomicAllowModeset != 0 || cs[0].user != 11 {
		t.Fatalf("commits = %+v, want one flip on CRTC 11 without a modeset", cs)
	}
}

func TestLitModeRefusedFallsBackToModeset(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{344, 194}, mode(1920, 1080, true))
	d.light(40, 10, mode(1366, 768, false))
	d.refuse = func(flags uint32) error {
		if flags&drm.AtomicAllowModeset == 0 {
			return errors.New("needs a modeset")
		}
		return nil
	}
	o := openFake(t, d, false, &Scaler{}).Outputs()[0]
	if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
		t.Fatal(err)
	}
	if cs := d.real(); len(cs) != 1 || cs[0].flags&drm.AtomicAllowModeset == 0 {
		t.Fatalf("commits = %+v, want the modeset after the refused test", cs)
	}
}

func TestFirmwarePlaceholderIsSkipped(t *testing.T) {
	t.Parallel()
	for _, firmware := range []bool{true, false} {
		d := newFakeDevice("/dev/dri/card0", 1)
		d.connect(40, [2]uint32{0, 0}, mode(1024, 768, true))
		d.light(40, 10, mode(1024, 768, true))
		c := openFake(t, d, firmware, &Scaler{})
		if got := len(c.Outputs()); got != map[bool]int{true: 0, false: 1}[firmware] {
			t.Errorf("firmware %v: %d outputs at 1024x768", firmware, got)
		}
	}
}

func TestPanelOrientationTurnsTheFrame(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	conn := d.connect(40, [2]uint32{135, 217}, mode(800, 1280, true))
	conn.props["panel orientation"] = 3
	o := openFake(t, d, false, &Scaler{}).Outputs()[0]
	if o.Rotation() != Clockwise || o.Frame().Size != image.Pt(1280, 800) {
		t.Fatalf("rotation %v frame %v, want clockwise 1280x800", o.Rotation(), o.Frame().Size)
	}
	setPixel(o.Frame(), 0, 0, 0xff112233)
	if err := o.Present(image.Rect(0, 0, 1, 1)); err != nil {
		t.Fatal(err)
	}
	fb := d.fbs[uint32(d.real()[0].values[20]["FB_ID"])]
	if fb.Width != 800 || fb.Height != 1280 {
		t.Fatalf("framebuffer %dx%d, want the panel's 800x1280", fb.Width, fb.Height)
	}
	buf := o.bufs[1-o.back].dumb
	dev := Frame{Pix: buf.Pix, Stride: int(buf.Pitch), Size: image.Pt(800, 1280)}
	if got := pixelAt(dev, 799, 0); got != 0xff112233 {
		t.Errorf("the top-left pixel landed elsewhere: device (799,0) = %#x", got)
	}
}

func TestFlipsArePacedAndDamageLimited(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	c := openFake(t, d, false, &Scaler{})
	o := c.Outputs()[0]
	f := o.Frame()
	for i := range f.Pix {
		f.Pix[i] = 0x11
	}
	if err := o.Present(image.Rectangle{Max: f.Size}); err != nil {
		t.Fatal(err)
	}
	if err := c.Flipped(drm.FlipEvent{UserData: 10}); err != nil {
		t.Fatal(err)
	}
	throbber := image.Rect(10, 10, 14, 12)
	setPixel(f, 11, 11, 0xffabcdef)
	if err := o.Present(throbber); err != nil {
		t.Fatal(err)
	}
	if err := o.Present(throbber); err != nil {
		t.Fatal(err)
	}
	cs := d.real()
	if len(cs) != 2 {
		t.Fatalf("%d commits, want 2: the third Present waits for the flip", len(cs))
	}
	clips := d.blobs[uint32(cs[1].values[20]["FB_DAMAGE_CLIPS"])]
	if clips != nil {
		t.Error("the damage clips blob outlived its commit")
	}
	second := o.bufs[1-o.back].dumb
	dev := Frame{Pix: second.Pix, Stride: int(second.Pitch), Size: f.Size}
	if got := pixelAt(dev, 11, 11); got != 0xffabcdef {
		t.Errorf("the throbber pixel is %#x", got)
	}
	if got := pixelAt(dev, 0, 0); got != 0x11111111 {
		t.Errorf("the second buffer was not brought up to date outside the throbber: %#x", got)
	}
	setPixel(f, 40, 20, 0xff000001)
	if err := c.Flipped(drm.FlipEvent{UserData: 10}); err != nil {
		t.Fatal(err)
	}
	if got := len(d.real()); got != 3 {
		t.Fatalf("%d commits after the flip, want the waiting frame committed", got)
	}
	first := o.bufs[1-o.back].dumb
	dev = Frame{Pix: first.Pix, Stride: int(first.Pitch), Size: f.Size}
	if got := pixelAt(dev, 40, 20); got != 0x11111111 {
		t.Errorf("a pixel outside every damage was copied: %#x", got)
	}
	if got := pixelAt(dev, 11, 11); got != 0xffabcdef {
		t.Errorf("the throbber did not reach the first buffer: %#x", got)
	}
}

func TestDamageClipsFollowRotation(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	conn := d.connect(40, [2]uint32{135, 217}, mode(40, 60, true))
	conn.props["panel orientation"] = 1
	c := openFake(t, d, false, &Scaler{})
	o := c.Outputs()[0]
	if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
		t.Fatal(err)
	}
	if err := c.Flipped(drm.FlipEvent{UserData: 10}); err != nil {
		t.Fatal(err)
	}
	if err := o.Present(image.Rect(0, 0, 10, 5)); err != nil {
		t.Fatal(err)
	}
	clip := d.created[uint32(d.real()[1].values[20]["FB_DAMAGE_CLIPS"])]
	if len(clip) != 16 {
		t.Fatalf("damage clips blob of %d bytes", len(clip))
	}
	for i, want := range []int32{30, 55, 40, 60} {
		if got := int32(binary.NativeEndian.Uint32(clip[i*4:])); got != want {
			t.Errorf("clip[%d] = %d, want %d", i, got, want)
		}
	}
}

func TestDeactivateAndReactivate(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	c := openFake(t, d, false, &Scaler{})
	o := c.Outputs()[0]
	if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
		t.Fatal(err)
	}
	if err := c.Flipped(drm.FlipEvent{UserData: 10}); err != nil {
		t.Fatal(err)
	}
	if err := c.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if d.master {
		t.Error("still master after Deactivate")
	}
	if err := o.Present(image.Rect(0, 0, 4, 4)); err != nil {
		t.Fatal(err)
	}
	if got := len(d.real()); got != 1 {
		t.Fatalf("%d commits while deactivated", got)
	}
	if err := c.Reactivate(); err != nil {
		t.Fatal(err)
	}
	cs := d.real()
	if !d.master || len(cs) != 2 || cs[1].flags&drm.AtomicAllowModeset == 0 {
		t.Fatalf("after Reactivate: master %v, commits %+v; want master and a modeset", d.master, cs)
	}
	d.masterErr = errors.New("busy")
	if err := c.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Reactivate(); err == nil {
		t.Error("Reactivate succeeded without master")
	}
}

func TestReleaseRetainsTheFrameOnScreen(t *testing.T) {
	t.Parallel()
	for _, retain := range []bool{true, false} {
		d := newFakeDevice("/dev/dri/card0", 1)
		d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
		c := openFake(t, d, false, &Scaler{})
		o := c.Outputs()[0]
		if err := o.Present(image.Rectangle{Max: o.Frame().Size}); err != nil {
			t.Fatal(err)
		}
		shown := uint32(d.real()[0].values[20]["FB_ID"])
		if err := c.Release(retain); err != nil {
			t.Fatal(err)
		}
		if retain && !slices.Equal(d.closedFBs, []uint32{shown}) {
			t.Errorf("retain: closed %v, want only the shown %d", d.closedFBs, shown)
		}
		if !retain && (len(d.closedFBs) != 0 || !slices.Contains(d.removed, shown)) {
			t.Errorf("no retain: closed %v, removed %v", d.closedFBs, d.removed)
		}
		if len(d.fbs) != 0 || d.destroyed != 2 || len(d.blobs) != 0 {
			t.Errorf("retain %v left framebuffers %v, %d dumbs destroyed, blobs %v", retain, d.fbs, d.destroyed, d.blobs)
		}
	}
}

func TestRescanFollowsHotplug(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 2)
	d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	c := openFake(t, d, false, &Scaler{})
	d.connect(41, [2]uint32{531, 299}, mode(32, 16, true))
	d.connectors[40].conn.Connection = drm.Disconnected
	if err := c.Rescan(); err != nil {
		t.Fatal(err)
	}
	outs := c.Outputs()
	if len(outs) != 1 || outs[0].connector.id != 41 {
		t.Fatalf("outputs after hotplug: %v", outs)
	}
	if outs[0].crtc.id != 10 {
		t.Errorf("the new output did not reuse the freed CRTC: %d", outs[0].crtc.id)
	}
	delete(d.connectors, 41)
	if err := c.Rescan(); err != nil {
		t.Fatal(err)
	}
	if len(c.Outputs()) != 0 {
		t.Error("an unplugged connector kept its output")
	}
}

func TestTooFewCRTCsLightsWhatFits(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	d.connect(41, [2]uint32{531, 299}, mode(64, 32, true))
	c, err := openCard(d, "card0", false, &Scaler{}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Rescan(); err == nil {
		t.Error("lighting two connectors on one CRTC reported no error")
	}
	if got := len(c.Outputs()); got != 1 {
		t.Errorf("%d outputs, want the one that fits", got)
	}
}

func TestOpenCardWithoutMaster(t *testing.T) {
	t.Parallel()
	d := newFakeDevice("/dev/dri/card0", 1)
	d.masterErr = errors.New("busy")
	if _, err := openCard(d, "card0", false, &Scaler{}, quiet); err == nil {
		t.Error("openCard succeeded without master")
	}
}

func fakeDisplay(t *testing.T, devs map[string]*fakeDevice, drivers map[string]string) (*Display, Paths) {
	t.Helper()
	dir := t.TempDir()
	paths := Paths{Dev: filepath.Join(dir, "dri"), Sysfs: filepath.Join(dir, "sys")}
	for _, p := range []string{paths.Dev, paths.Sysfs} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name := range devs {
		if err := os.WriteFile(filepath.Join(paths.Dev, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(paths.Dev, "renderD128"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, driver := range drivers {
		dev := filepath.Join(paths.Sysfs, name, "device")
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../../bus/platform/drivers/"+driver, filepath.Join(dev, "driver")); err != nil {
			t.Fatal(err)
		}
	}
	disp, err := open(paths, func(path string) (device, error) {
		d, ok := devs[filepath.Base(path)]
		if !ok {
			return nil, os.ErrNotExist
		}
		return d, nil
	}, false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return disp, paths
}

func switchDrivers(t *testing.T, simple, native *fakeDevice, firmwareScale int) *Display {
	t.Helper()
	devs := map[string]*fakeDevice{"card0": simple}
	disp, paths := fakeDisplay(t, devs, map[string]string{"card0": "simple-framebuffer"})
	outs := disp.Outputs()
	if len(outs) != 1 || outs[0].Scale() != firmwareScale || !outs[0].card.firmware {
		t.Fatalf("outputs on simpledrm: %v, want one at scale %d", outs, firmwareScale)
	}
	remove := "remove@/devices/platform/simple-framebuffer.0/drm/card0\x00ACTION=remove\x00SUBSYSTEM=drm\x00DEVNAME=dri/card0\x00"
	if err := disp.Handle(Event{uevent: []byte(remove)}); err != nil {
		t.Fatal(err)
	}
	if len(disp.Outputs()) != 0 || !simple.closed {
		t.Fatal("the removed simpledrm card is still open")
	}
	devs["card1"] = native
	if err := os.WriteFile(filepath.Join(paths.Dev, "card1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	add := "add@/devices/pci0000:00/0000:00:02.0/drm/card1\x00ACTION=add\x00SUBSYSTEM=drm\x00DEVNAME=dri/card1\x00"
	if err := disp.Handle(Event{uevent: []byte(add)}); err != nil {
		t.Fatal(err)
	}
	return disp
}

func TestDisplayFollowsTheDriverSwitch(t *testing.T) {
	t.Parallel()
	simple := newFakeDevice("/dev/dri/card0", 1)
	simple.connect(40, [2]uint32{1015, 571}, mode(3840, 2160, true))
	simple.light(40, 10, mode(3840, 2160, true))
	native := newFakeDevice("/dev/dri/card1", 1)
	native.connect(41, [2]uint32{1428, 803}, mode(3840, 2160, true))
	disp := switchDrivers(t, simple, native, 2)
	outs := disp.Outputs()
	if len(outs) != 1 || outs[0].card.firmware || outs[0].Scale() != 2 {
		t.Fatalf("outputs on the native driver: %v, want one at simpledrm's scale 2", outs)
	}
	if err := disp.Handle(Event{card: outs[0].card, flip: drm.FlipEvent{UserData: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := disp.Close(true); err != nil {
		t.Fatal(err)
	}
	if !native.closed {
		t.Error("Close left the native card open")
	}
}

func TestDriverSwitchToAnotherModeKeepsTheScale(t *testing.T) {
	t.Parallel()
	simple := newFakeDevice("/dev/dri/card0", 1)
	simple.connect(40, [2]uint32{339, 212}, mode(1280, 800, true))
	simple.light(40, 10, mode(1280, 800, true))
	native := newFakeDevice("/dev/dri/card1", 1)
	native.connect(41, [2]uint32{457, 305}, mode(1800, 1200, true), mode(1280, 800, false))
	disp := switchDrivers(t, simple, native, 1)
	outs := disp.Outputs()
	if len(outs) != 1 || outs[0].Frame().Size != image.Pt(1800, 1200) || outs[0].Scale() != 1 {
		t.Fatalf("outputs on the native driver: %v, want one at 1800x1200 with simpledrm's scale 1", outs)
	}
	if err := disp.Close(false); err != nil {
		t.Fatal(err)
	}
}

func TestDisplayWithoutCards(t *testing.T) {
	t.Parallel()
	disp, _ := fakeDisplay(t, map[string]*fakeDevice{}, nil)
	if len(disp.Outputs()) != 0 {
		t.Error("outputs without a card")
	}
	if err := disp.Close(false); err != nil {
		t.Fatal(err)
	}
	missing, err := open(Paths{Dev: filepath.Join(t.TempDir(), "none"), Sysfs: t.TempDir()}, nil, false, quiet)
	if err != nil || len(missing.Outputs()) != 0 {
		t.Errorf("a missing /dev/dri: %v, %v", missing, err)
	}
}

func TestDisplayDeactivatesNewCards(t *testing.T) {
	t.Parallel()
	a := newFakeDevice("/dev/dri/card0", 1)
	a.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	devs := map[string]*fakeDevice{"card0": a}
	disp, paths := fakeDisplay(t, devs, nil)
	if err := disp.Deactivate(); err != nil {
		t.Fatal(err)
	}
	b := newFakeDevice("/dev/dri/card1", 1)
	b.connect(41, [2]uint32{531, 299}, mode(64, 32, true))
	devs["card1"] = b
	if err := os.WriteFile(filepath.Join(paths.Dev, "card1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := disp.Handle(Event{uevent: []byte("add@/x/card1\x00SUBSYSTEM=drm\x00DEVNAME=dri/card1\x00")}); err != nil {
		t.Fatal(err)
	}
	if a.master || b.master {
		t.Errorf("master while deactivated: card0 %v, card1 %v", a.master, b.master)
	}
	if err := disp.Reactivate(); err != nil {
		t.Fatal(err)
	}
	if !a.master || !b.master || len(disp.Outputs()) != 2 {
		t.Errorf("after Reactivate: master %v %v, %d outputs", a.master, b.master, len(disp.Outputs()))
	}
}

func (r Rotation) pixel(view image.Point, x, y int) (int, int) {
	switch r {
	case UpsideDown:
		return view.X - 1 - x, view.Y - 1 - y
	case Clockwise:
		return view.Y - 1 - y, x
	case CounterClockwise:
		return y, view.X - 1 - x
	}
	return x, y
}

func TestBlitTurnsARectangleSpanningTiles(t *testing.T) {
	t.Parallel()
	view := image.Pt(2*blitTile+7, blitTile+13)
	r := image.Rect(3, 5, 2*blitTile+4, blitTile+11)
	for _, rot := range []Rotation{UpsideDown, Clockwise, CounterClockwise} {
		src := frameOf(view)
		for y := range view.Y {
			for x := range view.X {
				setPixel(src, x, y, uint32(1+y*view.X+x))
			}
		}
		dst := frameOf(rot.View(view))
		Blit(dst, src, r, rot)
		for y := range view.Y {
			for x := range view.X {
				dx, dy := rot.pixel(view, x, y)
				want := uint32(0)
				if image.Pt(x, y).In(r) {
					want = pixelAt(src, x, y)
				}
				if got := pixelAt(dst, dx, dy); got != want {
					t.Fatalf("%v: device (%d,%d) = %d, want %d", rot, dx, dy, got, want)
				}
			}
		}
	}
}

func busyFake() (*fakeDevice, map[string]*fakeDevice) {
	d := newFakeDevice("/dev/dri/card0", 1)
	d.connect(40, [2]uint32{531, 299}, mode(64, 32, true))
	d.masterErr = unix.EBUSY
	return d, map[string]*fakeDevice{"card0": d}
}

func nextEvent(t *testing.T, disp *Display) Event {
	t.Helper()
	select {
	case e := <-disp.Events():
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event to try the busy card again")
		return Event{}
	}
}

func assertNoEvent(t *testing.T, disp *Display) {
	t.Helper()
	select {
	case e := <-disp.Events():
		t.Fatalf("an event after the display stopped trying: %+v", e)
	case <-time.After(5 * busyRetryEvery):
	}
}

func TestDisplayLightsACardOnceItsMasterLetsGo(t *testing.T) {
	t.Parallel()
	d, devs := busyFake()
	disp, _ := fakeDisplay(t, devs, nil)
	if len(disp.Outputs()) != 0 {
		t.Fatal("a busy card has outputs")
	}
	if err := disp.Handle(nextEvent(t, disp)); err != nil {
		t.Fatal(err)
	}
	if len(disp.Outputs()) != 0 {
		t.Fatal("a card still busy has outputs")
	}
	e := nextEvent(t, disp)
	d.masterErr = nil
	if err := disp.Handle(e); err != nil {
		t.Fatal(err)
	}
	if !d.master || len(disp.Outputs()) != 1 {
		t.Errorf("after the master let go: master %v, %d outputs", d.master, len(disp.Outputs()))
	}
	assertNoEvent(t, disp)
}

func TestDisplayGivesUpOnACardThatStaysBusy(t *testing.T) {
	t.Parallel()
	_, devs := busyFake()
	disp, _ := fakeDisplay(t, devs, nil)
	e := nextEvent(t, disp)
	e.until = time.Now().Add(-time.Second)
	if err := disp.Handle(e); err != nil {
		t.Fatal(err)
	}
	if len(disp.Outputs()) != 0 {
		t.Errorf("after giving up: %d outputs", len(disp.Outputs()))
	}
	assertNoEvent(t, disp)
}

func TestDisplayStopsTryingABusyCardWhileDeactivated(t *testing.T) {
	t.Parallel()
	_, devs := busyFake()
	disp, _ := fakeDisplay(t, devs, nil)
	if err := disp.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if err := disp.Handle(nextEvent(t, disp)); err != nil {
		t.Fatal(err)
	}
	assertNoEvent(t, disp)
}

func TestDisplayDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()
	d, devs := busyFake()
	d.masterErr = unix.EPERM
	disp, _ := fakeDisplay(t, devs, nil)
	if len(disp.Outputs()) != 0 {
		t.Fatal("a card without master has outputs")
	}
	assertNoEvent(t, disp)
}
