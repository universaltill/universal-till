//go:build !wasip1

package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"sync"
	"unicode/utf8"
)

// FakeHost is an in-memory "ut" host for native unit tests: install it with
// UseFakeHost and call your handlers directly (or through Dispatch). It
// follows the host's buffer ABI and return codes, so the SDK's retry and
// error paths run exactly as on a till.
//
// A nil hook (HTTP, TCP, Views) answers ErrDenied, like a missing
// permission; Deny["<host function>"] denies any one call. A hook error that
// is an *Error returns its code, any other error ErrInternal. Hooks run
// without the host lock, so a hook may call the SDK itself. The host's size
// caps apply (storage 128 B keys / 64 KiB values, 256 KiB per stream read,
// 64 view_query and 64 item_image_open calls, 4 open item images per
// FakeHost — one FakeHost stands for one event).
// One FakeHost is installed process-wide: tests that use it must not run in
// parallel.
type FakeHost struct {
	mu sync.Mutex

	Storage  map[string][]byte
	Settings map[string]string
	// Secrets records secret_set calls; each is also readable via Settings.
	Secrets map[string]string
	Blobs   map[string][]byte
	// Uploads maps an upload token to the file behind it.
	Uploads map[string][]byte
	// ImportFiles maps an import file_handle to the staged file.
	ImportFiles map[int32][]byte
	// ItemImages maps an item id to its photos, as item_image_open returns
	// them (the till re-encodes; the fake hands back these bytes as is).
	// Granted unless Deny["item_image_open"].
	ItemImages map[string]ItemImageFiles

	HTTP  func(HTTPRequest) (HTTPResponse, error)
	TCP   func(host string, port int) (io.ReadWriteCloser, error)
	Views func(name string, args json.RawMessage) (json.RawMessage, error)

	// PluginID is the manifest id: event_publish accepts only
	// "<PluginID>.<name>" types, as the till does (NewFakeHost sets
	// "com.example.plugin").
	PluginID string
	// SecretSettings are the manifest's `"type": "secret"` setting keys;
	// secret_set on any other key is ErrDenied, as on the till.
	SecretSettings map[string]bool

	// Device answers device_id_get / device_local_ips_get /
	// device_timezone_get; all empty → ErrDenied, as without "device-info".
	DeviceID       string
	DeviceIPs      []string
	DeviceTimezone string

	// InJob makes job_progress succeed (it is ErrNotFound outside a job).
	InJob bool
	Deny  map[string]bool

	Logs      []string
	Published []PublishedEvent
	Progress  []JobReport
	// Calls counts host calls by function name.
	Calls map[string]int

	next      int32
	httpCache struct{ req, resp []byte }
	streams   map[int32]*fakeStream
	tcp       map[int32]io.ReadWriteCloser
	uploads   map[int32]*fakeUpload
	importPos map[int32]int
	puts      map[int32]*fakePut
	gets      map[int32]*bytes.Reader
	images    map[int32]*bytes.Reader
}

// PublishedEvent is one recorded event_publish.
type PublishedEvent struct {
	Type    string
	Payload json.RawMessage
}

// JobReport is one recorded job_progress.
type JobReport struct {
	Pct int
	Key string
}

// ItemImageFiles is one FakeHost item's photos; nil = none of that role.
// The "ref" role is AIRef when set, else Thumb, as on the till.
type ItemImageFiles struct {
	AIRef, Thumb []byte
}

type fakeStream struct {
	req  HTTPRequest
	body bytes.Buffer
	resp *HTTPResponse
	rd   *bytes.Reader
}

type fakeUpload struct {
	token string
	r     *bytes.Reader
}

type fakePut struct {
	name string
	buf  bytes.Buffer
}

// NewFakeHost returns a FakeHost with every map ready to fill.
func NewFakeHost() *FakeHost {
	return &FakeHost{
		Storage: map[string][]byte{}, Settings: map[string]string{}, Secrets: map[string]string{},
		PluginID: "com.example.plugin",
		Blobs:    map[string][]byte{}, Uploads: map[string][]byte{}, ImportFiles: map[int32][]byte{},
		ItemImages: map[string]ItemImageFiles{}, images: map[int32]*bytes.Reader{},
		SecretSettings: map[string]bool{}, Deny: map[string]bool{}, Calls: map[string]int{},
		streams: map[int32]*fakeStream{}, tcp: map[int32]io.ReadWriteCloser{},
		uploads: map[int32]*fakeUpload{}, importPos: map[int32]int{},
		puts: map[int32]*fakePut{}, gets: map[int32]*bytes.Reader{},
	}
}

// The host's caps (internal/data, internal/plugins) the fake enforces.
const (
	storageKeyMax   = 128
	storageValueMax = 64 << 10
	streamChunkMax  = 256 << 10
	viewNameMax     = 128
	viewArgsMax     = 4 << 10
	viewCallsMax    = 64
	secretKeyMax    = 128
	itemIDMax       = 128
	itemOpensMax    = 64
	itemHandlesMax  = 4
)

var (
	eventNameRe   = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)+$`)
	uploadTokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Cleaner is the part of testing.TB UseFakeHost needs.
type Cleaner interface{ Cleanup(func()) }

var (
	hostMu sync.Mutex
	host   *FakeHost
)

// UseFakeHost installs h for the rest of the test and restores the previous
// host when it ends.
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

// enter locks the installed host for one call; denied reports Deny[op].
func enter(op string) (*FakeHost, bool) {
	hostMu.Lock()
	h := host
	hostMu.Unlock()
	if h == nil {
		panic("plugin: no host outside wasm — call plugin.UseFakeHost in the test")
	}
	h.mu.Lock()
	h.Calls[op]++
	return h, h.Deny[op]
}

func (h *FakeHost) handle() int32 { h.next++; return h.next }

// unlocked runs a test hook without the host lock, so the hook may call the
// SDK (a logging test server, a net.Pipe peer driven through the SDK).
func (h *FakeHost) unlocked(f func()) {
	h.mu.Unlock()
	defer h.mu.Lock()
	f()
}

func capRead(dst []byte) []byte {
	if len(dst) > streamChunkMax {
		return dst[:streamChunkMax]
	}
	return dst
}

// fill is the buffer ABI: min(len, cap) bytes in, full length back.
func fill(dst, data []byte) int32 {
	copy(dst, data)
	return int32(len(data))
}

func hookCode(err error) int32 {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ErrInternal.Code
}

func rawLogWrite(msg []byte) {
	h, _ := enter("log_write")
	defer h.mu.Unlock()
	h.Logs = append(h.Logs, string(msg))
}

func rawStorageGet(key, dst []byte) int32 {
	h, denied := enter("storage_get")
	defer h.mu.Unlock()
	switch v, ok := h.Storage[string(key)]; {
	case len(key) == 0:
		return ErrInvalid.Code
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	default:
		return fill(dst, v)
	}
}

func rawStorageSet(key, val []byte) int32 {
	h, denied := enter("storage_set")
	defer h.mu.Unlock()
	switch {
	case len(key) == 0 || len(key) > storageKeyMax || len(val) > storageValueMax:
		return ErrInvalid.Code
	case denied:
		return ErrDenied.Code
	}
	h.Storage[string(key)] = bytes.Clone(val)
	return 0
}

func rawSettingsGet(key, dst []byte) int32 {
	h, denied := enter("settings_get")
	defer h.mu.Unlock()
	switch v, ok := h.Settings[string(key)]; {
	case len(key) == 0:
		return ErrInvalid.Code
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	default:
		return fill(dst, []byte(v))
	}
}

func rawSecretSet(key, val []byte) int32 {
	h, denied := enter("secret_set")
	defer h.mu.Unlock()
	switch {
	case denied || !h.SecretSettings[string(key)]:
		return ErrDenied.Code
	case len(key) == 0 || len(key) > secretKeyMax || len(val) > 64<<10 || !utf8.Valid(key) || !utf8.Valid(val):
		return ErrInvalid.Code
	}
	h.Secrets[string(key)] = string(val)
	h.Settings[string(key)] = string(val)
	return 0
}

func (h *FakeHost) doHTTP(req HTTPRequest) (*HTTPResponse, int32) {
	if h.HTTP == nil {
		return nil, ErrDenied.Code
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	var resp HTTPResponse
	var err error
	h.unlocked(func() { resp, err = h.HTTP(req) })
	if err != nil {
		return nil, hookCode(err)
	}
	return &resp, 0
}

func decodeWireRequest(raw []byte) (HTTPRequest, bool) {
	var w wireRequest
	if json.Unmarshal(raw, &w) != nil {
		return HTTPRequest{}, false
	}
	r := HTTPRequest{Method: w.Method, URL: w.URL, Headers: w.Headers}
	if w.BodyB64 != "" {
		var err error
		if r.Body, err = base64Decode(w.BodyB64); err != nil {
			return HTTPRequest{}, false
		}
	}
	return r, true
}

// rawHTTPRequest mirrors the host's retry cache: a too-small first buffer
// caches the response, and the identical retry replays it without calling
// the HTTP hook again (ut-docs#754).
func rawHTTPRequest(raw, dst []byte) int32 {
	h, denied := enter("http_request")
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	if h.httpCache.resp != nil && bytes.Equal(h.httpCache.req, raw) {
		resp := h.httpCache.resp
		if len(dst) >= len(resp) {
			h.httpCache.req, h.httpCache.resp = nil, nil
		}
		return fill(dst, resp)
	}
	req, ok := decodeWireRequest(raw)
	if !ok {
		return ErrInvalid.Code
	}
	resp, code := h.doHTTP(req)
	if code != 0 {
		return code
	}
	out, _ := json.Marshal(wireResponse{Status: resp.Status, Headers: resp.Headers, BodyB64: base64Encode(resp.Body)})
	if len(out) > len(dst) {
		h.httpCache.req, h.httpCache.resp = bytes.Clone(raw), out
	}
	return fill(dst, out)
}

func rawHTTPOpen(raw []byte) int32 {
	h, denied := enter("http_open")
	defer h.mu.Unlock()
	if denied || h.HTTP == nil {
		return ErrDenied.Code
	}
	req, ok := decodeWireRequest(raw)
	if !ok {
		return ErrInvalid.Code
	}
	if len(h.streams) >= 4 {
		return ErrBusy.Code
	}
	id := h.handle()
	h.streams[id] = &fakeStream{req: req}
	return id
}

func rawHTTPWrite(id int32, b []byte) int32 {
	h, denied := enter("http_write")
	defer h.mu.Unlock()
	s, ok := h.streams[id]
	switch {
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	case s.resp != nil:
		return ErrInvalid.Code
	}
	s.body.Write(b)
	return int32(len(b))
}

// send runs the request on the stream's first status/read.
func (h *FakeHost) send(id int32) (*fakeStream, int32) {
	s, ok := h.streams[id]
	if !ok {
		return nil, ErrNotFound.Code
	}
	if s.resp == nil {
		req := s.req
		req.Body = s.body.Bytes()
		resp, code := h.doHTTP(req)
		if code != 0 {
			delete(h.streams, id)
			return nil, code
		}
		s.resp, s.rd = resp, bytes.NewReader(resp.Body)
	}
	return s, 0
}

func rawHTTPStatus(id int32, dst []byte) int32 {
	h, denied := enter("http_status")
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	s, code := h.send(id)
	if code != 0 {
		return code
	}
	out, _ := json.Marshal(HTTPStatus{Status: s.resp.Status, Headers: s.resp.Headers})
	return fill(dst, out)
}

func rawHTTPRead(id int32, dst []byte) int32 {
	h, denied := enter("http_read")
	defer h.mu.Unlock()
	switch {
	case denied:
		return ErrDenied.Code
	case len(dst) == 0:
		return ErrInvalid.Code
	}
	s, code := h.send(id)
	if code != 0 {
		return code
	}
	n, _ := s.rd.Read(capRead(dst))
	return int32(n)
}

func rawHTTPClose(id int32) int32 {
	h, _ := enter("http_close")
	defer h.mu.Unlock()
	if _, ok := h.streams[id]; !ok {
		return ErrNotFound.Code
	}
	delete(h.streams, id)
	return 0
}

func rawTCPOpen(hostname []byte, port, _ uint32) int32 {
	h, denied := enter("tcp_open")
	defer h.mu.Unlock()
	if denied || h.TCP == nil {
		return ErrDenied.Code
	}
	if len(h.tcp) >= 4 {
		return ErrInvalid.Code // the host answers -4 to a fifth TCP handle
	}
	var c io.ReadWriteCloser
	var err error
	h.unlocked(func() { c, err = h.TCP(string(hostname), int(port)) })
	if err != nil {
		return hookCode(err)
	}
	id := h.handle()
	h.tcp[id] = c
	return id
}

func rawTCPWrite(id int32, b []byte) int32 {
	h, denied := enter("tcp_write")
	defer h.mu.Unlock()
	c, ok := h.tcp[id]
	switch {
	case !ok:
		return ErrNotFound.Code
	case denied:
		return ErrDenied.Code
	}
	var n int
	var err error
	h.unlocked(func() { n, err = c.Write(b) })
	if err != nil {
		return ErrInternal.Code
	}
	return int32(n)
}

func rawTCPRead(id int32, dst []byte, _ uint32) int32 {
	h, denied := enter("tcp_read")
	defer h.mu.Unlock()
	c, ok := h.tcp[id]
	switch {
	case !ok:
		return ErrNotFound.Code
	case denied:
		return ErrDenied.Code
	}
	var n int
	var err error
	h.unlocked(func() { n, err = c.Read(capRead(dst)) })
	if err != nil && n == 0 {
		return ErrInternal.Code // the till reports a closed peer as -3, never EOF
	}
	return int32(n)
}

func rawTCPClose(id int32) int32 {
	h, _ := enter("tcp_close")
	defer h.mu.Unlock()
	if c, ok := h.tcp[id]; ok {
		delete(h.tcp, id)
		h.unlocked(func() { _ = c.Close() })
	}
	return 0
}

func rawImportFileSize(id int32) int64 {
	h, _ := enter("import_file_size")
	defer h.mu.Unlock()
	f, ok := h.ImportFiles[id]
	if !ok {
		return int64(ErrNotFound.Code)
	}
	return int64(len(f))
}

func rawImportFileRead(id int32, dst []byte) int32 {
	h, _ := enter("import_file_read")
	defer h.mu.Unlock()
	f, ok := h.ImportFiles[id]
	if !ok {
		return ErrNotFound.Code
	}
	n := copy(dst, f[h.importPos[id]:])
	h.importPos[id] += n
	return int32(n)
}

func rawImportFileClose(id int32) int32 {
	h, _ := enter("import_file_close")
	defer h.mu.Unlock()
	delete(h.ImportFiles, id)
	delete(h.importPos, id)
	return 0
}

func rawUploadOpen(tok []byte) int32 {
	h, _ := enter("upload_open")
	defer h.mu.Unlock()
	f, ok := h.Uploads[string(tok)]
	switch {
	case !uploadTokenRe.Match(tok):
		return ErrInvalid.Code
	case !ok:
		return ErrNotFound.Code
	case len(h.uploads) >= 8:
		return ErrBusy.Code
	}
	id := h.handle()
	h.uploads[id] = &fakeUpload{token: string(tok), r: bytes.NewReader(f)}
	return id
}

func rawUploadRead(id int32, dst []byte) int32 {
	h, _ := enter("upload_read")
	defer h.mu.Unlock()
	u, ok := h.uploads[id]
	if !ok {
		return ErrNotFound.Code
	}
	n, _ := u.r.Read(capRead(dst))
	return int32(n)
}

// rawUploadClose consumes the token: every handle on that file closes.
func rawUploadClose(id int32) int32 {
	h, _ := enter("upload_close")
	defer h.mu.Unlock()
	if u, ok := h.uploads[id]; ok {
		delete(h.Uploads, u.token)
		for k, o := range h.uploads {
			if o.token == u.token {
				delete(h.uploads, k)
			}
		}
	}
	return 0
}

func rawViewQuery(name, args, dst []byte) int32 {
	h, denied := enter("view_query")
	defer h.mu.Unlock()
	if denied || h.Views == nil {
		return ErrDenied.Code
	}
	switch {
	case len(name) == 0 || len(name) > viewNameMax || len(args) > viewArgsMax || !json.Valid(args):
		return ErrInvalid.Code
	case h.Calls["view_query"] > viewCallsMax:
		return ErrQuota.Code
	}
	var out json.RawMessage
	var err error
	views, a := h.Views, bytes.Clone(args)
	h.unlocked(func() { out, err = views(string(name), a) })
	if err != nil {
		return hookCode(err)
	}
	if out == nil {
		out = json.RawMessage("[]")
	}
	return fill(dst, out)
}

func validBlobName(name []byte) bool {
	if len(name) == 0 || len(name) > 128 || string(name) == "." || string(name) == ".." || name[len(name)-1] == '.' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func rawBlobPutOpen(name []byte) int32 {
	h, denied := enter("blob_put_open")
	defer h.mu.Unlock()
	switch {
	case denied:
		return ErrDenied.Code
	case !validBlobName(name):
		return ErrInvalid.Code
	case len(h.puts)+len(h.gets) >= 8:
		return ErrBusy.Code
	}
	id := h.handle()
	h.puts[id] = &fakePut{name: string(name)}
	return id
}

func rawBlobWrite(id int32, b []byte) int32 {
	h, denied := enter("blob_write")
	defer h.mu.Unlock()
	p, ok := h.puts[id]
	switch {
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	}
	p.buf.Write(b)
	return int32(len(b))
}

func rawBlobCommit(id int32) int32 {
	h, denied := enter("blob_commit")
	defer h.mu.Unlock()
	p, ok := h.puts[id]
	switch {
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	}
	delete(h.puts, id)
	h.Blobs[p.name] = bytes.Clone(p.buf.Bytes())
	return 0
}

func rawBlobGetOpen(name []byte) int32 {
	h, denied := enter("blob_get_open")
	defer h.mu.Unlock()
	b, ok := h.Blobs[string(name)]
	switch {
	case denied:
		return ErrDenied.Code
	case !validBlobName(name):
		return ErrInvalid.Code
	case !ok:
		return ErrNotFound.Code
	case len(h.puts)+len(h.gets) >= 8:
		return ErrBusy.Code
	}
	id := h.handle()
	h.gets[id] = bytes.NewReader(b)
	return id
}

// rawBlobRead releases the handle at end of blob, as the host does.
func rawBlobRead(id int32, dst []byte) int32 {
	h, denied := enter("blob_read")
	defer h.mu.Unlock()
	r, ok := h.gets[id]
	switch {
	case denied:
		return ErrDenied.Code
	case !ok:
		return ErrNotFound.Code
	case len(dst) == 0:
		return ErrInvalid.Code
	}
	n, _ := r.Read(capRead(dst))
	if n == 0 {
		delete(h.gets, id)
	}
	return int32(n)
}

func rawBlobDelete(name []byte) int32 {
	h, denied := enter("blob_delete")
	defer h.mu.Unlock()
	_, ok := h.Blobs[string(name)]
	switch {
	case denied:
		return ErrDenied.Code
	case !validBlobName(name):
		return ErrInvalid.Code
	case !ok:
		return ErrNotFound.Code
	}
	delete(h.Blobs, string(name))
	return 0
}

func rawBlobList(dst []byte) int32 {
	h, denied := enter("blob_list")
	defer h.mu.Unlock()
	if denied {
		return ErrDenied.Code
	}
	list := make([]BlobInfo, 0, len(h.Blobs))
	for n, b := range h.Blobs {
		list = append(list, BlobInfo{Name: n, Size: int64(len(b))})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out, _ := json.Marshal(list)
	return fill(dst, out)
}

func rawItemImageOpen(id, role []byte) int32 {
	h, denied := enter("item_image_open")
	defer h.mu.Unlock()
	switch {
	case h.Calls["item_image_open"] > itemOpensMax:
		return ErrQuota.Code
	case denied:
		return ErrDenied.Code
	case len(id) == 0 || len(id) > itemIDMax || bytes.ContainsAny(id, `/\.`):
		return ErrInvalid.Code
	}
	f := h.ItemImages[string(id)]
	var img []byte
	switch string(role) {
	case "ai_ref":
		img = f.AIRef
	case "thumb":
		img = f.Thumb
	case "ref":
		img = f.AIRef
		if img == nil {
			img = f.Thumb
		}
	default:
		return ErrInvalid.Code
	}
	switch {
	case len(h.images) >= itemHandlesMax:
		return ErrBusy.Code
	case img == nil:
		return ErrNotFound.Code
	}
	n := h.handle()
	h.images[n] = bytes.NewReader(bytes.Clone(img))
	return n
}

// rawItemImageRead releases the handle at the end, as the host does.
func rawItemImageRead(id int32, dst []byte) int32 {
	// The host checks view:inventory on open only, never on a read, so
	// Deny["item_image_read"] has no effect here either (#4005 review).
	h, _ := enter("item_image_read")
	defer h.mu.Unlock()
	r, ok := h.images[id]
	switch {
	case !ok:
		return ErrNotFound.Code
	case len(dst) == 0:
		return ErrInvalid.Code
	}
	n, _ := r.Read(capRead(dst))
	if n == 0 {
		delete(h.images, id)
	}
	return int32(n)
}

func rawEventPublish(typ, payload []byte) int32 {
	h, denied := enter("event_publish")
	defer h.mu.Unlock()
	switch {
	case len(typ) > 256 || len(payload) > 64<<10:
		return ErrInvalid.Code
	case denied:
		return ErrDenied.Code
	case !eventNameRe.Match(typ) || !bytes.HasPrefix(typ, []byte(h.PluginID+".")):
		return ErrDenied.Code // the till's own-namespace rule (ADR-0121 §2)
	case len(payload) > 0 && !json.Valid(payload):
		return ErrInvalid.Code
	}
	p := json.RawMessage("null")
	if len(payload) > 0 {
		p = bytes.Clone(payload)
	}
	h.Published = append(h.Published, PublishedEvent{Type: string(typ), Payload: p})
	return 0
}

func rawJobProgress(pct uint32, key []byte) int32 {
	h, _ := enter("job_progress")
	defer h.mu.Unlock()
	switch {
	case !h.InJob:
		return ErrNotFound.Code
	case len(key) > 256:
		return ErrInvalid.Code
	}
	if pct > 100 {
		pct = 100
	}
	h.Progress = append(h.Progress, JobReport{Pct: int(pct), Key: string(key)})
	return 0
}

func base64Encode(b []byte) string          { return base64.StdEncoding.EncodeToString(b) }
func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func (h *FakeHost) deviceInfo(op string, val func() []byte, dst []byte) int32 {
	denied := h.Deny[op] || (h.DeviceID == "" && h.DeviceIPs == nil && h.DeviceTimezone == "")
	if denied {
		return ErrDenied.Code
	}
	return fill(dst, val())
}

func rawDeviceIDGet(dst []byte) int32 {
	h, _ := enter("device_id_get")
	defer h.mu.Unlock()
	return h.deviceInfo("device_id_get", func() []byte { return []byte(h.DeviceID) }, dst)
}

func rawDeviceLocalIPsGet(dst []byte) int32 {
	h, _ := enter("device_local_ips_get")
	defer h.mu.Unlock()
	return h.deviceInfo("device_local_ips_get", func() []byte {
		ips := h.DeviceIPs
		if ips == nil {
			ips = []string{}
		}
		b, _ := json.Marshal(ips)
		return b
	}, dst)
}

func rawDeviceTimezoneGet(dst []byte) int32 {
	h, _ := enter("device_timezone_get")
	defer h.mu.Unlock()
	return h.deviceInfo("device_timezone_get", func() []byte { return []byte(h.DeviceTimezone) }, dst)
}
