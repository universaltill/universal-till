// Package fleetlink is the main-till link of ADR-0114: one long-lived,
// bearer-authenticated connection per additional till or satellite, carrying
// state, nudges, presence and small requests — never bulk data (that stays on
// the existing /api/sync/* HTTP endpoints, ADR-0065/ADR-0093).
//
// The message schema (this file) is transport-neutral on purpose (ADR-0114
// §2): a move to SSE+POST, or to the cloud transport, replaces ws.go only.
// Peer and Hub speak to a Conn, never to a websocket type.
package fleetlink

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the envelope's "v". A frame with any other value is refused
// with an "unsupported_version" error, never guessed at.
const Version = 1

// Message types (ADR-0114 §2's table). Only the ones this card uses are
// handled; fleet/pairing/sat.* are reserved for their own cards and are
// named here so the vocabulary lives in one place.
const (
	TypeHello   = "hello"
	TypeSync    = "sync"
	TypeFleet   = "fleet"   // ADR-0114 §5, #2726 — not sent yet
	TypePairing = "pairing" // ADR-0114 §2 pairing push — not sent yet
	TypeReport  = "report"
	TypeBye     = "bye"
	TypePing    = "ping"
	TypePong    = "pong"
	TypeError   = "error"
)

// Error codes carried in an error payload.
const (
	CodeBusy               = "busy"                // queue or in-flight limit hit — retryable
	CodeUnknownType        = "unknown_type"        // no handler for this request type
	CodeUnsupportedVersion = "unsupported_version" // envelope v is not ours
	CodeBadMessage         = "bad_message"         // payload could not be decoded
	CodeInternal           = "internal"            // the handler failed
	CodeTimeout            = "timeout"             // the handler outlived its deadline
)

// Envelope is the one frame shape both directions use:
// {"v":1,"id":…,"type":…,"reply_to":…,"payload":{…}}. id is unique per
// sender; a response carries the request's id in reply_to.
type Envelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	ReplyTo string          `json:"reply_to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ErrorPayload is the payload of a "error" reply.
type ErrorPayload struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// RemoteError is what Request returns when the peer answered with an error
// payload.
type RemoteError struct {
	Code      string
	Retryable bool
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("fleetlink: peer replied %s (retryable=%t)", e.Code, e.Retryable)
}

var (
	// ErrBusy is returned locally when a request cannot even be queued:
	// the outbound queue is full or 8 requests are already in flight
	// (ADR-0114 §8). Retryable.
	ErrBusy = errors.New("fleetlink: busy")
	// ErrTimeout is returned when a request's deadline passes unanswered.
	ErrTimeout = errors.New("fleetlink: request deadline exceeded")
	// ErrClosed is returned for requests on (or pending when) the link
	// closes.
	ErrClosed = errors.New("fleetlink: link closed")
	// ErrTooLarge is returned for a frame the peer's read limit would
	// refuse — sending it would only get the whole link closed.
	ErrTooLarge = errors.New("fleetlink: message too large")
)

// errVersion and errMalformed are Decode's two failure classes; the reader
// answers the first with an unsupported_version error and drops the second.
var (
	errVersion   = errors.New("fleetlink: unsupported envelope version")
	errMalformed = errors.New("fleetlink: malformed envelope")
)

// Encode marshals an envelope, stamping v.
func Encode(e Envelope) ([]byte, error) {
	e.V = Version
	return json.Marshal(e)
}

// Decode parses and validates one frame. The envelope is returned even on
// errVersion so the caller can address its error reply.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", errMalformed, err)
	}
	if e.ID == "" || e.Type == "" {
		return Envelope{}, fmt.Errorf("%w: id and type are required", errMalformed)
	}
	if e.V != Version {
		return e, errVersion
	}
	return e, nil
}

// newMessage builds an envelope with a JSON-encoded payload (nil → none).
func newMessage(id, typ, replyTo string, payload any) (Envelope, error) {
	e := Envelope{V: Version, ID: id, Type: typ, ReplyTo: replyTo}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		e.Payload = raw
	}
	return e, nil
}
