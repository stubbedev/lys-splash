package console

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Capture is the console's output redirected into a pseudo-terminal with
// TIOCCONS, as plymouth's details view takes it: what is written to
// /dev/console, systemd's status lines among it, reaches Read instead of the
// console until Restore or Leave gives the console back.
type Capture struct {
	master  *os.File
	peer    *os.File
	console string
}

// CaptureConsole redirects the console under p.Dev into a new
// pseudo-terminal. It needs CAP_SYS_ADMIN, and fails while another process
// holds the console redirected.
func CaptureConsole(p Paths) (*Capture, error) {
	ptmx := filepath.Join(p.Dev, "ptmx")
	fd, err := unix.Open(ptmx, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("console: open %s: %w", ptmx, err)
	}
	master := os.NewFile(uintptr(fd), ptmx)
	c, err := redirect(master, filepath.Join(p.Dev, "console"))
	if err != nil {
		return nil, errors.Join(err, master.Close())
	}
	return c, nil
}

func redirect(master *os.File, console string) (*Capture, error) {
	var peer int
	err := device{master}.control(func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return fmt.Errorf("unlock: %w", err)
		}
		r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTPEER, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC)
		if errno != 0 {
			return errno
		}
		peer = int(r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("console: open the peer of %s: %w", master.Name(), err)
	}
	c := &Capture{master: master, peer: os.NewFile(uintptr(peer), master.Name()+" peer"), console: console}
	if err := unix.IoctlSetInt(peer, unix.TIOCCONS, 0); err != nil {
		return nil, errors.Join(fmt.Errorf("console: redirect %s into %s: %w", console, master.Name(), err), c.peer.Close())
	}
	return c, nil
}

// Read reads the console's output, blocking until there is some.
func (c *Capture) Read(b []byte) (int, error) {
	n, err := c.master.Read(b)
	if err != nil {
		return n, fmt.Errorf("console: read the captured console: %w", err)
	}
	return n, nil
}

// Restore gives the console back and writes lines to it, so that what the
// splash captured is not lost, then closes the pseudo-terminal, which also
// ends a blocked Read.
func (c *Capture) Restore(lines []string) error {
	return c.release(lines)
}

// Leave gives the console back without writing to it, for a splash whose
// last frame stays on screen, and closes the pseudo-terminal.
func (c *Capture) Leave() error {
	return c.release(nil)
}

func (c *Capture) release(lines []string) error {
	errs := []error{c.giveBack(lines)}
	if err := c.peer.Close(); err != nil {
		errs = append(errs, fmt.Errorf("console: close %s: %w", c.peer.Name(), err))
	}
	if err := c.master.Close(); err != nil {
		errs = append(errs, fmt.Errorf("console: close %s: %w", c.master.Name(), err))
	}
	return errors.Join(errs...)
}

func (c *Capture) giveBack(lines []string) error {
	f, err := os.OpenFile(c.console, os.O_WRONLY|unix.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("console: open %s to undo the redirect: %w", c.console, err)
	}
	err = device{f}.control(func(fd int) error { return unix.IoctlSetInt(fd, unix.TIOCCONS, 0) })
	if err != nil {
		err = fmt.Errorf("console: undo the redirect of %s: %w", c.console, err)
	} else if len(lines) > 0 {
		if _, werr := f.WriteString(strings.Join(lines, "\n") + "\n"); werr != nil {
			err = fmt.Errorf("console: write the captured lines back to %s: %w", c.console, werr)
		}
	}
	if cerr := f.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("console: close %s: %w", c.console, cerr))
	}
	return err
}
