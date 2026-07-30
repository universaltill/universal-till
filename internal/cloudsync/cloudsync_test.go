package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// openMigratedDB gives a test a real, fully migrated schema — same helper
// shape as internal/data's own (unexported there, so duplicated here).
func openMigratedDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

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
		{"id": "d4", "type": "set_price", "payload": map[string]any{"item_id": "itm-1", "price_minor": 250}},
		{"id": "d5", "type": "set_price", "payload": map[string]any{"item_id": "itm-1", "price_minor": -1}},
		{"id": "d6", "type": "adjust_stock", "payload": map[string]any{"item_id": "itm-1", "qty_delta": -2.5, "reason": "damaged"}},
		{"id": "d7", "type": "adjust_stock", "payload": map[string]any{"item_id": "itm-1", "qty_delta": 0}},
		{"id": "d8", "type": "rename_item", "payload": map[string]any{"item_id": "itm-1", "name": "Cola Zero"}},
		{"id": "d9", "type": "rename_item", "payload": map[string]any{"item_id": "itm-1", "name": "  "}},
		{"id": "d10", "type": "deactivate_item", "payload": map[string]any{"item_id": "itm-1"}},
		{"id": "d11", "type": "create_item", "payload": map[string]any{"name": "Fanta", "price_minor": 99, "barcode": "500999"}},
		{"id": "d12", "type": "create_item", "payload": map[string]any{"name": "", "price_minor": 99}},
		{"id": "d13", "type": "add_barcode", "payload": map[string]any{"item_id": "itm-1", "barcode": "500777"}},
		{"id": "d14", "type": "add_barcode", "payload": map[string]any{"item_id": "itm-1", "barcode": ""}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)

	var setKey, setVal, installed string
	var pricedItem string
	var pricedMinor int64
	var adjustedItem, adjustedReason, renamedTo, deactivated, created, barcoded string
	var adjustedDelta float64
	hooks := Hooks{
		SetPrice: func(ctx context.Context, itemID string, priceMinor int64) (string, error) {
			pricedItem, pricedMinor = itemID, priceMinor
			return "price set", nil
		},
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			adjustedItem, adjustedDelta, adjustedReason = itemID, delta, reason
			return "adjusted", nil
		},
		RenameItem: func(ctx context.Context, itemID, name string) (string, error) {
			renamedTo = name
			return "renamed", nil
		},
		DeactivateItem: func(ctx context.Context, itemID string) (string, error) {
			deactivated = itemID
			return "deactivated", nil
		},
		CreateItem: func(ctx context.Context, name string, priceMinor int64, barcode string) (string, error) {
			created = name + "/" + barcode
			return "created", nil
		},
		AddBarcode: func(ctx context.Context, itemID, barcode string) (string, error) {
			barcoded = itemID + "/" + barcode
			return "attached", nil
		},
		SetSetting: func(ctx context.Context, key, value string) (string, error) {
			setKey, setVal = key, value
			return key + " = " + value, nil
		},
		InstallPlugin: func(ctx context.Context, listingID string) (string, error) {
			installed = listingID
			return "installed x", nil
		},
		DeviceExtra: func(ctx context.Context) map[string]any {
			return map[string]any{
				"theme": "monarch",
				// A colliding key must never clobber a fixed report field.
				"role": "evil",
			}
		},
	}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if setKey != "display.osk" || setVal != "on" || installed != "lst-1" {
		t.Fatalf("hooks not driven: set=%s/%s installed=%s", setKey, setVal, installed)
	}
	if pricedItem != "itm-1" || pricedMinor != 250 {
		t.Fatalf("set_price hook not driven: %s/%d", pricedItem, pricedMinor)
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
	// DeviceExtra fields ride along; fixed fields win on collision.
	if dev["theme"] != "monarch" {
		t.Fatalf("device extra missing: %+v", dev)
	}

	// Results: d1 applied, d2 failed (unknown type), d3 applied.
	got := map[string]string{}
	for _, r := range cloud.results {
		got[r["directive_id"]] = r["status"]
	}
	if got["d1"] != "applied" || got["d2"] != "failed" || got["d3"] != "applied" {
		t.Fatalf("results = %+v", cloud.results)
	}
	// d4 valid price applied; d5 negative price rejected without the hook.
	if got["d4"] != "applied" || got["d5"] != "failed" {
		t.Fatalf("price results = %+v", cloud.results)
	}
	// d6 stock adjustment applied with reason; d7 zero delta rejected.
	if got["d6"] != "applied" || got["d7"] != "failed" {
		t.Fatalf("adjust results = %+v", cloud.results)
	}
	if adjustedItem != "itm-1" || adjustedDelta != -2.5 || adjustedReason != "damaged" {
		t.Fatalf("adjust hook: %s/%g/%s", adjustedItem, adjustedDelta, adjustedReason)
	}
	// d8 rename applied; d9 blank name rejected pre-hook (str() trims).
	if got["d8"] != "applied" || got["d9"] != "failed" || renamedTo != "Cola Zero" {
		t.Fatalf("rename results = %+v (renamed %q)", cloud.results, renamedTo)
	}
	if got["d10"] != "applied" || deactivated != "itm-1" {
		t.Fatalf("deactivate: %+v (%q)", cloud.results, deactivated)
	}
	// d11 create applied with barcode; d12 blank name rejected pre-hook.
	if got["d11"] != "applied" || got["d12"] != "failed" || created != "Fanta/500999" {
		t.Fatalf("create: %+v (%q)", cloud.results, created)
	}
	// d13 barcode attached; d14 blank barcode rejected pre-hook.
	if got["d13"] != "applied" || got["d14"] != "failed" || barcoded != "itm-1/500777" {
		t.Fatalf("barcode: %+v (%q)", cloud.results, barcoded)
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
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var-1", ItemID: "it-1", SKU: "SKU1-L", Name: "1.5L", Price: 210, IsActive: true})
	testsupport.SeedVariantBarcode(t, db, "5000000000028", "var-1", true)
	ctx := context.Background()
	cfg := testCfg(srv.URL)

	if err := Tick(ctx, cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots after first tick = %d, want 1", len(cloud.snapshots))
	}
	items := cloud.snapshots[0]["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("snapshot rows = %d, want item + variant", len(items))
	}
	row := items[0].(map[string]any)
	if row["name"] != "Coca-Cola" || row["barcode"] != "5000000000011" {
		t.Fatalf("snapshot row = %+v", row)
	}
	// The variant rides under its parent: composed name, own price/barcode,
	// and NO qty (stock is item-level; repeating it would double-count).
	vrow := items[1].(map[string]any)
	if vrow["name"] != "Coca-Cola — 1.5L" || vrow["barcode"] != "5000000000028" || vrow["price_minor"] != float64(210) {
		t.Fatalf("variant row = %+v", vrow)
	}
	if _, has := vrow["qty"]; has {
		t.Fatalf("variant row must not carry qty: %+v", vrow)
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

// SetItemPrice via the hook path must reach variants too: the snapshot lists
// variant rows, so a remote price edit may target a variant id.
func TestSetItemPriceReachesVariants(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-9", SKU: "S9", Name: "Tea", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var-9", ItemID: "it-9", SKU: "S9-L", Name: "Large", Price: 150, IsActive: true})

	repo := data.NewCatalogRepo(db)
	ctx := context.Background()
	if err := repo.SetItemPrice(ctx, "it-9", 110); err != nil {
		t.Fatalf("item price: %v", err)
	}
	if err := repo.SetItemPrice(ctx, "var-9", 160); err != nil {
		t.Fatalf("variant price: %v", err)
	}
	if err := repo.SetItemPrice(ctx, "nope", 1); err == nil {
		t.Fatal("want error for unknown id")
	}
	var base, vprice int64
	if err := db.QueryRow(`SELECT base_price FROM items WHERE id='it-9'`).Scan(&base); err != nil || base != 110 {
		t.Fatalf("base = %d, %v", base, err)
	}
	if err := db.QueryRow(`SELECT price FROM item_variants WHERE id='var-9'`).Scan(&vprice); err != nil || vprice != 160 {
		t.Fatalf("variant = %d, %v", vprice, err)
	}
}

// apply() with no hooks configured must fail cleanly (not panic) for every
// directive type that install_plugin/set_setting's existing test coverage
// doesn't already exercise — remove_plugin was never even given a directive
// in the big TestTickPushesHeartbeatAndAppliesDirectives table, and every
// other type's own "hook is nil" branch was untested there too (that table
// always wired every hook).
func TestApplyMissingHooksFailCleanly(t *testing.T) {
	cases := []directive{
		{ID: "x1", Type: "remove_plugin", Payload: map[string]any{"plugin_id": "p1"}},
		{ID: "x2", Type: "set_price", Payload: map[string]any{"item_id": "i1", "price_minor": 100}},
		{ID: "x3", Type: "adjust_stock", Payload: map[string]any{"item_id": "i1", "qty_delta": 1}},
		{ID: "x4", Type: "rename_item", Payload: map[string]any{"item_id": "i1", "name": "n"}},
		{ID: "x5", Type: "deactivate_item", Payload: map[string]any{"item_id": "i1"}},
		{ID: "x6", Type: "create_item", Payload: map[string]any{"name": "n", "price_minor": 100}},
		{ID: "x7", Type: "add_barcode", Payload: map[string]any{"item_id": "i1", "barcode": "b"}},
	}
	for _, d := range cases {
		status, msg := apply(context.Background(), d, Hooks{})
		if status != "failed" {
			t.Fatalf("%s: status = %q, want failed (msg %q)", d.Type, status, msg)
		}
	}
}

// set_setting/install_plugin's own nil-hook branches, and their "hook is
// present but the required field is blank" branches — the giant fixture
// table always wired both hooks, so neither nil-hook path ever ran, and it
// never sent an install_plugin directive with an empty listing_id.
func TestApplySetSettingAndInstallPluginGaps(t *testing.T) {
	status, _ := apply(context.Background(), directive{Type: "set_setting", Payload: map[string]any{"key": "k", "value": "v"}}, Hooks{})
	if status != "failed" {
		t.Fatalf("set_setting nil hook: status=%q", status)
	}
	status, _ = apply(context.Background(), directive{Type: "install_plugin", Payload: map[string]any{"listing_id": "l1"}}, Hooks{})
	if status != "failed" {
		t.Fatalf("install_plugin nil hook: status=%q", status)
	}

	hooks := Hooks{
		SetSetting:    func(ctx context.Context, key, value string) (string, error) { return "ok", nil },
		InstallPlugin: func(ctx context.Context, listingID string) (string, error) { return "ok", nil },
	}
	status, _ = apply(context.Background(), directive{Type: "set_setting", Payload: map[string]any{"value": "v"}}, hooks)
	if status != "failed" {
		t.Fatalf("set_setting empty key: status=%q", status)
	}
	status, _ = apply(context.Background(), directive{Type: "install_plugin", Payload: map[string]any{}}, hooks)
	if status != "failed" {
		t.Fatalf("install_plugin empty listing_id: status=%q", status)
	}
}

// Every hook-present-but-item_id-blank branch: the giant fixture table only
// ever sent these directives with a real item_id (it varied the OTHER
// field — price, delta, name — to test rejection), so this specific
// short-circuit was never hit for any of these five types.
func TestApplyEmptyItemIDWithHookPresent(t *testing.T) {
	hooks := Hooks{
		SetPrice: func(ctx context.Context, itemID string, priceMinor int64) (string, error) { return "ok", nil },
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			return "ok", nil
		},
		RenameItem:     func(ctx context.Context, itemID, name string) (string, error) { return "ok", nil },
		DeactivateItem: func(ctx context.Context, itemID string) (string, error) { return "ok", nil },
		AddBarcode:     func(ctx context.Context, itemID, barcode string) (string, error) { return "ok", nil },
	}
	cases := []directive{
		{Type: "set_price", Payload: map[string]any{"price_minor": float64(100)}},
		{Type: "adjust_stock", Payload: map[string]any{"qty_delta": float64(1)}},
		{Type: "rename_item", Payload: map[string]any{"name": "n"}},
		{Type: "deactivate_item", Payload: map[string]any{}},
		{Type: "add_barcode", Payload: map[string]any{"barcode": "b"}},
	}
	for _, d := range cases {
		if status, msg := apply(context.Background(), d, hooks); status != "failed" {
			t.Fatalf("%s with blank item_id: status=%q msg=%q", d.Type, status, msg)
		}
	}
}

// num()/fnum()'s final fallback (`return 0, false`) fires when the payload
// key is missing entirely or holds a non-numeric type (not just an
// unparseable string) — untested by every existing case, which always sends
// either a valid float64 or a numeric-looking string.
func TestApplyMissingNumericKeyRejected(t *testing.T) {
	hooks := Hooks{
		SetPrice: func(ctx context.Context, itemID string, priceMinor int64) (string, error) { return "ok", nil },
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			return "ok", nil
		},
	}
	status, _ := apply(context.Background(), directive{Type: "set_price", Payload: map[string]any{"item_id": "i1"}}, hooks)
	if status != "failed" {
		t.Fatalf("missing price_minor key: status=%q", status)
	}
	status, _ = apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{"item_id": "i1", "qty_delta": true}}, hooks)
	if status != "failed" {
		t.Fatalf("bool-typed qty_delta: status=%q", status)
	}
}

// A postResult delivery failure must not fail Tick itself — the directive
// stays "pending" on the cloud and the next tick's re-apply/re-report is
// exactly how this is meant to recover (see Tick's own comment).
func TestTickToleratesPostResultFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"directives": []map[string]any{
				{"id": "d1", "type": "set_setting", "payload": map[string]any{"key": "k", "value": "v"}},
			}},
		})
	})
	mux.HandleFunc("/v1/stores/directives/result", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hooks := Hooks{SetSetting: func(ctx context.Context, key, value string) (string, error) { return "set", nil }}
	if err := Tick(context.Background(), testCfg(srv.URL), testDB(t), hooks); err != nil {
		t.Fatalf("tick should tolerate a postResult failure, got %v", err)
	}
}

// pushSnapshotIfChanged's own upload failing (as opposed to the repo-read
// failures above) must surface as a real error too.
func TestPushSnapshotIfChangedUploadFails(t *testing.T) {
	d := openMigratedDB(t, "cloudsync-upload-fail.db")
	if _, err := d.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('it-1','SKU1','Coke',100,1)`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), d.DB); err == nil {
		t.Fatal("want error when the catalog-snapshot upload fails")
	}
}

// remove_plugin's own success path and its missing-plugin_id rejection — the
// only directive type the original apply() test table never included at all.
func TestApplyRemovePluginSuccess(t *testing.T) {
	var removed string
	hooks := Hooks{RemovePlugin: func(ctx context.Context, pluginID string) (string, error) {
		removed = pluginID
		return "removed x", nil
	}}
	status, msg := apply(context.Background(), directive{ID: "r1", Type: "remove_plugin", Payload: map[string]any{"plugin_id": "p-9"}}, hooks)
	if status != "applied" || msg != "removed x" || removed != "p-9" {
		t.Fatalf("remove_plugin success: status=%q msg=%q removed=%q", status, msg, removed)
	}
	status, msg = apply(context.Background(), directive{ID: "r2", Type: "remove_plugin", Payload: map[string]any{}}, hooks)
	if status != "failed" {
		t.Fatalf("remove_plugin missing id: status=%q msg=%q", status, msg)
	}
}

// A hook returning an error must surface as a "failed" result carrying the
// error text — untested by every existing case (all test hooks return nil).
func TestApplyHookErrorPropagates(t *testing.T) {
	hooks := Hooks{SetSetting: func(ctx context.Context, key, value string) (string, error) {
		return "", errors.New("disk full")
	}}
	status, msg := apply(context.Background(), directive{ID: "e1", Type: "set_setting", Payload: map[string]any{"key": "k", "value": "v"}}, hooks)
	if status != "failed" || msg != "disk full" {
		t.Fatalf("status=%q msg=%q, want failed/disk full", status, msg)
	}
}

// An applied hook that returns an empty message must default to "done" — the
// cloud's result column should never show a blank success.
func TestApplyDefaultsMessageWhenHookReturnsEmpty(t *testing.T) {
	hooks := Hooks{SetSetting: func(ctx context.Context, key, value string) (string, error) {
		return "", nil
	}}
	status, msg := apply(context.Background(), directive{ID: "e2", Type: "set_setting", Payload: map[string]any{"key": "k", "value": "v"}}, hooks)
	if status != "applied" || msg != "done" {
		t.Fatalf("status=%q msg=%q, want applied/done", status, msg)
	}
}

// JSON numbers normally decode as float64, but num()/fnum() also tolerate a
// string-encoded number — untested by every existing directive fixture,
// which only ever sends float64 payload values.
func TestApplyAcceptsStringFormNumbers(t *testing.T) {
	var pricedMinor int64
	var delta float64
	hooks := Hooks{
		SetPrice: func(ctx context.Context, itemID string, priceMinor int64) (string, error) {
			pricedMinor = priceMinor
			return "ok", nil
		},
		AdjustStock: func(ctx context.Context, itemID string, d float64, reason string) (string, error) {
			delta = d
			return "ok", nil
		},
	}
	status, _ := apply(context.Background(), directive{ID: "s1", Type: "set_price", Payload: map[string]any{"item_id": "i1", "price_minor": "250"}}, hooks)
	if status != "applied" || pricedMinor != 250 {
		t.Fatalf("string price_minor: status=%q priced=%d", status, pricedMinor)
	}
	status, _ = apply(context.Background(), directive{ID: "s2", Type: "adjust_stock", Payload: map[string]any{"item_id": "i1", "qty_delta": "-2.5"}}, hooks)
	if status != "applied" || delta != -2.5 {
		t.Fatalf("string qty_delta: status=%q delta=%g", status, delta)
	}
	// A non-numeric string must be rejected, not silently parsed as 0.
	status, _ = apply(context.Background(), directive{ID: "s3", Type: "set_price", Payload: map[string]any{"item_id": "i1", "price_minor": "not-a-number"}}, hooks)
	if status != "failed" {
		t.Fatalf("garbage price_minor string: status=%q, want failed", status)
	}
}

// pushSync's os.Stat(cfg.DBPath) success branch — every other test points
// DBPath at "/nonexistent" specifically to skip it.
func TestPushSyncReportsDBSize(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)

	f, err := os.CreateTemp(t.TempDir(), "fake.db")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, err := f.Write(make([]byte, 2<<20)); err != nil { // 2 MiB
		t.Fatalf("write: %v", err)
	}
	f.Close()

	cfg := testCfg(srv.URL)
	cfg.DBPath = f.Name()
	if err := Tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	devs := cloud.syncBodies[0]["devices"].([]any)
	health := devs[0].(map[string]any)["health"].(map[string]any)
	if health["db_mb"] != float64(2) {
		t.Fatalf("db_mb = %v, want 2", health["db_mb"])
	}
}

// A non-200 /v1/stores/sync response must surface as a real error out of
// Tick, not be swallowed — untested by every existing test, which always
// hits a 200.
func TestTickPropagatesSyncFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := Tick(context.Background(), testCfg(srv.URL), testDB(t), Hooks{})
	if err == nil {
		t.Fatal("want error on 500 sync response")
	}
}

// A malformed JSON body from /v1/stores/sync must surface as a decode error.
func TestPushSyncDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	err := Tick(context.Background(), testCfg(srv.URL), testDB(t), Hooks{})
	if err == nil {
		t.Fatal("want decode error on malformed sync response")
	}
}

// ListItems/ItemBarcodes failing must surface as a real error from
// pushSnapshotIfChanged, not panic or silently push an empty snapshot.
func TestPushSnapshotIfChangedRepoErrors(t *testing.T) {
	cfg := testCfg("http://unused.example")

	itemsDB := testsupport.NewCatalogTestDB(t)
	if _, err := itemsDB.Exec(`DROP TABLE items`); err != nil {
		t.Fatal(err)
	}
	if _, err := itemsDB.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(context.Background(), cfg, itemsDB); err == nil {
		t.Fatal("want error when items table is gone")
	}

	barcodesDB := testsupport.NewCatalogTestDB(t)
	if _, err := barcodesDB.Exec(`DROP TABLE item_barcodes`); err != nil {
		t.Fatal(err)
	}
	if _, err := barcodesDB.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, barcodesDB, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Coke", BasePrice: 100, IsActive: true})
	if err := pushSnapshotIfChanged(context.Background(), cfg, barcodesDB); err == nil {
		t.Fatal("want error when item_barcodes table is gone")
	}
}

// A real stock quantity must actually reach the snapshot's "qty" field — the
// existing snapshot test never seeds an inventory row (and the shared test
// schema has no stock_locations table at all, which ListStockLevels' query
// LEFT JOINs — so that query silently errors out (the err is never even
// logged), and this success path had never actually run for real. Per the
// tester skill's own rule for this exact shape ("use a real fully-migrated
// database instead of a partial schema that might drift from production"),
// this uses a real migrated DB rather than patching the minimal schema.
func TestPushSnapshotIfChangedIncludesRealStockQty(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")

	// Inserted directly (not via testsupport.SeedItem) so tax_code_id is a
	// real NULL rather than ""  — the real migrated schema FK-enforces it,
	// unlike the minimal test schema SeedItem is normally used against.
	if _, err := d.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('it-1','SKU1','Coke',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1','it-1','loc_main', 7.5)`); err != nil {
		t.Fatal(err)
	}

	if err := Tick(context.Background(), testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cloud.snapshots))
	}
	// The real migration seeds its own demo catalog alongside "it-1", so find
	// this test's own row rather than assuming it's first.
	var row map[string]any
	for _, r := range cloud.snapshots[0]["items"].([]any) {
		m := r.(map[string]any)
		if m["id"] == "it-1" {
			row = m
			break
		}
	}
	if row == nil {
		t.Fatal("it-1 not present in snapshot")
	}
	if row["qty"] != 7.5 {
		t.Fatalf("qty = %v, want 7.5", row["qty"])
	}
}

// waitGoroutineExit proves Start()'s spawned goroutine has actually
// returned — not inferred from "no more observed HTTP calls" (which stops
// short of the goroutine's synchronous post-tick work, e.g.
// uploadPendingIssueReports's filesystem read, and doesn't distinguish a
// genuine return from a hung loop that just happens to be between network
// calls). Start() has no join/done signal (a real gap, same class as the
// standing "app.Run doesn't join its background services" queue item — out
// of scope to fix here), so this is the direct proxy: sample
// runtime.NumGoroutine() back down to (at most) its pre-Start baseline.
// This is what actually catches a goroutine leak — deliberately breaking
// either of Start()'s two `case <-ctx.Done(): return` branches leaves the
// goroutine permanently blocked (spinning or parked on a never-firing
// timer), and this fails loudly instead of passing on a stale HTTP-call
// count.
//
// Known remaining boundary (not fully closed by this function, and not
// closeable from a test at all without Start() itself gaining a real
// join signal): confirmed under `go test -race -shuffle=on` that
// issue_reports_test.go's plain `issuereport.PendingDir = ...` writes can
// still race a Start-test goroutine's read of that same var, even after
// this function has observed the goroutine's exit. That's not a timing
// margin this function could widen — Go's race detector tracks actual
// synchronization primitives (channels, mutexes, WaitGroups), not
// wall-clock proof-of-quiescence from polling runtime.NumGoroutine(), so
// no amount of waiting here creates the happens-before edge the detector
// wants. Confirmed clean under plain `-race` (no shuffle, this package's
// and this repo's CI's actual test invocation) at -count=10. Logged as a
// follow-up on the "app.Run doesn't join its background services" item
// rather than fixed here — the real fix is the same one that item already
// needs (a done-channel/WaitGroup on Start()), and duplicating that
// architecture ad hoc in this batch would conflict with it landing there.
func waitGoroutineExit(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		// The shared package-level httpClient keeps idle keep-alive
		// connections (and their read-loop goroutines) around well past
		// when a request completes — real, harmless, and unrelated to
		// Start()'s own goroutine, but indistinguishable from a leak by
		// goroutine count alone unless closed explicitly first.
		httpClient.CloseIdleConnections()
		if n := runtime.NumGoroutine(); n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count still %d after cancel (baseline %d) — Start()'s goroutine leaked", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Start()'s background loop: fires shortly after boot, ticks on its own
// interval, and stops cleanly when its context is cancelled. Uses the
// package's own test-overridable firstDelay/tickInterval (see their comment)
// so this doesn't take real wall-clock minutes.
func TestStartRunsTickLoopAndStopsOnCancel(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	firstDelayNS.Store(int64(5 * time.Millisecond))
	tickIntervalNS.Store(int64(5 * time.Millisecond))

	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	count := func() int { cloud.mu.Lock(); defer cloud.mu.Unlock(); return len(cloud.syncBodies) }
	httpClient.CloseIdleConnections()
	baseline := runtime.NumGoroutine()

	Start(ctx, testCfg(srv.URL), db, Hooks{})

	deadline := time.Now().Add(2 * time.Second)
	for count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d ticks after 2s, want at least 2", count())
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	stoppedAt := count()
	waitGoroutineExit(t, baseline) // proves the goroutine actually returned before restoring the vars below
	firstDelayNS.Store(origFirst)
	tickIntervalNS.Store(origTick)
	if afterCancel := count(); afterCancel > stoppedAt+1 {
		// allow one in-flight tick to land right after cancel; anything more
		// means the loop kept running past ctx.Done().
		t.Fatalf("ticks kept growing after cancel: %d -> %d", stoppedAt, afterCancel)
	}
}

// Cancelling before the first tick ever fires must return the goroutine via
// its OUTER select's ctx.Done() case, not the loop's inner one — a longer
// firstDelay than the cancellation makes that the only reachable path.
func TestStartStopsBeforeFirstTickWhenCancelledEarly(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	firstDelayNS.Store(int64(time.Hour)) // long enough it will never fire in this test
	tickIntervalNS.Store(int64(time.Millisecond))

	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t) // created before baseline: sql.Open spawns a permanent
	// background connectionOpener goroutine for the DB's lifetime — capturing
	// baseline before this would count it as a false "leak" from Start().
	ctx, cancel := context.WithCancel(context.Background())
	count := func() int { cloud.mu.Lock(); defer cloud.mu.Unlock(); return len(cloud.syncBodies) }
	httpClient.CloseIdleConnections()
	baseline := runtime.NumGoroutine()

	Start(ctx, testCfg(srv.URL), db, Hooks{})
	cancel()
	waitGoroutineExit(t, baseline)
	firstDelayNS.Store(origFirst)
	tickIntervalNS.Store(origTick)

	if n := count(); n != 0 {
		t.Fatalf("tick fired %d times despite being cancelled before firstDelay elapsed", n)
	}
}

// A Tick error inside the loop (not the pre-first-tick wait) must be
// tolerated — logged, not fatal to the goroutine, and the loop keeps ticking.
func TestStartToleratesTickFailureAndKeepsLooping(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	firstDelayNS.Store(int64(2 * time.Millisecond))
	tickIntervalNS.Store(int64(2 * time.Millisecond))

	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError) // first tick: Tick() returns an error
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	}))
	defer srv.Close()
	db := testDB(t) // created before baseline, same reason as the test above
	ctx, cancel := context.WithCancel(context.Background())
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }
	httpClient.CloseIdleConnections()
	baseline := runtime.NumGoroutine()

	Start(ctx, testCfg(srv.URL), db, Hooks{})

	deadline := time.Now().Add(2 * time.Second)
	for count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d calls after 2s; loop did not survive the first tick's failure", count())
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	waitGoroutineExit(t, baseline)
	firstDelayNS.Store(origFirst)
	tickIntervalNS.Store(origTick)
}

// The real create path is idempotent on retry and refuses a taken barcode.
func TestCloudCreateItemIdempotency(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	if id, ok, _ := repo.FindActiveItemByName(ctx, "Sprite"); ok || id != "" {
		t.Fatal("unexpected pre-existing item")
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-s", SKU: "S", Name: "Sprite", BasePrice: 100, IsActive: true})
	if _, ok, err := repo.FindActiveItemByName(ctx, "Sprite"); err != nil || !ok {
		t.Fatalf("find by name: ok=%v err=%v", ok, err)
	}
}
