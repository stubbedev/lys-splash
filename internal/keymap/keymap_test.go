package keymap_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-splash/internal/keymap"
)

func root(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"systemd's vconsole.conf", map[string]string{"etc/vconsole.conf": "FONT=lat9w-16\nKEYMAP=de-latin1\n"}, "de-latin1"},
		{"quoted", map[string]string{"etc/vconsole.conf": "# the console\nKEYMAP=\"us\"\n"}, "us"},
		{"the last assignment wins", map[string]string{"etc/vconsole.conf": "KEYMAP=us\nKEYMAP='fr'\n"}, "fr"},
		{"console-setup's keyboard", map[string]string{"etc/default/keyboard": "XKBMODEL=\"pc105\"\nXKBLAYOUT=\"de\"\nXKBVARIANT=\"\"\n"}, "de"},
		{"vconsole.conf without a keymap falls through", map[string]string{"etc/vconsole.conf": "FONT=ter-v16n\n", "etc/default/keyboard": "XKBLAYOUT=dk\n"}, "dk"},
		{"vconsole.conf before console-setup", map[string]string{"etc/vconsole.conf": "KEYMAP=no\n", "etc/default/keyboard": "XKBLAYOUT=dk\n"}, "no"},
		{"a commented keymap is none", map[string]string{"etc/vconsole.conf": "#KEYMAP=de\n"}, ""},
		{"neither file", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := keymap.Load(root(t, tc.files)); err != nil || got != tc.want {
				t.Errorf("Load = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestLoadRefusesAnUnmatchedQuote(t *testing.T) {
	t.Parallel()
	if got, err := keymap.Load(root(t, map[string]string{"etc/vconsole.conf": "KEYMAP=\"de\n"})); err == nil {
		t.Errorf("Load = %q, want an error naming the quote", got)
	}
}

func TestLoadReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()
	dir := root(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "etc", "vconsole.conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := keymap.Load(dir); err == nil {
		t.Errorf("Load of a directory = %q, want an error", got)
	}
}
