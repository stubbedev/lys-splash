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
	return &Renderer{theme: t, face: face, bgrt: bgrt, logos: map[int]*render.Icon{}}, nil
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
// rectangle it changed. With prev nil the whole frame is drawn; otherwise
// only the parts whose state differs are redrawn, so a throbber step
// changes the throbber alone.
func (r *Renderer) Draw(f kms.Frame, scale int, prev *State, cur State) (image.Rectangle, error) {
	if scale < 1 {
		return image.Rectangle{}, fmt.Errorf("theme: scale %d is not positive", scale)
	}
	cv := render.New(f.Pix, f.Stride, f.Size.X, f.Size.Y)
	l := r.layout(f.Size, scale)
	full := image.Rectangle{Max: f.Size}
	if prev == nil || prev.Hidden != cur.Hidden {
		r.clear(cv, full)
		if cur.Hidden {
			return full, nil
		}
		if err := r.logo(cv, l); err != nil {
			return image.Rectangle{}, err
		}
		r.busy(cv, l, cur)
		r.bootProgress(cv, l, cur)
		r.message(cv, l, cur.Message)
		r.prompt(cv, l, cur.Prompt)
		return full, nil
	}
	if cur.Hidden {
		return image.Rectangle{}, nil
	}
	var damage image.Rectangle
	switch {
	case prev.Updating != cur.Updating || cur.Updating && prev.Progress != cur.Progress:
		r.clear(cv, l.busy)
		r.busy(cv, l, cur)
		damage = damage.Union(l.busy)
	case !cur.Updating && r.step(prev.Phase) != r.step(cur.Phase):
		r.clear(cv, l.throb)
		r.throbber(cv, l, cur.Phase)
		damage = damage.Union(l.throb)
	}
	if bootShown(*prev) != bootShown(cur) || bootShown(cur) && prev.Progress != cur.Progress {
		r.clear(cv, l.boot)
		r.bootProgress(cv, l, cur)
		damage = damage.Union(l.boot)
	}
	if prev.Message != cur.Message {
		r.clear(cv, l.message)
		r.message(cv, l, cur.Message)
		damage = damage.Union(l.message)
	}
	if prev.Prompt != cur.Prompt {
		r.clear(cv, l.prompt)
		r.prompt(cv, l, cur.Prompt)
		damage = damage.Union(l.prompt)
	}
	return damage.Intersect(full), nil
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

func (r *Renderer) busy(cv *render.Canvas, l layout, s State) {
	if !s.Updating {
		r.throbber(cv, l, s.Phase)
		return
	}
	c := l.busy.Min.Add(l.busy.Max).Div(2)
	w, h := barWidth*l.scale, barHeight*l.scale
	r.bar(cv, image.Rect(c.X-w/2, c.Y-h/2, c.X+w/2, c.Y+h/2), s.Progress)
}

// step is which of the throbber's distinct pictures phase shows.
func (r *Renderer) step(phase int) int {
	if r.theme.Throbber == Dots {
		return phase % Frames * dotCount / Frames
	}
	return phase % Frames
}

func (r *Renderer) throbber(cv *render.Canvas, l layout, phase int) {
	dim := r.dim()
	c := l.throb.Min.Add(l.throb.Max).Div(2)
	size := float64(throbberSize * l.scale)
	step := r.step(phase)
	switch r.theme.Throbber {
	case Spinner:
		width := size / 8
		radius := size/2 - width
		cv.Arc(float64(c.X), float64(c.Y), radius, width, 0, 2*math.Pi, dim)
		cv.Arc(float64(c.X), float64(c.Y), radius, width, 2*math.Pi*float64(step)/Frames-math.Pi/2, math.Pi/2, r.theme.Accent)
	case Dots:
		d := int(size / 4)
		for i := range dotCount {
			col := dim
			if i == step {
				col = r.theme.Accent
			}
			x := c.X + (i-1)*d*3/2
			cv.RoundedRect(render.Rect{X: x - d/2, Y: c.Y - d/2, W: d, H: d}, d/2, col)
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
	fill.Max.X = track.Min.X + track.Dx()*int(p)/int(protocol.MaxPercent)
	if fill.Dx() > 0 {
		cv.RoundedRect(rect(fill), radius, r.theme.Accent)
	}
}

func (r *Renderer) message(cv *render.Canvas, l layout, msg string) {
	if msg == "" {
		return
	}
	r.face.DrawAligned(cv, msg, rect(l.message), float64(textSize*l.scale), r.theme.Foreground, render.AlignCenter)
}

func (r *Renderer) prompt(cv *render.Canvas, l layout, p Prompt) {
	if p.Kind == NoPrompt {
		return
	}
	s := l.scale
	px := float64(textSize * s)
	text := l.prompt
	text.Max.Y = text.Min.Y + lineHeight*s
	r.face.DrawAligned(cv, p.Text, rect(text), px, r.theme.Foreground, render.AlignCenter)
	c := (l.prompt.Min.X + l.prompt.Max.X) / 2
	w := min(entryWidth*s, l.prompt.Dx())
	entry := image.Rect(c-w/2, text.Max.Y+gapInForm*s, c+w/2, l.prompt.Max.Y)
	cv.RoundedBorder(rect(entry), render.UniformCorners(entryHeight*s/4), render.UniformInsets(max(s, 1)), r.theme.Accent)
	if p.Shown == "" {
		return
	}
	inner := entry.Inset(entryHeight * s / 4)
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
