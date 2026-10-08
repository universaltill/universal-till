package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Event is what the till sends a plugin on stdin
// (architecture/wasm-runtime.md).
//
// Timestamp stays the raw RFC 3339 string: decoding it into a time.Time on
// every event cost ~2 ms per event under the interpreter (Android/iOS tills,
// wasmbench go-sdk-command, ut-docs#3951). Parse it with Time when needed.
type Event struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// Time parses Timestamp.
func (e Event) Time() (time.Time, error) { return time.Parse(time.RFC3339Nano, e.Timestamp) }

// Decode unmarshals the event's payload into v.
func (e Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, v)
}

// Handler answers one event. A nil answer writes nothing ("no opinion");
// []byte and json.RawMessage are written verbatim; anything else is JSON
// encoded. A non-nil error makes the module exit non-zero (ExitCode(n) picks
// n; ExitCode(0) still exits 1). The answer, if any, is written first but
// the till ignores stdout after a non-zero exit — it reaches the log only.
type Handler func(Event) (any, error)

// Handlers maps an event type to its handler. The key "*" catches every
// type without its own entry.
type Handlers map[string]Handler

// ExitCode is an error that sets the module's exit status. A bare ExitCode
// writes nothing to stderr: the handler logs its own reason.
type ExitCode int

func (c ExitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

// Run is a plugin's whole main: read the event from stdin, dispatch it,
// write the answer to stdout, exit. It never returns.
func Run(h Handlers) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read event:", err)
		os.Exit(1)
	}
	argType := ""
	if len(os.Args) > 1 {
		argType = os.Args[1]
	}
	out, err := Dispatch(h, raw, argType)
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	if err != nil {
		var code ExitCode
		if !errors.As(err, &code) || err != error(code) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(exitStatus(err))
	}
	os.Exit(0)
}

// Dispatch is Run without the process: it decodes raw, picks the handler for
// the event's type (argType when the event names none), and returns the
// encoded answer. An event no handler takes returns (nil, nil).
func Dispatch(h Handlers, raw []byte, argType string) ([]byte, error) {
	var e Event
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
	}
	if e.Type == "" {
		e.Type = argType
	}
	fn, ok := h[e.Type]
	if !ok {
		if fn, ok = h["*"]; !ok {
			return nil, nil
		}
	}
	answer, herr := fn(e)
	out, err := encodeAnswer(answer)
	if err != nil {
		return nil, errors.Join(herr, err)
	}
	return out, herr
}

func encodeAnswer(a any) ([]byte, error) {
	switch v := a.(type) {
	case nil:
		return nil, nil
	case []byte:
		return v, nil
	case json.RawMessage:
		return v, nil
	default:
		out, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode answer: %w", err)
		}
		return out, nil
	}
}

func exitStatus(err error) int {
	var code ExitCode
	if errors.As(err, &code) && code != 0 {
		return int(code)
	}
	return 1
}
