package theme

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/golden"
	"github.com/stubbedev/lys-lib/render"
	"golang.org/x/image/bmp"

	"github.com/stubbedev/lys-splash/internal/kms"
)

func TestDefaultParses(t *testing.T) {
	t.Parallel()
	d := Default()
	if d.Background != render.RGB(0, 0, 0) || d.Logo != FirmwareLogo || d.Throbber != Spinner || d.Units {
		t.Errorf("Default() = %+v", d)
	}
}

func TestParseOverridesTheDefault(t *testing.T) {
	t.Parallel()
	got, err := Parse("theme.yaml", []byte("accent: \"#102030\"\nthrobber: dots\nunits: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Accent, want.Throbber, want.Units = render.RGB(0x10, 0x20, 0x30), Dots, true
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

func draw(t *testing.T, r *Renderer, f kms.Frame, scale int, prev *State, cur State) image.Rectangle {
	t.Helper()
	d, err := r.Draw(f, scale, prev, cur)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func goldenTolerance() golden.Tolerance {
	if runtime.GOARCH == "amd64" {
		return golden.Tolerance{}
	}
	return golden.Tolerance{ChannelDiff: 2}
}

func check(t *testing.T, name string, f kms.Frame) {
	t.Helper()
	golden.Check(t, goldenDir, name, render.NRGBA(f.Pix, f.Stride, f.Size.X, f.Size.Y), goldenTolerance())
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
			if d := draw(t, renderer(t, tc.theme, tc.bgrt), f, tc.scale, nil, tc.state); d != (image.Rectangle{Max: tc.size}) {
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
	if d := draw(t, r, f, 1, &shown, hidden); d != (image.Rectangle{Max: size}) {
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
	d := draw(t, r, f, 1, &prev, next)
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
	if d := draw(t, r, f, 1, &prev, next); d != l.boot {
		t.Errorf("a boot progress step damaged %v, want the bar's %v", d, l.boot)
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
