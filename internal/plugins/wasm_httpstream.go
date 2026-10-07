package plugins

// Streaming HTTP for wasm plugins (ADR-0121 §3 http:stream, ut-docs#3156):
//
//	http_open(reqPtr, reqLen) -> handle | err
//	http_write(h, bufPtr, bufLen) -> bytes accepted | err
//	http_status(h, dstPtr, dstCap) -> full length of {"status":N,"headers":{…}} | err
//	http_read(h, dstPtr, dstCap) -> n (0 = end of body) | err
//	http_close(h) -> 0 | err
//
// http_open takes http_request's JSON without the body — {method, url,
// headers} — and applies exactly http_request's egress policy
// (admitHTTPRequest: scheme rule, name check, http:lan, net:validation:, the
// dial-time IP rule through the same client). Nothing is sent yet:
// http_write appends to an in-memory request body, and the request goes out
// on the first http_status or http_read (a later http_write is
// hostErrInvalid). The response body is then streamed — NDJSON/SSE from a
// local model server arrives chunk by chunk, never buffered whole.
//
// Bounds: the request body and the bytes read from the response are each
// capped at the manifest's limits.http_body_mb (Manifest.EffectiveLimits,
// platform-clamped; 8 MiB when the manifest cannot be read) — over it is
// hostErrQuota and the handle is closed. At most maxHTTPStreamHandles open
// handles per event (one more is hostErrBusy). Each http_read gets at most
// min(dstCap, httpStreamReadCap) bytes and gives up after
// httpStreamIdleTimeout without data; the request context derives from the
// event's, so the event deadline cancels any I/O in flight.
//
// Handles live on the per-event hostState, NOT process-wide: a stream never
// outlives the event that opened it. handleEvent closes every handle when
// the event returns — success, error, deadline or trap.
//
// The http:stream permission is checked (CheckPermission: audited, revocable
// live) on every call but http_close: releasing a handle is always allowed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// permHTTPStream gates the http_* stream functions.
	permHTTPStream = "http:stream"
	// maxHTTPStreamHandles caps a plugin's open streams per event (ADR-0121 §3).
	maxHTTPStreamHandles = 4
	// httpStreamReadCap bounds one http_read regardless of dstCap.
	httpStreamReadCap = 256 << 10
)

// httpStreamIdleTimeout: an http_read that gets no byte for this long fails
// (hostErrInternal) and closes the handle (ADR-0121 §3 "idle 30 s"). A var so
// tests can shorten it.
var httpStreamIdleTimeout = 30 * time.Second

// httpStreamLimitBytes is the per-stream byte cap (request body, and
// response bytes read) from the manifest's limits.http_body_mb, clamped to
// goos's ceiling; nil (manifest unreadable) gets the default.
func httpStreamLimitBytes(m *Manifest, goos string) int64 {
	if m == nil {
		m = &Manifest{}
	}
	return int64(m.EffectiveLimits(goos).HTTPBodyMB) << 20
}

// httpStream is one open handle.
type httpStream struct {
	ctx     context.Context // event ctx + egress grants; cancel ends all I/O
	cancel  context.CancelFunc
	method  string
	url     *url.URL
	headers map[string]string
	body    bytes.Buffer
	sent    bool
	resp    *http.Response
	status  []byte // http_status's JSON, built once at send
	read    int64  // response bytes handed to the guest
	eof     bool
}

func (st *httpStream) release() {
	if st.cancel != nil {
		st.cancel()
	}
	if st.resp != nil {
		_ = st.resp.Body.Close()
	}
}

// httpStreams is the per-event handle registry (on hostState). Handles count
// from 0 within the event and are never reused within it.
type httpStreams struct {
	mu   sync.Mutex
	next int32
	open map[int32]*httpStream
}

func (r *httpStreams) add(st *httpStream) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.open) >= maxHTTPStreamHandles {
		return 0, false
	}
	if r.open == nil {
		r.open = map[int32]*httpStream{}
	}
	h := r.next
	r.next++
	r.open[h] = st
	return h, true
}

func (r *httpStreams) get(h int32) (*httpStream, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.open[h]
	return st, ok
}

// close releases one handle; unknown handles are a no-op here (http_close
// reports them as hostErrNotFound itself).
func (r *httpStreams) close(h int32) {
	r.mu.Lock()
	st, ok := r.open[h]
	delete(r.open, h)
	r.mu.Unlock()
	if ok {
		st.release()
	}
}

// closeAll releases every handle — handleEvent defers it so no stream
// outlives its event.
func (r *httpStreams) closeAll() {
	r.mu.Lock()
	open := r.open
	r.open = nil
	r.mu.Unlock()
	for _, st := range open {
		st.release()
	}
}

// streamLimit is this event's http:stream byte cap, read lazily (once per
// event) from the plugin's installed manifest.
func (s *hostState) streamLimit(ctx context.Context) int64 {
	s.limitOnce.Do(func() {
		m, ok, err := InstalledManifest(ctx, s.db, s.pluginID)
		if err != nil {
			logging.L().Warnf("[wasm:%s] manifest unreadable, http:stream uses the default body limit: %v", s.pluginID, err)
		}
		if err != nil || !ok {
			m = nil
		}
		s.httpBodyLimit = httpStreamLimitBytes(m, goruntime.GOOS)
	})
	return s.httpBodyLimit
}

// streamCall resolves the caller and checks http:stream for one call.
func streamCall(ctx context.Context) (*hostState, int32) {
	s, ok := stateFrom(ctx)
	if !ok {
		return nil, hostErrInternal
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, permHTTPStream); err != nil {
		return nil, hostErrDenied
	}
	return s, 0
}

func hostHTTPOpen(ctx context.Context, m api.Module, reqPtr, reqLen uint32) int32 {
	s, code := streamCall(ctx)
	if code != 0 {
		return code
	}
	if httpEgressDemoDenied(s) {
		return hostErrDenied
	}
	raw, ok := readGuest(m, reqPtr, reqLen)
	if !ok {
		return hostErrInvalid
	}
	var req struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return hostErrInvalid
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	u, grants, code := admitHTTPRequest(ctx, s, req.URL)
	if code != 0 {
		return code
	}
	// Refuse a bad method now, not at send.
	if _, err := http.NewRequestWithContext(ctx, req.Method, u.String(), nil); err != nil {
		return hostErrInvalid
	}
	rctx, cancel := context.WithCancel(withEgressGrants(ctx, grants))
	st := &httpStream{ctx: rctx, cancel: cancel, method: req.Method, url: u, headers: req.Headers}
	h, ok := s.streams.add(st)
	if !ok {
		cancel()
		logging.L().Infof("[wasm:%s] http_open refused: max %d open streams", s.pluginID, maxHTTPStreamHandles)
		return hostErrBusy
	}
	return h
}

func hostHTTPWrite(ctx context.Context, m api.Module, h int32, ptr, length uint32) int32 {
	s, code := streamCall(ctx)
	if code != 0 {
		return code
	}
	st, ok := s.streams.get(h)
	if !ok {
		return hostErrNotFound
	}
	if st.sent {
		return hostErrInvalid
	}
	buf, ok := readGuest(m, ptr, length)
	if !ok {
		return hostErrInvalid
	}
	if limit := s.streamLimit(ctx); int64(st.body.Len())+int64(len(buf)) > limit {
		s.streams.close(h)
		logging.L().Infof("[wasm:%s] http_write: request body over the %d-byte limit, stream closed", s.pluginID, limit)
		return hostErrQuota
	}
	st.body.Write(buf)
	return int32(len(buf))
}

// streamSend sends the request on first use. A failure closes the handle.
func streamSend(s *hostState, h int32, st *httpStream) int32 {
	if st.sent {
		return 0
	}
	st.sent = true
	req, err := http.NewRequestWithContext(st.ctx, st.method, st.url.String(), bytes.NewReader(st.body.Bytes()))
	if err != nil {
		s.streams.close(h)
		return hostErrInvalid
	}
	for k, v := range st.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "UniversalTill-plugin/"+s.pluginID)
	client := s.httpClient
	if client == nil {
		client = defaultPluginHTTPClient
	}
	resp, err := client.Do(req)
	st.body = bytes.Buffer{}
	if err != nil {
		s.streams.close(h)
		if errors.Is(err, errEgressDenied) {
			logEgressDenied(s.pluginID, err)
			return hostErrDenied
		}
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err // never log the URL (query strings can hold tokens)
		}
		logging.L().Infof("[wasm:%s] http stream %s %s failed: %v", s.pluginID, st.method, st.url.Hostname(), cause)
		return hostErrInternal
	}
	headers := map[string]string{}
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	status, err := json.Marshal(map[string]any{"status": resp.StatusCode, "headers": headers})
	if err != nil {
		_ = resp.Body.Close()
		s.streams.close(h)
		return hostErrInternal
	}
	st.resp, st.status = resp, status
	return 0
}

func hostHTTPStatus(ctx context.Context, m api.Module, h int32, dstPtr, dstCap uint32) int32 {
	s, code := streamCall(ctx)
	if code != 0 {
		return code
	}
	st, ok := s.streams.get(h)
	if !ok {
		return hostErrNotFound
	}
	if code := streamSend(s, h, st); code != 0 {
		return code
	}
	return writeGuest(m, dstPtr, dstCap, st.status)
}

func hostHTTPRead(ctx context.Context, m api.Module, h int32, dstPtr, dstCap uint32) int32 {
	s, code := streamCall(ctx)
	if code != 0 {
		return code
	}
	st, ok := s.streams.get(h)
	if !ok {
		return hostErrNotFound
	}
	if dstCap == 0 {
		return hostErrInvalid
	}
	if code := streamSend(s, h, st); code != 0 {
		return code
	}
	if st.eof {
		return 0
	}
	limit := s.streamLimit(ctx)
	want := int64(dstCap)
	if want > httpStreamReadCap {
		want = httpStreamReadCap
	}
	// Ask for one byte past the limit at most, so a body over it is seen.
	if rem := limit - st.read + 1; want > rem {
		want = rem
	}
	// Check the destination BEFORE reading (as tcp_read, ut-docs#614): a
	// body read cannot be un-read.
	if _, ok := m.Memory().Read(dstPtr, uint32(want)); !ok {
		return hostErrInvalid
	}
	buf := make([]byte, want)
	idle := time.AfterFunc(httpStreamIdleTimeout, st.cancel)
	n, err := readSome(st.resp.Body, buf)
	timedOut := !idle.Stop()
	if n > 0 {
		st.read += int64(n)
		if st.read > limit {
			s.streams.close(h)
			logging.L().Infof("[wasm:%s] http_read: response over the %d-byte limit, stream closed", s.pluginID, limit)
			return hostErrQuota
		}
		if errors.Is(err, io.EOF) {
			st.eof = true
		}
		return writeGuest(m, dstPtr, dstCap, buf[:n])
	}
	if errors.Is(err, io.EOF) {
		st.eof = true
		return 0
	}
	s.streams.close(h)
	reason := "read failed"
	if timedOut {
		reason = "idle timeout"
	} else if ctx.Err() != nil {
		reason = "event deadline"
	}
	logging.L().Infof("[wasm:%s] http_read %s %s: %s", s.pluginID, st.method, st.url.Hostname(), reason)
	return hostErrInternal
}

// readSome is one Read that does not report (0, nil) — a reader may return
// that, and the guest would take it for end of body.
func readSome(r io.Reader, buf []byte) (int, error) {
	for i := 0; i < 100; i++ {
		n, err := r.Read(buf)
		if n > 0 || err != nil {
			return n, err
		}
	}
	return 0, io.ErrNoProgress
}

func hostHTTPClose(ctx context.Context, h int32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if _, ok := s.streams.get(h); !ok {
		return hostErrNotFound
	}
	s.streams.close(h)
	return 0
}
