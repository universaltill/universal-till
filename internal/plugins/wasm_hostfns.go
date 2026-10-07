package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// Host functions v2 (docs: architecture/wasm-runtime.md). Guests import
// module "ut"; every capability is permission-gated per call through
// CheckPermission, so denials are audited and grants are revocable live.
//
// Buffer ABI: data-returning calls write min(len, dstCap) bytes and return
// the FULL length — a guest seeing len > cap retries with a bigger buffer.
// Negative returns: -1 not found (incl. an unknown or closed handle),
// -2 permission denied, -3 internal error (incl. a timed-out read), -4
// invalid/too large, -5 quota exceeded (ADR-0121 §3: e.g. an http:stream
// body over limits.http_body_mb), -6 busy (too many open handles).
const (
	hostErrNotFound = -1
	hostErrDenied   = -2
	hostErrInternal = -3
	hostErrInvalid  = -4
	hostErrQuota    = -5
	hostErrBusy     = -6
)

const httpResponseCap = 256 << 10 // response body cap (base64-decoded bytes)

// validationResponseCap is httpResponseCap for a host the plugin holds
// net:validation:<host> for (ut-docs#3226).
const validationResponseCap = 8 << 20 // 8 MiB — bounds a WASM guest's validation-data fetch (OCSP/CRL/TSA, PAdES trusted-list XML); real-world CRLs and EU trusted-list files run well under this, and the guest's own linear memory is capped at 64 MiB regardless

// hostState carries the calling plugin's identity into host functions via
// the per-instantiation context: one host-module registration serves every
// plugin, in parallel, without shared mutable state.
type hostState struct {
	pluginID string
	db       *sql.DB
	// httpClient is the http_request egress client (tests inject a stubbed
	// resolver/dialer); nil → defaultPluginHTTPClient.
	httpClient *http.Client

	// httpCacheReq/httpCacheResp cache the most recently completed
	// hostHTTPRequest call for THIS event (ut-docs#754). WasmRuntime.HandleEvent
	// creates a fresh hostState per event/module instantiation, so the cache
	// dies with the instance — no explicit expiry, no cross-event or
	// cross-plugin leakage. See hostHTTPRequest for why this exists.
	httpCacheReq  []byte
	httpCacheResp []byte

	// streams are this event's http:stream handles (wasm_httpstream.go) —
	// per event, never process-wide; handleEvent closes them all when the
	// event returns.
	streams httpStreams
	// httpBodyLimit is the http:stream byte cap from the manifest's
	// limits.http_body_mb, resolved once per event (streamLimit).
	limitOnce     sync.Once
	httpBodyLimit int64

	// viewCalls counts this event's view_query calls (viewCallsPerEvent);
	// viewsUsed is the installed manifest's views_used, resolved once per
	// event (wasm_views.go) — it only changes on reinstall, which reloads
	// the module. viewsOK is false when no manifest could be read.
	viewCalls int
	viewsOnce sync.Once
	viewsUsed []string
	viewsOK   bool
}

type hostStateKey struct{}

func withHostState(ctx context.Context, s *hostState) context.Context {
	return context.WithValue(ctx, hostStateKey{}, s)
}

func stateFrom(ctx context.Context) (*hostState, bool) {
	s, ok := ctx.Value(hostStateKey{}).(*hostState)
	return s, ok && s != nil && s.db != nil
}

// readGuest returns a VIEW into guest module memory — not a copy. A caller
// that needs the bytes to outlive this call (e.g. as a cache key) must
// clone them explicitly; the guest is free to overwrite this region before
// its own memory is next read.
func readGuest(m api.Module, ptr, length uint32) ([]byte, bool) {
	if length == 0 {
		return nil, true
	}
	return m.Memory().Read(ptr, length)
}

// writeGuest writes min(len(data), cap) into the guest buffer and returns
// the full length per the buffer ABI.
func writeGuest(m api.Module, ptr, capacity uint32, data []byte) int32 {
	n := uint32(len(data))
	if n > capacity {
		n = capacity
	}
	if n > 0 && !m.Memory().Write(ptr, data[:n]) {
		return hostErrInvalid
	}
	return int32(len(data))
}

// instantiateHostModule registers the "ut" host module on the runtime.
func instantiateHostModule(ctx context.Context, rt wazero.Runtime) error {
	_, err := rt.NewHostModuleBuilder("ut").
		NewFunctionBuilder().WithFunc(hostLogWrite).Export("log_write").
		NewFunctionBuilder().WithFunc(hostStorageGet).Export("storage_get").
		NewFunctionBuilder().WithFunc(hostStorageSet).Export("storage_set").
		NewFunctionBuilder().WithFunc(hostHTTPRequest).Export("http_request").
		NewFunctionBuilder().WithFunc(hostSettingsGet).Export("settings_get").
		NewFunctionBuilder().WithFunc(hostTCPOpen).Export("tcp_open").
		NewFunctionBuilder().WithFunc(hostTCPWrite).Export("tcp_write").
		NewFunctionBuilder().WithFunc(hostTCPRead).Export("tcp_read").
		NewFunctionBuilder().WithFunc(hostTCPClose).Export("tcp_close").
		NewFunctionBuilder().WithFunc(hostImportFileSize).Export("import_file_size").
		NewFunctionBuilder().WithFunc(hostImportFileRead).Export("import_file_read").
		NewFunctionBuilder().WithFunc(hostImportFileClose).Export("import_file_close").
		NewFunctionBuilder().WithFunc(hostHTTPOpen).Export("http_open").
		NewFunctionBuilder().WithFunc(hostHTTPWrite).Export("http_write").
		NewFunctionBuilder().WithFunc(hostHTTPStatus).Export("http_status").
		NewFunctionBuilder().WithFunc(hostHTTPRead).Export("http_read").
		NewFunctionBuilder().WithFunc(hostHTTPClose).Export("http_close").
		NewFunctionBuilder().WithFunc(hostViewQuery).Export("view_query").
		NewFunctionBuilder().WithFunc(hostSecretSet).Export("secret_set").
		Instantiate(ctx)
	return err
}

// hostSettingsGet lets a plugin read one of its OWN declared settings (the
// values a manager enters in the plugin settings editor). This is what makes
// configurable WASM plugins possible — e.g. an ERP connector reading its
// endpoint URL and auth token (ADR-0014). Own-settings only; no cross-plugin
// access, no extra permission. Returns the plain string value; hostErrNotFound
// when the key isn't set.
func hostSettingsGet(ctx context.Context, m api.Module, keyPtr, keyLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	key, ok := readGuest(m, keyPtr, keyLen)
	if !ok || len(key) == 0 {
		return hostErrInvalid
	}
	val, found, err := data.NewPluginRepo(s.db).GetPluginSetting(ctx, s.pluginID, string(key))
	if err != nil {
		return hostErrInternal
	}
	if !found {
		return hostErrNotFound
	}
	// Settings are stored as JSON; unwrap a JSON string so the plugin gets the
	// plain value ("http://host" not "\"http://host\""). Non-string JSON
	// (numbers, objects) is passed through raw.
	out := []byte(val)
	var str string
	if json.Unmarshal(out, &str) == nil {
		out = []byte(str)
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

func hostLogWrite(ctx context.Context, m api.Module, ptr, length uint32) {
	s, ok := stateFrom(ctx)
	if !ok {
		return
	}
	msg, ok := readGuest(m, ptr, length)
	if !ok {
		return
	}
	logging.L().Infof("[wasm:%s] %s", s.pluginID, strings.TrimSpace(string(msg)))
}

func hostStorageGet(ctx context.Context, m api.Module, keyPtr, keyLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	key, ok := readGuest(m, keyPtr, keyLen)
	if !ok || len(key) == 0 {
		return hostErrInvalid
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, "storage"); err != nil {
		return hostErrDenied
	}
	val, err := data.NewPluginRepo(s.db).StorageGet(ctx, s.pluginID, string(key))
	switch {
	case errors.Is(err, data.ErrStorageNotFound):
		return hostErrNotFound
	case err != nil:
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, val)
}

func hostStorageSet(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	key, kok := readGuest(m, keyPtr, keyLen)
	val, vok := readGuest(m, valPtr, valLen)
	if !kok || !vok || len(key) == 0 {
		return hostErrInvalid
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, "storage"); err != nil {
		return hostErrDenied
	}
	err := data.NewPluginRepo(s.db).StorageSet(ctx, s.pluginID, string(key), val)
	switch {
	case errors.Is(err, data.ErrStorageTooLarge):
		return hostErrInvalid
	case err != nil:
		return hostErrInternal
	}
	return 0
}

// hostHTTPRequest performs one outbound HTTP call for the plugin. The URL's
// hostname must be covered by a granted `net:<host>` permission; https only,
// except plain http to localhost (dev/Ollama), and — with http:lan
// (ADR-0121 §2, ut-docs#3156) — plain http to an exactly granted or
// endpoint-setting host on a non-public address (admitHTTPHop). A host named by a
// net:validation:<host> grant (ut-docs#3226) may also be fetched over plain
// http, with validationResponseCap instead of httpResponseCap, and is always
// public-only: it must resolve to a public address, whatever other net:
// grant the plugin holds for it. Runs under the module's event deadline.
//
// Buffer-ABI retry cache (ut-docs#754): per the module's buffer ABI, a
// guest that undersizes dstCap gets back the FULL response length and is
// expected to "call again with a bigger buffer" — passing the identical
// request bytes. Every other buffer-ABI call re-derives its answer
// idempotently on retry (a SQLite read, a file/socket cursor guarded by
// #614's pre-read bounds check); this one does not — without a cache, the
// retry would re-issue the LIVE HTTP request. Harmless for an idempotent
// GET, but a real duplicate side effect for a payment/ERP-connector plugin
// POSTing a charge or order with an undersized response buffer.
//
// The cache is deliberately narrow — populated ONLY when this call's own
// response overflowed the guest's dstCap (the one case the buffer ABI
// itself says "call again"), and cleared the moment a hit is served into a
// buffer big enough to hold the whole thing. Caching every successful call
// unconditionally was tried and rejected during review: it silently
// collapsed two genuinely separate, adequately-buffered calls with
// byte-identical request bytes (a poll loop, a deliberate duplicate
// submission) into one, which is a worse bug than the one this fix exists
// to close. A failed call is never cached at all — retrying a real failure
// is supposed to hit the network again.
func hostHTTPRequest(ctx context.Context, m api.Module, reqPtr, reqLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if httpEgressDemoDenied(s) {
		return hostErrDenied
	}
	raw, ok := readGuest(m, reqPtr, reqLen)
	if !ok {
		return hostErrInvalid
	}
	if s.httpCacheResp != nil && bytes.Equal(s.httpCacheReq, raw) {
		resp := s.httpCacheResp
		if dstCap >= uint32(len(resp)) {
			// This call's buffer is big enough for the whole cached
			// response — the buffer-ABI retry this cache exists for is
			// now complete. Clear it so a LATER call with the same
			// request bytes (not part of this retry) is treated as a
			// fresh, real request instead of silently reusing a stale
			// answer.
			s.httpCacheReq = nil
			s.httpCacheResp = nil
		}
		return writeGuest(m, dstPtr, dstCap, resp)
	}
	var req struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		BodyB64 string            `json:"body_b64"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return hostErrInvalid
	}
	u, grants, code := admitHTTPRequest(ctx, s, req.URL)
	if code != 0 {
		return code
	}
	ctx = withEgressGrants(ctx, grants)
	body, err := base64.StdEncoding.DecodeString(req.BodyB64)
	if err != nil {
		return hostErrInvalid
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return hostErrInvalid
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("User-Agent", "UniversalTill-plugin/"+s.pluginID)
	client := s.httpClient
	if client == nil {
		client = defaultPluginHTTPClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		if errors.Is(err, errEgressDenied) {
			logEgressDenied(s.pluginID, err)
			return hostErrDenied
		}
		// *url.Error's message embeds the full URL (query strings can hold
		// tokens) — log the host and the underlying cause only.
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err
		}
		logging.L().Infof("[wasm:%s] http %s %s failed: %v", s.pluginID, req.Method, u.Hostname(), cause)
		return hostErrInternal
	}
	defer resp.Body.Close()
	respBody, err := readHTTPResponseBody(resp, grants)
	if err != nil {
		return hostErrInternal
	}
	headers := map[string]string{}
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	out, err := json.Marshal(map[string]any{
		"status":   resp.StatusCode,
		"headers":  headers,
		"body_b64": base64.StdEncoding.EncodeToString(respBody),
	})
	if err != nil {
		return hostErrInternal
	}
	if uint32(len(out)) > dstCap {
		// Only cache the overflow case — this call's own buffer didn't fit
		// the response, so per the buffer ABI the guest WILL call again
		// with a bigger buffer, passing the identical request bytes.
		// Key is a COPY of raw: raw is a live view into guest linear
		// memory (readGuest → m.Memory().Read, no copy), which the guest
		// is free to overwrite/reuse before its retry call — holding onto
		// it uncopied would compare against whatever later lands at that
		// address instead of the original request bytes.
		s.httpCacheReq = bytes.Clone(raw)
		s.httpCacheResp = out
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

// httpEgressDemoDenied: the public demo till has no outbound network
// (ADR-0113 §1.6) — every http egress is permission denied, whatever the
// plugin was granted.
func httpEgressDemoDenied(s *hostState) bool {
	if netaccess.Demo() {
		logging.L().Infof("[wasm:%s] http egress denied: demo mode (ADR-0113)", s.pluginID)
		return true
	}
	return false
}

// admitHTTPRequest is the precheck http_request and http_open share: the URL
// must parse with a host, and its first hop must pass admitHTTPHop (scheme
// rule + name check: net:<host>, net:@setting, net:*, net:validation:,
// http:lan). It returns the URL and the request chain's egressGrants seeded
// with that hop's decision — the dialer enforces the IP rule against them,
// redirects add theirs (checkPluginRedirect) — or a hostErr* code, already
// logged.
//
// Configurable connectors (ERP webhooks, ADR-0014) that don't know their
// target host until install-time settings declare net:* (review-gated) or a
// setting-bound grant. net:* reaches PUBLIC addresses only; a LAN/loopback
// target needs the exact grant — enforced at dial time against the resolved
// IP, redirects included (wasm_egress.go, ut-docs#2891). A validation grant
// passes the name check on its own and is never exact, even alongside
// net:<host> for the same host (ut-docs#3226).
func admitHTTPRequest(ctx context.Context, s *hostState, rawURL string) (*url.URL, *egressGrants, int32) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil, nil, hostErrInvalid
	}
	d, code, err := admitHTTPHop(ctx, s, u)
	switch {
	case code == hostErrInvalid:
		logging.L().Infof("[wasm:%s] http egress denied: host %s: scheme %s (https only; plain http only to loopback, a net:validation: host, or an exactly granted LAN host under http:lan)", s.pluginID, u.Hostname(), u.Scheme)
		return nil, nil, code
	case code != 0:
		logEgressDenied(s.pluginID, err)
		return nil, nil, code
	}
	grants := &egressGrants{exact: map[string]bool{}}
	grants.record(u.Hostname(), d)
	return u, grants, 0
}

// readHTTPResponseBody reads resp's body up to the response cap and no
// further: validationResponseCap when the host that actually answered (the
// last redirect hop, resp.Request) is one the chain approved through a
// net:validation: grant (ut-docs#3226), httpResponseCap otherwise.
func readHTTPResponseBody(resp *http.Response, grants *egressGrants) ([]byte, error) {
	limit := int64(httpResponseCap)
	if resp.Request != nil && resp.Request.URL != nil && grants.isValidation(resp.Request.URL.Hostname()) {
		limit = validationResponseCap
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// hostAllowedScheme: https anywhere the permission allows; plain http only
// (http:lan aside — admitHTTPHop) to loopback (a self-hosted Ollama or dev service on the till itself) —
// except that allowPlainPublicHTTP (the host is named by a granted
// net:validation:<host>, ut-docs#3226) allows plain http to that host too.
// That host is public-only at dial time (admitHTTPHop never makes it
// exact), so plain http never reaches a non-public address through this
// exception.
func hostAllowedScheme(u *url.URL, allowPlainPublicHTTP bool) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		if allowPlainPublicHTTP {
			return true
		}
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
}

// logEgressDenied logs a refused request at Info: plugin id, host, reason —
// never the URL or body.
func logEgressDenied(pluginID string, err error) {
	var de *egressDeniedError
	if errors.As(err, &de) {
		logging.L().Infof("[wasm:%s] %s", pluginID, de.Error())
		return
	}
	logging.L().Infof("[wasm:%s] plugin egress denied", pluginID)
}

// pluginHasNetPermission reports whether any granted permission is net:*,
// which widens the module's event deadline for network round-trips.
func pluginHasNetPermission(ctx context.Context, db *sql.DB, pluginID string) bool {
	perms, err := data.NewPluginRepo(db).ListPermissions(ctx, pluginID)
	if err != nil {
		return false
	}
	for _, p := range perms {
		if p.Granted && strings.HasPrefix(p.Permission, "net:") {
			return true
		}
	}
	return false
}
