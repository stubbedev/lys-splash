package kms

import "image"

const maxDamageRects = 8

// Damage is the rectangles of a frame that changed, kept apart so that what
// lies between them is neither copied nor reported to the kernel. Rectangles
// never overlap; past the capacity the two that grow the least are merged.
type Damage struct {
	rects [maxDamageRects]image.Rectangle
	n     int
}

// DamageOf is the damage of rects.
func DamageOf(rects ...image.Rectangle) Damage {
	var d Damage
	for _, r := range rects {
		d.Add(r)
	}
	return d
}

func area(r image.Rectangle) int {
	return r.Dx() * r.Dy()
}

// Add marks r as changed.
func (d *Damage) Add(r image.Rectangle) {
	if r.Empty() {
		return
	}
	for i := 0; i < d.n; {
		if !d.rects[i].Overlaps(r) {
			i++
			continue
		}
		r = r.Union(d.rects[i])
		d.remove(i)
		i = 0
	}
	if d.n == maxDamageRects {
		i := d.cheapestMerge(r)
		merged := r.Union(d.rects[i])
		d.remove(i)
		d.Add(merged)
		return
	}
	d.rects[d.n] = r
	d.n++
}

func (d *Damage) remove(i int) {
	d.n--
	d.rects[i] = d.rects[d.n]
	d.rects[d.n] = image.Rectangle{}
}

func (d *Damage) cheapestMerge(r image.Rectangle) int {
	best, growth := 0, -1
	for i := range d.n {
		g := area(r.Union(d.rects[i])) - area(d.rects[i])
		if growth < 0 || g < growth {
			best, growth = i, g
		}
	}
	return best
}

// Merge adds everything o marks.
func (d *Damage) Merge(o *Damage) {
	for _, r := range o.Rects() {
		d.Add(r)
	}
}

// Intersect cuts the damage down to bounds.
func (d *Damage) Intersect(bounds image.Rectangle) {
	kept := Damage{}
	for _, r := range d.Rects() {
		kept.Add(r.Intersect(bounds))
	}
	*d = kept
}

// Empty says nothing changed.
func (d *Damage) Empty() bool {
	return d.n == 0
}

// Rects are the rectangles that changed.
func (d *Damage) Rects() []image.Rectangle {
	return d.rects[:d.n]
}
