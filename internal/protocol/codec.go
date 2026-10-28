package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stubbedev/lys-lib/intconv"
)

const (
	noArgument  = 0x00
	hasArgument = 0x02
)

// MaxArgument is the longest argument a request carries, in bytes: its length
// byte also counts the trailing NUL.
const MaxArgument = 254

// AppendRequest appends r's encoding to dst. It fails on an argument longer
// than MaxArgument or holding a NUL, which the wire cannot carry.
func AppendRequest(dst []byte, r Request) ([]byte, error) {
	arg, ok := r.argument()
	if !ok {
		return append(dst, byte(r.Command()), noArgument), nil
	}
	if len(arg) > MaxArgument {
		return dst, fmt.Errorf("protocol: the argument of %q is %d bytes, more than %d", r.Command(), len(arg), MaxArgument)
	}
	if strings.IndexByte(arg, 0) >= 0 {
		return dst, fmt.Errorf("protocol: the argument of %q holds a NUL", r.Command())
	}
	dst = append(dst, byte(r.Command()), hasArgument, intconv.Must[byte](len(arg)+1))
	dst = append(dst, arg...)
	return append(dst, 0), nil
}

// ErrorKind tells what made a request undecodable.
type ErrorKind uint8

const (
	// Malformed is broken framing: the stream cannot be read further.
	Malformed ErrorKind = iota
	// UnknownCommand is a well-framed request with a command byte this
	// protocol lacks; the stream continues after it.
	UnknownCommand
	// BadArgument is a well-framed request whose argument does not parse;
	// the stream continues after it.
	BadArgument
)

// DecodeError is a request Decode could not decode: what was wrong, and the
// offending byte and its offset in the stream.
type DecodeError struct {
	Kind   ErrorKind
	Offset int64
	Byte   byte
	Reason string
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("protocol: byte %#02x at offset %d: %s", e.Byte, e.Offset, e.Reason)
}

// Fatal reports whether the stream is unreadable past e.
func (e *DecodeError) Fatal() bool {
	return e.Kind == Malformed
}

// Decoder reads requests from a byte stream.
type Decoder struct {
	r   *bufio.Reader
	off int64
	arg [MaxArgument + 1]byte
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReader(r)}
}

// Decode reads the next request. It returns io.EOF when the stream ends
// between requests, an error wrapping io.ErrUnexpectedEOF when it ends inside
// one, and a *DecodeError for a request it cannot decode; after a fatal one
// the stream is unreadable.
func (d *Decoder) Decode() (Request, error) {
	start := d.off
	cmd, err := d.byte(start, true)
	if err != nil {
		return nil, err
	}
	marker, err := d.byte(start, false)
	if err != nil {
		return nil, err
	}
	arg, present := "", false
	switch marker {
	case noArgument:
	case hasArgument:
		if arg, err = d.argument(start); err != nil {
			return nil, err
		}
		present = true
	default:
		return nil, &DecodeError{Kind: Malformed, Offset: d.off - 1, Byte: marker, Reason: "neither NUL nor 0x02 after the command"}
	}
	return request(Command(cmd), arg, present, start)
}

func (d *Decoder) byte(start int64, first bool) (byte, error) {
	b, err := d.r.ReadByte()
	if err != nil {
		return 0, d.short(start, err, first)
	}
	d.off++
	return b, nil
}

func (d *Decoder) short(start int64, err error, first bool) error {
	if errors.Is(err, io.EOF) {
		if first {
			return io.EOF
		}
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("protocol: the request at offset %d: %w", start, err)
}

func (d *Decoder) argument(start int64) (string, error) {
	size, err := d.byte(start, false)
	if err != nil {
		return "", err
	}
	if size == 0 {
		return "", &DecodeError{Kind: Malformed, Offset: d.off - 1, Byte: size, Reason: "zero argument length"}
	}
	buf := d.arg[:size]
	n, err := io.ReadFull(d.r, buf)
	d.off += int64(n)
	if err != nil {
		return "", d.short(start, err, false)
	}
	if last := buf[size-1]; last != 0 {
		return "", &DecodeError{Kind: Malformed, Offset: d.off - 1, Byte: last, Reason: "argument not NUL-terminated"}
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf), nil
}

func request(cmd Command, arg string, present bool, at int64) (Request, error) {
	switch cmd {
	case CmdPing:
		return Ping{}, nil
	case CmdUpdate:
		return Update{Status: arg}, nil
	case CmdChangeMode:
		return ChangeMode{Mode: arg}, nil
	case CmdSystemUpdate:
		p, err := ParsePercent(arg)
		if err != nil || !present {
			return nil, &DecodeError{Kind: BadArgument, Offset: at, Byte: byte(cmd), Reason: fmt.Sprintf("progress %q is not a percentage from 0 to 100", arg)}
		}
		return SystemUpdate{Progress: p}, nil
	case CmdSystemInitialized:
		return SystemInitialized{}, nil
	case CmdDeactivate:
		return Deactivate{}, nil
	case CmdReactivate:
		return Reactivate{}, nil
	case CmdQuit:
		return Quit{Retain: arg != ""}, nil
	case CmdReload:
		return Reload{}, nil
	case CmdPassword:
		return Password{Prompt: arg}, nil
	case CmdCachedPassword:
		return CachedPassword{}, nil
	case CmdQuestion:
		return Question{Prompt: arg}, nil
	case CmdShowMessage:
		return ShowMessage{Text: arg}, nil
	case CmdHideMessage:
		return HideMessage{Text: arg}, nil
	case CmdWatchKeystroke:
		return WatchKeystroke{Keys: arg}, nil
	case CmdIgnoreKeystroke:
		return IgnoreKeystroke{Keys: arg}, nil
	case CmdPauseProgress:
		return PauseProgress{}, nil
	case CmdResumeProgress:
		return ResumeProgress{}, nil
	case CmdShowSplash:
		return ShowSplash{}, nil
	case CmdHideSplash:
		return HideSplash{}, nil
	case CmdNewRoot:
		return NewRoot{Dir: arg}, nil
	case CmdHasActiveVT:
		return HasActiveVT{}, nil
	case CmdError:
		return Error{}, nil
	}
	return nil, &DecodeError{Kind: UnknownCommand, Offset: at, Byte: byte(cmd), Reason: "unknown command"}
}
