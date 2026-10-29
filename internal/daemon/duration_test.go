package daemon

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

func TestLoadBootDurationMissing(t *testing.T) {
	t.Parallel()
	if _, err := LoadBootDuration(filepath.Join(t.TempDir(), "boot-duration")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file loaded with %v, want fs.ErrNotExist", err)
	}
}

func TestLoadBootDurationCorrupt(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "\n", "abc", "12s", "0", "-3.5", "NaN", "Inf", "1e300", "12.5 13"} {
		path := filepath.Join(t.TempDir(), "boot-duration")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		d, err := LoadBootDuration(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%q loaded as %v, %v; want a corrupt-file error", text, d, err)
		}
	}
}

func TestBootDurationRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lys-splash", "boot-duration")
	for _, d := range []time.Duration{12345 * time.Millisecond, 3 * time.Minute} {
		if err := SaveBootDuration(path, d); err != nil {
			t.Fatal(err)
		}
		got, err := LoadBootDuration(path)
		if err != nil || got != d {
			t.Errorf("saved %v, loaded %v, %v", d, got, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "boot-duration" {
		t.Errorf("the directory holds %v after two saves, want the file alone", entries)
	}
	if err := SaveBootDuration(path, 0); err == nil {
		t.Error("a zero duration was saved")
	}
}

func TestSaveBootDurationFailureLeavesNoTemporary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "boot-duration")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveBootDuration(path, time.Second); err == nil {
		t.Fatal("saving over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Errorf("a failed save left %v", entries)
	}
}

func TestBootProgress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		elapsed, total time.Duration
		want           protocol.Percent
	}{
		{0, 10 * time.Second, 0},
		{5 * time.Second, 10 * time.Second, 50},
		{9990 * time.Millisecond, 10 * time.Second, 99},
		{20 * time.Second, 10 * time.Second, 99},
		{1000 * 24 * time.Hour, 2000 * 24 * time.Hour, 50},
	}
	for _, tc := range tests {
		if got := bootProgress(tc.elapsed, tc.total); got != tc.want {
			t.Errorf("bootProgress(%v, %v) = %d, want %d", tc.elapsed, tc.total, got, tc.want)
		}
	}
}

func TestBootRecordsItsDurationOnceWritable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mode     Mode
		writable bool
		recorded bool
	}{
		{"boot with a writable root", Boot, true, true},
		{"boot with a read-only root", Boot, false, false},
		{"shutdown", Shutdown, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := start(t, setup{mode: tc.mode, screens: 1})
			if tc.writable {
				r.call(t, protocol.SystemInitialized{}, protocol.Ack{})
			}
			r.call(t, protocol.Quit{}, protocol.Ack{})
			if err := r.finished(t); err != nil {
				t.Fatal(err)
			}
			d, err := LoadBootDuration(r.duration)
			switch {
			case tc.recorded && (err != nil || d <= 0):
				t.Errorf("the quit recorded %v, %v; want a positive duration", d, err)
			case !tc.recorded && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("the quit recorded %v, %v; want no file", d, err)
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestBootReadsThePreviousDurationAfterTheNewRoot(t *testing.T) {
	t.Parallel()
	const missing, corrupt = `"no previous boot to measure progress by"`, `"showing no boot progress"`
	tests := []struct {
		name     string
		previous string
		missing  int
		corrupt  int
	}{
		{"recorded", "3600.000\n", 1, 0},
		{"corrupt", "soon\n", 1, 1},
		{"missing", "", 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "boot-duration")
			var logs lockedBuffer
			r := start(t, setup{screens: 1, duration: path, log: slog.New(slog.NewTextHandler(&logs, nil)), chroot: func(string) error {
				if tc.previous == "" {
					return nil
				}
				return os.WriteFile(path, []byte(tc.previous), 0o600)
			}})
			r.call(t, protocol.NewRoot{Dir: root}, protocol.Ack{})
			r.call(t, protocol.Quit{}, protocol.Ack{})
			if err := r.finished(t); err != nil {
				t.Fatal(err)
			}
			out := logs.String()
			if strings.Count(out, missing) != tc.missing || strings.Count(out, corrupt) != tc.corrupt {
				t.Errorf("want the file missing %d times and corrupt %d times, logged:\n%s", tc.missing, tc.corrupt, out)
			}
		})
	}
}
