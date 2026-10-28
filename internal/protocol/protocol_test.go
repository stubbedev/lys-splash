package protocol

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

var requestCases = []struct {
	name string
	req  Request
	wire string
}{
	{"ping", Ping{}, "P\x00"},
	{"update", Update{Status: "systemd-udevd.service"}, "U\x02\x16systemd-udevd.service\x00"},
	{"change mode", ChangeMode{Mode: "updates"}, "C\x02\x08updates\x00"},
	{"system update", SystemUpdate{Progress: 42}, "u\x02\x0342\x00"},
	{"system update done", SystemUpdate{Progress: MaxPercent}, "u\x02\x04100\x00"},
	{"system initialized", SystemInitialized{}, "S\x00"},
	{"deactivate", Deactivate{}, "D\x00"},
	{"reactivate", Reactivate{}, "r\x00"},
	{"quit", Quit{}, "Q\x02\x01\x00"},
	{"quit retain", Quit{Retain: true}, "Q\x02\x02\x01\x00"},
	{"reload", Reload{}, "l\x00"},
	{"password", Password{Prompt: "Disk password:"}, "*\x02\x0fDisk password:\x00"},
	{"password without prompt", Password{}, "*\x00"},
	{"cached password", CachedPassword{}, "c\x00"},
	{"question", Question{Prompt: "Continue?"}, "W\x02\x0aContinue?\x00"},
	{"show message", ShowMessage{Text: "hello"}, "M\x02\x06hello\x00"},
	{"hide message", HideMessage{Text: "hello"}, "m\x02\x06hello\x00"},
	{"watch keystroke", WatchKeystroke{Keys: "yn"}, "K\x02\x03yn\x00"},
	{"watch any keystroke", WatchKeystroke{}, "K\x00"},
	{"ignore keystroke", IgnoreKeystroke{Keys: "yn"}, "L\x02\x03yn\x00"},
	{"pause progress", PauseProgress{}, "A\x00"},
	{"resume progress", ResumeProgress{}, "a\x00"},
	{"show splash", ShowSplash{}, "$\x00"},
	{"hide splash", HideSplash{}, "H\x00"},
	{"new root", NewRoot{Dir: "/sysroot"}, "R\x02\x09/sysroot\x00"},
	{"has active vt", HasActiveVT{}, "V\x00"},
	{"error", Error{}, "!\x00"},
}

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range requestCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := AppendRequest(nil, tc.req)
			if err != nil {
				t.Fatalf("AppendRequest(%#v): %v", tc.req, err)
			}
			if string(got) != tc.wire {
				t.Errorf("AppendRequest(%#v) = %q, want %q", tc.req, got, tc.wire)
			}
			req, err := NewDecoder(strings.NewReader(tc.wire)).Decode()
			if err != nil {
				t.Fatalf("Decode(%q): %v", tc.wire, err)
			}
			if !reflect.DeepEqual(req, tc.req) {
				t.Errorf("Decode(%q) = %#v, want %#v", tc.wire, req, tc.req)
			}
		})
	}
}

func decodeAll(t *testing.T, wire string) []Request {
	t.Helper()
	d := NewDecoder(strings.NewReader(wire))
	var out []Request
	for {
		r, err := d.Decode()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Decode after %d requests of %q: %v", len(out), wire, err)
		}
		out = append(out, r)
	}
}

func TestSystemdBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wire string
		want []Request
	}{
		{
			name: "plymouth_send_msg pausing",
			wire: "M\x02\x19Starting Disk Encryption\x00A\x00",
			want: []Request{ShowMessage{Text: "Starting Disk Encryption"}, PauseProgress{}},
		},
		{
			name: "plymouth_send_msg resuming",
			wire: "M\x02\x05done\x00a\x00",
			want: []Request{ShowMessage{Text: "done"}, ResumeProgress{}},
		},
		{
			name: "manager_send_unit_plymouth",
			wire: "U\x02\x16systemd-fsck@.service\x00",
			want: []Request{Update{Status: "systemd-fsck@.service"}},
		},
		{
			name: "plymouth_hide_splash",
			wire: "H\x00",
			want: []Request{HideSplash{}},
		},
		{
			name: "ask_password_plymouth cached then asked",
			wire: "c\x00*\x02\x27Please enter passphrase for disk root:\x00",
			want: []Request{CachedPassword{}, Password{Prompt: "Please enter passphrase for disk root:"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decodeAll(t, tc.wire); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decode %q = %#v, want %#v", tc.wire, got, tc.want)
			}
		})
	}
}

func TestDecodeArgumentOnArgumentlessCommand(t *testing.T) {
	t.Parallel()
	got := decodeAll(t, "P\x02\x04abc\x00")
	if !reflect.DeepEqual(got, []Request{Ping{}}) {
		t.Errorf("decode = %#v, want a ping", got)
	}
}

func TestDecodeTruncatesAtInteriorNUL(t *testing.T) {
	t.Parallel()
	got := decodeAll(t, "M\x02\x06ab\x00cd\x00")
	if !reflect.DeepEqual(got, []Request{ShowMessage{Text: "ab"}}) {
		t.Errorf("decode = %#v, want the text up to the first NUL", got)
	}
}

func TestDecodeErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		wire   string
		kind   ErrorKind
		offset int64
		b      byte
		fatal  bool
	}{
		{"unknown command", "P\x00z\x00", UnknownCommand, 2, 'z', false},
		{"unknown command with argument", "x\x02\x02y\x00", UnknownCommand, 0, 'x', false},
		{"zero length", "M\x02\x00", Malformed, 2, 0, true},
		{"missing NUL", "M\x02\x03abc", Malformed, 5, 'c', true},
		{"bad marker", "P\x01", Malformed, 1, 0x01, true},
		{"progress not a number", "u\x02\x04abc\x00", BadArgument, 0, 'u', false},
		{"progress above 100", "u\x02\x04101\x00", BadArgument, 0, 'u', false},
		{"progress negative", "u\x02\x03-1\x00", BadArgument, 0, 'u', false},
		{"progress signed", "u\x02\x03+1\x00", BadArgument, 0, 'u', false},
		{"progress missing", "u\x00", BadArgument, 0, 'u', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := NewDecoder(strings.NewReader(tc.wire))
			var err error
			for err == nil {
				_, err = d.Decode()
			}
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("Decode(%q) error = %v, want a *DecodeError", tc.wire, err)
			}
			if de.Kind != tc.kind || de.Offset != tc.offset || de.Byte != tc.b || de.Fatal() != tc.fatal {
				t.Errorf("Decode(%q) = %+v (fatal %v), want kind %d offset %d byte %#02x fatal %v", tc.wire, de, de.Fatal(), tc.kind, tc.offset, tc.b, tc.fatal)
			}
			if !strings.Contains(de.Error(), "offset") {
				t.Errorf("error %q does not name the offset", de)
			}
		})
	}
}

func TestDecodeContinuesAfterRecoverableError(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("z\x00P\x00"))
	if _, err := d.Decode(); err == nil {
		t.Fatal("Decode of an unknown command succeeded")
	}
	r, err := d.Decode()
	if err != nil || r != (Ping{}) {
		t.Fatalf("Decode after the unknown command = %#v, %v; want a ping", r, err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{"P", "M\x02", "M\x02\x06hel"} {
		_, err := NewDecoder(strings.NewReader(wire)).Decode()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("Decode(%q) = %v, want io.ErrUnexpectedEOF", wire, err)
		}
	}
	if _, err := NewDecoder(strings.NewReader("")).Decode(); !errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Decode of an empty stream = %v, want io.EOF", err)
	}
}

func TestAppendRequestErrors(t *testing.T) {
	t.Parallel()
	for _, r := range []Request{
		ShowMessage{Text: strings.Repeat("x", MaxArgument+1)},
		Password{Prompt: "a\x00b"},
	} {
		if got, err := AppendRequest([]byte("keep"), r); err == nil || string(got) != "keep" {
			t.Errorf("AppendRequest(%T) = %q, %v; want an error and dst unchanged", r, got, err)
		}
	}
	if _, err := AppendRequest(nil, ShowMessage{Text: strings.Repeat("x", MaxArgument)}); err != nil {
		t.Errorf("AppendRequest of a %d-byte argument: %v", MaxArgument, err)
	}
}

func TestResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		resp Response
		wire string
	}{
		{"ack", Ack{}, "\x06"},
		{"nak", Nak{}, "\x15"},
		{"no answer", NoAnswer{}, "\x05"},
		{"answer", Answer{Data: []byte("secret")}, "\x02\x06\x00\x00\x00secret"},
		{"empty answer", Answer{Data: []byte{}}, "\x02\x00\x00\x00\x00"},
		{"one cached", MultipleAnswers{Answers: [][]byte{[]byte("a")}}, "\t\x02\x00\x00\x00a\x00"},
		{"two cached", MultipleAnswers{Answers: [][]byte{[]byte("ab"), []byte("c")}}, "\t\x05\x00\x00\x00ab\x00c\x00"},
		{"empty cached", MultipleAnswers{Answers: [][]byte{{}}}, "\t\x01\x00\x00\x00\x00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := AppendResponse(nil, tc.resp)
			if err != nil {
				t.Fatalf("AppendResponse(%#v): %v", tc.resp, err)
			}
			if string(got) != tc.wire {
				t.Errorf("AppendResponse(%#v) = %q, want %q", tc.resp, got, tc.wire)
			}
			back, err := ReadResponse(bytes.NewReader(got))
			if err != nil {
				t.Fatalf("ReadResponse(%q): %v", got, err)
			}
			if !reflect.DeepEqual(back, tc.resp) {
				t.Errorf("ReadResponse(%q) = %#v, want %#v", got, back, tc.resp)
			}
		})
	}
}

func TestAppendResponseErrors(t *testing.T) {
	t.Parallel()
	for _, r := range []Response{
		Answer{Data: []byte("a\x00b")},
		MultipleAnswers{},
		MultipleAnswers{Answers: [][]byte{[]byte("ok"), []byte("x\x00")}},
		Answer{Data: make([]byte, MaxAnswer+1)},
	} {
		if got, err := AppendResponse([]byte("keep"), r); err == nil || string(got) != "keep" {
			t.Errorf("AppendResponse(%T) = %d bytes, %v; want an error and dst unchanged", r, len(got), err)
		}
	}
}

func TestReadResponseErrors(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{
		"",
		"\x01",
		"\x02\x01\x00",
		"\x02\x05\x00\x00\x00abc",
		"\x02\x01\x00\x10\x00",
		"\t\x00\x00\x00\x00",
		"\t\x02\x00\x00\x00ab",
		"\x02\x03\x00\x00\x00a\x00b",
	} {
		if r, err := ReadResponse(strings.NewReader(wire)); err == nil {
			t.Errorf("ReadResponse(%q) = %#v, want an error", wire, r)
		}
	}
}

func FuzzDecode(f *testing.F) {
	for _, tc := range requestCases {
		f.Add([]byte(tc.wire))
	}
	f.Add([]byte("M\x02\x00"))
	f.Add([]byte("u\x02\x04abc\x00"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		d := NewDecoder(bytes.NewReader(wire))
		for range len(wire) + 1 {
			r, err := d.Decode()
			var de *DecodeError
			switch {
			case err == nil:
				enc, encErr := AppendRequest(nil, r)
				if encErr != nil {
					t.Fatalf("decoded %#v does not encode: %v", r, encErr)
				}
				again, err := NewDecoder(bytes.NewReader(enc)).Decode()
				if err != nil || !reflect.DeepEqual(again, r) {
					t.Fatalf("%#v encodes to %q, which decodes to %#v, %v", r, enc, again, err)
				}
			case errors.As(err, &de) && !de.Fatal():
			default:
				return
			}
		}
		t.Fatalf("decoding %d bytes did not end", len(wire))
	})
}

func FuzzReadResponse(f *testing.F) {
	f.Add([]byte("\x06"))
	f.Add([]byte("\t\x05\x00\x00\x00ab\x00c\x00"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		r, err := ReadResponse(bytes.NewReader(wire))
		if err != nil {
			return
		}
		enc, err := AppendResponse(nil, r)
		if err != nil {
			t.Fatalf("read %#v does not encode: %v", r, err)
		}
		if !bytes.HasPrefix(wire, enc) {
			t.Fatalf("%q reads as %#v, which encodes to %q", wire, r, enc)
		}
	})
}
