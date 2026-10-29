package main

import (
	"io"
	"testing"

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
