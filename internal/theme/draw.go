package theme

import (
	_ "embed" // the font and the lys logo
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/lys-lib/render"

	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
)

//go:embed assets/Cantarell-Regular.ttf
var fontTTF []byte

//go:embed assets/lys.svg
var lysSVG []byte

// Frames is how many steps one turn of the throbber takes.
const Frames = 36

// PromptKind is what a prompt asks for.
type PromptKind uint8

// The prompt kinds.
const (
	NoPrompt PromptKind = iota
	PasswordPrompt
	QuestionPrompt
)

// Prompt is the question on screen and what is shown of the answer typed so
// far: one bullet per character for a password, the text for a question.
type Prompt struct {
	Kind  PromptKind
	Text  string
	Shown string
}

// State is what the splash shows. Progress is the update's while Updating,
// and else the boot's while Booting.
type State struct {
	Hidden   bool
	Message  string
	Prompt   Prompt
	Updating bool
	Booting  bool
	Progress protocol.Percent
	Phase    int
}

// Renderer draws frames from states with one theme. It is not safe for
// concurrent use.
type Renderer struct {
	theme Theme
	face  *render.Typeface
	bgrt  *BGRT
	logos map[int]*render.Icon
	steps map[throbberStep][]byte
	cv    render.Canvas
}

type throbberStep struct {
	scale int
	step  int
}

// NewRenderer prepares drawing with t; bgrt is the firmware's logo, nil
// when it left none.
func NewRenderer(t Theme, bgrt *BGRT) (*Renderer, error) {
	face, err := render.LoadFont(fontTTF)
	if err != nil {
		return nil, fmt.Errorf("theme: load the embedded font: %w", err)
	}
	if t.Logo == LysLogo {
		bgrt = nil
	}
	return &Renderer{theme: t, face: face, bgrt: bgrt, logos: map[int]*render.Icon{}, steps: map[throbberStep][]byte{}}, nil
}

type layout struct {
	scale   int
	logo    image.Rectangle
	busy    image.Rectangle
	throb   image.Rectangle
	boot    image.Rectangle
	message image.Rectangle
	prompt  image.Rectangle
}

const (
	lysLogoSize   = 128
	throbberSize  = 40
	dotCount      = 3
	barWidth      = 240
	barHeight     = 8
	bootBarHeight = 4
	textSize      = 16
	bulletSize    = 24
	lineHeight    = 28
	margin        = 32
	entryWidth    = 320
	entryHeight   = 40
	gapAfterLogo  = 48
	gapAfterBusy  = 24
	gapBeforeForm = 16
	gapInForm     = 8
)

func (r *Renderer) layout(size image.Point, scale int) layout {
	s := func(v int) int { return v * scale }
	l := layout{scale: scale}
	if r.bgrt != nil {
		l.logo = r.bgrt.place(size)
	} else {
		n := s(lysLogoSize)
		at := image.Pt((size.X-n)/2, size.Y*382/1000-n/2)
		l.logo = image.Rectangle{Min: at, Max: at.Add(image.Pt(n, n))}
	}
	busyW := max(s(throbberSize), s(barWidth))
	y := l.logo.Max.Y + s(gapAfterLogo)
	l.busy = image.Rect((size.X-busyW)/2, y, (size.X+busyW)/2, y+s(throbberSize))
	l.boot = image.Rect((size.X-s(barWidth))/2, l.busy.Max.Y+s(gapInForm), (size.X+s(barWidth))/2, l.busy.Max.Y+s(gapInForm+bootBarHeight))
	y = l.busy.Max.Y + s(gapAfterBusy)
	l.message = image.Rect(s(margin), y, size.X-s(margin), y+s(lineHeight))
	y = l.message.Max.Y + s(gapBeforeForm)
	l.prompt = image.Rect(s(margin), y, size.X-s(margin), y+s(lineHeight+gapInForm+entryHeight))
	overflow := l.prompt.Max.Y - (size.Y - s(gapInForm))
	room := l.busy.Min.Y - (l.logo.Max.Y + s(gapInForm))
	if up := image.Pt(0, min(overflow, room)); up.Y > 0 {
		l.busy, l.boot, l.message, l.prompt = l.busy.Sub(up), l.boot.Sub(up), l.message.Sub(up), l.prompt.Sub(up)
	}
	n := s(throbberSize)
	at := l.busy.Min.Add(l.busy.Max).Div(2).Sub(image.Pt(n/2, n/2))
	l.throb = image.Rectangle{Min: at, Max: at.Add(image.Pt(n, n))}
	return l
}

// Draw brings frame f, shown at scale, from prev to cur and returns the
// rectangles it changed. With prev nil the whole frame is drawn; otherwise only
// the pixels whose state differs are redrawn: the entry for a key typed, the
// text's extent for a message, the columns between two fills of a bar.
func (r *Renderer) Draw(f kms.Frame, scale int, prev *State, cur State) (kms.Damage, error) {
	if scale < 1 {
		return kms.Damage{}, fmt.Errorf("theme: scale %d is not positive", scale)
	}
	cv := r.cv.Reset(f.Pix, f.Stride, f.Size.X, f.Size.Y, 1, 1)
	l := r.layout(f.Size, scale)
	full := image.Rectangle{Max: f.Size}
	if prev == nil || prev.Hidden != cur.Hidden {
		r.clear(cv, full)
		if cur.Hidden {
			return kms.DamageOf(full), nil
		}
		if err := r.logo(cv, l); err != nil {
			return kms.Damage{}, err
		}
		r.busy(cv, l, cur)
		r.bootProgress(cv, l, cur)
		r.message(cv, l, cur.Message)
		r.prompt(cv, l, cur.Prompt)
		return kms.DamageOf(full), nil
	}
	if cur.Hidden {
		return kms.Damage{}, nil
	}
	var damage kms.Damage
	switch {
	case prev.Updating != cur.Updating:
		r.clear(cv, l.busy)
		r.busy(cv, l, cur)
		damage.Add(l.busy)
	case cur.Updating && prev.Progress != cur.Progress:
		damage.Add(r.moveBar(cv, l.updateBar(), prev.Progress, cur.Progress))
	case !cur.Updating && r.step(prev.Phase) != r.step(cur.Phase):
		r.stepThrobber(f, cv, l, prev.Phase, cur.Phase, &damage)
	}
	switch {
	case bootShown(*prev) != bootShown(cur):
		r.clear(cv, l.boot)
		r.bootProgress(cv, l, cur)
		damage.Add(l.boot)
	case bootShown(cur) && prev.Progress != cur.Progress:
		damage.Add(r.moveBar(cv, l.boot, prev.Progress, cur.Progress))
	}
	if prev.Message != cur.Message {
		damage.Add(r.redrawLine(cv, l.message, prev.Message, cur.Message, float64(textSize*scale)))
	}
	if prev.Prompt != cur.Prompt {
		r.redrawPrompt(cv, l, prev.Prompt, cur.Prompt, &damage)
	}
	damage.Intersect(full)
	return damage, nil
}

// fillEnd is the column a bar's fill reaches at p.
func fillEnd(track image.Rectangle, p protocol.Percent) int {
	return track.Min.X + track.Dx()*int(p)/int(protocol.MaxPercent)
}

// moveBar brings a bar from one fill to another and returns the columns that
// changed: those between the two ends and the rounded end of the shorter fill,
// or from the track's start when that fill is too short to have a straight
// middle, since then its left end changes shape too.
func (r *Renderer) moveBar(cv *render.Canvas, track image.Rectangle, from, to protocol.Percent) image.Rectangle {
	a, b := fillEnd(track, from), fillEnd(track, to)
	if a > b {
		a, b = b, a
	}
	if a == b {
		return image.Rectangle{}
	}
	radius := track.Dy() / 2
	start := a - radius
	if a-track.Min.X < 2*radius {
		start = track.Min.X
	}
	band := image.Rect(start, track.Min.Y, b, track.Max.Y)
	r.clear(cv, band)
	clip := cv.PushClip(rect(band))
	r.bar(cv, track, to)
	cv.PopClip(clip)
	return band
}

// textExtent is the part of box that centred text at px can reach, with room
// for what a glyph's outline overhangs its advance.
func (r *Renderer) textExtent(box image.Rectangle, s string, px float64) image.Rectangle {
	if s == "" {
		return image.Rectangle{}
	}
	half := (int(math.Ceil(r.face.Shape(s, px).Advance())) + int(px)) / 2
	mid := (box.Min.X + box.Max.X) / 2
	return image.Rect(mid-half, box.Min.Y, mid+half, box.Max.Y).Intersect(box)
}

func (r *Renderer) redrawLine(cv *render.Canvas, box image.Rectangle, from, to string, px float64) image.Rectangle {
	extent := r.textExtent(box, from, px).Union(r.textExtent(box, to, px))
	r.clear(cv, extent)
	r.line(cv, box, to, px)
	return extent
}

func (r *Renderer) line(cv *render.Canvas, box image.Rectangle, s string, px float64) {
	if s == "" {
		return
	}
	clip := cv.PushClip(rect(box))
	r.face.DrawAligned(cv, s, rect(box), px, r.theme.Foreground, render.AlignCenter)
	cv.PopClip(clip)
}

func (r *Renderer) redrawPrompt(cv *render.Canvas, l layout, from, to Prompt, damage *kms.Damage) {
	if from.Text != to.Text {
		damage.Add(r.redrawLine(cv, l.promptLine(), from.Text, to.Text, float64(textSize*l.scale)))
	}
	switch {
	case (from.Kind == NoPrompt) != (to.Kind == NoPrompt):
		entry := l.entry()
		r.clear(cv, entry)
		if to.Kind != NoPrompt {
			r.entryBorder(cv, l)
			r.shown(cv, l, to)
		}
		damage.Add(entry)
	case to.Kind != NoPrompt && (from.Shown != to.Shown || from.Kind != to.Kind):
		inner := l.entryInner()
		r.clear(cv, inner)
		r.shown(cv, l, to)
		damage.Add(inner)
	}
}

func rect(r image.Rectangle) render.Rect {
	return render.Rect{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}
}

func (r *Renderer) clear(cv *render.Canvas, at image.Rectangle) {
	cv.ClearDevice(rect(at), r.theme.Background)
}

func (r *Renderer) logo(cv *render.Canvas, l layout) error {
	if r.bgrt != nil {
		cv.DrawImageDevice(r.bgrt.Image, l.logo.Min.X, l.logo.Min.Y)
		return nil
	}
	icon, ok := r.logos[l.scale]
	if !ok {
		n := lysLogoSize * l.scale
		var err error
		if icon, err = render.LoadSVG(lysSVG, n, n); err != nil {
			return fmt.Errorf("theme: rasterize the lys logo at %dpx: %w", n, err)
		}
		r.logos[l.scale] = icon
	}
	icon.Draw(cv, l.logo.Min.X, l.logo.Min.Y)
	return nil
}

func mix(a, b render.Color, num, denom uint32) render.Color {
	ch := func(x, y uint8) uint8 {
		return uint8((uint32(x)*(denom-num) + uint32(y)*num) / denom) //nolint:gosec // a weighted mean of two bytes is a byte
	}
	return render.RGB(ch(a.R(), b.R()), ch(a.G(), b.G()), ch(a.B(), b.B()))
}

func (r *Renderer) dim() render.Color {
	return mix(r.theme.Background, r.theme.Accent, 1, 4)
}

func (l layout) updateBar() image.Rectangle {
	c := l.busy.Min.Add(l.busy.Max).Div(2)
	w, h := barWidth*l.scale, barHeight*l.scale
	return image.Rect(c.X-w/2, c.Y-h/2, c.X+w/2, c.Y+h/2)
}

func (r *Renderer) busy(cv *render.Canvas, l layout, s State) {
	if !s.Updating {
		r.throbber(cv, l, s.Phase)
		return
	}
	r.bar(cv, l.updateBar(), s.Progress)
}

// step is which of the throbber's distinct pictures phase shows.
func (r *Renderer) step(phase int) int {
	if r.theme.Throbber == Dots {
		return phase % Frames * dotCount / Frames
	}
	return phase % Frames
}

// PhasesToNextPicture is how many phases from phase the throbber shows a
// different picture.
func (r *Renderer) PhasesToNextPicture(phase int) int {
	if r.theme.Throbber != Dots {
		return 1
	}
	phase %= Frames
	return ((phase*dotCount/Frames+1)*Frames+dotCount-1)/dotCount - phase
}

// stepThrobber brings the throbber from one phase to another and adds what
// changed to damage: the two dots that swap, or the whole of a spinner. Each
// picture is computed per pixel once and copied from then on, since a boot
// repeats the same few dozen for as long as it lasts.
func (r *Renderer) stepThrobber(f kms.Frame, cv *render.Canvas, l layout, from, to int, damage *kms.Damage) {
	at := l.throb
	if !at.In(image.Rectangle{Max: f.Size}) {
		r.clear(cv, at)
		r.throbber(cv, l, to)
		damage.Add(at)
		return
	}
	key := throbberStep{l.scale, r.step(to)}
	row := at.Dx() * kms.BytesPerPixel
	saved, cached := r.steps[key]
	if !cached {
		r.clear(cv, at)
		r.throbber(cv, l, to)
		saved = make([]byte, row*at.Dy())
		r.steps[key] = saved
		for y := range at.Dy() {
			copy(saved[y*row:][:row], f.Pix[(at.Min.Y+y)*f.Stride+at.Min.X*kms.BytesPerPixel:])
		}
	}
	if r.theme.Throbber != Dots {
		if cached {
			restore(f, saved, at, at)
		}
		damage.Add(at)
		return
	}
	for _, step := range [...]int{r.step(from), r.step(to)} {
		dot := l.dot(step)
		if cached {
			restore(f, saved, at, dot)
		}
		damage.Add(dot)
	}
}

// restore copies region of the picture saved of rectangle at back to the frame.
func restore(f kms.Frame, saved []byte, at, region image.Rectangle) {
	row := at.Dx() * kms.BytesPerPixel
	n := region.Dx() * kms.BytesPerPixel
	for y := region.Min.Y; y < region.Max.Y; y++ {
		x := (region.Min.X - at.Min.X) * kms.BytesPerPixel
		copy(f.Pix[y*f.Stride+region.Min.X*kms.BytesPerPixel:][:n], saved[(y-at.Min.Y)*row+x:][:n])
	}
}

func (l layout) dotSize() int {
	return throbberSize * l.scale / 4
}

// dot is where the throbber's dot i lies.
func (l layout) dot(i int) image.Rectangle {
	c := l.throb.Min.Add(l.throb.Max).Div(2)
	d := l.dotSize()
	x := c.X + (i-1)*d*3/2 - d/2
	return image.Rect(x, c.Y-d/2, x+d, c.Y-d/2+d)
}

func (r *Renderer) throbber(cv *render.Canvas, l layout, phase int) {
	dim := r.dim()
	c := l.throb.Min.Add(l.throb.Max).Div(2)
	size := float64(throbberSize * l.scale)
	step := r.step(phase)
	switch r.theme.Throbber {
	case Spinner:
		width := float64(size / 8)
		radius := float64(size/2) - width
		cv.Arc(float64(c.X), float64(c.Y), radius, width, 0, 2*math.Pi, dim)
		cv.Arc(float64(c.X), float64(c.Y), radius, width, 2*math.Pi*float64(step)/Frames-math.Pi/2, math.Pi/2, r.theme.Accent)
	case Dots:
		d := l.dotSize()
		for i := range dotCount {
			col := dim
			if i == step {
				col = r.theme.Accent
			}
			cv.RoundedRect(rect(l.dot(i)), d/2, col)
		}
	}
}

func bootShown(s State) bool {
	return s.Booting && !s.Updating
}

func (r *Renderer) bootProgress(cv *render.Canvas, l layout, s State) {
	if bootShown(s) {
		r.bar(cv, l.boot, s.Progress)
	}
}

func (r *Renderer) bar(cv *render.Canvas, track image.Rectangle, p protocol.Percent) {
	radius := track.Dy() / 2
	cv.RoundedRect(rect(track), radius, r.dim())
	fill := track
	fill.Max.X = fillEnd(track, p)
	if fill.Dx() > 0 {
		cv.RoundedRect(rect(fill), radius, r.theme.Accent)
	}
}

func (r *Renderer) message(cv *render.Canvas, l layout, msg string) {
	r.line(cv, l.message, msg, float64(textSize*l.scale))
}

func (l layout) promptLine() image.Rectangle {
	line := l.prompt
	line.Max.Y = line.Min.Y + lineHeight*l.scale
	return line
}

func (l layout) entry() image.Rectangle {
	s := l.scale
	c := (l.prompt.Min.X + l.prompt.Max.X) / 2
	w := min(entryWidth*s, l.prompt.Dx())
	return image.Rect(c-w/2, l.promptLine().Max.Y+gapInForm*s, c+w/2, l.prompt.Max.Y)
}

func (l layout) entryInner() image.Rectangle {
	return l.entry().Inset(entryHeight * l.scale / 4)
}

func (r *Renderer) prompt(cv *render.Canvas, l layout, p Prompt) {
	if p.Kind == NoPrompt {
		return
	}
	r.line(cv, l.promptLine(), p.Text, float64(textSize*l.scale))
	r.entryBorder(cv, l)
	r.shown(cv, l, p)
}

func (r *Renderer) entryBorder(cv *render.Canvas, l layout) {
	s := l.scale
	cv.RoundedBorder(rect(l.entry()), render.UniformCorners(entryHeight*s/4), render.UniformInsets(max(s, 1)), r.theme.Accent)
}

func (r *Renderer) shown(cv *render.Canvas, l layout, p Prompt) {
	if p.Shown == "" {
		return
	}
	s := l.scale
	px := float64(textSize * s)
	inner := l.entryInner()
	prev := cv.PushClip(rect(inner))
	if p.Kind == PasswordPrompt {
		px = float64(bulletSize * s)
	}
	shaped := r.face.Shape(p.Shown, px)
	adv := int(math.Ceil(shaped.Advance()))
	x := inner.Min.X + (inner.Dx()-adv)/2
	if adv > inner.Dx() {
		x = inner.Max.X - adv
	}
	baseline := inner.Min.Y + (inner.Dy()+int(shaped.Ascent()-shaped.Descent()))/2
	shaped.Draw(cv, x, baseline, r.theme.Foreground)
	cv.PopClip(prev)
}
