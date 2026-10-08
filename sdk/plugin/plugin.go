// Package plugin is the Go guest SDK for Universal Till WASM plugins
// (ADR-0121 F4, ut-docs#3951): typed wrappers for every "ut" host function,
// the Run event loop, and a native fake host (fakehost.go) so plugin logic
// is unit-tested with plain `go test`.
//
// Build a plugin with GOOS=wasip1 GOARCH=wasm. Under any other GOOS the
// host calls go to the FakeHost installed with UseFakeHost.
//
// Host-function contract: ut-docs reference/plugin-host-functions.md.
package plugin

import (
	"errors"
	"fmt"
)

// ABI is the highest manifest `wasm_abi` this SDK speaks. The SDK's own
// major version moves only on an incompatible Go API change, never with ABI.
const ABI = 2

// Error is a negative host-function return. Compare with errors.Is against
// the sentinels below; Op names the host function that failed.
type Error struct {
	Op   string
	Code int32
}

func (e *Error) Error() string {
	name := codeNames[e.Code]
	if name == "" {
		name = "error"
	}
	if e.Op == "" {
		return fmt.Sprintf("ut: %s (%d)", name, e.Code)
	}
	return fmt.Sprintf("ut %s: %s (%d)", e.Op, name, e.Code)
}

// Is matches a sentinel (Op "") by code, so any *Error with Code -2 is
// errors.Is(err, ErrDenied).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Op == "" && t.Code == e.Code
}

// The host's negative return codes (reference/plugin-host-functions.md,
// "Buffer ABI").
var (
	ErrNotFound = &Error{Code: -1} // not found, unknown or closed handle, not in a job
	ErrDenied   = &Error{Code: -2} // permission denied (audited on the till)
	ErrInternal = &Error{Code: -3} // host error, timed-out read
	ErrInvalid  = &Error{Code: -4} // invalid input or over a size cap
	ErrQuota    = &Error{Code: -5} // quota or rate limit
	ErrBusy     = &Error{Code: -6} // too many open handles
)

var codeNames = map[int32]string{
	-1: "not found", -2: "permission denied", -3: "internal error",
	-4: "invalid", -5: "quota exceeded", -6: "busy",
}

// ErrTruncated: a buffer-ABI call still did not fit after its one retry at
// the reported size (the value grew between the two calls).
var ErrTruncated = errors.New("ut: value changed size between calls")

func codeErr(op string, n int32) error {
	if n >= 0 {
		return nil
	}
	return &Error{Op: op, Code: n}
}

// defaultBuf is the first buffer a buffer-ABI call gets; httpBuf is
// http_request's (a 256 KiB body is ~350 KiB of base64 JSON).
const (
	defaultBuf = 64 << 10
	httpBuf    = 512 << 10
)

// viewMax is the host's view_query result cap (internal/data.CoreViewMaxResult;
// ErrQuota past it, never truncated). ViewQuery's first buffer is this size:
// each call counts against the 64-per-event view cap, so a retry would cost
// the plugin a second one.
const viewMax = 256 << 10

// bufCall runs a buffer-ABI call: the host writes min(len, cap) bytes and
// returns the full length. A larger length is retried exactly once with a
// buffer of that size and the same request bytes (ADR-0121 F4) — for
// http_request the host replays its cached response, so the retry never
// repeats the side effect (ut-docs#754).
func bufCall(op string, size int, call func(dst []byte) int32) ([]byte, error) {
	buf := make([]byte, size)
	n := call(buf)
	if n < 0 {
		return nil, codeErr(op, n)
	}
	if int(n) <= len(buf) {
		return buf[:n], nil
	}
	buf = make([]byte, n)
	n = call(buf)
	if n < 0 {
		return nil, codeErr(op, n)
	}
	if int(n) > len(buf) {
		return nil, fmt.Errorf("ut %s: %w", op, ErrTruncated)
	}
	return buf[:n], nil
}
