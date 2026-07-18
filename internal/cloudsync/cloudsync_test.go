package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	return db
}

// fakeCloud implements the two ADR-0018 endpoints and records what the till
// sent.
type fakeCloud struct {
	mu         sync.Mutex
	syncBodies []map[string]any
	results    []map[string]string
	directives []map[string]any
	snapshots  []map[string]any
}

func (f *fakeCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.syncBodies = append(f.syncBodies, body)
		dirs := f.directives
		f.directives = nil // deliver once, like the real cloud resolves them
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"directives": dirs},
		})
	})
	mux.HandleFunc("/v1/stores/catalog-snapshot", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.snapshots = append(f.snapshots, body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	})
	mux.HandleFunc("/v1/stores/directives/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.results = append(f.results, body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	})
	return mux
}

func testCfg(url string) *config.Config {
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = url
	cfg.Marketplace.StoreID = "store-1"
	cfg.Marketplace.MerchantToken = "tok-1"
	cfg.DBPath = "/nonexistent" // health db_mb simply omitted
	return cfg
}

func TestTickPushesHeartbeatAndAppliesDirectives(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "d1", "type": "set_setting", "payload": map[string]any{"key": "display.osk", "value": "on"}},
		{"id": "d2", "type": "reboot", "payload": map[string]any{}},
		{"id": "d3", "type": "install_plugin", "payload": map[string]any{"listing_id": "lst-1"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)

	var setKey, setVal, installed string
	hooks := Hooks{
		SetSetting: func(ctx context.Context, key, value string) (string, error) {
			setKey, setVal = key, value
			return key + " = " + value, nil
		},
		InstallPlugin: func(ctx context.Context, listingID string) (string, error) {
			installed = listingID
			return "installed x", nil
		},
	}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if setKey != "display.osk" || setVal != "on" || installed != "lst-1" {
		t.Fatalf("hooks not driven: set=%s/%s installed=%s", setKey, setVal, installed)
	}

	// Heartbeat carried this device with role + platform + health.
	if len(cloud.syncBodies) != 1 {
		t.Fatalf("sync calls = %d", len(cloud.syncBodies))
	}
	devs := cloud.syncBodies[0]["devices"].([]any)
	dev := devs[0].(map[string]any)
	if dev["role"] != "primary" || dev["platform"] == "" || dev["health"] == nil {
		t.Fatalf("device report incomplete: %+v", dev)
	}

	// Results: d1 applied, d2 failed (unknown type), d3 applied.
	got := map[string]string{}
	for _, r := range cloud.results {
		got[r["directive_id"]] = r["status"]
	}
	if got["d1"] != "applied" || got["d2"] != "failed" || got["d3"] != "applied" {
		t.Fatalf("results = %+v", cloud.results)
	}
}

func TestTickRolesFollowSettings(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	if err := Tick(ctx, testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	// backoffice wins over replica (a manager device may still LAN-sync).
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('display.mode','backoffice')`); err != nil {
		t.Fatal(err)
	}
	if err := Tick(ctx, testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}

	role := func(i int) string {
		devs := cloud.syncBodies[i]["devices"].([]any)
		return devs[0].(map[string]any)["role"].(string)
	}
	if role(0) != "replica" || role(1) != "backoffice" {
		t.Fatalf("roles = %s, %s", role(0), role(1))
	}
}

func TestSnapshotPushGatedByHash(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Coca-Cola", BasePrice: 120, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000000000011", "it-1", true)
	ctx := context.Background()
	cfg := testCfg(srv.URL)

	if err := Tick(ctx, cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots after first tick = %d, want 1", len(cloud.snapshots))
	}
	items := cloud.snapshots[0]["items"].([]any)
	row := items[0].(map[string]any)
	if row["name"] != "Coca-Cola" || row["barcode"] != "5000000000011" {
		t.Fatalf("snapshot row = %+v", row)
	}

	// Unchanged data → no second push.
	if err := Tick(ctx, cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshot re-pushed without changes (%d)", len(cloud.snapshots))
	}

	// A price change → pushed again.
	if _, err := db.Exec(`UPDATE items SET base_price = 130 WHERE id = 'it-1'`); err != nil {
		t.Fatal(err)
	}
	if err := Tick(ctx, cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick3: %v", err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("snapshot not re-pushed after change (%d)", len(cloud.snapshots))
	}
}

func TestTickSkipsWhenUnregistered(t *testing.T) {
	cfg := &config.Config{} // no endpoint/store/token
	if err := Tick(context.Background(), cfg, testDB(t), Hooks{}); err != nil {
		t.Fatalf("unregistered tick should no-op, got %v", err)
	}
}
