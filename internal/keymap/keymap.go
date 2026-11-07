package keymap

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type source struct {
	path string
	key  string
}

var sources = [...]source{
	{path: "etc/vconsole.conf", key: "KEYMAP"},
	{path: "etc/default/keyboard", key: "XKBLAYOUT"},
}

func Load(root string) (string, error) {
	for _, s := range sources {
		path := filepath.Join(root, s.path)
		data, err := os.ReadFile(path) //nolint:gosec // the system's own console configuration
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("keymap: %w", err)
		}
		name, err := value(data, s.key)
		if err != nil {
			return "", fmt.Errorf("keymap: %s: %w", path, err)
		}
		if name != "" {
			return name, nil
		}
	}
	return "", nil
}

func value(data []byte, key string) (string, error) {
	var found string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		name, raw, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || strings.TrimSpace(name) != key {
			continue
		}
		v, err := unquote(strings.TrimSpace(raw))
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		found = v
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return found, nil
}

func unquote(s string) (string, error) {
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return s, nil
	}
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return "", fmt.Errorf("%s has an unmatched quote", s)
	}
	return s[1 : len(s)-1], nil
}
