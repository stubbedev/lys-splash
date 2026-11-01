// Command lys-splash is the lys desktop's boot splash. Run as plymouthd, or
// as lys-splash with --mode first, it is the daemon; run as plymouth, or as
// lys-splash with anything else, it is the client.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/atomicfile"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-splash/internal/client"
	"github.com/stubbedev/lys-splash/internal/console"
	"github.com/stubbedev/lys-splash/internal/daemon"
	"github.com/stubbedev/lys-splash/internal/kms"
	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/server"
	"github.com/stubbedev/lys-splash/internal/theme"
)

const (
	defaultTheme  = "/etc/lys-splash/theme.yaml"
	daemonizedEnv = "LYS_SPLASH_DAEMONIZED"
	readyTimeout  = 30 * time.Second
	selfExe       = "/proc/self/exe"
)

func main() {
	os.Exit(run(os.Args))
}

type role uint8

const (
	clientRole role = iota
	daemonRole
	plymouthdRole
)

func roleOf(argv []string) role {
	switch name := filepath.Base(strings.TrimPrefix(argv[0], string(daemon.RootStorageMarker))); {
	case name == "plymouthd":
		return plymouthdRole
	case name == "plymouth":
		return clientRole
	case len(argv) > 1 && (argv[1] == "--mode" || strings.HasPrefix(argv[1], "--mode=")):
		return daemonRole
	}
	return clientRole
}

func run(argv []string) int {
	if roleOf(argv) == clientRole {
		c := client.Client{Socket: protocol.SocketName, Stdout: os.Stdout, Stderr: os.Stderr, Command: client.RunCommand}
		return c.Main(argv[1:])
	}
	return runDaemon(argv, roleOf(argv) == plymouthdRole)
}

type options struct {
	mode     daemon.Mode
	theme    string
	pidFile  string
	noDaemon bool
	debug    bool
}

func parseOptions(name string, args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "boot", "what the splash shows for: boot, shutdown, reboot, kexec or updates")
	o := options{}
	fs.StringVar(&o.theme, "theme", defaultTheme, "the theme.yaml")
	fs.StringVar(&o.pidFile, "pid-file", "", "write the daemon's pid here")
	fs.BoolVar(&o.noDaemon, "no-daemon", false, "as plymouthd, stay in the foreground")
	fs.BoolVar(&o.debug, "debug", false, "log at debug level")
	fs.Bool("attach-to-session", true, "accepted for plymouthd's command line; the splash always runs on the console's session")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("%s: unexpected %q", name, fs.Arg(0))
	}
	var err error
	o.mode, err = daemon.ParseMode(*mode)
	return o, err
}

func runDaemon(argv []string, plymouthd bool) int {
	o, err := parseOptions(filepath.Base(argv[0]), argv[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	level := slog.LevelInfo
	if o.debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	initrd, err := daemon.InInitrd()
	if err != nil {
		log.Error("starting", "err", err)
		return 1
	}
	if daemon.NeedsReexec(o.mode, argv[0], initrd) {
		marked := append([]string{string(daemon.RootStorageMarker) + argv[0]}, argv[1:]...)
		err := unix.Exec(selfExe, marked, os.Environ())
		log.Error("re-executing with argv[0] marked for systemd to spare the splash", "err", err)
		return 1
	}
	if plymouthd && !o.noDaemon && os.Getenv(daemonizedEnv) == "" {
		if err := daemonize(argv); err != nil {
			log.Error("daemonizing", "err", err)
			return 1
		}
		return 0
	}
	if err := serve(o, log); err != nil {
		log.Error("the splash failed", "err", err)
		return 1
	}
	return 0
}

func daemonize(argv []string) error {
	addr := "@lys-splash-ready-" + rand.Text()
	ready, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("listen for the daemon's readiness: %w", err)
	}
	err = startDetached(argv, addr, ready)
	if cerr := ready.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close the readiness socket: %w", cerr))
	}
	return err
}

func startDetached(argv []string, addr string, ready *net.UnixConn) error {
	cmd := exec.Command(selfExe, argv[1:]...) //nolint:gosec // the daemon re-runs itself with its own arguments
	cmd.Args[0] = argv[0]
	cmd.Env = append(os.Environ(), daemonizedEnv+"=1", daemon.NotifyEnv+"="+addr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	got := make(chan error, 1)
	go func() { got <- awaitReady(ready) }()
	select {
	case err := <-got:
		if err != nil {
			return errors.Join(err, cmd.Process.Kill())
		}
		return nil
	case err := <-exited:
		return fmt.Errorf("the daemon exited before it was ready: %w", err)
	}
}

func awaitReady(c *net.UnixConn) error {
	if err := c.SetReadDeadline(time.Now().Add(readyTimeout)); err != nil {
		return err
	}
	buf := make([]byte, 256)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return fmt.Errorf("wait for the daemon to be ready: %w", err)
		}
		for line := range strings.SplitSeq(string(buf[:n]), "\n") {
			if line == "READY=1" {
				return nil
			}
		}
	}
}

func writePid(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the pid file is world-readable, as plymouthd's is
		return fmt.Errorf("create the pid file's directory: %w", err)
	}
	return atomicfile.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// listen takes the protocol's socket. With another daemon listening it
// reports ready to notify and returns no server, as plymouthd exits 0 when
// one is already running.
func listen(socket, notify string, log *slog.Logger) (*server.Server, error) {
	srv, err := server.Listen(socket, 0, log)
	if errors.Is(err, server.ErrRunning) {
		log.Info("a splash is already running", "socket", socket)
		return nil, daemon.Notify(notify, "READY=1")
	}
	return srv, err
}

func serve(o options, log *slog.Logger) (err error) {
	notify := os.Getenv(daemon.NotifyEnv)
	srv, err := listen(protocol.SocketName, notify, log)
	if srv == nil {
		return err
	}
	t, err := theme.Load(o.theme)
	if err != nil {
		log.Warn("falling back to the default theme", "err", err)
	}
	bgrt, err := theme.LoadBGRT(theme.BGRTDir)
	if err != nil {
		log.Warn("drawing without the firmware's logo", "err", err)
	}
	var vt daemon.Terminal
	switch v, err := console.OpenActive(console.SystemPaths); {
	case errors.Is(err, console.ErrNoVT):
		log.Info("no VT: no keys are read, so prompts get no answer", "err", err)
	case err != nil:
		log.Warn("running without the VT", "err", err)
	default:
		vt = v
	}
	var capture daemon.Console
	if c, err := console.CaptureConsole(console.SystemPaths); err != nil {
		log.Warn("showing no boot messages", "err", err)
	} else {
		capture = c
	}
	display, err := kms.Open(kms.SystemPaths, log)
	if err != nil {
		err = errors.Join(err, srv.Close())
		if vt != nil {
			err = errors.Join(err, vt.Restore())
		}
		if capture != nil {
			err = errors.Join(err, capture.Leave())
		}
		return err
	}
	if o.pidFile != "" {
		if err := writePid(o.pidFile); err != nil {
			log.Warn("writing the pid file", "err", err)
		} else {
			defer func() {
				if rerr := os.Remove(o.pidFile); rerr != nil && !errors.Is(rerr, os.ErrNotExist) { //nolint:gosec // the pid file is the administrator's --pid-file
					err = errors.Join(err, rerr)
				}
			}()
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cfg := daemon.Config{
		Mode:         o.mode,
		ThemePath:    o.theme,
		Server:       srv,
		Notify:       notify,
		Chroot:       daemon.SwitchRoot,
		BootDuration: daemon.BootDurationFile,
		Log:          log,
	}
	return daemon.Run(ctx, cfg, t, bgrt, daemon.Devices{Display: daemon.KMS{Display: display}, VT: vt, Console: capture})
}
