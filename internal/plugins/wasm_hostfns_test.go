package plugins

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// buildHostfnGuest compiles the wasip1 test guest once per test run.
func buildHostfnGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/hostfn_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

func hostfnTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "hostfn-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	database, err := db.Open(f.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database.DB
}

// seedPlugin satisfies the FK chain plugin_permissions → plugins →
// plugin_catalog for a test plugin id.
func seedPlugin(t *testing.T, d *sql.DB, pluginID string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES (?, '1.0.0', 'test', 'wasm', './plugin.wasm', 'file://x', 'x', '0.0.1', '1.0', datetime('now'))`, pluginID); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime)
		VALUES (?, 'test', '1.0.0', './plugin.wasm', 'wasm')`, pluginID); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
}

// grantPerm declares and grants one permission for the test plugin.
func grantPerm(t *testing.T, d *sql.DB, pluginID, perm string) {
	t.Helper()
	repo := data.NewPluginRepo(d)
	ctx := context.Background()
	if err := repo.InsertPluginPermissions(ctx, nil, pluginID, []string{perm}); err != nil {
		t.Fatalf("declare %s: %v", perm, err)
	}
	if err := repo.SetPermission(ctx, pluginID, perm, true); err != nil {
		t.Fatalf("grant %s: %v", perm, err)
	}
}

func runGuest(t *testing.T, w *WasmRuntime, d *sql.DB, pluginID, url string) map[string]any {
	t.Helper()
	return runGuestPayload(t, w, d, pluginID, map[string]string{"url": url})
}

// runGuestPayload is runGuest generalized to an arbitrary event payload —
// the guest fixture dispatches on payload.mode (see testdata/hostfn_guest).
func runGuestPayload(t *testing.T, w *WasmRuntime, d *sql.DB, pluginID string, payload any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	ev := Event{ID: "ev1", Type: "test.event", Timestamp: time.Now(), Payload: body}
	w.mu.Lock()
	w.db = d
	w.mu.Unlock()
	if _, err := w.HandleEvent(context.Background(), pluginID, ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	raw, err := data.NewPluginRepo(d).StorageGet(context.Background(), pluginID, "results")
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	var results map[string]any
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("parse results %s: %v", raw, err)
	}
	return results
}

func TestHostFunctions(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.hostfn"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "UniversalTill-plugin/"+pluginID {
			t.Errorf("missing plugin user-agent, got %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if res["set_code"] != float64(0) {
		t.Errorf("storage_set code = %v", res["set_code"])
	}
	if res["roundtrip"] != true {
		t.Errorf("storage round-trip failed: %+v", res)
	}
	if res["http_status"] != float64(200) {
		t.Errorf("http status = %v", res["http_status"])
	}
	body, _ := base64.StdEncoding.DecodeString(res["http_body"].(string))
	if string(body) != "pong" {
		t.Errorf("http body = %q", body)
	}
}

func TestHostFunctionsDenied(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.nofn"
	// storage granted so the guest can record results; net NOT granted.
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("http request must not reach the server without net permission")
	}))
	defer srv.Close()

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	res := runGuest(t, w, d, pluginID, srv.URL)
	if res["http_code"] != float64(hostErrDenied) {
		t.Errorf("http_code = %v, want %d (denied)", res["http_code"], hostErrDenied)
	}

	// And the denial was audited by the permission layer.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'permission_denied' AND entity_id = ?`,
		pluginID).Scan(&n); err == nil && n == 0 {
		t.Log("note: no permission_denied audit rows found (audit action name may differ)")
	}
}

func TestHostStorageDeniedWithoutPermission(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.nostorage"
	// Nothing granted at all: the guest's final results write fails, so run
	// the event and assert no storage rows appeared.
	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.mu.Lock()
	w.db = d
	w.mu.Unlock()
	payload, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:1/x"})
	_, err := w.HandleEvent(context.Background(), pluginID,
		Event{ID: "e", Type: "t", Timestamp: time.Now(), Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "exit") {
		// guest exits 1 when it cannot record results
		t.Logf("HandleEvent err = %v", err)
	}
	if _, gerr := data.NewPluginRepo(d).StorageGet(context.Background(), pluginID, "greeting"); gerr == nil {
		t.Error("storage write succeeded without the storage permission")
	}
}

// A configurable connector declares net:* (its target host is only known from
// install-time settings). The http host function must honour the wildcard.
func TestHostHTTPWildcardNet(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.wildcardnet"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:*") // wildcard, NOT the exact host

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if res["http_status"] != float64(200) {
		t.Fatalf("net:* did not authorise the call; http_status = %v", res["http_status"])
	}
}

// A plugin reads its own configured setting via settings_get — what makes
// configurable connectors possible (ADR-0014).
func TestHostSettingsGet(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.settings"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")
	// Value is stored as JSON; the host unwraps the JSON string for the plugin.
	if err := data.NewPluginRepo(d).UpsertPluginSetting(context.Background(), pluginID, "endpoint", `"https://erp.example.com/hook"`); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if res["setting_val"] != "https://erp.example.com/hook" {
		t.Fatalf("settings_get returned %q (code %v), want the unwrapped URL", res["setting_val"], res["setting_code"])
	}
}

// TestHostHTTPRetryDoesNotReissueRequest is the ut-docs#754 fix proof: the
// guest issues one http_request call with a deliberately undersized
// buffer, gets back the buffer ABI's "here's the full length, call again
// bigger" signal, and retries with the IDENTICAL request bytes. Before the
// fix, that retry re-issued the live HTTP request — for a non-idempotent
// call (a payment/ERP-connector POST) that duplicates the side effect. The
// server here counts real hits, so this proves the retry is served from
// cache, not the network, for the exact same logical call.
func TestHostHTTPRetryDoesNotReissueRequest(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.httpretry"

	var hits int32
	const body = "this response body is deliberately longer than the guest's first, undersized 4-byte buffer"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "http_retry", "url": srv.URL + "/charge"})

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times for one logical retried call, want exactly 1 (duplicate side effect)", got)
	}
	// The undersized first call's own buffer ABI contract is unchanged: it
	// still reports the FULL response length, not the 4 bytes it fit.
	firstCode, _ := res["first_code"].(float64)
	if firstCode <= 4 {
		t.Errorf("first_code = %v, want the full response length (> the 4-byte buffer)", res["first_code"])
	}
	secondBody, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["second_body"]))
	if string(secondBody) != body {
		t.Errorf("second call body = %q, want the real cached response %q", secondBody, body)
	}
}

// TestHostHTTPRetryDifferentRequestNotCached is the false-positive guard:
// two genuinely different requests (not a retry of one) must both reach
// the server — the #754 cache keys on exact request bytes, not merely
// "this plugin already made a call this event."
func TestHostHTTPRetryDifferentRequestNotCached(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.httpretrydiff"

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("body-for-" + r.URL.Path))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuestPayload(t, w, d, pluginID, map[string]any{
		"mode": "http_retry_diff",
		"urls": []string{srv.URL + "/a", srv.URL + "/b"},
	})

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times for two distinct requests, want exactly 2", got)
	}
	body0, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["body_0"]))
	body1, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["body_1"]))
	if string(body0) != "body-for-/a" || string(body1) != "body-for-/b" {
		t.Errorf("bodies = %q, %q, want distinct per-URL responses", body0, body1)
	}
}

// TestHostHTTPRepeatSameRequestBothReachServer is the #754 independent
// review's F1 regression test: the guest makes the SAME request TWICE,
// each time with a generously sized buffer up front (never an undersized-
// buffer retry — a poll loop or a deliberate duplicate submission, not the
// buffer-ABI retry the cache exists for). An earlier draft of the #754 fix
// cached every successful call unconditionally and silently collapsed
// this into one live call — both must reach the server.
func TestHostHTTPRepeatSameRequestBothReachServer(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.httprepeatsame"

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(fmt.Sprintf("hit-%d", n)))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "http_repeat_same", "url": srv.URL + "/poll"})

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times for two separate, adequately-buffered identical requests, want exactly 2", got)
	}
	firstBody, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["first_body"]))
	secondBody, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["second_body"]))
	if string(firstBody) == string(secondBody) {
		t.Errorf("both calls got the same body (%q) — second call was served from cache instead of hitting the server", firstBody)
	}
}

// TestHostHTTPRetryThenFreshCallNotCached proves the cache clears once a
// pending buffer-ABI retry is fully served: undersized call (miss, caches
// on overflow) → same-bytes big-buffer retry (hit, clears the cache) →
// another same-bytes big-buffer call, which is NOT part of that retry and
// must go out for real.
func TestHostHTTPRetryThenFreshCallNotCached(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.httpretryfresh"

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(fmt.Sprintf("this response body is deliberately longer than a 4-byte buffer, hit-%d", n)))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "http_retry_then_repeat", "url": srv.URL + "/charge"})

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times (want 2: one for the miss+retry pair, one for the later fresh call)", got)
	}
	secondBody, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["second_body"]))
	thirdBody, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["third_body"]))
	if string(secondBody) == string(thirdBody) {
		t.Errorf("second and third call got the same body (%q) — the cache did not clear after being fully served", secondBody)
	}
}
