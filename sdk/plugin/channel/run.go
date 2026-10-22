package channel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Event is what the host sends the guest on stdin: one ask or tick.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"` // an Ask* constant
	Payload json.RawMessage `json:"payload"`
}

// Decode unmarshals the event's payload into v.
func (e Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, v)
}

// Handler answers one ask. A nil answer writes nothing; []byte and
// json.RawMessage are written verbatim; anything else is JSON encoded. A
// non-nil error makes the module exit non-zero.
type Handler func(Event) (any, error)

// Handlers maps an ask (an Ask* constant) to its handler. The host calls only
// asks the manifest's capabilities need; an ask without a handler is an
// error answer, never a silent success.
type Handlers map[string]Handler

// Run is a guest's whole main: read the event from stdin, dispatch it, write
// the answer to stdout, exit. It never returns.
func Run(h Handlers) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read event:", err)
		os.Exit(1)
	}
	out, err := Dispatch(h, raw)
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// ErrNoHandler is returned by Dispatch for an ask the guest does not handle.
var ErrNoHandler = errors.New("channel: no handler for ask")

// Dispatch is Run without the process.
func Dispatch(h Handlers, raw []byte) ([]byte, error) {
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	fn, ok := h[e.Type]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoHandler, e.Type)
	}
	answer, herr := fn(e)
	var out []byte
	switch v := answer.(type) {
	case nil:
	case []byte:
		out = v
	case json.RawMessage:
		out = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, errors.Join(herr, fmt.Errorf("encode answer: %w", err))
		}
		out = b
	}
	return out, herr
}
