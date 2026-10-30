package kms

import (
	"errors"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/drm"
)

var errNoMode = errors.New("kms: the connector lists no mode")

// chooseMode picks the mode an output shows: the mode its CRTC already shows
// when it is lit, so the first frame needs no modeset, otherwise the sink's
// preferred mode, otherwise its first.
func chooseMode(modes []drm.Mode, lit *drm.Mode) (drm.Mode, error) {
	if lit != nil {
		return *lit, nil
	}
	if len(modes) == 0 {
		return drm.Mode{}, errNoMode
	}
	return modes[max(slices.IndexFunc(modes, drm.Mode.Preferred), 0)], nil
}

func modeSize(m drm.Mode) image.Point {
	return image.Pt(int(m.HDisplay), int(m.VDisplay))
}

// placeholder reports whether a firmware framebuffer's mode is one the
// firmware sets up in place of the panel's own resolution, which plymouth
// does not draw on, waiting for the native driver instead.
func placeholder(size image.Point) bool {
	switch size {
	case image.Pt(640, 480), image.Pt(800, 600), image.Pt(1024, 768):
		return true
	}
	return false
}

// Scaler chooses each output's scale as plymouth does, except that once a
// firmware framebuffer forced a guess, the native driver's outputs keep the
// guessed value even when their mode differs, so the picture does not jump.
type Scaler struct {
	guessed int
}

// Scale is the scale of an output showing size pixels on a display of mm
// millimetres, as the connector reports it from EDID. A firmware framebuffer
// that does not know its display's size guesses from the resolution alone,
// and every later output takes the firmware's guess.
func (s *Scaler) Scale(size image.Point, mm [2]uint32, firmware bool) int {
	if firmware && (s.guessed > 0 || !knowsSize(size, mm)) {
		s.guessed = guessScale(size)
	}
	if s.guessed > 0 {
		return s.guessed
	}
	return physicalScale(size, mm)
}

func knowsSize(size image.Point, mm [2]uint32) bool {
	hdpi := uint64(size.X) * 254 / ((uint64(mm[0]) + 1) * 10) //nolint:gosec // mode sizes are positive
	vdpi := uint64(size.Y) * 254 / ((uint64(mm[1]) + 1) * 10) //nolint:gosec // mode sizes are positive
	return hdpi > 96 && vdpi > 96
}

func guessScale(size image.Point) int {
	w, h := size.X, size.Y
	if h > w {
		w, h = h, w
	}
	if 2*w == 3*h {
		if w >= 1800 && h >= 1200 {
			return 2
		}
		return 1
	}
	if w >= 2880 && h >= 1620 {
		return 2
	}
	return 1
}

func physicalScale(size image.Point, mm [2]uint32) int {
	switch mm {
	case [2]uint32{160, 90}, [2]uint32{160, 100}, [2]uint32{16, 9}, [2]uint32{16, 10}:
		return 1
	}
	if mm[0] == 0 || mm[1] == 0 {
		return 1
	}
	inches := math.Hypot(float64(mm[0]), float64(mm[1])) / 25.4
	dpi := math.Hypot(float64(size.X), float64(size.Y)) / inches
	target := 135.0
	if inches >= 20 {
		target = 110
	}
	if dpi/target > 1.625 {
		return 2
	}
	return 1
}
