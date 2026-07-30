package data

// Coverage batch 8 — POSRepo inventory/stock group:
// AggregateInventory, CheckNegativeInventory, RecordNegativeInventoryOverride,
// GetLowStockItems, ListStockLocations, ListStockLevels, CurrentQty.
//
// The init migration seeds demo data: 3 stock locations (loc_back "Back
// Store", loc_main "Main Store", loc_wh "Warehouse"), ~50 items and ~62
// inventory rows — but no seeded item has reorder_level > 0, so the
// low-stock queries start empty. Tests below either assert against the
// known seed (locations) or use "b8-" prefixed rows and filter to them.

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openB8InvDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "batch8.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

// seedB8Item inserts an item (satisfying NOT NULL base_price) with a reorder level.
func seedB8Item(t *testing.T, d *db.DB, id, sku, name string, reorder int, active int) {
	t.Helper()
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, reorder_level, is_active) VALUES (?,?,?,100,?,?)`,
		id, sku, name, reorder, active)
}

// seedB8Inventory inserts an item-level inventory row at a location.
func seedB8Inventory(t *testing.T, d *db.DB, itemID, locationID string, qty float64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES (?,?,NULL,?,?)`,
		"inv-"+itemID+"-"+locationID, itemID, locationID, qty)
}

// seedB8Variant inserts a parent item + variant and a variant-level inventory row.
func seedB8Variant(t *testing.T, d *db.DB, itemID, variantID, locationID string, qty float64) {
	t.Helper()
	seedB8Item(t, d, itemID, "sku-"+itemID, "B8 Parent "+itemID, 0, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES (?,?,?,150)`,
		variantID, itemID, "330ml")
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES (?,NULL,?,?,?)`,
		"inv-"+variantID, variantID, locationID, qty)
}

func b8Tx(t *testing.T, d *db.DB) *sql.Tx {
	t.Helper()
	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func TestAggregateInventory_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Validation errors — each mutually exclusive precondition.
	if _, err := repo.AggregateInventory(ctx, nil, "", "b8-i1", ""); err == nil || !strings.Contains(err.Error(), "locationID required") {
		t.Fatalf("empty location: want locationID error, got %v", err)
	}
	if _, err := repo.AggregateInventory(ctx, nil, "loc_main", "", ""); err == nil || !strings.Contains(err.Error(), "itemID or variantID required") {
		t.Fatalf("no item/variant: want required error, got %v", err)
	}
	if _, err := repo.AggregateInventory(ctx, nil, "loc_main", "b8-i1", "b8-v1"); err == nil || !strings.Contains(err.Error(), "cannot specify both") {
		t.Fatalf("both item+variant: want both error, got %v", err)
	}

	// Item stocked at two locations: quantity is per-location, never summed.
	seedB8Item(t, d, "b8-agg", "b8-agg-sku", "B8 Agg", 0, 1)
	seedB8Inventory(t, d, "b8-agg", "loc_main", 7.5)
	seedB8Inventory(t, d, "b8-agg", "loc_back", 4)

	if qty, err := repo.AggregateInventory(ctx, nil, "loc_main", "b8-agg", ""); err != nil || qty != 7.5 {
		t.Fatalf("loc_main qty: got %v err=%v, want 7.5", qty, err)
	}
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_back", "b8-agg", ""); err != nil || qty != 4 {
		t.Fatalf("loc_back qty: got %v err=%v, want 4", qty, err)
	}
	// Third location with no row: 0 with nil error (ErrNoRows swallowed).
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_wh", "b8-agg", ""); err != nil || qty != 0 {
		t.Fatalf("no-row location: got %v err=%v, want 0,<nil>", qty, err)
	}
	// Unknown item entirely: also 0, nil.
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_main", "b8-ghost", ""); err != nil || qty != 0 {
		t.Fatalf("unknown item: got %v err=%v, want 0,<nil>", qty, err)
	}

	// Variant-level row: matched via variantID, and never via the parent item id.
	seedB8Variant(t, d, "b8-aggp", "b8-aggv", "loc_main", 3)
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_main", "", "b8-aggv"); err != nil || qty != 3 {
		t.Fatalf("variant qty: got %v err=%v, want 3", qty, err)
	}
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_main", "b8-aggp", ""); err != nil || qty != 0 {
		t.Fatalf("parent item must not see variant row: got %v err=%v, want 0", qty, err)
	}
	// An item id passed in the variant slot must not match an item-level row.
	if qty, err := repo.AggregateInventory(ctx, nil, "loc_main", "", "b8-agg"); err != nil || qty != 0 {
		t.Fatalf("item id in variant slot must not match: got %v err=%v, want 0", qty, err)
	}

	// Works inside a transaction, reading uncommitted writes.
	tx := b8Tx(t, d)
	if _, err := tx.Exec(`UPDATE inventory SET quantity = 9 WHERE item_id = 'b8-agg' AND location_id = 'loc_main'`); err != nil {
		t.Fatalf("tx update: %v", err)
	}
	if qty, err := repo.AggregateInventory(ctx, tx, "loc_main", "b8-agg", ""); err != nil || qty != 9 {
		t.Fatalf("in-tx qty: got %v err=%v, want 9", qty, err)
	}
}

func TestCheckNegativeInventory_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Zero/negative requests are a no-op check — even without a transaction.
	if err := repo.CheckNegativeInventory(ctx, nil, "loc_main", "b8-i", "", 0); err != nil {
		t.Fatalf("qty 0: want nil, got %v", err)
	}
	if err := repo.CheckNegativeInventory(ctx, nil, "loc_main", "b8-i", "", -2); err != nil {
		t.Fatalf("qty -2: want nil, got %v", err)
	}
	// A positive request requires a transaction.
	if err := repo.CheckNegativeInventory(ctx, nil, "loc_main", "b8-i", "", 1); err == nil || !strings.Contains(err.Error(), "transaction required") {
		t.Fatalf("nil tx: want transaction required, got %v", err)
	}

	seedB8Item(t, d, "b8-chk", "b8-chk-sku", "B8 Check", 0, 1)
	seedB8Inventory(t, d, "b8-chk", "loc_main", 5)
	seedB8Variant(t, d, "b8-chkp", "b8-chkv", "loc_main", 2)

	tx := b8Tx(t, d)
	// Selling exactly down to zero is allowed — threshold is strictly below zero.
	if err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "b8-chk", "", 5); err != nil {
		t.Fatalf("exact-to-zero: want nil, got %v", err)
	}
	// One unit over goes negative — rejected with have/need in the message.
	err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "b8-chk", "", 6)
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("over stock: want insufficient stock, got %v", err)
	}
	if !strings.Contains(err.Error(), "have 5.00, need 6.00") || !strings.Contains(err.Error(), "b8-chk") || !strings.Contains(err.Error(), "loc_main") {
		t.Fatalf("error must carry item, location, have/need: %v", err)
	}
	// Fractional overshoot (weighed items) is also rejected.
	if err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "b8-chk", "", 5.01); err == nil {
		t.Fatal("fractional overshoot: want error, got nil")
	}
	// No inventory row at all counts as zero on hand.
	if err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "b8-never-stocked", "", 1); err == nil || !strings.Contains(err.Error(), "have 0.00, need 1.00") {
		t.Fatalf("never stocked: want have 0.00 error, got %v", err)
	}
	// Variant path: 2 on hand.
	if err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "", "b8-chkv", 2); err != nil {
		t.Fatalf("variant exact: want nil, got %v", err)
	}
	if err := repo.CheckNegativeInventory(ctx, tx, "loc_main", "", "b8-chkv", 3); err == nil || !strings.Contains(err.Error(), "b8-chkv") {
		t.Fatalf("variant over: want error naming variant, got %v", err)
	}
}

func TestRecordNegativeInventoryOverride_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	if _, err := repo.RecordNegativeInventoryOverride(ctx, OverrideNegativeInventory{Reason: "r"}); err == nil || !strings.Contains(err.Error(), "actorID required") {
		t.Fatalf("missing actor: got %v", err)
	}
	if _, err := repo.RecordNegativeInventoryOverride(ctx, OverrideNegativeInventory{ActorID: "system"}); err == nil || !strings.Contains(err.Error(), "reason required") {
		t.Fatalf("missing reason: got %v", err)
	}

	// Item-level override — 'system' user is seeded by migration 003 and
	// satisfies audit_log's actor FK.
	id, err := repo.RecordNegativeInventoryOverride(ctx, OverrideNegativeInventory{
		ActorID: "system", Reason: "manager approved oversell",
		ItemID: "b8-item", LocationID: "loc_main", QtyBefore: -2.5,
	})
	if err != nil || id == "" {
		t.Fatalf("override: id=%q err=%v", id, err)
	}

	var actorID, entityType, entityID, action, dataJSON string
	row := d.DB.QueryRow(`SELECT actor_id, entity_type, entity_id, action, data_json FROM audit_log WHERE id = ?`, id)
	if err := row.Scan(&actorID, &entityType, &entityID, &action, &dataJSON); err != nil {
		t.Fatalf("audit row missing: %v", err)
	}
	if actorID != "system" || entityType != "inventory" || action != "negative_inventory_override" {
		t.Fatalf("audit identity fields wrong: actor=%q type=%q action=%q", actorID, entityType, action)
	}
	if entityID != "loc_main:b8-item" {
		t.Fatalf("entity_id: got %q, want loc_main:b8-item", entityID)
	}
	var payload struct {
		Reason   string `json:"reason"`
		Snapshot struct {
			ItemID     string  `json:"item_id"`
			VariantID  string  `json:"variant_id"`
			LocationID string  `json:"location_id"`
			QtyBefore  float64 `json:"qty_before"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("data_json not valid JSON: %v (%s)", err, dataJSON)
	}
	if payload.Reason != "manager approved oversell" {
		t.Fatalf("reason: got %q", payload.Reason)
	}
	if payload.Snapshot.ItemID != "b8-item" || payload.Snapshot.LocationID != "loc_main" || payload.Snapshot.QtyBefore != -2.5 {
		t.Fatalf("snapshot wrong: %+v", payload.Snapshot)
	}

	// Variant-level override: entity_id falls back to the variant id.
	id2, err := repo.RecordNegativeInventoryOverride(ctx, OverrideNegativeInventory{
		ActorID: "system", Reason: "variant oversell",
		VariantID: "b8-var", LocationID: "loc_back", QtyBefore: 0,
	})
	if err != nil {
		t.Fatalf("variant override: %v", err)
	}
	var entityID2 string
	if err := d.DB.QueryRow(`SELECT entity_id FROM audit_log WHERE id = ?`, id2).Scan(&entityID2); err != nil {
		t.Fatalf("variant audit row: %v", err)
	}
	if entityID2 != "loc_back:b8-var" {
		t.Fatalf("variant entity_id: got %q, want loc_back:b8-var", entityID2)
	}
}

func TestGetLowStockItems_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// No seeded demo item carries a reorder level, so the report starts empty.
	if items, err := repo.GetLowStockItems(ctx, ""); err != nil || len(items) != 0 {
		t.Fatalf("baseline: got %d items err=%v, want 0", len(items), err)
	}

	// Names chosen so ORDER BY i.name is deterministic.
	seedB8Item(t, d, "b8-low", "sku-low", "B8A Low", 10, 1)
	seedB8Inventory(t, d, "b8-low", "loc_main", 4)
	seedB8Item(t, d, "b8-just", "sku-just", "B8B JustBelow", 10, 1)
	seedB8Inventory(t, d, "b8-just", "loc_main", 9)
	seedB8Item(t, d, "b8-at", "sku-at", "B8C AtLevel", 10, 1)
	seedB8Inventory(t, d, "b8-at", "loc_main", 10)
	seedB8Item(t, d, "b8-oth", "sku-oth", "B8D OtherLoc", 5, 1)
	seedB8Inventory(t, d, "b8-oth", "loc_back", 1)
	seedB8Item(t, d, "b8-zero", "sku-zero", "B8E NoReorder", 0, 1)
	seedB8Inventory(t, d, "b8-zero", "loc_main", 0)

	// Unfiltered: strictly-below-reorder rows only, name order.
	items, err := repo.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("unfiltered: got %d items %+v, want 3", len(items), items)
	}
	wantIDs := []string{"b8-low", "b8-just", "b8-oth"}
	for i, want := range wantIDs {
		if items[i].ItemID != want {
			t.Fatalf("order[%d]: got %q, want %q (full: %+v)", i, items[i].ItemID, want, items)
		}
	}
	first := items[0]
	if first.Name != "B8A Low" || first.SKU != "sku-low" || first.LocationID != "loc_main" ||
		first.LocationName != "Main Store" || first.CurrentQty != 4 || first.ReorderLevel != 10 {
		t.Fatalf("row fields wrong: %+v", first)
	}
	// At-level (10 of 10) is NOT low — the threshold is strictly below.
	for _, it := range items {
		if it.ItemID == "b8-at" || it.ItemID == "b8-zero" {
			t.Fatalf("item %s must not be reported low", it.ItemID)
		}
	}

	// Location filter narrows to that location's rows only.
	mainOnly, err := repo.GetLowStockItems(ctx, "loc_main")
	if err != nil || len(mainOnly) != 2 || mainOnly[0].ItemID != "b8-low" || mainOnly[1].ItemID != "b8-just" {
		t.Fatalf("loc_main filter: got %+v err=%v", mainOnly, err)
	}
	backOnly, err := repo.GetLowStockItems(ctx, "loc_back")
	if err != nil || len(backOnly) != 1 || backOnly[0].ItemID != "b8-oth" || backOnly[0].LocationName != "Back Store" || backOnly[0].CurrentQty != 1 {
		t.Fatalf("loc_back filter: got %+v err=%v", backOnly, err)
	}
	if none, err := repo.GetLowStockItems(ctx, "loc-nope"); err != nil || len(none) != 0 {
		t.Fatalf("unknown location: got %+v err=%v", none, err)
	}
}

func TestListStockLocations_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Fresh DB carries exactly the three seeded locations, ordered by name.
	locs, err := repo.ListStockLocations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	wantSeed := []StockLocation{
		{ID: "loc_back", Name: "Back Store"},
		{ID: "loc_main", Name: "Main Store"},
		{ID: "loc_wh", Name: "Warehouse"},
	}
	if len(locs) != 3 {
		t.Fatalf("seeded locations: got %+v, want 3", locs)
	}
	for i, want := range wantSeed {
		if locs[i] != want {
			t.Fatalf("seed[%d]: got %+v, want %+v", i, locs[i], want)
		}
	}

	// A new location sorts into place by name (here: first).
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_t8', 'AAA Front')`)
	locs, err = repo.ListStockLocations(ctx)
	if err != nil || len(locs) != 4 {
		t.Fatalf("after insert: got %+v err=%v", locs, err)
	}
	if locs[0].ID != "loc_t8" || locs[0].Name != "AAA Front" {
		t.Fatalf("name ordering: first is %+v, want loc_t8/AAA Front", locs[0])
	}
}

func TestListStockLevels_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "b8-lvl", "sku-lvl", "B8 Level", 3, 1)
	seedB8Inventory(t, d, "b8-lvl", "loc_main", 12.5)
	seedB8Inventory(t, d, "b8-lvl", "loc_back", 2)
	seedB8Item(t, d, "b8-lvl-off", "sku-lvl-off", "B8 Level Inactive", 3, 0)
	seedB8Inventory(t, d, "b8-lvl-off", "loc_main", 99)
	// Variant-level inventory (item_id NULL) is not part of this per-item view.
	seedB8Variant(t, d, "b8-lvlp", "b8-lvlv", "loc_main", 6)

	levels, err := repo.ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("list levels: %v", err)
	}
	var mine []LowStockItem
	for _, l := range levels {
		switch l.ItemID {
		case "b8-lvl":
			mine = append(mine, l)
		case "b8-lvl-off":
			t.Fatalf("inactive item leaked into stock levels: %+v", l)
		case "b8-lvlp":
			// A leaked variant inventory row would surface under its
			// PARENT item's id (the items JOIN resolves i.id), never
			// the variant id — so this is the id that must be absent.
			t.Fatalf("variant-level inventory surfaced through its parent item: %+v", l)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("b8-lvl rows: got %+v, want 2 (one per location)", mine)
	}
	// Same item's rows are ordered by location name: Back Store, then Main Store.
	back, main := mine[0], mine[1]
	if back.LocationID != "loc_back" || back.LocationName != "Back Store" || back.CurrentQty != 2 {
		t.Fatalf("back row: %+v", back)
	}
	if main.LocationID != "loc_main" || main.LocationName != "Main Store" || main.CurrentQty != 12.5 {
		t.Fatalf("main row: %+v", main)
	}
	if back.Name != "B8 Level" || back.SKU != "sku-lvl" || back.ReorderLevel != 3 {
		t.Fatalf("item fields: %+v", back)
	}
}

func TestCurrentQty_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Not found: qty 0, found=false, no error.
	if qty, found, err := repo.CurrentQty(ctx, nil, "loc_main", "b8-cq-ghost", ""); err != nil || found || qty != 0 {
		t.Fatalf("not found: qty=%v found=%v err=%v", qty, found, err)
	}

	seedB8Item(t, d, "b8-cq", "sku-cq", "B8 CQ", 0, 1)
	seedB8Inventory(t, d, "b8-cq", "loc_main", 7.5)
	if qty, found, err := repo.CurrentQty(ctx, nil, "loc_main", "b8-cq", ""); err != nil || !found || qty != 7.5 {
		t.Fatalf("found: qty=%v found=%v err=%v, want 7.5,true", qty, found, err)
	}
	// Same item, different location: independent row / not found.
	if qty, found, err := repo.CurrentQty(ctx, nil, "loc_wh", "b8-cq", ""); err != nil || found || qty != 0 {
		t.Fatalf("other location: qty=%v found=%v err=%v", qty, found, err)
	}

	// Variant path.
	seedB8Variant(t, d, "b8-cqp", "b8-cqv", "loc_back", 3)
	if qty, found, err := repo.CurrentQty(ctx, nil, "loc_back", "", "b8-cqv"); err != nil || !found || qty != 3 {
		t.Fatalf("variant: qty=%v found=%v err=%v, want 3,true", qty, found, err)
	}

	// Inside a transaction it reads uncommitted state.
	tx := b8Tx(t, d)
	if _, err := tx.Exec(`UPDATE inventory SET quantity = 1.25 WHERE item_id = 'b8-cq'`); err != nil {
		t.Fatalf("tx update: %v", err)
	}
	if qty, found, err := repo.CurrentQty(ctx, tx, "loc_main", "b8-cq", ""); err != nil || !found || qty != 1.25 {
		t.Fatalf("in-tx: qty=%v found=%v err=%v, want 1.25,true", qty, found, err)
	}
}

// TestCurrentQty_AfterStockMovements_Batch8 drives CurrentQty through the
// write path callers actually use: EnsureStockLocation + RecordStockMovement
// (receive then sale), asserting the running aggregate.
func TestCurrentQty_AfterStockMovements_Batch8(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	locID, err := repo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatalf("ensure location: %v", err)
	}
	if locID != "loc_main" {
		t.Fatalf("EnsureStockLocation must reuse the seeded loc_main, got %q", locID)
	}

	seedB8Item(t, d, "b8-mv", "sku-mv", "B8 Move", 0, 1)

	// First movement with no prior inventory row: insert-aggregate branch.
	if _, err := repo.RecordStockMovement(ctx, nil, StockMovementInput{
		ItemID: "b8-mv", LocationID: locID, Type: "receive", Quantity: 10,
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	// Second movement updates the existing aggregate.
	if _, err := repo.RecordStockMovement(ctx, nil, StockMovementInput{
		ItemID: "b8-mv", LocationID: locID, Type: "sale", Quantity: -3,
	}); err != nil {
		t.Fatalf("sale: %v", err)
	}

	qty, found, err := repo.CurrentQty(ctx, nil, locID, "b8-mv", "")
	if err != nil || !found || qty != 7 {
		t.Fatalf("after movements: qty=%v found=%v err=%v, want 7,true", qty, found, err)
	}
}
