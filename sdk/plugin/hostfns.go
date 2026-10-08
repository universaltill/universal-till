package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Log writes one line into the till's log, prefixed with the plugin id.
func Log(msg string) { rawLogWrite([]byte(msg)) }

// Logf is Log with fmt.Sprintf formatting.
func Logf(format string, args ...any) { Log(fmt.Sprintf(format, args...)) }

// StorageGet reads key from the plugin's own key/value store (permission
// "storage"). A missing key is ErrNotFound.
func StorageGet(key string) ([]byte, error) {
	k := []byte(key)
	return bufCall("storage_get", defaultBuf, func(dst []byte) int32 { return rawStorageGet(k, dst) })
}

// StorageSet stores value under key (permission "storage").
func StorageSet(key string, value []byte) error {
	return codeErr("storage_set", rawStorageSet([]byte(key), value))
}

// SettingsGet reads one of the plugin's own declared settings (secrets
// included). An unset key is ErrNotFound.
func SettingsGet(key string) (string, error) {
	k := []byte(key)
	v, err := bufCall("settings_get", defaultBuf, func(dst []byte) int32 { return rawSettingsGet(k, dst) })
	return string(v), err
}

// SecretSet stores a credential the plugin obtained under one of its own
// `"type": "secret"` settings (permission "secret:write"). Read it back with
// SettingsGet.
func SecretSet(key, value string) error {
	return codeErr("secret_set", rawSecretSet([]byte(key), []byte(value)))
}

// HTTPRequest is an outbound request. Method defaults to GET.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is http_request's answer. The body is capped by the host
// (256 KiB, 8 MiB from a net:validation: host) and truncated past it.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type wireRequest struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	BodyB64 string            `json:"body_b64,omitempty"`
}

type wireResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

func (r HTTPRequest) wire(withBody bool) ([]byte, error) {
	w := wireRequest{Method: r.Method, URL: r.URL, Headers: r.Headers}
	if withBody && len(r.Body) > 0 {
		w.BodyB64 = base64.StdEncoding.EncodeToString(r.Body)
	}
	return json.Marshal(w)
}

// HTTP sends one request and returns the whole response (permission
// "net:<host>" or similar — reference "Where you can connect"). A too-small
// first buffer is retried with the same bytes, which the host answers from
// its cache: the request is never sent twice by the SDK.
func HTTP(req HTTPRequest) (*HTTPResponse, error) {
	raw, err := req.wire(true)
	if err != nil {
		return nil, err
	}
	out, err := bufCall("http_request", httpBuf, func(dst []byte) int32 { return rawHTTPRequest(raw, dst) })
	if err != nil {
		return nil, err
	}
	var w wireResponse
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, fmt.Errorf("ut http_request: decode response: %w", err)
	}
	body, err := base64.StdEncoding.DecodeString(w.BodyB64)
	if err != nil {
		return nil, fmt.Errorf("ut http_request: decode body: %w", err)
	}
	return &HTTPResponse{Status: w.Status, Headers: w.Headers, Body: body}, nil
}

// ViewQuery runs a named read-only core view (ADR-0121 §5) and returns its
// JSON array. args is JSON-encoded; nil sends {}.
func ViewQuery(name string, args any) (json.RawMessage, error) {
	a := []byte("{}")
	if args != nil {
		var err error
		if a, err = json.Marshal(args); err != nil {
			return nil, err
		}
	}
	n := []byte(name)
	out, err := bufCall("view_query", viewMax, func(dst []byte) int32 { return rawViewQuery(n, a, dst) })
	return json.RawMessage(out), err
}

// EventPublish raises an event in the plugin's own namespace, delivered
// after this event returns (ADR-0121 §3). payload is JSON-encoded; nil
// sends null, []byte and json.RawMessage go verbatim.
func EventPublish(eventType string, payload any) error {
	p, err := encodeAnswer(payload)
	if err != nil {
		return err
	}
	return codeErr("event_publish", rawEventPublish([]byte(eventType), p))
}

// JobProgress reports progress from inside a job: pct is clamped to 0–100,
// key is a key in the plugin's own locales ("" keeps the current message).
// Outside a job it is ErrNotFound.
func JobProgress(pct int, key string) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return codeErr("job_progress", rawJobProgress(uint32(pct), []byte(key)))
}

// readResult maps a plain read() return: n > 0 bytes, 0 at end of stream.
func readResult(op string, p []byte, n int32) (int, error) {
	switch {
	case n < 0:
		return 0, codeErr(op, n)
	case n == 0 && len(p) > 0:
		return 0, io.EOF
	}
	return int(n), nil
}

// writeAll loops a write host call until b is consumed.
func writeAll(op string, b []byte, call func([]byte) int32) (int, error) {
	done := 0
	for done < len(b) {
		n := call(b[done:])
		if n < 0 {
			return done, codeErr(op, n)
		}
		if n == 0 {
			return done, io.ErrShortWrite
		}
		done += int(n)
	}
	return done, nil
}

// HTTPStream is an http:stream handle: write the request body, then read the
// response as it arrives. Handles last only for the current event.
type HTTPStream struct{ h int32 }

// HTTPStatus is a stream's response status line and headers.
type HTTPStatus struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// HTTPOpen opens a streaming request (permission "http:stream" plus the
// egress grant). req.Body, if any, is written straight away; more can follow
// with Write. Nothing is sent until Status or Read.
func HTTPOpen(req HTTPRequest) (*HTTPStream, error) {
	raw, err := req.wire(false)
	if err != nil {
		return nil, err
	}
	h := rawHTTPOpen(raw)
	if h < 0 {
		return nil, codeErr("http_open", h)
	}
	s := &HTTPStream{h: h}
	if len(req.Body) > 0 {
		if _, err := s.Write(req.Body); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Write appends to the request body (before the first Status/Read).
func (s *HTTPStream) Write(p []byte) (int, error) {
	return writeAll("http_write", p, func(b []byte) int32 { return rawHTTPWrite(s.h, b) })
}

// Status sends the request if needed and returns the response status.
func (s *HTTPStream) Status() (HTTPStatus, error) {
	var st HTTPStatus
	out, err := bufCall("http_status", 16<<10, func(dst []byte) int32 { return rawHTTPStatus(s.h, dst) })
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return st, fmt.Errorf("ut http_status: decode: %w", err)
	}
	return st, nil
}

// Read reads the response body; io.EOF at its end. Never retried.
func (s *HTTPStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return readResult("http_read", p, rawHTTPRead(s.h, p))
}

// Close releases the handle; a second Close is ErrNotFound.
func (s *HTTPStream) Close() error { return codeErr("http_close", rawHTTPClose(s.h)) }

// TCPConn is a raw TCP handle (permission "tcp:<host>:<port>"). Handles are
// plugin-scoped and survive across events: keep Handle() in storage and
// rebuild the conn with TCPConnHandle.
type TCPConn struct {
	h int32
	// ReadTimeout bounds each Read (the host clamps it to [1 ms, 30 s] and
	// to the event deadline). Zero means 5 s.
	ReadTimeout time.Duration
}

// TCPOpen dials host:port; timeout 0 means 5 s (host clamps to [1 ms, 15 s]).
func TCPOpen(host string, port int, timeout time.Duration) (*TCPConn, error) {
	h := rawTCPOpen([]byte(host), uint32(port), millis(timeout))
	if h < 0 {
		return nil, codeErr("tcp_open", h)
	}
	return &TCPConn{h: h}, nil
}

// TCPConnHandle wraps a handle kept from an earlier event.
func TCPConnHandle(h int32) *TCPConn { return &TCPConn{h: h} }

// Handle is the host handle, for keeping across events.
func (c *TCPConn) Handle() int32 { return c.h }

// Write sends all of p (each host call has a 10 s write deadline).
func (c *TCPConn) Write(p []byte) (int, error) {
	return writeAll("tcp_write", p, func(b []byte) int32 { return rawTCPWrite(c.h, b) })
}

// Read is one read under ReadTimeout. A timeout or a closed peer is
// ErrInternal — the till never reports io.EOF on TCP — so frame your own
// messages rather than reading to EOF.
func (c *TCPConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return readResult("tcp_read", p, rawTCPRead(c.h, p, millis(c.ReadTimeout)))
}

// Close releases the handle (idempotent on the host).
func (c *TCPConn) Close() error { return codeErr("tcp_close", rawTCPClose(c.h)) }

// millis converts a timeout for the host: 0 or less is the 5 s default, and
// a positive one rounds up to whole milliseconds (never to 0).
func millis(d time.Duration) uint32 {
	if d <= 0 {
		d = 5 * time.Second
	}
	return uint32((d + time.Millisecond - 1) / time.Millisecond)
}

// ImportFile is a staged import file (the `file_handle` of an
// import.requested.ask payload).
type ImportFile struct{ h int32 }

// ImportFileHandle wraps the payload's file_handle.
func ImportFileHandle(h int32) *ImportFile { return &ImportFile{h: h} }

// Size is the staged file's total size in bytes.
func (f *ImportFile) Size() (int64, error) {
	n := rawImportFileSize(f.h)
	if n < 0 {
		return 0, codeErr("import_file_size", int32(n))
	}
	return n, nil
}

// Read reads the next chunk; io.EOF at the end. Never retried.
func (f *ImportFile) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return readResult("import_file_read", p, rawImportFileRead(f.h, p))
}

// Close releases the handle and deletes the staged file.
func (f *ImportFile) Close() error { return codeErr("import_file_close", rawImportFileClose(f.h)) }

// Upload is an operator-chosen file from a plugin view `file` field.
type Upload struct{ h int32 }

// UploadOpen opens the file behind an upload token (32 lower-case hex).
func UploadOpen(token string) (*Upload, error) {
	h := rawUploadOpen([]byte(token))
	if h < 0 {
		return nil, codeErr("upload_open", h)
	}
	return &Upload{h: h}, nil
}

// Read reads the next chunk; io.EOF at the end. Never retried.
func (u *Upload) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return readResult("upload_read", p, rawUploadRead(u.h, p))
}

// Close closes every handle on the file and consumes its token.
func (u *Upload) Close() error { return codeErr("upload_close", rawUploadClose(u.h)) }

// BlobWriter is an uncommitted blob put (permission "blob:own"). Nothing is
// visible under its name until Commit; an uncommitted put is discarded when
// the event ends.
type BlobWriter struct{ h int32 }

// BlobCreate opens a put for name ([a-z0-9._-]{1,128}).
func BlobCreate(name string) (*BlobWriter, error) {
	h := rawBlobPutOpen([]byte(name))
	if h < 0 {
		return nil, codeErr("blob_put_open", h)
	}
	return &BlobWriter{h: h}, nil
}

// Write appends to the put. ErrQuota discards it and closes the handle.
func (w *BlobWriter) Write(p []byte) (int, error) {
	return writeAll("blob_write", p, func(b []byte) int32 { return rawBlobWrite(w.h, b) })
}

// Commit atomically replaces any blob of that name. The handle is spent
// either way.
func (w *BlobWriter) Commit() error { return codeErr("blob_commit", rawBlobCommit(w.h)) }

// BlobReader reads a committed blob.
type BlobReader struct{ h int32 }

// BlobOpen opens a committed blob; a missing one is ErrNotFound.
func BlobOpen(name string) (*BlobReader, error) {
	h := rawBlobGetOpen([]byte(name))
	if h < 0 {
		return nil, codeErr("blob_get_open", h)
	}
	return &BlobReader{h: h}, nil
}

// Read reads the next chunk; io.EOF at the end, which also releases the
// handle on the host. Never retried.
func (r *BlobReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return readResult("blob_read", p, rawBlobRead(r.h, p))
}

// BlobDelete removes a committed blob; a missing one is ErrNotFound.
func BlobDelete(name string) error { return codeErr("blob_delete", rawBlobDelete([]byte(name))) }

// BlobInfo is one blob_list entry.
type BlobInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// BlobList lists the plugin's committed blobs, sorted by name.
func BlobList() ([]BlobInfo, error) {
	out, err := bufCall("blob_list", defaultBuf, rawBlobList)
	if err != nil {
		return nil, err
	}
	var list []BlobInfo
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("ut blob_list: decode: %w", err)
	}
	return list, nil
}

// Handle is the host handle, for logging or a host-level test.
func (s *HTTPStream) Handle() int32 { return s.h }

// Handle is the host handle, for logging.
func (f *ImportFile) Handle() int32 { return f.h }

// Handle is the host handle, for logging.
func (u *Upload) Handle() int32 { return u.h }

// Handle is the host handle, for logging.
func (w *BlobWriter) Handle() int32 { return w.h }

// Handle is the host handle, for logging.
func (r *BlobReader) Handle() int32 { return r.h }

// DeviceID is this till's stable device UUID (permission "device-info",
// ADR-0140). Every successful call is audited on the till.
func DeviceID() (string, error) {
	v, err := bufCall("device_id_get", defaultBuf, rawDeviceIDGet)
	return string(v), err
}

// DeviceLocalIPs is the till's own interface addresses at call time
// (permission "device-info"). An empty list is a real answer.
func DeviceLocalIPs() ([]string, error) {
	v, err := bufCall("device_local_ips_get", defaultBuf, rawDeviceLocalIPsGet)
	if err != nil {
		return nil, err
	}
	ips := []string{}
	if err := json.Unmarshal(v, &ips); err != nil {
		return nil, fmt.Errorf("ut device_local_ips_get: decode: %w", err)
	}
	return ips, nil
}

// DeviceTimezone is the till's UTC offset at call time, as "UTC±hh:mm"
// (permission "device-info").
func DeviceTimezone() (string, error) {
	v, err := bufCall("device_timezone_get", defaultBuf, rawDeviceTimezoneGet)
	return string(v), err
}
