package channel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Error is a negative host-function return. Compare with errors.Is against
// the sentinels below; Op names the host function that failed. The codes are
// the same as the till's "ut" module (plugin-host-functions.md, "Buffer ABI").
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
		return fmt.Sprintf("channel: %s (%d)", name, e.Code)
	}
	return fmt.Sprintf("channel %s: %s (%d)", e.Op, name, e.Code)
}

// Is matches a sentinel (Op "") by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Op == "" && t.Code == e.Code
}

// The host's negative return codes.
var (
	ErrNotFound = &Error{Code: -1} // no such cursor, op or account
	ErrDenied   = &Error{Code: -2} // not allowed: another host, a forbidden header, a scheme other than https
	ErrInternal = &Error{Code: -3} // host error, including a call past its deadline
	ErrInvalid  = &Error{Code: -4} // malformed input or over a size cap
	ErrQuota    = &Error{Code: -5} // the response was over the cap, or a rate limit
	ErrBusy     = &Error{Code: -6} // the connection is busy
)

var codeNames = map[int32]string{
	-1: "not found", -2: "permission denied", -3: "internal error",
	-4: "invalid", -5: "quota exceeded", -6: "busy",
}

// ErrTruncated: a buffer-ABI call still did not fit after its one retry.
var ErrTruncated = errors.New("channel: value changed size between calls")

func codeErr(op string, n int32) error {
	if n >= 0 {
		return nil
	}
	return &Error{Op: op, Code: n}
}

const (
	defaultBuf = 64 << 10
	httpBuf    = 512 << 10
)

// bufCall runs a buffer-ABI call: the host writes min(len, cap) bytes and
// returns the full length. A larger length is retried exactly once with a
// buffer of that size and the same request bytes; the host answers the retry
// from its cache, so the request is never performed twice.
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
		return nil, fmt.Errorf("channel %s: %w", op, ErrTruncated)
	}
	return buf[:n], nil
}

// Log writes one structured line. code is a short stable identifier
// ("import.page"); fields are scalar key/values. Never put a customer's
// name, address, email or phone in fields: the host drops free text it
// cannot vouch for.
func Log(level int, code string, fields map[string]any) {
	raw, err := json.Marshal(fields)
	if err != nil {
		raw = []byte("{}")
	}
	rawLog(int32(level), []byte(code), raw)
}

type wireHTTPRequest struct {
	Method   string            `json:"method,omitempty"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	BodyB64  string            `json:"body_b64,omitempty"`
	CostHint int               `json:"cost_hint,omitempty"`
	Throttle *Throttle         `json:"throttle,omitempty"`
}

type wireHTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

// HTTP sends one request through the host (the only network path a channel
// guest has). The host checks the method and host against the connection's
// account, adds the credential, paces, retries 429/THROTTLED/5xx with
// jitter and caps the response.
func HTTP(req HTTPRequest) (*HTTPResponse, error) {
	w := wireHTTPRequest{Method: req.Method, URL: req.URL, Headers: req.Headers, CostHint: req.CostHint, Throttle: req.Throttle}
	if len(req.Body) > 0 {
		w.BodyB64 = base64.StdEncoding.EncodeToString(req.Body)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	out, err := bufCall(FnHTTP, httpBuf, func(dst []byte) int32 { return rawHTTP(raw, dst) })
	if err != nil {
		return nil, err
	}
	var r wireHTTPResponse
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("channel %s: decode response: %w", FnHTTP, err)
	}
	body, err := base64.StdEncoding.DecodeString(r.BodyB64)
	if err != nil {
		return nil, fmt.Errorf("channel %s: decode body: %w", FnHTTP, err)
	}
	return &HTTPResponse{Status: r.Status, Headers: r.Headers, Body: body}, nil
}

// CursorGet reads this connection's cursor key. A missing key is ErrNotFound.
func CursorGet(key string) ([]byte, error) {
	k := []byte(key)
	return bufCall(FnCursorGet, defaultBuf, func(dst []byte) int32 { return rawCursorGet(k, dst) })
}

// CursorSet stores a cursor value. All of a connection's values together may
// not exceed MaxCursorBytes (ErrQuota).
func CursorSet(key string, value []byte) error {
	return codeErr(FnCursorSet, rawCursorSet([]byte(key), value))
}

// Submit hands the host a batch of records of one kind (a Kind* constant),
// at most MaxBatchRecords. records is a slice of the matching record type;
// the host scopes every record to the connection and answers per record.
func Submit(kind string, records any) ([]SubmitResult, error) {
	raw, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	var count []json.RawMessage
	if err := json.Unmarshal(raw, &count); err != nil {
		return nil, fmt.Errorf("channel %s: records must be a slice: %w", FnSubmit, err)
	}
	if len(count) > MaxBatchRecords {
		return nil, &Error{Op: FnSubmit, Code: ErrInvalid.Code}
	}
	k := []byte(kind)
	out, err := bufCall(FnSubmit, defaultBuf, func(dst []byte) int32 { return rawSubmit(k, raw, dst) })
	if err != nil {
		return nil, err
	}
	var res []SubmitResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("channel %s: decode results: %w", FnSubmit, err)
	}
	return res, nil
}

// OutboxNext returns up to max pending outbound operations for this
// connection, coalesced by the host.
func OutboxNext(max int) ([]OutboxOp, error) {
	if max <= 0 || max > MaxOutboxBatch {
		return nil, &Error{Op: FnOutboxNext, Code: ErrInvalid.Code}
	}
	out, err := bufCall(FnOutboxNext, defaultBuf, func(dst []byte) int32 { return rawOutboxNext(int32(max), dst) })
	if err != nil {
		return nil, err
	}
	var ops []OutboxOp
	if err := json.Unmarshal(out, &ops); err != nil {
		return nil, fmt.Errorf("channel %s: decode ops: %w", FnOutboxNext, err)
	}
	return ops, nil
}

// OutboxAck acknowledges one operation by its id.
func OutboxAck(opID string, ack Ack) error {
	raw, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	return codeErr(FnOutboxAck, rawOutboxAck([]byte(opID), raw))
}
