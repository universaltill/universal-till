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

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Host functions v2 (docs: architecture/wasm-runtime.md). Guests import
// module "ut"; every capability is permission-gated per call through
// CheckPermission, so denials are audited and grants are revocable live.
//
// Buffer ABI: data-returning calls write min(len, dstCap) bytes and return
// the FULL length — a guest seeing len > cap retries with a bigger buffer.
// Negative returns: -1 not found, -2 permission denied, -3 internal error,
// -4 invalid/too large.
const (
	hostErrNotFound = -1
	hostErrDenied   = -2
	hostErrInternal = -3
	hostErrInvalid  = -4
)

const httpResponseCap = 256 << 10 // response body cap (base64-decoded bytes)

// hostState carries the calling plugin's identity into host functions via
// the per-instantiation context: one host-module registration serves every
// plugin, in parallel, without shared mutable state.
type hostState struct {
	pluginID string
	db       *sql.DB
}

type hostStateKey struct{}

func withHostState(ctx context.Context, s *hostState) context.Context {
	return context.WithValue(ctx, hostStateKey{}, s)
}

func stateFrom(ctx context.Context) (*hostState, bool) {
	s, ok := ctx.Value(hostStateKey{}).(*hostState)
	return s, ok && s != nil && s.db != nil
}

// readGuest copies a guest buffer out of module memory.
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
// except plain http to localhost (dev/Ollama). Runs under the module's
// event deadline.
func hostHTTPRequest(ctx context.Context, m api.Module, reqPtr, reqLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	raw, ok := readGuest(m, reqPtr, reqLen)
	if !ok {
		return hostErrInvalid
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
	u, err := url.Parse(req.URL)
	if err != nil || u.Hostname() == "" {
		return hostErrInvalid
	}
	if !hostAllowedScheme(u) {
		return hostErrInvalid
	}
	// Grant the call when the plugin holds either the exact host permission
	// (net:<host>) or the wildcard (net:*). Configurable connectors (ERP
	// webhooks, ADR-0014) don't know their target host until install-time
	// settings, so they declare net:* and are review-gated accordingly.
	if err := CheckPermission(ctx, s.db, s.pluginID, "net:"+u.Hostname()); err != nil {
		if err2 := CheckPermission(ctx, s.db, s.pluginID, "net:*"); err2 != nil {
			return hostErrDenied
		}
	}
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
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		logging.L().Infof("[wasm:%s] http %s %s failed: %v", s.pluginID, req.Method, u.Hostname(), err)
		return hostErrInternal
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, httpResponseCap))
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
	return writeGuest(m, dstPtr, dstCap, out)
}

// hostAllowedScheme: https anywhere the permission allows; plain http only
// to loopback (a self-hosted Ollama or dev service on the till itself).
func hostAllowedScheme(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
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
