package theme

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-splash/internal/kms"
)

func BenchmarkThrobberStep(b *testing.B) {
	r, err := NewRenderer(Default(), nil)
	if err != nil {
		b.Fatal(err)
	}
	size := image.Pt(3840, 2160)
	f := kms.Frame{Pix: make([]byte, size.X*size.Y*kms.BytesPerPixel), Stride: size.X * kms.BytesPerPixel, Size: size}
	prev := State{}
	if _, err := r.Draw(f, 2, nil, prev); err != nil {
		b.Fatal(err)
	}
	step := func() {
		cur := State{Phase: (prev.Phase + 1) % Frames}
		if _, err := r.Draw(f, 2, &prev, cur); err != nil {
			b.Fatal(err)
		}
		prev = cur
	}
	for range Frames {
		step()
	}
	b.ReportAllocs()
	for b.Loop() {
		step()
	}
}
