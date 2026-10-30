package client

import (
	"bytes"
	"crypto/rand"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-splash/internal/protocol"
	"github.com/stubbedev/lys-splash/internal/server"
)

type daemon struct {
	mu       sync.Mutex
	got      []protocol.Request
	answer   func(protocol.Request) protocol.Response
	hangUpOn protocol.Command
}

func (d *daemon) Request(c *server.Call) {
	d.mu.Lock()
	d.got = append(d.got, c.Request)
	d.mu.Unlock()
	if c.Request.Command() == d.hangUpOn {
		return
	}
	if err := c.Reply(d.answer(c.Request)); err != nil {
		panic(err)
	}
}

func (d *daemon) HangUp(*server.Conn) {}

func (d *daemon) requests() []protocol.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.got
}

func ack(protocol.Request) protocol.Response { return protocol.Ack{} }

func serve(t *testing.T, d *daemon) (string, *server.Server) {
	t.Helper()
	name := "@lys-splash-client-test-" + rand.Text()
	s, err := server.Listen(name, uint32(os.Getuid()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(d) }()
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return name, s
}

type ran struct {
	argv  []string
	input string
}

type harness struct {
	client   *Client
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	commands []ran
	status   []int
}

func newHarness(socket string) *harness {
	h := &harness{}
	h.client = &Client{Socket: socket, Stdout: &h.stdout, Stderr: &h.stderr, Command: func(argv []string, input []byte) (int, error) {
		in := "<none>"
		if input != nil {
			in = string(input)
		}
		h.commands = append(h.commands, ran{argv: argv, input: in})
		status := 0
		if len(h.status) > 0 {
			status, h.status = h.status[0], h.status[1:]
		}
		return status, nil
	}}
	return h
}

func TestSubcommandsSendTheirRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want []protocol.Request
	}{
		{[]string{"--ping"}, []protocol.Request{protocol.Ping{}}},
		{[]string{"--has-active-vt"}, []protocol.Request{protocol.HasActiveVT{}}},
		{[]string{"--quit"}, []protocol.Request{protocol.Quit{}}},
		{[]string{"--sysinit"}, []protocol.Request{protocol.SystemInitialized{}}},
		{[]string{"--show-splash"}, []protocol.Request{protocol.ShowSplash{}}},
		{[]string{"--hide-splash"}, []protocol.Request{protocol.HideSplash{}}},
		{[]string{"--details"}, []protocol.Request{protocol.Error{}}},
		{[]string{"--newroot=/sysroot"}, []protocol.Request{protocol.NewRoot{Dir: "/sysroot"}}},
		{[]string{"--update", "udev.service"}, []protocol.Request{protocol.Update{Status: "udev.service"}}},
		{[]string{"--ignore-keystroke=yn"}, []protocol.Request{protocol.IgnoreKeystroke{Keys: "yn"}}},
		{[]string{"show-splash"}, []protocol.Request{protocol.ShowSplash{}}},
		{[]string{"hide-splash"}, []protocol.Request{protocol.HideSplash{}}},
		{[]string{"display-message", "--text=Hello"}, []protocol.Request{protocol.ShowMessage{Text: "Hello"}}},
		{[]string{"message", "--text", "Hi"}, []protocol.Request{protocol.ShowMessage{Text: "Hi"}}},
		{[]string{"hide-message", "--text=Hello"}, []protocol.Request{protocol.HideMessage{Text: "Hello"}}},
		{[]string{"change-mode", "--updates"}, []protocol.Request{protocol.ChangeMode{Mode: "updates"}}},
		{[]string{"change-mode", "--boot-up"}, []protocol.Request{protocol.ChangeMode{Mode: "boot-up"}}},
		{[]string{"system-update", "--progress=40"}, []protocol.Request{protocol.SystemUpdate{Progress: 40}}},
		{[]string{"update", "--status=fsck"}, []protocol.Request{protocol.Update{Status: "fsck"}}},
		{[]string{"update-root-fs", "--new-root-dir=/sysroot"}, []protocol.Request{protocol.NewRoot{Dir: "/sysroot"}}},
		{[]string{"update-root-fs", "--read-write"}, []protocol.Request{protocol.SystemInitialized{}}},
		{[]string{"deactivate"}, []protocol.Request{protocol.Deactivate{}}},
		{[]string{"reactivate"}, []protocol.Request{protocol.Reactivate{}}},
		{[]string{"quit"}, []protocol.Request{protocol.Quit{}}},
		{[]string{"quit", "--retain-splash"}, []protocol.Request{protocol.Quit{Retain: true}}},
		{[]string{"reload"}, []protocol.Request{protocol.Reload{}}},
		{[]string{"pause-progress"}, []protocol.Request{protocol.PauseProgress{}}},
		{[]string{"unpause-progress"}, []protocol.Request{protocol.ResumeProgress{}}},
		{[]string{"report-error"}, []protocol.Request{protocol.Error{}}},
		{[]string{"ignore-keystroke", "--keys=q"}, []protocol.Request{protocol.IgnoreKeystroke{Keys: "q"}}},
	}
	for _, tc := range tests {
		d := &daemon{answer: ack}
		socket, _ := serve(t, d)
		h := newHarness(socket)
		if got := h.client.Main(tc.args); got != ExitOK {
			t.Errorf("plymouth %v exited %d: %s", tc.args, got, h.stderr.String())
		}
		if got := d.requests(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("plymouth %v sent %#v, want %#v", tc.args, got, tc.want)
		}
	}
}

func TestNakFails(t *testing.T) {
	t.Parallel()
	socket, _ := serve(t, &daemon{answer: func(protocol.Request) protocol.Response { return protocol.Nak{} }})
	for _, args := range [][]string{{"--ping"}, {"--has-active-vt"}, {"quit"}, {"update-root-fs", "--new-root-dir=/x"}} {
		if got := newHarness(socket).client.Main(args); got != ExitFailed {
			t.Errorf("plymouth %v after NAK exited %d, want %d", args, got, ExitFailed)
		}
	}
}

func TestNoDaemon(t *testing.T) {
	t.Parallel()
	socket := "@lys-splash-client-nobody-" + rand.Text()
	for _, args := range [][]string{{"--ping"}, {"quit"}, {"ask-for-password"}} {
		if got := newHarness(socket).client.Main(args); got != ExitNoDaemon {
			t.Errorf("plymouth %v without a daemon exited %d, want %d", args, got, ExitNoDaemon)
		}
	}
	if got := newHarness(socket).client.Main([]string{"--wait"}); got != ExitOK {
		t.Errorf("plymouth --wait without a daemon exited %d, want 0", got)
	}
	h := newHarness(socket)
	h.status = []int{1, 0}
	if got := h.client.Main([]string{"ask-for-password", "--command=/sbin/unlock root", "--number-of-tries=3"}); got != ExitOK {
		t.Errorf("--command without a daemon exited %d", got)
	}
	if len(h.commands) != 2 || h.commands[0].input != "<none>" || !reflect.DeepEqual(h.commands[0].argv, []string{"/sbin/unlock", "root"}) {
		t.Errorf("--command without a daemon ran %+v, want it twice with no input", h.commands)
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()
	socket, _ := serve(t, &daemon{answer: ack})
	for _, args := range [][]string{nil, {"frobnicate"}, {"--bogus"}, {"quit", "extra"}, {"system-update", "--progress=101"}, {"--"}} {
		if got := newHarness(socket).client.Main(args); got != ExitFailed {
			t.Errorf("plymouth %v exited %d, want %d", args, got, ExitFailed)
		}
	}
	if got := newHarness(socket).client.Main([]string{"--help"}); got != ExitOK {
		t.Errorf("plymouth --help exited %d", got)
	}
}

func TestDisconnected(t *testing.T) {
	t.Parallel()
	d := &daemon{answer: ack, hangUpOn: protocol.CmdQuit}
	socket, s := serve(t, d)
	h := newHarness(socket)
	done := make(chan int, 1)
	go func() { done <- h.client.Main([]string{"quit"}) }()
	for len(d.requests()) == 0 {
		select {
		case got := <-done:
			t.Fatalf("quit returned %d before the daemon saw it", got)
		default:
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != ExitDisconnected {
		t.Errorf("a hang-up mid-request exited %d, want %d", got, ExitDisconnected)
	}
}

func TestWaitReturnsWhenTheDaemonQuits(t *testing.T) {
	t.Parallel()
	socket, s := serve(t, &daemon{answer: ack})
	h := newHarness(socket)
	done := make(chan int, 1)
	go func() { done <- h.client.Main([]string{"--wait"}) }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != ExitOK {
		t.Errorf("--wait exited %d", got)
	}
}

func answering(answers map[protocol.Command][]protocol.Response) func(protocol.Request) protocol.Response {
	var mu sync.Mutex
	return func(r protocol.Request) protocol.Response {
		mu.Lock()
		defer mu.Unlock()
		queue := answers[r.Command()]
		if len(queue) == 0 {
			return protocol.Ack{}
		}
		answers[r.Command()] = queue[1:]
		return queue[0]
	}
}

func TestAskForPasswordPrintsTheAnswer(t *testing.T) {
	t.Parallel()
	d := &daemon{answer: answering(map[protocol.Command][]protocol.Response{protocol.CmdPassword: {protocol.Answer{Data: []byte("hunter2")}}})}
	socket, _ := serve(t, d)
	h := newHarness(socket)
	if got := h.client.Main([]string{"ask-for-password", "--prompt=Disk"}); got != ExitOK {
		t.Fatalf("exited %d: %s", got, h.stderr.String())
	}
	if h.stdout.String() != "hunter2" {
		t.Errorf("printed %q", h.stdout.String())
	}
	want := []protocol.Request{protocol.PauseProgress{}, protocol.Password{Prompt: "Disk"}, protocol.ResumeProgress{}}
	if got := d.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %#v, want %#v", got, want)
	}
}

func TestAskForPasswordFailures(t *testing.T) {
	t.Parallel()
	for name, resp := range map[string]protocol.Response{"no answer": protocol.NoAnswer{}, "interrupted": protocol.Answer{Data: []byte("\x03")}} {
		d := &daemon{answer: answering(map[protocol.Command][]protocol.Response{protocol.CmdPassword: {resp}})}
		socket, _ := serve(t, d)
		h := newHarness(socket)
		if got := h.client.Main([]string{"ask-for-password", "--dont-pause-progress"}); got != ExitFailed {
			t.Errorf("%s: exited %d, want %d", name, got, ExitFailed)
		}
		if h.stdout.Len() != 0 {
			t.Errorf("%s: printed %q", name, h.stdout.String())
		}
		if got := d.requests(); !reflect.DeepEqual(got, []protocol.Request{protocol.Password{}}) {
			t.Errorf("%s: sent %#v", name, got)
		}
	}
}

func TestAskForPasswordWithCommand(t *testing.T) {
	t.Parallel()
	d := &daemon{answer: answering(map[protocol.Command][]protocol.Response{
		protocol.CmdCachedPassword: {protocol.MultipleAnswers{Answers: [][]byte{[]byte("old")}}},
		protocol.CmdPassword:       {protocol.Answer{Data: []byte("wrong")}, protocol.Answer{Data: []byte("right")}},
	})}
	socket, _ := serve(t, d)
	h := newHarness(socket)
	h.status = []int{1, 1, 0}
	if got := h.client.Main([]string{"ask-for-password", "--command=cryptsetup open", "--number-of-tries=3", "--prompt=root"}); got != ExitOK {
		t.Fatalf("exited %d: %s", got, h.stderr.String())
	}
	inputs := make([]string, len(h.commands))
	for i, c := range h.commands {
		inputs[i] = c.input
	}
	if strings.Join(inputs, ",") != "old,wrong,right" {
		t.Errorf("the command was given %v", inputs)
	}
	want := []protocol.Request{protocol.PauseProgress{}, protocol.CachedPassword{}, protocol.Password{Prompt: "root"}, protocol.Password{Prompt: "root"}, protocol.ResumeProgress{}}
	if got := d.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %#v, want %#v", got, want)
	}
}

func TestAskForPasswordGivesUpAfterTries(t *testing.T) {
	t.Parallel()
	d := &daemon{answer: answering(map[protocol.Command][]protocol.Response{
		protocol.CmdPassword: {protocol.Answer{Data: []byte("a")}, protocol.Answer{Data: []byte("b")}},
	})}
	socket, _ := serve(t, d)
	h := newHarness(socket)
	h.status = []int{5, 5, 0}
	if got := h.client.Main([]string{"ask-for-password", "--command=unlock", "--number-of-tries=2", "--dont-pause-progress"}); got != 5 {
		t.Errorf("exited %d, want the command's 5", got)
	}
	if len(h.commands) != 2 {
		t.Errorf("ran the command %d times, want 2", len(h.commands))
	}
}

func TestAskQuestionAndWatchKeystroke(t *testing.T) {
	t.Parallel()
	d := &daemon{answer: answering(map[protocol.Command][]protocol.Response{
		protocol.CmdQuestion:       {protocol.Answer{Data: []byte("yes")}},
		protocol.CmdWatchKeystroke: {protocol.Answer{Data: []byte("y")}, protocol.NoAnswer{}},
	})}
	socket, _ := serve(t, d)
	h := newHarness(socket)
	if got := h.client.Main([]string{"ask-question", "--prompt=Go?"}); got != ExitOK || h.stdout.String() != "yes" {
		t.Errorf("ask-question exited %d printing %q", got, h.stdout.String())
	}
	h.stdout.Reset()
	if got := h.client.Main([]string{"watch-keystroke", "--keys=yn"}); got != ExitOK || h.stdout.String() != "y" {
		t.Errorf("watch-keystroke exited %d printing %q", got, h.stdout.String())
	}
	if got := h.client.Main([]string{"watch-keystroke", "--keys=yn"}); got != ExitFailed {
		t.Errorf("an unanswered watch exited %d", got)
	}
	want := []protocol.Request{protocol.PauseProgress{}, protocol.Question{Prompt: "Go?"}, protocol.ResumeProgress{}, protocol.WatchKeystroke{Keys: "yn"}, protocol.WatchKeystroke{Keys: "yn"}}
	if got := d.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %#v, want %#v", got, want)
	}
}

func TestRunCommand(t *testing.T) {
	t.Parallel()
	if got, err := RunCommand([]string{"sh", "-c", "read x; test \"$x\" = secret"}, []byte("secret\n")); err != nil || got != 0 {
		t.Errorf("RunCommand with the right input = %d, %v", got, err)
	}
	if got, err := RunCommand([]string{"sh", "-c", "exit 4"}, nil); err != nil || got != 4 {
		t.Errorf("RunCommand exiting 4 = %d, %v", got, err)
	}
	if _, err := RunCommand([]string{"/nonexistent/command"}, nil); err == nil {
		t.Error("running a missing command succeeded")
	}
	if _, err := RunCommand(nil, nil); err == nil {
		t.Error("running no command succeeded")
	}
}
