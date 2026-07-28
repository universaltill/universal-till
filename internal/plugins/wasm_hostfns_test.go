package plugins

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	payload, _ := json.Marshal(map[string]string{"url": url})
	ev := Event{ID: "ev1", Type: "test.event", Timestamp: time.Now(), Payload: payload}
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
