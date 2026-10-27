package cloudsync

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Inventory node in my. (ut-docs#3643): the snapshot carries per-location
// stock levels, and adjust_stock takes an optional location_id/variant_id.

func stockLevelRowsByKey(t *testing.T, snap map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := snap["stock_levels"].([]any)
	if !ok {
		t.Fatalf("stock_levels = %#v, want an array", snap["stock_levels"])
	}
	out := map[string]map[string]any{}
	for _, r := range raw {
		m := r.(map[string]any)
		if len(m) != 4 {
			t.Fatalf("stock level row %v: want exactly item_id, variant_id, location_id, qty", m)
		}
		out[fmt.Sprintf("%v|%v|%v", m["item_id"], m["variant_id"], m["location_id"])] = m
	}
	return out
}

// The snapshot carries real inventory rows of active stock-tracked items and
// variants as [{item_id, variant_id, location_id, qty}]: a variant row
// carries its parent item_id and its own variant_id, an item row "". An
// untracked item's row is left out. The items' own summed qty is untouched,
// and a stock-only change pushes again (the field is hashed).
func TestSnapshotIncludesStockLevels(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")
	ctx := context.Background()
	repo := data.NewPOSRepo(d.DB)
	backID, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('it-1','SKU1','Coke',100,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('it-2','SKU2','Shirt',900,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, stock_untracked) VALUES ('it-3','SKU3','Service',500,1,1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-1','it-2','SKU2-L','Large',900,1)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1','it-1','loc_main', 7.5)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-2','it-1','` + backID + `', 2)`,
		`INSERT INTO inventory (id, variant_id, location_id, quantity) VALUES ('inv-3','v-1','loc_main', 4)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-4','it-3','loc_main', 9)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := Tick(ctx, testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cloud.snapshots))
	}
	rows := stockLevelRowsByKey(t, cloud.snapshots[0])
	if m := rows["it-1||loc_main"]; m == nil || m["qty"] != 7.5 {
		t.Fatalf("it-1 @ loc_main row = %v", m)
	}
	if m := rows["it-1||"+backID]; m == nil || m["qty"] != 2.0 {
		t.Fatalf("it-1 @ back room row = %v", m)
	}
	if m := rows["it-2|v-1|loc_main"]; m == nil || m["qty"] != 4.0 {
		t.Fatalf("variant row = %v (want parent item_id it-2, variant_id v-1)", m)
	}
	for k := range rows {
		if k == "it-3||loc_main" || k == "|v-1|loc_main" || k == "it-2||loc_main" {
			t.Fatalf("unexpected stock level row %q", k)
		}
	}
	// The per-item summed qty stays as it was (item rows only).
	for _, r := range cloud.snapshots[0]["items"].([]any) {
		if m := r.(map[string]any); m["id"] == "it-1" && m["qty"] != 9.5 {
			t.Fatalf("it-1 qty = %v, want 9.5", m["qty"])
		}
	}

	// Unchanged: no second push.
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("unchanged snapshot pushed again: %d", len(cloud.snapshots))
	}
	// A variant-only stock change (no item qty moves) pushes.
	if _, err := repo.RecordStockMovement(ctx, nil, data.StockMovementInput{
		VariantID: "v-1", LocationID: backID, Type: "adjust", Quantity: 3, ActorID: "system",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(ctx, testCfg(srv.URL), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	if len(cloud.snapshots) != 2 {
		t.Fatalf("a stock-only change must re-push the snapshot: %d pushes", len(cloud.snapshots))
	}
	if m := stockLevelRowsByKey(t, cloud.snapshots[1])["it-2|v-1|"+backID]; m == nil || m["qty"] != 3.0 {
		t.Fatalf("new variant row = %v", m)
	}
}

// A stock-levels read error leaves the field out (the cloud keeps what it
// had) rather than sending [] — and the rest of the snapshot still goes.
func TestSnapshotOmitsStockLevelsOnReadError(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	d := openMigratedDB(t, "cloudsync.db")
	if _, err := d.Exec(`DROP TABLE inventory`); err != nil {
		t.Fatal(err)
	}
	if err := Tick(context.Background(), testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cloud.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cloud.snapshots))
	}
	if v, ok := cloud.snapshots[0]["stock_levels"]; ok {
		t.Fatalf("stock_levels = %#v, want the field omitted on a read error", v)
	}
	if _, ok := cloud.snapshots[0]["stock_locations"]; !ok {
		t.Fatal("stock_locations missing: only stock_levels should drop")
	}
}

// The rows are ordered by (item_id, variant_id, location_id) so the hash is
// stable, and capped at the cloud's 50 000-row limit.
func TestSnapshotStockLevelRowsSortedAndCapped(t *testing.T) {
	levels := []data.LowStockItem{
		{ItemID: "b", LocationID: "l1", CurrentQty: 1},
		{ItemID: "a", VariantID: "v2", LocationID: "l1", CurrentQty: 2},
		{ItemID: "a", LocationID: "l2", CurrentQty: 3},
		{ItemID: "a", VariantID: "v1", LocationID: "l1", CurrentQty: 4},
	}
	got := snapshotStockLevels(levels)
	want := []snapshotStockLevelRow{
		{ItemID: "a", LocationID: "l2", Qty: 3},
		{ItemID: "a", VariantID: "v1", LocationID: "l1", Qty: 4},
		{ItemID: "a", VariantID: "v2", LocationID: "l1", Qty: 2},
		{ItemID: "b", LocationID: "l1", Qty: 1},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}

	many := make([]data.LowStockItem, maxSnapshotStockLevels+5)
	for i := range many {
		many[i] = data.LowStockItem{ItemID: fmt.Sprintf("i%06d", i), LocationID: "l"}
	}
	if n := len(snapshotStockLevels(many)); n != maxSnapshotStockLevels {
		t.Fatalf("rows = %d, want capped at %d", n, maxSnapshotStockLevels)
	}
	if maxSnapshotStockLevels != 50000 {
		t.Fatalf("cap = %d, want the cloud's 50000", maxSnapshotStockLevels)
	}
}

// adjust_stock passes the optional location_id and variant_id (trimmed) to
// the hook; absent or blank → "" (today's behaviour); a non-string is
// refused before the hook runs.
func TestApplyAdjustStock_LocationAndVariant(t *testing.T) {
	var gotItem, gotVariant, gotLoc, gotReason string
	var gotDelta float64
	calls := 0
	hooks := Hooks{AdjustStock: func(_ context.Context, itemID, variantID, locationID string, delta float64, reason string) (string, error) {
		calls++
		gotItem, gotVariant, gotLoc, gotDelta, gotReason = itemID, variantID, locationID, delta, reason
		return "ok", nil
	}}
	status, _ := apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{
		"item_id": "it-2", "variant_id": " v-1 ", "location_id": " loc-back ", "qty_delta": -2.0, "reason": "damaged",
	}}, hooks)
	if status != "applied" || gotItem != "it-2" || gotVariant != "v-1" || gotLoc != "loc-back" || gotDelta != -2 || gotReason != "damaged" {
		t.Fatalf("status=%q item=%q variant=%q loc=%q delta=%g reason=%q", status, gotItem, gotVariant, gotLoc, gotDelta, gotReason)
	}

	// Absent fields: an older cloud's payload, today's behaviour.
	status, _ = apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{
		"item_id": "it-1", "qty_delta": 1.0,
	}}, hooks)
	if status != "applied" || gotVariant != "" || gotLoc != "" {
		t.Fatalf("absent fields: status=%q variant=%q loc=%q", status, gotVariant, gotLoc)
	}

	before := calls
	for _, tc := range []struct {
		key, want string
	}{{"location_id", "invalid location_id"}, {"variant_id", "invalid variant_id"}} {
		status, msg := apply(context.Background(), directive{Type: "adjust_stock", Payload: map[string]any{
			"item_id": "it-1", "qty_delta": 1.0, tc.key: 7,
		}}, hooks)
		if status != "failed" || msg != tc.want {
			t.Fatalf("non-string %s: status=%q msg=%q", tc.key, status, msg)
		}
	}
	if calls != before {
		t.Fatal("a refused payload must not reach the hook")
	}
}
