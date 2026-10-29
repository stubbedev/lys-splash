package daemon

import (
	"fmt"
	"os"
	"strings"
)

// Mode is what the splash is shown for.
type Mode uint8

// The modes plymouthd's --mode takes.
const (
	Boot Mode = iota
	Shutdown
	Reboot
	Kexec
	Updates
)

var modeNames = map[string]Mode{"boot": Boot, "shutdown": Shutdown, "reboot": Reboot, "kexec": Kexec, "updates": Updates}

// ParseMode reads a --mode value.
func ParseMode(s string) (Mode, error) {
	m, ok := modeNames[s]
	if !ok {
		return 0, fmt.Errorf("daemon: mode %q is none of boot, shutdown, reboot, kexec and updates", s)
	}
	return m, nil
}

func (m Mode) String() string {
	for name, v := range modeNames {
		if v == m {
			return name
		}
	}
	return fmt.Sprintf("Mode(%d)", m)
}

// stopping reports whether the machine goes down in m, so the splash stays
// until the power goes rather than waiting for a quit.
func (m Mode) stopping() bool {
	return m == Shutdown || m == Reboot || m == Kexec
}

// updatingModes are change-mode values that show the update progress bar.
var updatingModes = []string{"updates", "system-upgrade", "firmware-upgrade"}

// InitrdRelease is the file whose presence marks the initrd.
const InitrdRelease = "/etc/initrd-release"

// RootStorageMarker is the first byte of argv[0] that makes systemd spare a
// process when it switches out of the initrd.
const RootStorageMarker = '@'

// NeedsReexec reports whether the splash, about to run in mode with argv0,
// must first re-execute itself with RootStorageMarker leading argv[0], once.
// In boot mode inside the initrd the marker lets it survive the switch to
// the real root. In the shutdown, reboot and kexec modes it keeps
// systemd-shutdown from killing it, so its card stays open until the power
// goes: the kernel puts fbdev's console back on the screen when the last
// file on a card closes, CLOSEFB or not.
func NeedsReexec(mode Mode, argv0 string, initrd bool) bool {
	return (mode == Boot && initrd || mode.stopping()) && !strings.HasPrefix(argv0, string(RootStorageMarker))
}

// InInitrd reports whether this process runs in the initrd.
func InInitrd() (bool, error) {
	_, err := os.Stat(InitrdRelease)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("daemon: check for %s: %w", InitrdRelease, err)
	}
	return true, nil
}
