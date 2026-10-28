package kms

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Action is what a uevent reports happened to a device.
type Action uint8

// The actions the splash follows; every other uevent action is Other.
const (
	Other Action = iota
	Add
	Remove
	Change
)

func actionOf(s string) Action {
	switch s {
	case "add":
		return Add
	case "remove":
		return Remove
	case "change":
		return Change
	}
	return Other
}

// Uevent is a kernel uevent about a device.
type Uevent struct {
	Action    Action
	DevPath   string
	Subsystem string
	DevName   string
	Hotplug   bool
}

// Card is the card node's name under /dev/dri, such as "card0", when the
// uevent is about a DRM card.
func (u Uevent) Card() (string, bool) {
	if u.Subsystem != "drm" {
		return "", false
	}
	name, ok := strings.CutPrefix(u.DevName, "dri/")
	if !ok || !isCard(name) {
		return "", false
	}
	return name, true
}

func isCard(name string) bool {
	digits, ok := strings.CutPrefix(name, "card")
	if !ok || digits == "" {
		return false
	}
	for _, c := range []byte(digits) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

var errUdevMessage = errors.New("kms: a udev message, not a kernel uevent")

// ParseUevent reads one message from a NETLINK_KOBJECT_UEVENT socket:
// "ACTION@DEVPATH", then NUL-separated KEY=VALUE pairs. It fails on a
// message udev rebroadcasts, on a missing header and on an ACTION or DEVPATH
// pair that contradicts the header.
func ParseUevent(msg []byte) (Uevent, error) {
	if bytes.HasPrefix(msg, []byte("libudev\x00")) {
		return Uevent{}, errUdevMessage
	}
	fields := strings.Split(strings.TrimRight(string(msg), "\x00"), "\x00")
	action, path, ok := strings.Cut(fields[0], "@")
	if !ok || action == "" || path == "" {
		return Uevent{}, fmt.Errorf("kms: uevent header %q is not ACTION@DEVPATH", fields[0])
	}
	u := Uevent{Action: actionOf(action), DevPath: path}
	for _, f := range fields[1:] {
		key, value, ok := strings.Cut(f, "=")
		if !ok {
			return Uevent{}, fmt.Errorf("kms: uevent for %s has field %q without '='", path, f)
		}
		switch key {
		case "ACTION":
			if value != action {
				return Uevent{}, fmt.Errorf("kms: uevent header says %q, ACTION says %q", action, value)
			}
		case "DEVPATH":
			if value != path {
				return Uevent{}, fmt.Errorf("kms: uevent header names %s, DEVPATH names %s", path, value)
			}
		case "SUBSYSTEM":
			u.Subsystem = value
		case "DEVNAME":
			u.DevName = value
		case "HOTPLUG":
			u.Hotplug = value == "1"
		}
	}
	return u, nil
}

const kernelGroup = 1

// ueventSocket is a NETLINK_KOBJECT_UEVENT socket subscribed to the kernel's
// uevents, read through the runtime's poller so Close unblocks a read.
type ueventSocket struct {
	f   *os.File
	buf []byte
}

func openUevents() (*ueventSocket, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, fmt.Errorf("kms: open a uevent socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: kernelGroup}); err != nil {
		return nil, errors.Join(fmt.Errorf("kms: subscribe to kernel uevents: %w", err), unix.Close(fd))
	}
	return &ueventSocket{f: os.NewFile(uintptr(fd), "uevent"), buf: make([]byte, 1<<16)}, nil
}

// read waits for the next uevent message.
func (s *ueventSocket) read() ([]byte, error) {
	n, err := s.f.Read(s.buf)
	if err != nil {
		return nil, fmt.Errorf("kms: read a uevent: %w", err)
	}
	return s.buf[:n], nil
}

func (s *ueventSocket) close() error {
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("kms: close the uevent socket: %w", err)
	}
	return nil
}
