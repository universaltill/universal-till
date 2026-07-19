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
	"github.com/universaltill/universal-till/internal/data"
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
		{"id": "d4", "type": "set_price", "payload": map[string]any{"item_id": "itm-1", "price_minor": 250}},
		{"id": "d5", "type": "set_price", "payload": map[string]any{"item_id": "itm-1", "price_minor": -1}},
		{"id": "d6", "type": "adjust_stock", "payload": map[string]any{"item_id": "itm-1", "qty_delta": -2.5, "reason": "damaged"}},
		{"id": "d7", "type": "adjust_stock", "payload": map[string]any{"item_id": "itm-1", "qty_delta": 0}},
		{"id": "d8", "type": "rename_item", "payload": map[string]any{"item_id": "itm-1", "name": "Cola Zero"}},
		{"id": "d9", "type": "rename_item", "payload": map[string]any{"item_id": "itm-1", "name": "  "}},
		{"id": "d10", "type": "deactivate_item", "payload": map[string]any{"item_id": "itm-1"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)

	var setKey, setVal, installed string
	var pricedItem string
	var pricedMinor int64
	var adjustedItem, adjustedReason, renamedTo, deactivated string
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
