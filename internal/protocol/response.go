package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ResponseType is a response's type byte.
type ResponseType byte

// The response types.
const (
	TypeAck             ResponseType = 0x06
	TypeNak             ResponseType = 0x15
	TypeAnswer          ResponseType = 0x02
	TypeMultipleAnswers ResponseType = '\t'
	TypeNoAnswer        ResponseType = 0x05
)

// MaxAnswer is the longest ANSWER or MULTIPLE_ANSWERS payload the plymouth
// client reads, in bytes.
const MaxAnswer = 1 << 20

// Response is one response, one of the types in this file.
type Response interface {
	// Type is the response's type byte.
	Type() ResponseType
}

// Ack acknowledges a request.
type Ack struct{}

// Nak refuses a request.
type Nak struct{}

// Answer answers a password, question or keystroke request. Its bytes may be
// a secret; whoever holds the Answer zeroes them once sent.
type Answer struct{ Data []byte }

// MultipleAnswers answers a cached-password request with every password
// typed so far, at least one. Like Answer, its bytes are secrets.
type MultipleAnswers struct{ Answers [][]byte }

// NoAnswer answers a request the daemon has no answer for.
type NoAnswer struct{}

func (Ack) Type() ResponseType             { return TypeAck }
func (Nak) Type() ResponseType             { return TypeNak }
func (Answer) Type() ResponseType          { return TypeAnswer }
func (MultipleAnswers) Type() ResponseType { return TypeMultipleAnswers }
func (NoAnswer) Type() ResponseType        { return TypeNoAnswer }

// AppendResponse appends r's encoding to dst. It fails on an answer holding a
// NUL, which the client would split, on MultipleAnswers with no answer, which
// is NoAnswer on the wire, and on a payload longer than MaxAnswer. The
// appended bytes copy any secret in r; the caller zeroes them once written.
func AppendResponse(dst []byte, r Response) ([]byte, error) {
	switch r := r.(type) {
	case Answer:
		if bytes.IndexByte(r.Data, 0) >= 0 {
			return dst, errors.New("protocol: an answer holds a NUL")
		}
		dst, err := header(dst, TypeAnswer, len(r.Data))
		if err != nil {
			return dst, err
		}
		return append(dst, r.Data...), nil
	case MultipleAnswers:
		if len(r.Answers) == 0 {
			return dst, errors.New("protocol: MULTIPLE_ANSWERS with no answer")
		}
		size := 0
		for _, a := range r.Answers {
			if bytes.IndexByte(a, 0) >= 0 {
				return dst, errors.New("protocol: a cached answer holds a NUL")
			}
			size += len(a) + 1
		}
		dst, err := header(dst, TypeMultipleAnswers, size)
		if err != nil {
			return dst, err
		}
		for _, a := range r.Answers {
			dst = append(append(dst, a...), 0)
		}
		return dst, nil
	}
	return append(dst, byte(r.Type())), nil
}

func header(dst []byte, t ResponseType, size int) ([]byte, error) {
	if size > MaxAnswer {
		return dst, fmt.Errorf("protocol: a %d-byte answer is longer than %d", size, MaxAnswer)
	}
	return binary.LittleEndian.AppendUint32(append(dst, byte(t)), uint32(size)), nil //nolint:gosec // bounded by MaxAnswer above
}

// ReadResponse reads one response from r as the plymouth client does: a
// payload longer than MaxAnswer, an ANSWER holding a NUL, a MULTIPLE_ANSWERS
// payload that is empty or not NUL-terminated, and an unknown type byte are
// errors.
func ReadResponse(r io.Reader) (Response, error) {
	var head [1]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, fmt.Errorf("protocol: read a response: %w", err)
	}
	switch t := ResponseType(head[0]); t {
	case TypeAck:
		return Ack{}, nil
	case TypeNak:
		return Nak{}, nil
	case TypeNoAnswer:
		return NoAnswer{}, nil
	case TypeAnswer:
		data, err := payload(r, t)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil, errors.New("protocol: ANSWER payload holds a NUL")
		}
		return Answer{Data: data}, nil
	case TypeMultipleAnswers:
		data, err := payload(r, t)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 || data[len(data)-1] != 0 {
			return nil, errors.New("protocol: MULTIPLE_ANSWERS payload is empty or not NUL-terminated")
		}
		answers := make([][]byte, 0, bytes.Count(data, []byte{0}))
		for a := range bytes.SplitSeq(data[:len(data)-1], []byte{0}) {
			answers = append(answers, a)
		}
		return MultipleAnswers{Answers: answers}, nil
	default:
		return nil, fmt.Errorf("protocol: unknown response type %#02x", byte(t))
	}
}

func payload(r io.Reader, t ResponseType) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, fmt.Errorf("protocol: read the length of a %#02x response: %w", byte(t), err)
	}
	n := binary.LittleEndian.Uint32(size[:])
	if n > MaxAnswer {
		return nil, fmt.Errorf("protocol: a %#02x response of %d bytes is longer than %d", byte(t), n, MaxAnswer)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("protocol: read a %d-byte %#02x response: %w", n, byte(t), err)
	}
	return data, nil
}
