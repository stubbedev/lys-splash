// Package client is the plymouth command line: each subcommand and option
// becomes requests to the daemon, and the daemon's responses become the
// exit status and output plymouth's client gives, so scripts that call
// plymouth keep working.
package client

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/lys-splash/internal/protocol"
)

// The exit statuses.
const (
	ExitOK = 0
	// ExitFailed is a refused request, an unanswered prompt or bad usage.
	ExitFailed = 1
	// ExitDisconnected is the daemon hanging up mid-request, as plymouth
	// reports it.
	ExitDisconnected = 2
	// ExitNoDaemon is no daemon listening.
	ExitNoDaemon = 3
)

const pingTimeout = 5 * time.Second

// interrupted is the answer of a prompt abandoned with Ctrl-C.
const interrupted = 0x03

// Client runs plymouth commands against the daemon at Socket.
type Client struct {
	Socket string
	Stdout io.Writer
	Stderr io.Writer
	// Command runs a --command with the answer on its standard input, or
	// none when input is nil, and returns its exit status.
	Command func(argv []string, input []byte) (int, error)
}

// RunCommand runs argv with input on its standard input, as --command does.
func RunCommand(argv []string, input []byte) (int, error) {
	if len(argv) == 0 {
		return ExitFailed, errors.New("client: an empty --command")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // running the caller's --command is the point
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 127, fmt.Errorf("client: run %q: %w", argv[0], err)
	}
	return ExitOK, nil
}

type errDisconnected struct{ err error }

func (e errDisconnected) Error() string {
	return "unexpectedly disconnected from the splash daemon: " + e.err.Error()
}
func (e errDisconnected) Unwrap() error { return e.err }

type session struct {
	c net.Conn
}

func (s *session) do(r protocol.Request) (protocol.Response, error) {
	wire, err := protocol.AppendRequest(nil, r)
	if err != nil {
		return nil, err
	}
	if _, err := s.c.Write(wire); err != nil {
		return nil, errDisconnected{err}
	}
	resp, err := protocol.ReadResponse(s.c)
	if err != nil {
		return nil, errDisconnected{err}
	}
	return resp, nil
}

func (s *session) ack(r protocol.Request) (int, error) {
	resp, err := s.do(r)
	if err != nil {
		return 0, err
	}
	if _, ok := resp.(protocol.Ack); ok {
		return ExitOK, nil
	}
	return ExitFailed, nil
}

type action struct {
	run func(s *session) (int, error)
	// alone runs instead of run when no daemon listens; nil means
	// ExitNoDaemon.
	alone func() (int, error)
}

// Main runs the plymouth command line args, without the program name, and
// returns the exit status.
func (c *Client) Main(args []string) int {
	cmd, err := c.parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitFailed
	}
	conn, err := net.Dial("unix", c.Socket)
	if err != nil {
		if cmd.alone != nil {
			return c.finish(cmd.alone())
		}
		fmt.Fprintf(c.Stderr, "no splash daemon is running: %v\n", err)
		return ExitNoDaemon
	}
	status := c.finish(cmd.run(&session{c: conn}))
	if err := conn.Close(); err != nil {
		fmt.Fprintf(c.Stderr, "closing the connection to the splash daemon: %v\n", err)
	}
	return status
}

func (c *Client) finish(status int, err error) int {
	if err == nil {
		return status
	}
	fmt.Fprintln(c.Stderr, err)
	if _, ok := errors.AsType[errDisconnected](err); ok {
		return ExitDisconnected
	}
	return ExitFailed
}

func (c *Client) parse(args []string) (*action, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: plymouth [--ping | --wait | ...] or plymouth COMMAND [OPTIONS]")
	}
	if strings.HasPrefix(args[0], "-") {
		return c.options(args)
	}
	return c.subcommand(args[0], args[1:])
}

func argv(command string) []string {
	if command == "" {
		return nil
	}
	return strings.Fields(command)
}

func newFlags(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

func (c *Client) options(args []string) (*action, error) {
	fs := newFlags("plymouth", c.Stderr)
	ping := fs.Bool("ping", false, "check whether the daemon is running")
	wait := fs.Bool("wait", false, "wait until the daemon quits")
	quit := fs.Bool("quit", false, "tell the daemon to quit")
	vt := fs.Bool("has-active-vt", false, "check whether the daemon has an active VT")
	sysinit := fs.Bool("sysinit", false, "tell the daemon the root is writable")
	show := fs.Bool("show-splash", false, "show the splash")
	hide := fs.Bool("hide-splash", false, "hide the splash")
	askPassword := fs.Bool("ask-for-password", false, "ask for a password")
	details := fs.Bool("details", false, "tell the daemon the boot hit an error")
	newroot := fs.String("newroot", "", "tell the daemon the new root is mounted at this directory")
	update := fs.String("update", "", "tell the daemon a boot status")
	ignore := fs.String("ignore-keystroke", "", "stop waiting for these keys")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("plymouth: unexpected %q after the options", fs.Arg(0))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	simple := []struct {
		on  bool
		req protocol.Request
	}{
		{*show, protocol.ShowSplash{}},
		{*hide, protocol.HideSplash{}},
		{*quit, protocol.Quit{}},
		{*sysinit, protocol.SystemInitialized{}},
		{*details, protocol.Error{}},
		{set["newroot"], protocol.NewRoot{Dir: *newroot}},
		{set["update"], protocol.Update{Status: *update}},
		{set["ignore-keystroke"], protocol.IgnoreKeystroke{Keys: *ignore}},
	}
	switch {
	case *ping:
		return &action{run: func(s *session) (int, error) {
			if err := s.c.SetDeadline(time.Now().Add(pingTimeout)); err != nil {
				return 0, err
			}
			return s.ack(protocol.Ping{})
		}}, nil
	case *vt:
		return &action{run: func(s *session) (int, error) { return s.ack(protocol.HasActiveVT{}) }}, nil
	case *wait:
		return &action{run: waitQuit, alone: func() (int, error) { return ExitOK, nil }}, nil
	case *askPassword:
		return c.password("", nil, 1, false), nil
	}
	for _, o := range simple {
		if o.on {
			req := o.req
			return &action{run: func(s *session) (int, error) { return s.ack(req) }}, nil
		}
	}
	return nil, errors.New("plymouth: no option given")
}

func waitQuit(s *session) (int, error) {
	_, err := s.c.Read(make([]byte, 1))
	if errors.Is(err, io.EOF) {
		return ExitOK, nil
	}
	if err != nil {
		return 0, fmt.Errorf("client: wait for the daemon to quit: %w", err)
	}
	return ExitFailed, errors.New("client: the daemon sent data while waiting for it to quit")
}

var changeModes = []string{"boot-up", "shutdown", "reboot", "updates", "system-upgrade", "firmware-upgrade", "system-reset"}

func (c *Client) subcommand(name string, args []string) (*action, error) {
	fs := newFlags("plymouth "+name, c.Stderr)
	ack := func(req func() protocol.Request) *action {
		return &action{run: func(s *session) (int, error) { return s.ack(req()) }}
	}
	var cmd *action
	switch name {
	case "show-splash":
		cmd = ack(func() protocol.Request { return protocol.ShowSplash{} })
	case "hide-splash":
		cmd = ack(func() protocol.Request { return protocol.HideSplash{} })
	case "display-message", "message":
		text := fs.String("text", "", "the message")
		cmd = ack(func() protocol.Request { return protocol.ShowMessage{Text: *text} })
	case "hide-message":
		text := fs.String("text", "", "the message")
		cmd = ack(func() protocol.Request { return protocol.HideMessage{Text: *text} })
	case "change-mode":
		modes := make(map[string]*bool, len(changeModes))
		for _, m := range changeModes {
			modes[m] = fs.Bool(m, false, "switch to "+m)
		}
		cmd = ack(func() protocol.Request {
			for _, m := range changeModes {
				if *modes[m] {
					return protocol.ChangeMode{Mode: m}
				}
			}
			return protocol.ChangeMode{}
		})
	case "system-update":
		progress := fs.Int("progress", 0, "the update's progress in percent")
		cmd = &action{run: func(s *session) (int, error) {
			p, err := protocol.ParsePercent(strconv.Itoa(*progress))
			if err != nil {
				return ExitFailed, fmt.Errorf("plymouth system-update: --progress: %w", err)
			}
			return s.ack(protocol.SystemUpdate{Progress: p})
		}}
	case "update":
		status := fs.String("status", "", "the boot status")
		cmd = ack(func() protocol.Request { return protocol.Update{Status: *status} })
	case "update-root-fs":
		dir := fs.String("new-root-dir", "", "the root about to be switched to")
		rw := fs.Bool("read-write", false, "the root is writable")
		cmd = ack(func() protocol.Request {
			if *rw {
				return protocol.SystemInitialized{}
			}
			return protocol.NewRoot{Dir: *dir}
		})
	case "deactivate":
		cmd = ack(func() protocol.Request { return protocol.Deactivate{} })
	case "reactivate":
		cmd = ack(func() protocol.Request { return protocol.Reactivate{} })
	case "quit":
		retain := fs.Bool("retain-splash", false, "keep the splash's frame on screen")
		cmd = ack(func() protocol.Request { return protocol.Quit{Retain: *retain} })
	case "reload":
		cmd = ack(func() protocol.Request { return protocol.Reload{} })
	case "pause-progress":
		cmd = ack(func() protocol.Request { return protocol.PauseProgress{} })
	case "unpause-progress":
		cmd = ack(func() protocol.Request { return protocol.ResumeProgress{} })
	case "report-error":
		cmd = ack(func() protocol.Request { return protocol.Error{} })
	case "ignore-keystroke":
		keys := fs.String("keys", "", "the keys")
		cmd = ack(func() protocol.Request { return protocol.IgnoreKeystroke{Keys: *keys} })
	case "ask-for-password":
		prompt := fs.String("prompt", "", "the prompt")
		command := fs.String("command", "", "a command given the password on its standard input")
		tries := fs.Int("number-of-tries", 0, "how often to ask before giving up, with --command; 0 is no limit")
		dontPause := fs.Bool("dont-pause-progress", false, "keep the progress going while asking")
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		return c.password(*prompt, argv(*command), *tries, !*dontPause), nil
	case "ask-question":
		prompt := fs.String("prompt", "", "the question")
		command := fs.String("command", "", "a command given the answer on its standard input")
		dontPause := fs.Bool("dont-pause-progress", false, "keep the progress going while asking")
		cmd = &action{run: func(s *session) (int, error) {
			return c.question(s, protocol.Question{Prompt: *prompt}, argv(*command), !*dontPause)
		}}
	case "watch-keystroke":
		keys := fs.String("keys", "", "the keys to wait for")
		command := fs.String("command", "", "a command given the key on its standard input")
		cmd = &action{run: func(s *session) (int, error) {
			return c.question(s, protocol.WatchKeystroke{Keys: *keys}, argv(*command), false)
		}}
	default:
		return nil, fmt.Errorf("plymouth: unknown command %q", name)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("plymouth %s: unexpected %q", name, fs.Arg(0))
	}
	return cmd, nil
}

func (c *Client) answered(s *session, req protocol.Request) ([]byte, bool, error) {
	resp, err := s.do(req)
	if err != nil {
		return nil, false, err
	}
	a, ok := resp.(protocol.Answer)
	return a.Data, ok, nil
}

func (c *Client) paused(s *session, pause bool, ask func() (int, error)) (int, error) {
	if pause {
		if _, err := s.do(protocol.PauseProgress{}); err != nil {
			return 0, err
		}
	}
	status, err := ask()
	if err != nil || !pause {
		return status, err
	}
	if _, err := s.do(protocol.ResumeProgress{}); err != nil {
		return 0, err
	}
	return status, nil
}

func (c *Client) question(s *session, req protocol.Request, command []string, pause bool) (int, error) {
	return c.paused(s, pause, func() (int, error) {
		answer, ok, err := c.answered(s, req)
		if err != nil || !ok {
			return ExitFailed, err
		}
		if command != nil {
			_, err := c.Command(command, answer)
			return ExitOK, err
		}
		_, err = c.Stdout.Write(answer)
		return ExitOK, err
	})
}

func (c *Client) password(prompt string, command []string, tries int, pause bool) *action {
	cmd := &action{run: func(s *session) (int, error) {
		return c.paused(s, pause, func() (int, error) { return c.askPassword(s, prompt, command, tries) })
	}}
	if command != nil {
		cmd.alone = func() (int, error) { return c.commandAlone(command, tries) }
	}
	return cmd
}

func limit(tries int) int {
	if tries <= 0 {
		return int(^uint(0) >> 1)
	}
	return tries
}

func (c *Client) commandAlone(command []string, tries int) (int, error) {
	status := 127
	for range limit(tries) {
		var err error
		if status, err = c.Command(command, nil); err != nil || status == ExitOK {
			return status, err
		}
	}
	return status, nil
}

func (c *Client) askPassword(s *session, prompt string, command []string, tries int) (int, error) {
	if command == nil {
		answer, ok, err := c.answered(s, protocol.Password{Prompt: prompt})
		defer clear(answer)
		if err != nil || !ok || len(answer) > 0 && answer[0] == interrupted {
			return ExitFailed, err
		}
		_, err = c.Stdout.Write(answer)
		return ExitOK, err
	}
	resp, err := s.do(protocol.CachedPassword{})
	if err != nil {
		return 0, err
	}
	if cached, ok := resp.(protocol.MultipleAnswers); ok {
		for _, a := range cached.Answers {
			status, err := c.Command(command, a)
			clear(a)
			if err != nil || status == ExitOK {
				return status, err
			}
		}
	}
	status := 127
	for range limit(tries) {
		answer, ok, err := c.answered(s, protocol.Password{Prompt: prompt})
		if err != nil {
			return 0, err
		}
		if !ok {
			return c.commandAlone(command, tries)
		}
		if len(answer) > 0 && answer[0] == interrupted {
			clear(answer)
			return ExitFailed, nil
		}
		status, err = c.Command(command, answer)
		clear(answer)
		if err != nil || status == ExitOK {
			return status, err
		}
	}
	return status, nil
}
