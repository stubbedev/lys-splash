package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-splash/internal/daemon"
)

func TestRoleOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		argv []string
		want role
	}{
		{[]string{"/usr/sbin/plymouthd", "--mode=boot"}, plymouthdRole},
		{[]string{"plymouthd"}, plymouthdRole},
		{[]string{"@/usr/sbin/plymouthd", "--mode=boot"}, plymouthdRole},
		{[]string{"/usr/bin/plymouth", "--ping"}, clientRole},
		{[]string{"plymouth", "--mode=boot"}, clientRole},
		{[]string{"/bin/lys-splash", "--mode=boot"}, daemonRole},
		{[]string{"@/bin/lys-splash", "--mode", "shutdown"}, daemonRole},
		{[]string{"lys-splash", "quit", "--retain-splash"}, clientRole},
		{[]string{"lys-splash", "--ping"}, clientRole},
		{[]string{"lys-splash"}, clientRole},
	}
	for _, tc := range tests {
		if got := roleOf(tc.argv); got != tc.want {
			t.Errorf("roleOf(%q) = %d, want %d", tc.argv, got, tc.want)
		}
	}
}

func TestParseOptions(t *testing.T) {
	t.Parallel()
	o, err := parseOptions("plymouthd", []string{"--mode=reboot", "--attach-to-session", "--pid-file=/run/plymouth/pid"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.mode != daemon.Reboot || o.pidFile != "/run/plymouth/pid" || o.theme != defaultTheme || o.noDaemon {
		t.Errorf("parseOptions = %+v", o)
	}
	if o, err := parseOptions("lys-splash", nil, io.Discard); err != nil || o.mode != daemon.Boot {
		t.Errorf("the default mode = %+v, %v", o, err)
	}
	for _, args := range [][]string{{"--mode=suspend"}, {"--frobnicate"}, {"--mode=boot", "extra"}} {
		if _, err := parseOptions("lys-splash", args, io.Discard); err == nil {
			t.Errorf("parseOptions(%q) succeeded", args)
		}
	}
}

func TestDetachedStderrPrefersTheKernelsLog(t *testing.T) {
	t.Parallel()
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	device := filepath.Join(t.TempDir(), "kmsg")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, target, closeFile := detachedStderr(device, log)
	if target != logsToKmsg || f.Name() != device {
		t.Errorf("with the device present, stderr = %s and target = %s", f.Name(), target)
	}
	if err := closeFile(); err != nil {
		t.Error(err)
	}
	if logged.Len() != 0 {
		t.Errorf("logged with the device present: %s", logged.String())
	}

	f, target, closeFile = detachedStderr(device+"-missing", log)
	if target != logsToStderr || f != os.Stderr {
		t.Errorf("with no device, stderr = %s and target = %s", f.Name(), target)
	}
	if err := closeFile(); err != nil {
		t.Error(err)
	}
	if !strings.Contains(logged.String(), "kmsg-missing") {
		t.Errorf("the missing device was not reported: %q", logged.String())
	}
}

func TestListenWithADaemonRunningReportsReady(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.DiscardHandler)
	socket := "@lys-splash-main-test-" + rand.Text()
	addr := "@lys-splash-main-notify-" + rand.Text()
	ready, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	first, err := listen(socket, addr, quiet)
	if err != nil || first == nil {
		t.Fatalf("the first daemon's listen = %v, %v", first, err)
	}
	defer first.Close()
	second, err := listen(socket, addr, quiet)
	if err != nil || second != nil {
		t.Fatalf("the second daemon's listen = %v, %v; want no server and no error", second, err)
	}
	if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := ready.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Errorf("the second daemon notified %q, %v; want READY=1", buf[:n], err)
	}
}
