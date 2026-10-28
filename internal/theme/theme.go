// Package theme is how the splash looks: the validated theme.yaml, and the
// drawing of a frame from the splash's state, damage included, with no KMS
// underneath so tests can render it.
package theme

import (
	_ "embed" // the default theme
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/render"
	"go.yaml.in/yaml/v3"
)

//go:embed default.yaml
var defaultYAML []byte

// Logo is what the splash shows as its logo.
type Logo uint8

const (
	// FirmwareLogo is the firmware's own logo from the ACPI BGRT table when
	// it left one on screen, otherwise the lys logo.
	FirmwareLogo Logo = iota
	// LysLogo is always the lys logo.
	LysLogo
)

// Throbber is the busy animation's style.
type Throbber uint8

const (
	// Spinner is an arc turning around a faint ring.
	Spinner Throbber = iota
	// Dots is three dots lighting up in turn.
	Dots
)

// Theme is a validated theme.
type Theme struct {
	Background render.Color
	Foreground render.Color
	Accent     render.Color
	Logo       Logo
	Throbber   Throbber
	// Units shows the name of each unit systemd starts on the message line.
	Units bool
}

// Default is the theme embedded in the binary.
func Default() Theme {
	t, err := parse("default.yaml", defaultYAML, nil)
	if err != nil {
		panic(err)
	}
	return t
}

// Load reads the theme at path. A missing file is the default theme; a file
// that cannot be read or does not validate is an error naming the file and
// the field, returned with the default theme for the splash to fall back to.
func Load(path string) (Theme, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the theme path is the administrator's configuration
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("theme: %w", err)
	}
	t, err := Parse(path, data)
	if err != nil {
		return Default(), err
	}
	return t, nil
}

// Parse validates a theme.yaml read from file; a field it leaves out keeps
// the default theme's value.
func Parse(file string, data []byte) (Theme, error) {
	base := Default()
	return parse(file, data, &base)
}

func parse(file string, data []byte, base *Theme) (Theme, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Theme{}, fmt.Errorf("theme: %s: %w", file, err)
	}
	var t Theme
	if base != nil {
		t = *base
	}
	fields := map[string]func(*yaml.Node) error{
		"background": decode(&t.Background, parseColor),
		"foreground": decode(&t.Foreground, parseColor),
		"accent":     decode(&t.Accent, parseColor),
		"logo":       decode(&t.Logo, enum(map[string]Logo{"firmware": FirmwareLogo, "lys": LysLogo})),
		"throbber":   decode(&t.Throbber, enum(map[string]Throbber{"spinner": Spinner, "dots": Dots})),
		"units":      decode(&t.Units, func(b bool) (bool, error) { return b, nil }),
	}
	var errs []error
	seen := map[string]bool{}
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return Theme{}, fmt.Errorf("theme: %s: line %d: the theme is not a mapping of fields", file, root.Line)
		}
		for i := 0; i+1 < len(root.Content); i += 2 {
			key, value := root.Content[i], root.Content[i+1]
			set, ok := fields[key.Value]
			if !ok {
				errs = append(errs, fmt.Errorf("theme: %s: line %d: %s is not a theme field; the fields are %s", file, key.Line, key.Value, strings.Join(slices.Sorted(maps.Keys(fields)), ", ")))
				continue
			}
			seen[key.Value] = true
			if err := set(value); err != nil {
				errs = append(errs, fmt.Errorf("theme: %s: line %d: %s: %w", file, value.Line, key.Value, err))
			}
		}
	}
	if base == nil {
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			if !seen[name] {
				errs = append(errs, fmt.Errorf("theme: %s: %s is missing", file, name))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Theme{}, err
	}
	return t, nil
}

func decode[R, T any](dst *T, conv func(R) (T, error)) func(*yaml.Node) error {
	return func(n *yaml.Node) error {
		var raw R
		if err := n.Decode(&raw); err != nil {
			return err
		}
		v, err := conv(raw)
		if err != nil {
			return err
		}
		*dst = v
		return nil
	}
}

func parseColor(s string) (render.Color, error) {
	if len(s) != 7 || s[0] != '#' {
		return 0, fmt.Errorf("%q is not #rrggbb", s)
	}
	rgb, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not #rrggbb", s)
	}
	return render.Color(0xff000000 | intconv.Must[uint32](rgb)), nil
}

func enum[T any](names map[string]T) func(string) (T, error) {
	return func(s string) (T, error) {
		t, ok := names[s]
		if !ok {
			var zero T
			return zero, fmt.Errorf("%q is not one of %s", s, strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		}
		return t, nil
	}
}
