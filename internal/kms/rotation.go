package kms

import (
	"encoding/binary"
	"fmt"
	"image"
)

// Rotation is how a frame is turned on its way to a panel, so that a panel
// mounted sideways or upside down shows it upright.
type Rotation uint8

// The rotations, following the connector's "panel orientation" as plymouth
// maps it: "Upside Down" turns the frame half way, "Left Side Up" counter
// clockwise and "Right Side Up" clockwise.
const (
	Upright Rotation = iota
	UpsideDown
	Clockwise
	CounterClockwise
)

// rotationOf maps a "panel orientation" enumerator name to a Rotation.
func rotationOf(orientation string) (Rotation, error) {
	switch orientation {
	case "Normal":
		return Upright, nil
	case "Upside Down":
		return UpsideDown, nil
	case "Left Side Up":
		return CounterClockwise, nil
	case "Right Side Up":
		return Clockwise, nil
	}
	return Upright, fmt.Errorf("kms: unknown panel orientation %q", orientation)
}

func (r Rotation) String() string {
	switch r {
	case UpsideDown:
		return "upside down"
	case Clockwise:
		return "clockwise"
	case CounterClockwise:
		return "counter clockwise"
	}
	return "upright"
}

func (r Rotation) sideways() bool {
	return r == Clockwise || r == CounterClockwise
}

// View is the size of the upright frame shown on a scanout buffer of size.
func (r Rotation) View(size image.Point) image.Point {
	if r.sideways() {
		return image.Pt(size.Y, size.X)
	}
	return size
}

// Device maps rectangle v of an upright frame of size view to the rectangle
// of the scanout buffer it lands on.
func (r Rotation) Device(view image.Point, v image.Rectangle) image.Rectangle {
	w, h := v.Dx(), v.Dy()
	switch r {
	case UpsideDown:
		return image.Rect(view.X-v.Max.X, view.Y-v.Max.Y, view.X-v.Min.X, view.Y-v.Min.Y)
	case Clockwise:
		x := view.Y - v.Max.Y
		return image.Rect(x, v.Min.X, x+h, v.Min.X+w)
	case CounterClockwise:
		y := view.X - v.Max.X
		return image.Rect(v.Min.Y, y, v.Min.Y+h, y+w)
	}
	return v
}

// Frame is 32-bit pixels in rows: little-endian ARGB8888 or XRGB8888, whose
// bytes are blue, green, red and alpha or padding.
type Frame struct {
	Pix    []byte
	Stride int
	Size   image.Point
}

// BytesPerPixel is the size of a Frame's pixel.
const BytesPerPixel = 4

// Blit copies rectangle r of the upright frame src into dst, turned by rot;
// dst is the scanout buffer, of size rot.View(src.Size) turned back.
func Blit(dst, src Frame, r image.Rectangle, rot Rotation) {
	r = r.Intersect(image.Rectangle{Max: src.Size})
	if r.Empty() {
		return
	}
	if rot == Upright {
		n := r.Dx() * BytesPerPixel
		for y := r.Min.Y; y < r.Max.Y; y++ {
			s := y*src.Stride + r.Min.X*BytesPerPixel
			d := y*dst.Stride + r.Min.X*BytesPerPixel
			copy(dst.Pix[d:d+n], src.Pix[s:s+n])
		}
		return
	}
	d := rot.Device(src.Size, r)
	if !rot.sideways() {
		blitTurned(dst, src, d, rot)
		return
	}
	for ty := d.Min.Y; ty < d.Max.Y; ty += blitTile {
		for tx := d.Min.X; tx < d.Max.X; tx += blitTile {
			tile := image.Rect(tx, ty, tx+blitTile, ty+blitTile).Intersect(d)
			blitTurned(dst, src, tile, rot)
		}
	}
}

const blitTile = 32

func blitTurned(dst, src Frame, d image.Rectangle, rot Rotation) {
	for dy := d.Min.Y; dy < d.Max.Y; dy++ {
		x, y, stepX, stepY := rot.source(src.Size, d.Min.X, dy)
		at, step := y*src.Stride+x*BytesPerPixel, stepY*src.Stride+stepX*BytesPerPixel
		row := dst.Pix[dy*dst.Stride+d.Min.X*BytesPerPixel : dy*dst.Stride+d.Max.X*BytesPerPixel]
		for len(row) >= BytesPerPixel {
			binary.LittleEndian.PutUint32(row, binary.LittleEndian.Uint32(src.Pix[at:]))
			row = row[BytesPerPixel:]
			at += step
		}
	}
}

func (r Rotation) source(view image.Point, dx, dy int) (x, y, stepX, stepY int) {
	switch r {
	case UpsideDown:
		return view.X - 1 - dx, view.Y - 1 - dy, -1, 0
	case Clockwise:
		return dy, view.Y - 1 - dx, 0, -1
	case CounterClockwise:
		return view.X - 1 - dy, dx, 0, 1
	}
	return dx, dy, 1, 0
}
