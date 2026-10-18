package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// adjust_stock from my.'s inventory node (ut-docs#3643): optional
// location_id and variant_id, validated till-side; absent → the old pick.

func stockMovementCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func inventoryQty(t *testing.T, db *sql.DB, itemID, variantID, locationID string) float64 {
	t.Helper()
	var q float64
	var err error
	if variantID != "" {
		err = db.QueryRow(`SELECT quantity FROM inventory WHERE variant_id = ? AND item_id IS NULL AND location_id = ?`, variantID, locationID).Scan(&q)
	} else {
		err = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id = ? AND variant_id IS NULL AND location_id = ?`, itemID, locationID).Scan(&q)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestCloudAdjustStock_ChosenLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp) // item-level adjust (ADR-0043)
	ctx := t.Context()
	backID, err := data.NewPOSRepo(dp.Db).CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudAdjustStock(ctx, dp, "itm1", "", backID, 4, "recount"); err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", backID); q != 4 {
		t.Fatalf("back room qty = %v, want 4", q)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 50 {
		t.Fatalf("main qty = %v, want untouched 50", q)
	}
}

func TestCloudAdjustStock_RefusesUnknownOrInactiveLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp) // item-level adjust (ADR-0043)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	backID, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStockLocationActive(ctx, backID, false); err != nil {
		t.Fatal(err)
	}
	before := stockMovementCount(t, dp.Db)
	for _, tc := range []struct{ loc, want string }{
		{"loc-nope", "stock location not found"},
		{backID, "stock location is inactive"},
	} {
		_, err := cloudAdjustStock(ctx, dp, "itm1", "", tc.loc, 1, "x")
		if err == nil || err.Error() != tc.want {
			t.Fatalf("location %q: err = %v, want %q", tc.loc, err, tc.want)
		}
	}
	if n := stockMovementCount(t, dp.Db); n != before {
		t.Fatalf("a refused adjustment wrote %d movements", n-before)
	}
}

func TestCloudAdjustStock_Variant(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	// No location: the variant's own existing row's location (loc_main).
	if _, err := cloudAdjustStock(ctx, dp, "itm1", "var1", "", -5, "damaged"); err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "", "var1", "loc_main"); q != 25 {
		t.Fatalf("var1 qty = %v, want 30-5=25", q)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 50 {
		t.Fatalf("parent qty = %v, want untouched 50", q)
	}
	var itemID sql.NullString
	if err := dp.Db.QueryRow(`SELECT item_id FROM stock_movements WHERE variant_id = 'var1'`).Scan(&itemID); err != nil {
		t.Fatalf("variant movement: %v", err)
	}
	if itemID.Valid {
		t.Fatalf("variant movement item_id = %q, want NULL (variant-scoped)", itemID.String)
	}
}

func TestCloudAdjustStock_RefusesForeignOrUnknownVariant(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm2','XYZ','Pear',100,'tax_std',1)`); err != nil {
		t.Fatal(err)
	}
	before := stockMovementCount(t, dp.Db)
	for _, tc := range []struct{ item, variant string }{{"itm2", "var1"}, {"itm1", "var-nope"}} {
		_, err := cloudAdjustStock(ctx, dp, tc.item, tc.variant, "loc_main", 1, "x")
		if err == nil || err.Error() != "variant not found" {
			t.Fatalf("item %q variant %q: err = %v, want variant not found", tc.item, tc.variant, err)
		}
	}
	if n := stockMovementCount(t, dp.Db); n != before {
		t.Fatalf("a refused adjustment wrote %d movements", n-before)
	}
}

func TestCloudAdjustStock_RefusesUnknownOrUntrackedItem(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.Exec(`UPDATE items SET stock_untracked = 1 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	before := stockMovementCount(t, dp.Db)
	for _, tc := range []struct{ item, loc, want string }{
		{"itm1", "", "item does not track stock"},
		{"itm1", "loc_main", "item does not track stock"},
		{"itm-nope", "", "item not found"},
	} {
		_, err := cloudAdjustStock(ctx, dp, tc.item, "", tc.loc, 1, "x")
		if err == nil || err.Error() != tc.want {
			t.Fatalf("item %q: err = %v, want %q", tc.item, err, tc.want)
		}
	}
	if n := stockMovementCount(t, dp.Db); n != before {
		t.Fatalf("a refused adjustment wrote %d movements", n-before)
	}
}

// The connector event names the variant, as the local adjust's does.
func TestCloudAdjustStock_EventCarriesVariant(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	m := &plugins.Manifest{
		ID: "com.example.stock-3643", Name: "Stock 3643", Version: "1.0.0", Entrypoint: "./c",
		Hooks:       []plugins.ManifestHook{{Event: "stock.adjusted", Action: "sync"}},
		Permissions: []string{"events:receive"},
	}
	if err := plugins.PersistManifest(ctx, dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.GrantPermission(ctx, dp.Db, m.ID, "events:receive"); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(dp.Db)
	ch, err := bus.Subscribe(context.Background(), m.ID, []string{"stock.adjusted"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Unsubscribe(m.ID) })

	if _, err := cloudAdjustStock(ctx, dp, "itm1", "var1", "loc_main", 2, "found"); err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	select {
	case ev := <-ch:
		var got plugins.StockAdjustedEvent
		if err := json.Unmarshal(ev.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if got.ItemID != "itm1" || got.VariantID != "var1" || got.Location != "loc_main" || got.DeltaQty != 2 {
			t.Fatalf("event = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stock.adjusted event")
	}
}

// Like the local adjust, a cloud adjustment nudges linked tills' stock.
func TestCloudAdjustStock_NudgesLinkedTillsStock(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	deactivateSeededVariant(t, f.dp) // item-level adjust (ADR-0043)
	if _, err := cloudAdjustStock(t.Context(), f.dp, "itm1", "", "loc_main", 3, "recount"); err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	waitSync(t, c, fleetlink.ScopeStock)
}

// ADR-0043: an item with an active variant is stocked per variant, so an
// item-level adjustment (no variant_id) is refused before anything is
// written; once its only variant is inactive the item-level adjust works.
func TestCloudAdjustStock_RefusesItemLevelOnVariantStockedItem(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := stockMovementCount(t, dp.Db)
	for _, loc := range []string{"", "loc_main"} {
		_, err := cloudAdjustStock(ctx, dp, "itm1", "", loc, 1, "x")
		if err == nil || err.Error() != "item has variants; choose a variant" {
			t.Fatalf("location %q: err = %v, want item has variants; choose a variant", loc, err)
		}
	}
	if n := stockMovementCount(t, dp.Db); n != before {
		t.Fatalf("a refused adjustment wrote %d movements", n-before)
	}
	deactivateSeededVariant(t, dp)
	if _, err := cloudAdjustStock(ctx, dp, "itm1", "", "loc_main", 1, "x"); err != nil {
		t.Fatalf("item with only an inactive variant: %v", err)
	}
}

// deactivateSeededVariant retires seedForPages' itm1 variant (var1) so a
// test can drive an item-level adjustment of itm1 (ADR-0043: an item with
// an active variant is stocked per variant only).
func deactivateSeededVariant(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.Exec(`UPDATE item_variants SET is_active = 0 WHERE id = 'var1'`); err != nil {
		t.Fatal(err)
	}
}
