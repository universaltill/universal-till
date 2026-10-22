//go:build !wasip1

package channel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"sync"
)

// Cleaner is the part of *testing.T UseFakeHost needs.
type Cleaner interface{ Cleanup(func()) }

// FakeHost is an in-memory "channel" host for native unit tests: install it
// with UseFakeHost and call your handlers directly (or through Dispatch). It
// follows the host's buffer ABI and size caps, so the SDK's retry and error
// paths run as in the cloud. One FakeHost is installed process-wide: tests
// that use it must not run in parallel.
//
// It does NOT enforce the egress allow-list, redirects or pacing: those are
// the host's, and ut-cloud's channelhost tests cover them. Use HTTP to play
// the channel's server.
type FakeHost struct {
	mu sync.Mutex

	// HTTP answers channel_http; nil answers ErrDenied.
	HTTP func(HTTPRequest) (HTTPResponse, error)
	// Cursors is the per-connection key/value store (64 KiB in total).
	Cursors map[string][]byte
	// Submitted records every Submit call, in order.
	Submitted []SubmittedBatch
	// Reject, if set, decides a record's result (default ResultAccepted).
	Reject func(kind string, external string) *SubmitResult
	// Outbox holds the pending operations OutboxNext hands out (FIFO).
	Outbox []OutboxOp
	// Acks maps an operation id to the Ack the guest sent.
	Acks map[string]Ack
	// Logs records every Log call as "level code {fields}".
	Logs []LogLine
	// Deny["<host function>"] denies any one call with ErrDenied.
	Deny map[string]bool
	// Calls counts calls per host function.
	Calls map[string]int

	// retry holds an answer that did not fit the guest's buffer, by call:
	// the guest's one retry with the same request bytes is answered from it
	// and never performed again (contract section 3.1).
	retry map[string][]byte
}

// SubmittedBatch is one Submit call as the host saw it.
type SubmittedBatch struct {
	Kind    string
	Records []json.RawMessage
}

// LogLine is one Log call.
type LogLine struct {
	Level  int
	Code   string
	Fields map[string]any
}

// NewFakeHost returns an empty host.
func NewFakeHost() *FakeHost {
	return &FakeHost{
		Cursors: map[string][]byte{}, Acks: map[string]Ack{},
		Deny: map[string]bool{}, Calls: map[string]int{},
	}
}

var (
	hostMu sync.Mutex
	host   *FakeHost
)

// UseFakeHost installs h for the rest of the test.
func UseFakeHost(t Cleaner, h *FakeHost) {
	hostMu.Lock()
	prev := host
	host = h
	hostMu.Unlock()
	t.Cleanup(func() {
		hostMu.Lock()
		host = prev
		hostMu.Unlock()
	})
}

func enter(op string) (*FakeHost, bool) {
	hostMu.Lock()
	h := host
	hostMu.Unlock()
	if h == nil {
		panic("channel: no host outside wasm — call channel.UseFakeHost in the test")
	}
	h.mu.Lock()
	h.Calls[op]++
	return h, h.Deny[op]
}

// fill writes v into dst as far as it fits and returns len(v): the buffer ABI.
func fill(dst, v []byte) int32 {
	copy(dst, v)
	return int32(len(v))
}

// cached answers a retry: if the previous identical call's answer did not
// fit, it is returned once (h.mu held).
func (h *FakeHost) cached(dst []byte, fn string, req []byte) (int32, bool) {
	key := fn + "\x00" + string(req)
	v, ok := h.retry[key]
	if !ok {
		return 0, false
	}
	delete(h.retry, key)
	return fill(dst, v), true
}

// answer fills dst and remembers v for the retry when it did not fit
// (h.mu held).
func (h *FakeHost) answer(dst []byte, fn string, req, v []byte) int32 {
	if len(v) > len(dst) {
		if h.retry == nil {
			h.retry = map[string][]byte{}
		}
		h.retry[fn+"\x00"+string(req)] = v
	}
	return fill(dst, v)
}

func rawHTTP(req, dst []byte) int32 {
	h, denied := enter(FnHTTP)
	hook := h.HTTP
	if n, ok := h.cached(dst, FnHTTP, req); ok && !denied {
		h.mu.Unlock()
		return n
	}
	h.mu.Unlock()
	if denied || hook == nil {
		return ErrDenied.Code
	}
	var w wireHTTPRequest
	if err := json.Unmarshal(req, &w); err != nil || w.URL == "" {
		return ErrInvalid.Code
	}
	r := HTTPRequest{Method: w.Method, URL: w.URL, Headers: w.Headers, CostHint: w.CostHint}
	if w.BodyB64 != "" {
		b, err := base64.StdEncoding.DecodeString(w.BodyB64)
		if err != nil {
			return ErrInvalid.Code
		}
		r.Body = b
	}
	resp, err := hook(r)
	if err != nil {
		if e, ok := err.(*Error); ok {
			return e.Code
		}
		return ErrInternal.Code
	}
	out, _ := json.Marshal(wireHTTPResponse{Status: resp.Status, Headers: resp.Headers, BodyB64: base64.StdEncoding.EncodeToString(resp.Body)})
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.answer(dst, FnHTTP, req, out)
}

func rawCursorGet(key, dst []byte) int32 {
	h, denied := enter(FnCursorGet)
	defer h.mu.Unlock()
	v, ok := h.Cursors[string(key)]
	switch {
	case denied:
		return ErrDenied.Code
	case len(key) == 0:
		return ErrInvalid.Code
	case !ok:
		return ErrNotFound.Code
	}
	return h.answer(dst, FnCursorGet, key, v)
}

func rawCursorSet(key, val []byte) int32 {
	h, denied := enter(FnCursorSet)
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	if len(key) == 0 || len(key) > 128 {
		return ErrInvalid.Code
	}
	total := len(val)
	for k, v := range h.Cursors {
		if k != string(key) {
			total += len(v)
		}
	}
	if total > MaxCursorBytes {
		return ErrQuota.Code
	}
	h.Cursors[string(key)] = bytes.Clone(val)
	return 0
}

func rawSubmit(kind, batch, dst []byte) int32 {
	h, denied := enter(FnSubmit)
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	if n, ok := h.cached(dst, FnSubmit, append(append([]byte(nil), kind...), batch...)); ok {
		return n
	}
	switch string(kind) {
	case KindListing, KindCategory, KindStockObserved, KindOrder, KindOrderUpdate, KindRefund, KindCustomer:
	default:
		return ErrInvalid.Code
	}
	var recs []json.RawMessage
	if err := json.Unmarshal(batch, &recs); err != nil || len(recs) > MaxBatchRecords {
		return ErrInvalid.Code
	}
	h.Submitted = append(h.Submitted, SubmittedBatch{Kind: string(kind), Records: recs})
	results := make([]SubmitResult, 0, len(recs))
	for _, r := range recs {
		var id struct {
			ExternalID string `json:"external_id"`
		}
		_ = json.Unmarshal(r, &id)
		res := SubmitResult{ExternalID: id.ExternalID, Status: ResultAccepted}
		if id.ExternalID == "" {
			res = SubmitResult{Status: ResultRejected, Reason: "missing_external_id"}
		} else if h.Reject != nil {
			if rej := h.Reject(string(kind), id.ExternalID); rej != nil {
				res = *rej
			}
		}
		results = append(results, res)
	}
	out, _ := json.Marshal(results)
	return h.answer(dst, FnSubmit, append(append([]byte(nil), kind...), batch...), out)
}

func rawOutboxNext(max int32, dst []byte) int32 {
	h, denied := enter(FnOutboxNext)
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	if c, ok := h.cached(dst, FnOutboxNext, []byte{byte(max)}); ok {
		return c
	}
	n := int(max)
	if n > len(h.Outbox) {
		n = len(h.Outbox)
	}
	out, _ := json.Marshal(h.Outbox[:n])
	if n == 0 {
		out = []byte("[]")
	}
	return h.answer(dst, FnOutboxNext, []byte{byte(max)}, out)
}

func rawOutboxAck(id, ack []byte) int32 {
	h, denied := enter(FnOutboxAck)
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	var a Ack
	if err := json.Unmarshal(ack, &a); err != nil {
		return ErrInvalid.Code
	}
	idx := -1
	for i, op := range h.Outbox {
		if op.ID == string(id) {
			idx = i
		}
	}
	if idx < 0 {
		return ErrNotFound.Code
	}
	h.Acks[string(id)] = a
	h.Outbox = append(h.Outbox[:idx], h.Outbox[idx+1:]...)
	return 0
}

func rawLog(level int32, code, fields []byte) {
	h, _ := enter(FnLog)
	defer h.mu.Unlock()
	var f map[string]any
	_ = json.Unmarshal(fields, &f)
	h.Logs = append(h.Logs, LogLine{Level: int(level), Code: string(code), Fields: f})
}

// CursorKeys lists the stored cursor keys, sorted.
func (h *FakeHost) CursorKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.Cursors))
	for k := range h.Cursors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
