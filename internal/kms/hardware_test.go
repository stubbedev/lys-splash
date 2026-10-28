package kms

import (
	"image"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/drm"
)

const vmEnv = "LYS_SPLASH_KMS_VM"

func realDisplay(t *testing.T) *Display {
	t.Helper()
	if os.Getenv(vmEnv) == "" {
		t.Skipf("%s is unset: these tests take every card's DRM master, so only checks.vm-kms runs them", vmEnv)
	}
	d, err := Open(SystemPaths, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Outputs()) == 0 {
		t.Fatal("no output lit on this machine's cards")
	}
	return d
}

func awaitFlip(t *testing.T, d *Display, o *Output) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for o.flipping {
		select {
		case e := <-d.Events():
			if err := d.Handle(e); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatalf("no flip on %s within 5s", o.Name())
		}
	}
}

func fill(f Frame, r image.Rectangle, v byte) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := f.Pix[y*f.Stride+r.Min.X*4 : y*f.Stride+r.Max.X*4]
		for i := range row {
			row[i] = v
		}
	}
}

func planeShows(t *testing.T, dev *drm.Device, o *Output) uint64 {
	t.Helper()
	props, err := dev.Properties(o.plane.id, drm.ObjectPlane)
	if err != nil {
		t.Fatal(err)
	}
	fb, ok := props["FB_ID"]
	if !ok {
		t.Fatalf("plane %d has no FB_ID property", o.plane.id)
	}
	return fb.Value
}

func TestRealCardFlipsDeactivatesAndRetains(t *testing.T) {
	for _, retain := range []bool{true, false} {
		d := realDisplay(t)
		o := d.Outputs()[0]
		f := o.Frame()
		fill(f, image.Rectangle{Max: f.Size}, 0x40)
		if err := o.Present(image.Rectangle{Max: f.Size}); err != nil {
			t.Fatal(err)
		}
		awaitFlip(t, d, o)
		for i := range 3 {
			r := image.Rect(10*i, 10, 10*i+8, 18)
			fill(f, r, 0xff)
			if err := o.Present(r); err != nil {
				t.Fatal(err)
			}
			awaitFlip(t, d, o)
		}
		if err := d.Deactivate(); err != nil {
			t.Fatal(err)
		}
		other, err := drm.Open(o.card.dev.Path())
		if err != nil {
			t.Fatal(err)
		}
		if err := other.SetClientCap(drm.ClientCapAtomic, 1); err != nil {
			t.Fatal(err)
		}
		if err := other.SetMaster(); err != nil {
			t.Fatalf("another file could not take master after Deactivate: %v", err)
		}
		if err := other.DropMaster(); err != nil {
			t.Fatal(err)
		}
		if err := d.Reactivate(); err != nil {
			t.Fatal(err)
		}
		awaitFlip(t, d, o)
		shown := uint64(o.bufs[1-o.back].fb)
		if got := planeShows(t, other, o); got != shown {
			t.Fatalf("the plane shows framebuffer %d, want the splash's %d", got, shown)
		}
		if err := d.Close(retain); err != nil {
			t.Fatal(err)
		}
		want := map[bool]uint64{true: shown, false: 0}[retain]
		if got := planeShows(t, other, o); got != want {
			t.Errorf("retain %v: after Close the plane shows framebuffer %d, want %d", retain, got, want)
		}
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
