package kms

import (
	"image"
	"testing"
)

func BenchmarkBlitFullFrame(b *testing.B) {
	view := image.Pt(2560, 1600)
	src := frameOf(view)
	for _, rot := range []Rotation{Upright, UpsideDown, Clockwise, CounterClockwise} {
		dst := frameOf(rot.View(view))
		b.Run(rot.String(), func(b *testing.B) {
			b.SetBytes(int64(len(src.Pix)))
			for b.Loop() {
				Blit(dst, src, image.Rectangle{Max: view}, rot)
			}
		})
	}
}
