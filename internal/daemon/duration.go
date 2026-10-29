package daemon

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/lys-lib/atomicfile"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

// BootDurationFile is where a boot records how long its splash showed, for
// the next boot's progress.
const BootDurationFile = "/var/lib/lys-splash/boot-duration"

const maxBootSeconds = math.MaxInt64 / float64(time.Second)

// LoadBootDuration reads the duration a previous boot recorded at path: a
// positive number of seconds. A missing file is an error wrapping
// fs.ErrNotExist.
func LoadBootDuration(path string) (time.Duration, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the boot-duration file is the daemon's own state
	if err != nil {
		return 0, fmt.Errorf("daemon: read the boot duration: %w", err)
	}
	text := strings.TrimSpace(string(data))
	secs, err := strconv.ParseFloat(text, 64)
	if err != nil || !(secs > 0 && secs < maxBootSeconds) {
		return 0, fmt.Errorf("daemon: the boot duration in %s is %q, not a positive number of seconds", path, text)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// SaveBootDuration records d at path, replacing the file whole so a crash
// leaves either the previous duration or this one, and creating its
// directory.
func SaveBootDuration(path string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("daemon: the boot duration %v for %s is not positive", d, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the boot duration is not secret
		return fmt.Errorf("daemon: create the directory for %s: %w", path, err)
	}
	text := strconv.FormatFloat(d.Seconds(), 'f', 3, 64) + "\n"
	if err := atomicfile.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("daemon: record the boot duration: %w", err)
	}
	return nil
}

// bootProgress is how far along a boot is after elapsed when the previous
// one took total: proportional, and short of done until the splash quits.
func bootProgress(elapsed, total time.Duration) protocol.Percent {
	if elapsed >= total {
		return protocol.MaxPercent - 1
	}
	return protocol.Percent(float64(max(elapsed, 0)) / float64(total) * float64(protocol.MaxPercent))
}
