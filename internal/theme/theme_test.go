package theme

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/golden"
	"github.com/stubbedev/lys-lib/render"
	"golang.org/x/image/bmp"

	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
)

func TestDefaultParses(t *testing.T) {
	t.Parallel()
	d := Default()
	if d.Background != render.RGB(0, 0, 0) || d.Logo != FirmwareLogo || d.Throbber != Spinner || d.Units || !d.PromptHints {
		t.Errorf("Default() = %+v", d)
	}
}

func TestParseOverridesTheDefault(t *testing.T) {
	t.Parallel()
	got, err := Parse("theme.yaml", []byte("accent: \"#102030\"\nthrobber: dots\nunits: true\nprompt-hints: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Accent, want.Throbber, want.Units, want.PromptHints = render.RGB(0x10, 0x20, 0x30), Dots, true, false
	if got != want {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
	if got, err := Parse("empty.yaml", nil); err != nil || got != Default() {
		t.Errorf("Parse of an empty file = %+v, %v; want the default", got, err)
	}
}

func TestParseErrorsNameTheField(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"background: red\n":           "background",
		"foreground: \"#12345\"\n":    "foreground",
		"accent: \"#gg0000\"\n":       "accent",
		"logo: plymouth\n":            "logo",
		"throbber: wheel\n":           "throbber",
		"units: maybe\n":              "units",
		"prompt-hints: 1\n":           "prompt-hints",
		"colour: \"#000000\"\n":       "colour",
		"background: [1, 2]\n":        "background",
		"logo: lys\nthrobber: none\n": "throbber",
	}
	for doc, field := range tests {
		_, err := Parse("theme.yaml", []byte(doc))
		if err == nil {
			t.Errorf("Parse(%q) succeeded", doc)
			continue
		}
		if !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), "theme.yaml") {
			t.Errorf("Parse(%q) = %v, want an error naming theme.yaml and %s", doc, err, field)
		}
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := Load(filepath.Join(dir, "missing.yaml")); err != nil || got != Default() {
		t.Errorf("Load of a missing file = %+v, %v; want the default", got, err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("logo: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(bad); err == nil || got != Default() {
		t.Errorf("Load of an invalid file = %+v, %v; want the default and an error", got, err)
	}
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("logo: lys\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(good); err != nil || got.Logo != LysLogo {
		t.Errorf("Load = %+v, %v", got, err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load of a directory succeeded")
	}
}

func testLogo() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 96, 48))
	for y := range 48 {
		for x := range 96 {
			c := color.RGBA{0x20, 0x60, 0xd0, 0xff}
			if (x/12+y/12)%2 == 0 {
				c = color.RGBA{0xf0, 0xf0, 0xf0, 0xff}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func writeBGRT(t *testing.T, status string, offset image.Point) string {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, testLogo()); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"status":  status,
		"xoffset": strconv.Itoa(offset.X) + "\n",
		"yoffset": strconv.Itoa(offset.Y) + "\n",
		"image":   buf.String(),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadBGRT(t *testing.T) {
	t.Parallel()
	b, err := LoadBGRT(writeBGRT(t, "1\n", image.Pt(272, 129)))
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.Offset != image.Pt(272, 129) || b.Image.Bounds().Size() != image.Pt(96, 48) {
		t.Fatalf("LoadBGRT = %+v", b)
	}
	if b, err := LoadBGRT(writeBGRT(t, "0\n", image.Pt(1, 1))); b != nil || err != nil {
		t.Errorf("a logo no longer on screen loaded as %+v, %v", b, err)
	}
	if b, err := LoadBGRT(filepath.Join(t.TempDir(), "none")); b != nil || err != nil {
		t.Errorf("a machine without BGRT loaded %+v, %v", b, err)
	}
	corrupt := writeBGRT(t, "1\n", image.Pt(1, 1))
	if err := os.WriteFile(filepath.Join(corrupt, "image"), []byte("BMnot really"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBGRT(corrupt); err == nil {
		t.Error("a corrupt BGRT image loaded")
	}
	badStatus := writeBGRT(t, "yes\n", image.Pt(1, 1))
	if _, err := LoadBGRT(badStatus); err == nil {
		t.Error("a non-numeric status loaded")
	}
}

func TestBGRTPlacement(t *testing.T) {
	t.Parallel()
	b := &BGRT{Image: testLogo(), Offset: image.Pt(272, 100)}
	if got := b.place(image.Pt(640, 400)); got.Min != image.Pt(272, 100) {
		t.Errorf("at the firmware's resolution the logo moved to %v", got.Min)
	}
	if got := b.place(image.Pt(1280, 800)); got.Min != image.Pt(592, 800*382/1000-24) {
		t.Errorf("at another resolution the logo is at %v, want centred at the golden ratio", got.Min)
	}
}

const goldenDir = "testdata/golden"

func frame(size image.Point) kms.Frame {
	return kms.Frame{Pix: make([]byte, size.X*size.Y*4), Stride: size.X * 4, Size: size}
}

func bounds(d kms.Damage) image.Rectangle {
	var b image.Rectangle
	for _, r := range d.Rects() {
		b = b.Union(r)
	}
	return b
}

func draw(t *testing.T, r *Renderer, f kms.Frame, scale int, prev *State, cur State) kms.Damage {
	t.Helper()
	d, err := r.Draw(f, scale, prev, cur)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func check(t *testing.T, name string, f kms.Frame) {
	t.Helper()
	golden.Check(t, goldenDir, name, render.NRGBA(f.Pix, f.Stride, f.Size.X, f.Size.Y), golden.Tolerance{})
}

func renderer(t *testing.T, th Theme, bgrt *BGRT) *Renderer {
	t.Helper()
	r, err := NewRenderer(th, bgrt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGoldenFrames(t *testing.T) {
	t.Parallel()
	size := image.Pt(800, 600)
	bgrt := &BGRT{Image: testLogo(), Offset: image.Pt(352, 205)}
	dots := Default()
	dots.Throbber = Dots
	tests := []struct {
		name  string
		theme Theme
		bgrt  *BGRT
		scale int
		size  image.Point
		state State
	}{
		{"boot", Default(), nil, 1, size, State{Phase: 3}},
		{"boot-bgrt", Default(), bgrt, 1, size, State{Phase: 3}},
		{"boot-dots", dots, nil, 1, size, State{Phase: 14}},
		{"password", Default(), bgrt, 1, size, State{Prompt: Prompt{Kind: PasswordPrompt, Text: "Please enter passphrase for disk root:", Shown: strings.Repeat("•", 7)}}},
		{"password-capslock", Default(), nil, 1, size, State{Prompt: Prompt{Kind: PasswordPrompt, Text: "Please enter passphrase for disk root:", Shown: strings.Repeat("•", 3)}, Hints: Hints{CapsLock: true}}},
		{"password-keymap", Default(), nil, 1, size, State{Prompt: Prompt{Kind: PasswordPrompt, Text: "Please enter passphrase for disk root:", Shown: strings.Repeat("•", 3)}, Hints: Hints{Keymap: "de"}}},
		{"question", Default(), nil, 1, size, State{Prompt: Prompt{Kind: QuestionPrompt, Text: "Continue booting?", Shown: "yes"}}},
		{"message", Default(), nil, 1, size, State{Message: "Starting Disk Encryption", Phase: 9}},
		{"update", Default(), nil, 1, size, State{Updating: true, Progress: 42, Message: "Installing updates"}},
		{"boot-progress-0", Default(), nil, 1, size, State{Booting: true, Progress: 0, Phase: 3}},
		{"boot-progress-50", Default(), nil, 1, size, State{Booting: true, Progress: 50, Phase: 3}},
		{"boot-progress-99", Default(), nil, 1, size, State{Booting: true, Progress: 99, Phase: 3}},
		{"scale2", Default(), nil, 2, image.Pt(1600, 1200), State{Message: "Starting Disk Encryption", Phase: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := frame(tc.size)
			if d := bounds(draw(t, renderer(t, tc.theme, tc.bgrt), f, tc.scale, nil, tc.state)); d != (image.Rectangle{Max: tc.size}) {
				t.Errorf("a first frame damaged %v, want all of it", d)
			}
			check(t, tc.name, f)
		})
	}
}

func TestGoldenRotated(t *testing.T) {
	t.Parallel()
	panel := image.Pt(600, 800)
	f := frame(kms.Clockwise.View(panel))
	draw(t, renderer(t, Default(), nil), f, 1, nil, State{Message: "Turned", Phase: 3})
	dev := frame(panel)
	kms.Blit(dev, f, image.Rectangle{Max: f.Size}, kms.Clockwise)
	check(t, "rotated", dev)
}

func TestHiddenBlanksAndShowsAgain(t *testing.T) {
	t.Parallel()
	size := image.Pt(320, 200)
	r := renderer(t, Default(), nil)
	f := frame(size)
	shown := State{Message: "hi"}
	draw(t, r, f, 1, nil, shown)
	hidden := State{Hidden: true, Message: "hi"}
	if d := bounds(draw(t, r, f, 1, &shown, hidden)); d != (image.Rectangle{Max: size}) {
		t.Errorf("hiding damaged %v", d)
	}
	if !bytes.Equal(f.Pix, bytes.Repeat([]byte{0, 0, 0, 0xff}, size.X*size.Y)) {
		t.Error("a hidden splash is not the background alone")
	}
	if d := draw(t, r, f, 1, &hidden, State{Hidden: true, Message: "other"}); !d.Empty() {
		t.Errorf("a change while hidden damaged %v", d)
	}
	again := frame(size)
	draw(t, r, again, 1, nil, shown)
	draw(t, r, f, 1, &hidden, shown)
	if !bytes.Equal(f.Pix, again.Pix) {
		t.Error("showing again differs from a fresh frame")
	}
}

func TestThrobberDamageCoversOnlyTheThrobber(t *testing.T) {
	t.Parallel()
	size := image.Pt(640, 400)
	r := renderer(t, Default(), nil)
	f := frame(size)
	prev := State{Message: "booting", Phase: 0}
	draw(t, r, f, 1, nil, prev)
	before := bytes.Clone(f.Pix)
	next := prev
	next.Phase = 1
	d := bounds(draw(t, r, f, 1, &prev, next))
	l := r.layout(size, 1)
	if d != l.throb {
		t.Fatalf("a throbber step damaged %v, want the throbber's %v", d, l.throb)
	}
	changed := image.Rectangle{}
	for y := range size.Y {
		for x := range size.X {
			i := y*f.Stride + x*4
			if !bytes.Equal(f.Pix[i:i+4], before[i:i+4]) {
				changed = changed.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if changed.Empty() || !changed.In(d) {
		t.Errorf("pixels changed in %v, outside the damage %v", changed, d)
	}
	fresh := frame(size)
	draw(t, r, fresh, 1, nil, next)
	if !bytes.Equal(fresh.Pix, f.Pix) {
		t.Error("the damage-limited frame differs from a full redraw")
	}
	if d := draw(t, r, f, 1, &next, next); !d.Empty() {
		t.Errorf("an unchanged state damaged %v", d)
	}
}

func TestEveryPartialRedrawMatchesAFullOne(t *testing.T) {
	t.Parallel()
	size := image.Pt(640, 400)
	r := renderer(t, Default(), nil)
	steps := []State{
		{Phase: 1},
		{Phase: 2, Message: "Starting"},
		{Phase: 2, Message: "Starting", Prompt: Prompt{Kind: PasswordPrompt, Text: "Disk:", Shown: "•"}},
		{Phase: 2, Message: "Starting", Prompt: Prompt{Kind: PasswordPrompt, Text: "Disk:", Shown: "••"}},
		{Phase: 3, Message: "Starting"},
		{Updating: true, Progress: 10},
		{Updating: true, Progress: 90, Phase: 5},
		{Phase: 6},
		{Phase: 6, Booting: true},
		{Phase: 7, Booting: true, Progress: 40},
		{Phase: 7, Booting: true, Progress: 40, Updating: true},
		{Phase: 8, Booting: true, Progress: 60},
		{Phase: 8, Message: "a"},
		{Phase: 8, Message: "a much longer message with fjgyQ overhanging glyphs"},
		{Phase: 8, Message: "jjj"},
		{Phase: 8, Message: strings.Repeat("far too long for the box ", 20)},
		{Phase: 8, Message: "short"},
		{Phase: 8, Prompt: Prompt{Kind: QuestionPrompt, Text: "Go on?", Shown: "y"}},
		{Phase: 8, Prompt: Prompt{Kind: QuestionPrompt, Text: "Go on?", Shown: "yes"}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "Go on?", Shown: "•••"}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "Passphrase for /dev/sda2:", Shown: "•••"}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "Passphrase for /dev/sda2:", Shown: strings.Repeat("•", 80)}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "x"}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "x"}, Hints: Hints{Keymap: "de-latin1"}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "x", Shown: "•"}, Hints: Hints{Keymap: "de-latin1", CapsLock: true}},
		{Phase: 8, Prompt: Prompt{Kind: PasswordPrompt, Text: "x", Shown: "•"}, Hints: Hints{Keymap: "us", CapsLock: false}},
		{Phase: 8, Hints: Hints{Keymap: "us", CapsLock: true}},
		{Phase: 8},
		{Updating: true, Progress: 0},
		{Updating: true, Progress: 1},
		{Updating: true, Progress: 2},
		{Updating: true, Progress: 3},
		{Updating: true, Progress: 7},
		{Updating: true, Progress: 3},
		{Updating: true, Progress: 100},
		{Updating: true, Progress: 0},
	}
	f := frame(size)
	var prev *State
	for i, s := range steps {
		draw(t, r, f, 1, prev, s)
		fresh := frame(size)
		draw(t, r, fresh, 1, nil, s)
		if !bytes.Equal(fresh.Pix, f.Pix) {
			t.Errorf("step %d: the partial redraw differs from a full one", i)
		}
		prev = &steps[i]
	}
}

func TestBootProgressDamagesOnlyItsBar(t *testing.T) {
	t.Parallel()
	size := image.Pt(640, 400)
	r := renderer(t, Default(), nil)
	f := frame(size)
	prev := State{Booting: true, Progress: 10, Phase: 4}
	draw(t, r, f, 1, nil, prev)
	next := prev
	next.Progress = 11
	l := r.layout(size, 1)
	if d := bounds(draw(t, r, f, 1, &prev, next)); d.Empty() || !d.In(l.boot) {
		t.Errorf("a boot progress step damaged %v, want part of the bar's %v", d, l.boot)
	}
	if l.boot.Overlaps(l.busy) || l.boot.Overlaps(l.message) {
		t.Errorf("the boot progress bar %v overlaps the throbber %v or the message %v", l.boot, l.busy, l.message)
	}
	without := frame(size)
	draw(t, r, without, 1, nil, State{Progress: 10, Phase: 4})
	if bytes.Equal(without.Pix, f.Pix) {
		t.Error("the boot progress draws nothing")
	}
}

func TestDrawRefusesABadScale(t *testing.T) {
	t.Parallel()
	if _, err := renderer(t, Default(), nil).Draw(frame(image.Pt(8, 8)), 0, nil, State{}); err == nil {
		t.Error("Draw at scale 0 succeeded")
	}
}

func TestDotsOnlyRedrawWhenTheLitDotMoves(t *testing.T) {
	t.Parallel()
	size := image.Pt(640, 400)
	dots := Default()
	dots.Throbber = Dots
	r := renderer(t, dots, nil)
	f := frame(size)
	prev := State{}
	draw(t, r, f, 1, nil, prev)
	damaged := 0
	for phase := 1; phase <= Frames; phase++ {
		cur := State{Phase: phase % Frames}
		if d := draw(t, r, f, 1, &prev, cur); !d.Empty() {
			damaged++
		}
		fresh := frame(size)
		draw(t, r, fresh, 1, nil, cur)
		if !bytes.Equal(fresh.Pix, f.Pix) {
			t.Fatalf("phase %d differs from a full redraw", cur.Phase)
		}
		prev = cur
	}
	if damaged != dotCount {
		t.Errorf("a turn of the dots damaged %d frames, want %d", damaged, dotCount)
	}
}

func TestThrobberStepsMatchAFullRedrawOnEveryTurn(t *testing.T) {
	t.Parallel()
	dots := Default()
	dots.Throbber = Dots
	themes := map[string]Theme{"spinner": Default(), "dots": dots}
	for name, th := range themes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			size := image.Pt(800, 600)
			r := renderer(t, th, nil)
			full := renderer(t, th, nil)
			for _, scale := range []int{1, 2} {
				f := frame(size)
				prev := State{}
				draw(t, r, f, scale, nil, prev)
				for phase := 1; phase <= 2*Frames+1; phase++ {
					cur := State{Phase: phase % Frames}
					draw(t, r, f, scale, &prev, cur)
					fresh := frame(size)
					draw(t, full, fresh, scale, nil, cur)
					if !bytes.Equal(fresh.Pix, f.Pix) {
						t.Fatalf("scale %d, step %d differs from a full redraw", scale, phase)
					}
					prev = cur
				}
			}
		})
	}
}

func TestThrobberOffTheFrameDoesNotPanic(t *testing.T) {
	t.Parallel()
	size := image.Pt(30, 30)
	r := renderer(t, Default(), nil)
	f := frame(size)
	prev := State{}
	draw(t, r, f, 1, nil, prev)
	for phase := 1; phase <= Frames; phase++ {
		cur := State{Phase: phase}
		draw(t, r, f, 1, &prev, cur)
		prev = cur
	}
}

func TestPhasesToNextPictureLandOnTheNextPicture(t *testing.T) {
	t.Parallel()
	for _, throbber := range []Throbber{Spinner, Dots} {
		th := Default()
		th.Throbber = throbber
		r := renderer(t, th, nil)
		for phase := range Frames {
			n := r.PhasesToNextPicture(phase)
			for i := 1; i < n; i++ {
				if r.step(phase+i) != r.step(phase) {
					t.Fatalf("throbber %v: phase %d changes after %d phases, not %d", throbber, phase, i, n)
				}
			}
			if r.step(phase+n) == r.step(phase) {
				t.Errorf("throbber %v: phase %d shows the same picture after %d phases", throbber, phase, n)
			}
		}
	}
}

func TestBarMovesMatchAFullRedraw(t *testing.T) {
	t.Parallel()
	size := image.Pt(800, 600)
	r := renderer(t, Default(), nil)
	full := renderer(t, Default(), nil)
	for _, scale := range []int{1, 2} {
		for _, booting := range []bool{true, false} {
			f := frame(size)
			at := func(p protocol.Percent) State {
				return State{Booting: booting, Updating: !booting, Progress: p}
			}
			prev := at(0)
			draw(t, r, f, scale, nil, prev)
			for _, p := range []protocol.Percent{1, 2, 3, 4, 5, 6, 20, 21, 99, 100, 97, 1, 0, 50, 49} {
				cur := at(p)
				draw(t, r, f, scale, &prev, cur)
				fresh := frame(size)
				draw(t, full, fresh, scale, nil, cur)
				if !bytes.Equal(fresh.Pix, f.Pix) {
					t.Errorf("scale %d, booting %v, %d%% to %d%% differs from a full redraw", scale, booting, prev.Progress, p)
				}
				prev = cur
			}
		}
	}
}

func damagedArea(d kms.Damage) int {
	total := 0
	for _, r := range d.Rects() {
		total += r.Dx() * r.Dy()
	}
	return total
}

func TestDamageIsOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	const scale = 1
	size := image.Pt(1920, 1080)
	th := Default()
	th.Throbber = Dots
	r := renderer(t, th, nil)
	l := r.layout(size, scale)
	dot := l.dotSize() * l.dotSize()
	barArea := func(track image.Rectangle) int { return track.Dx() * track.Dy() }
	prompt := func(shown string) State {
		return State{Prompt: Prompt{Kind: PasswordPrompt, Text: "Disk:", Shown: shown}}
	}
	tests := []struct {
		name     string
		from, to State
		rects    int
		within   image.Rectangle
		most     int
	}{
		{"a dots step is the two dots that swap", State{Phase: 0}, State{Phase: 12}, 2, l.throb, 2 * dot},
		{"a key typed is the entry's inside", prompt("•"), prompt("••"), 1, l.entryInner(), area(l.entryInner())},
		{"a short message is its text", State{}, State{Message: "Starting"}, 1, l.message, 150 * l.message.Dy()},
		{"the prompt's text is its extent", prompt("•"), State{Prompt: Prompt{Kind: PasswordPrompt, Text: "Disk two:", Shown: "•"}}, 1, l.promptLine(), 150 * l.promptLine().Dy()},
		{"a percent of boot is its columns", State{Booting: true, Progress: 50}, State{Booting: true, Progress: 51}, 1, l.boot, barArea(l.boot) / 10},
		{"a percent of update is its columns", State{Updating: true, Progress: 50}, State{Updating: true, Progress: 51}, 1, l.updateBar(), barArea(l.updateBar()) / 10},
		{"a message and a boot step stay apart", State{Booting: true, Progress: 50}, State{Booting: true, Progress: 51, Message: "m"}, 2, l.busy.Union(l.message).Union(l.boot), barArea(l.boot)/10 + 50*l.message.Dy()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := renderer(t, th, nil)
			f := frame(size)
			draw(t, r, f, scale, nil, tc.from)
			before := bytes.Clone(f.Pix)
			d := draw(t, r, f, scale, &tc.from, tc.to)
			if got := len(d.Rects()); got != tc.rects {
				t.Errorf("%d rectangles %v, want %d", got, d.Rects(), tc.rects)
			}
			if !bounds(d).In(tc.within) {
				t.Errorf("damage %v outside %v", d.Rects(), tc.within)
			}
			if got := damagedArea(d); got > tc.most {
				t.Errorf("%d pixels damaged, want at most %d", got, tc.most)
			}
			t.Logf("%s: %d pixels in %v", tc.name, damagedArea(d), d.Rects())
			for y := range size.Y {
				for x := range size.X {
					i := y*f.Stride + x*4
					if !bytes.Equal(f.Pix[i:i+4], before[i:i+4]) && !slices.ContainsFunc(d.Rects(), image.Pt(x, y).In) {
						t.Fatalf("pixel (%d,%d) changed outside the damage %v", x, y, d.Rects())
					}
				}
			}
		})
	}
}

func area(r image.Rectangle) int {
	return r.Dx() * r.Dy()
}

func TestHintsShowOnlyWithAPromptAndDamageOnlyThemselves(t *testing.T) {
	t.Parallel()
	size := image.Pt(800, 600)
	r := renderer(t, Default(), nil)
	l := r.layout(size, 1)
	f := frame(size)
	prompt := Prompt{Kind: PasswordPrompt, Text: "Disk:", Shown: "••"}
	off := State{Prompt: prompt, Hints: Hints{Keymap: "de"}}
	draw(t, r, f, 1, nil, off)
	on := off
	on.Hints.CapsLock = true
	if d := bounds(draw(t, r, f, 1, &off, on)); d.Empty() || !d.In(l.caps) {
		t.Errorf("Caps Lock coming on damaged %v, want part of the hint line %v", d, l.caps)
	}
	if d := bounds(draw(t, r, f, 1, &on, off)); d.Empty() || !d.In(l.caps) {
		t.Errorf("Caps Lock going off damaged %v, want part of the hint line %v", d, l.caps)
	}
	if l.caps.Overlaps(l.entry()) || l.keymap.Overlaps(l.entry()) || l.keymap.Empty() {
		t.Errorf("the hints at %v and %v overlap the entry %v or have no room", l.caps, l.keymap, l.entry())
	}
	blank, hidden := frame(size), frame(size)
	draw(t, r, blank, 1, nil, State{})
	draw(t, r, hidden, 1, nil, State{Hints: Hints{Keymap: "de", CapsLock: true}})
	if !bytes.Equal(blank.Pix, hidden.Pix) {
		t.Error("the hints show without a prompt")
	}
}
