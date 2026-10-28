package theme

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/image/bmp"
)

// BGRTDir is where Linux exposes the ACPI BGRT table.
const BGRTDir = "/sys/firmware/acpi/bgrt"

// BGRT is the logo the firmware left on screen: its image and where the
// firmware drew it, in the firmware's own pixels.
type BGRT struct {
	Image  image.Image
	Offset image.Point
}

// LoadBGRT reads the firmware's logo from dir. A machine without a BGRT
// table, or one whose status says the logo is no longer on screen, has
// none: nil and no error.
func LoadBGRT(dir string) (*BGRT, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("theme: open the BGRT table: %w", err)
	}
	b, err := loadBGRT(root)
	if cerr := root.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("theme: close the BGRT table: %w", cerr))
	}
	return b, err
}

func loadBGRT(root *os.Root) (*BGRT, error) {
	status, err := readInt(root, "status")
	if err != nil {
		return nil, err
	}
	if status&1 == 0 {
		return nil, nil
	}
	x, err := readInt(root, "xoffset")
	if err != nil {
		return nil, err
	}
	y, err := readInt(root, "yoffset")
	if err != nil {
		return nil, err
	}
	data, err := root.ReadFile("image")
	if err != nil {
		return nil, fmt.Errorf("theme: the BGRT image: %w", err)
	}
	img, err := bmp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("theme: decode the BGRT image in %s: %w", root.Name(), err)
	}
	return &BGRT{Image: img, Offset: image.Pt(x, y)}, nil
}

func readInt(root *os.Root, name string) (int, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return 0, fmt.Errorf("theme: the BGRT %s: %w", name, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("theme: the BGRT %s in %s: %w", name, root.Name(), err)
	}
	return v, nil
}

// place is where the firmware logo goes on a frame of size: at the
// firmware's offsets when they centre it horizontally on this size, which
// means the firmware drew at this resolution, otherwise centred at the golden
// ratio's height as plymouth places it.
func (b *BGRT) place(size image.Point) image.Rectangle {
	s := b.Image.Bounds().Size()
	at := b.Offset
	if (size.X-s.X)/2 != b.Offset.X {
		at = image.Pt((size.X-s.X)/2, size.Y*382/1000-s.Y/2)
	}
	return image.Rectangle{Min: at, Max: at.Add(s)}
}
