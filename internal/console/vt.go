// Package console owns the virtual terminal while the splash shows: it keeps
// the kernel's text off the splash, reads keys already mapped by the
// console's keymap, and shows the splash as text when there is no graphical
// output.
package console

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	kdSetMode   = 0x4b3a
	kdGetMode   = 0x4b3b
	kdGetKBMode = 0x4b44
	kdSetKBMode = 0x4b45

	kdText     = 0x00
	kdGraphics = 0x01
	kUnicode   = 0x03
)

// Paths are where the VTs are found.
type Paths struct {
	Sysfs string
	Dev   string
}

// SystemPaths are the kernel's: /sys/class/tty and /dev.
var SystemPaths = Paths{Sysfs: "/sys/class/tty", Dev: "/dev"}

// ErrNoVT is what OpenActive wraps on a kernel without virtual terminals.
var ErrNoVT = errors.New("console: the kernel has no virtual terminals")

// terminal is the VT's device, its modes and its termios.
type terminal interface {
	Name() string
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Close() error
	Mode() (int, error)
	SetMode(mode int) error
	KeyboardMode() (int, error)
	SetKeyboardMode(mode int) error
	Termios() (unix.Termios, error)
	SetTermios(t unix.Termios) error
	Winsize() (unix.Winsize, error)
}

type device struct {
	*os.File
}

func (d device) control(f func(fd int) error) error {
	raw, err := d.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

func (d device) ioctlGet(req uint) (v int, err error) {
	err = d.control(func(fd int) (err error) {
		v, err = unix.IoctlGetInt(fd, req)
		return err
	})
	return v, err
}

func (d device) ioctlSet(req uint, value int) error {
	return d.control(func(fd int) error { return unix.IoctlSetInt(fd, req, value) })
}

func (d device) Mode() (int, error)             { return d.ioctlGet(kdGetMode) }
func (d device) SetMode(mode int) error         { return d.ioctlSet(kdSetMode, mode) }
func (d device) KeyboardMode() (int, error)     { return d.ioctlGet(kdGetKBMode) }
func (d device) SetKeyboardMode(mode int) error { return d.ioctlSet(kdSetKBMode, mode) }

func (d device) Termios() (t unix.Termios, err error) {
	err = d.control(func(fd int) error {
		got, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err == nil {
			t = *got
		}
		return err
	})
	return t, err
}

func (d device) Winsize() (w unix.Winsize, err error) {
	err = d.control(func(fd int) error {
		got, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err == nil {
			w = *got
		}
		return err
	})
	return w, err
}

func (d device) SetTermios(t unix.Termios) error {
	return d.control(func(fd int) error { return unix.IoctlSetTermios(fd, unix.TCSETS, &t) })
}

// VT is the active virtual terminal, taken over: graphics mode keeps the
// kernel's text off the splash, and its keyboard reads characters one at a
// time without echo.
type VT struct {
	t        terminal
	mode     int
	kbMode   int
	termios  unix.Termios
	raw      unix.Termios
	restored bool
}

// OpenActive opens the active VT and takes it over. On a kernel without
// VTs it fails with an error wrapping ErrNoVT.
func OpenActive(p Paths) (*VT, error) {
	active, err := os.ReadFile(filepath.Join(p.Sysfs, "tty0", "active"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: no %s", ErrNoVT, filepath.Join(p.Sysfs, "tty0"))
	}
	if err != nil {
		return nil, fmt.Errorf("console: the active VT: %w", err)
	}
	name := strings.TrimSpace(string(active))
	if !strings.HasPrefix(name, "tty") || strings.ContainsRune(name, '/') {
		return nil, fmt.Errorf("console: the active VT is %q, not a ttyN", name)
	}
	path := filepath.Join(p.Dev, name)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("console: open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	v, err := takeOver(device{f})
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return v, nil
}

func takeOver(t terminal) (*VT, error) {
	v := &VT{t: t}
	var err error
	if v.mode, err = t.Mode(); err != nil {
		return nil, v.fail("read the display mode", err)
	}
	if v.kbMode, err = t.KeyboardMode(); err != nil {
		return nil, v.fail("read the keyboard mode", err)
	}
	if v.termios, err = t.Termios(); err != nil {
		return nil, v.fail("read the terminal settings", err)
	}
	v.raw = v.termios
	v.raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHONL | unix.ISIG | unix.IEXTEN
	v.raw.Iflag &^= unix.IXON | unix.ISTRIP | unix.INLCR | unix.IGNCR
	v.raw.Cc[unix.VMIN], v.raw.Cc[unix.VTIME] = 1, 0
	if err := v.Reclaim(); err != nil {
		return nil, errors.Join(err, v.Restore())
	}
	return v, nil
}

// Reclaim makes the terminal raw and the keyboard unicode again, for when
// another process reset the VT under the splash: systemd resets the
// console's terminal when it starts in the real root, which with the
// console on a VT leaves the splash reading whole lines.
func (v *VT) Reclaim() error {
	if err := v.t.SetTermios(v.raw); err != nil {
		return v.fail("make the terminal raw", err)
	}
	if err := v.t.SetKeyboardMode(kUnicode); err != nil {
		return v.fail("set the keyboard to unicode", err)
	}
	return nil
}

func (v *VT) fail(what string, err error) error {
	return fmt.Errorf("console: %s of %s: %w", what, v.t.Name(), err)
}

// Name is the VT's device path.
func (v *VT) Name() string {
	return v.t.Name()
}

// Graphics switches the VT to graphics mode, so the kernel draws no text
// over the splash.
func (v *VT) Graphics() error {
	if err := v.t.SetMode(kdGraphics); err != nil {
		return v.fail("switch to graphics mode", err)
	}
	return nil
}

// Text switches the VT back to text mode, for boot messages or the text
// splash, keeping the keyboard the splash's.
func (v *VT) Text() error {
	if err := v.t.SetMode(kdText); err != nil {
		return v.fail("switch to text mode", err)
	}
	return nil
}

// Rows is how many lines of text the VT shows.
func (v *VT) Rows() (int, error) {
	w, err := v.t.Winsize()
	if err != nil {
		return 0, v.fail("read the size", err)
	}
	return int(w.Row), nil
}

// Read reads keyboard input, blocking until there is some.
func (v *VT) Read(b []byte) (int, error) {
	n, err := v.t.Read(b)
	if err != nil {
		return n, fmt.Errorf("console: read %s: %w", v.t.Name(), err)
	}
	return n, nil
}

// Write writes text to the VT.
func (v *VT) Write(b []byte) (int, error) {
	n, err := v.t.Write(b)
	if err != nil {
		return n, fmt.Errorf("console: write %s: %w", v.t.Name(), err)
	}
	return n, nil
}

// Restore gives the VT back as it was: its display mode, keyboard mode and
// terminal settings, then closes it, which also ends a blocked Read. Only
// the first Restore or Leave acts.
func (v *VT) Restore() error {
	return v.release(true)
}

// Leave gives the keyboard and terminal settings back but keeps graphics
// mode, so the kernel's console does not draw over a frame the splash
// leaves on screen, and closes the VT. Only the first Restore or Leave
// acts.
func (v *VT) Leave() error {
	return v.release(false)
}

func (v *VT) release(mode bool) error {
	if v.restored {
		return nil
	}
	v.restored = true
	var errs []error
	if err := v.t.SetTermios(v.termios); err != nil {
		errs = append(errs, v.fail("restore the terminal settings", err))
	}
	if err := v.t.SetKeyboardMode(v.kbMode); err != nil {
		errs = append(errs, v.fail(fmt.Sprintf("restore keyboard mode %d", v.kbMode), err))
	}
	if mode {
		if err := v.t.SetMode(v.mode); err != nil {
			errs = append(errs, v.fail(fmt.Sprintf("restore display mode %d", v.mode), err))
		}
	}
	if err := v.t.Close(); err != nil {
		errs = append(errs, v.fail("close", err))
	}
	return errors.Join(errs...)
}
