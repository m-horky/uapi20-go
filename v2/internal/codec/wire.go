package codec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Message is the JSON wire representation of a varlink request or reply.
type Message struct {
	Method     string    `json:"method,omitempty"`
	Parameters *RawValue `json:"parameters,omitempty"`
	Error      string    `json:"error,omitempty"`
	More       bool      `json:"more,omitempty"`
	Continues  bool      `json:"continues,omitempty"`
	Oneway     bool      `json:"oneway,omitempty"`
	Upgrade    bool      `json:"upgrade,omitempty"`

	// Extensions holds vendor extension objects received with the message,
	// keyed by their reverse-domain name. Extensions are validated but not
	// interpreted, and they are not re-emitted when the message is marshaled.
	Extensions map[string]RawValue `json:"-"`
}

// ErrMalformedMessage is returned when decoding a message that violates the
// varlink wire protocol. There is no well-defined way to reply to such a
// message, so the connection must be closed.
var ErrMalformedMessage = errors.New("varlink: malformed message")

// Wire reads and writes JSON varlink messages over framed I/O.
type Wire struct {
	reader *Reader
	framer Framer
	writer io.Writer
}

// NewWire returns a Wire using its own buffered reader and framer.
func NewWire(r io.Reader, w io.Writer, bufferSize, maxFrameSize int) *Wire {
	framer := New(maxFrameSize)
	return &Wire{
		reader: NewReader(r, bufferSize, maxFrameSize),
		framer: framer,
		writer: w,
	}
}

// NewWireFromReader returns a Wire that reuses an existing frame Reader.
func NewWireFromReader(r *Reader, w io.Writer, maxFrameSize int) *Wire {
	framer := New(maxFrameSize)
	return &Wire{
		reader: r,
		framer: framer,
		writer: w,
	}
}

// ResetReader switches the Wire to a new underlying reader while preserving internal scratch buffers.
func (w *Wire) ResetReader(r io.Reader) {
	w.reader.Reset(r)
}

// ReadMessage reads and strictly decodes one JSON wire message.
func (w *Wire) ReadMessage() (Message, error) {
	frame, err := w.reader.ReadFrame()
	if err != nil {
		return Message{}, err
	}
	return DecodeMessage(frame)
}

// WriteMessage encodes and writes one JSON wire message.
func (w *Wire) WriteMessage(msg Message) error {
	frame, err := marshalJSON(msg)
	if err != nil {
		return err
	}
	return w.framer.WriteFrame(w.writer, frame)
}

// Buffered returns the number of bytes already read from the underlying reader
// but not consumed by the wire decoder.
func (w *Wire) Buffered() int {
	return w.reader.BufferedReader().Buffered()
}

// ReadBuffered copies already-buffered bytes into dst without reading from the
// underlying reader.
func (w *Wire) ReadBuffered(dst []byte) (int, error) {
	return w.reader.BufferedReader().Read(dst)
}

// DecodeMessage strictly decodes one JSON wire message.
//
// It enforces the protocol rules for which the specification requires the
// connection to be closed: the top-level value must be a JSON object, only
// members defined by the specification (or vendor extension objects named in
// reverse-domain notation) may be present, the call flags must be JSON
// booleans, at most one of oneway, more and upgrade may be set, parameters
// must be an object, and an error reply must not continue a call. Errors
// returned by DecodeMessage wrap ErrMalformedMessage.
func DecodeMessage(frame []byte) (Message, error) {
	var members map[string]RawValue
	if err := unmarshalJSON(frame, &members); err != nil {
		return Message{}, malformed("invalid JSON: %v", err)
	}

	var msg Message
	flags := 0
	for key, raw := range members {
		switch key {
		case "method":
			if err := unmarshalJSON(raw, &msg.Method); err != nil {
				return Message{}, malformed("member %q: %v", key, err)
			}
		case "error":
			if err := unmarshalJSON(raw, &msg.Error); err != nil {
				return Message{}, malformed("member %q: %v", key, err)
			}
		case "parameters":
			if trimmed := bytes.TrimSpace(raw); !isNull(trimmed) && trimmed[0] != '{' {
				return Message{}, malformed("parameters must be an object")
			}
			if isNull(bytes.TrimSpace(raw)) {
				continue // absent and null parameters are equivalent
			}
			params := append(RawValue(nil), raw...)
			msg.Parameters = &params
		case "oneway", "more", "upgrade", "continues":
			value, err := decodeBool(raw)
			if err != nil {
				return Message{}, malformed("member %q: %v", key, err)
			}
			switch key {
			case "oneway":
				msg.Oneway = value
			case "more":
				msg.More = value
			case "upgrade":
				msg.Upgrade = value
			case "continues":
				msg.Continues = value
			}
			if value && key != "continues" {
				flags++
			}
		default:
			// Vendor extensions are nested in objects named in reverse-domain
			// notation; any other unknown member is a protocol violation.
			if !strings.ContainsRune(key, '.') {
				return Message{}, malformed("unknown member %q", key)
			}
			if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
				return Message{}, malformed("vendor extension %q must be an object", key)
			}
			if msg.Extensions == nil {
				msg.Extensions = make(map[string]RawValue)
			}
			msg.Extensions[key] = append(RawValue(nil), raw...)
		}
	}
	if flags > 1 {
		return Message{}, malformed("at most one of oneway, more and upgrade may be set")
	}
	if msg.Error != "" && msg.Continues {
		return Message{}, malformed("an error reply must not continue a call")
	}
	return msg, nil
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformedMessage, fmt.Sprintf(format, args...))
}

func decodeBool(raw RawValue) (bool, error) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("must be a boolean")
	}
}

func isNull(raw []byte) bool {
	return string(raw) == "null"
}

// MarshalMessage encodes one JSON wire message without writing it.
func MarshalMessage(msg Message) ([]byte, error) {
	return marshalJSON(msg)
}

// MarshalParameters encodes arbitrary parameters into a raw JSON value.
func MarshalParameters(v any) (*RawValue, error) {
	return marshalParameters(v)
}

// DecodeParameters decodes a raw JSON value into out.
func DecodeParameters(raw *RawValue, out any) error {
	return decodeParameters(raw, out)
}
