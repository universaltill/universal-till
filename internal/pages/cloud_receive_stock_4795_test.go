package pages

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
)

// adjust_stock type=receive from my. (ut-docs#4795): the same stock movement
// record the till's own POST /api/inventory/receipt writes (type receive,
// the unit cost, the audit row), at the chosen location.

func TestCloudReceiveStock_RecordsAReceiptWithCostAndAudit(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	backID, err := data.NewPOSRepo(dp.Db).CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := cloudReceiveStock(ctx, dp, "itm1", "", backID, 12, 149, "delivery")
	if err != nil {
		t.Fatalf("cloudReceiveStock: %v", err)
	}
	if msg != "stock received: +12" {
		t.Fatalf("msg = %q", msg)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", backID); q != 12 {
		t.Fatalf("back room qty = %v, want 12", q)
	}
	var typ string
	var qty float64
	var cost sql.NullInt64
	if err := dp.Db.QueryRow(`SELECT type, quantity, cost_price FROM stock_movements WHERE location_id = ?`, backID).Scan(&typ, &qty, &cost); err != nil {
		t.Fatal(err)
	}
	if typ != "receive" || qty != 12 || !cost.Valid || cost.Int64 != 149 {
		t.Fatalf("movement = %s %v cost %v, want receive 12 cost 149", typ, qty, cost)
	}
	var action, actor, payload string
	if err := dp.Db.QueryRow(`SELECT action, actor_id, data_json FROM audit_log WHERE entity_type = 'inventory' ORDER BY created_at DESC LIMIT 1`).Scan(&action, &actor, &payload); err != nil {
		t.Fatal(err)
	}
	if action != "receive" || actor != "system" {
		t.Fatalf("audit = %s by %s, want receive by system", action, actor)
	}
}

func TestCloudReceiveStock_NoCostIsNull(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	if _, err := cloudReceiveStock(t.Context(), dp, "itm1", "", "loc_main", 3, 0, ""); err != nil {
		t.Fatal(err)
	}
	var cost sql.NullInt64
	if err := dp.Db.QueryRow(`SELECT cost_price FROM stock_movements WHERE type = 'receive'`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost.Valid {
		t.Fatalf("cost = %v, want NULL for none", cost.Int64)
	}
}

func TestCloudReceiveStock_SameRefusalsAsAnAdjustment(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := stockMovementCount(t, dp.Db)
	if _, err := cloudReceiveStock(ctx, dp, "itm1", "var-nope", "loc_main", 1, 0, "x"); err == nil || err.Error() != "variant not found" {
		t.Fatalf("unknown variant: %v", err)
	}
	if _, err := cloudReceiveStock(ctx, dp, "itm1", "", "loc_nope", 1, 0, "x"); err == nil {
		t.Fatal("unknown location must be refused")
	}
	if _, err := cloudReceiveStock(ctx, dp, "itm1", "var1", "loc_main", -1, 0, "x"); err == nil {
		t.Fatal("a negative receipt must be refused")
	}
	if n := stockMovementCount(t, dp.Db); n != before {
		t.Fatalf("refused receipts wrote %d movements", n-before)
	}
}

// Parity with ut-cloud (ut-docs#4795): the adjust_stock directive carries the
// fields of the till's own receipt form. ut-cloud's
// internal/stockreceiptcontract reads this struct from source; this pins
// the other side, and the cost ceiling the cloud mirrors.
func TestStockReceiptRequestFieldsArePinned(t *testing.T) {
	typ := reflect.TypeOf(StockReceiptRequest{})
	got := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		got[f.Tag.Get("json")] = f.Type.String()
	}
	want := map[string]string{"item_id": "string", "variant_id": "string", "location_id": "string",
		"type": "string", "quantity": "float64", "cost_price": "int64", "reason": "string"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StockReceiptRequest = %v, want %v: ut-cloud's adjust_stock (type, cost_price, location_id, qty_delta, reason) and my.'s dialog follow this form — change them together", got, want)
	}
	if cloudsync.MaxReceiveCostMinor != 999_999_999 {
		t.Fatalf("receipt cost ceiling = %d: ut-cloud claims.MaxStockCostMinor must match", cloudsync.MaxReceiveCostMinor)
	}
}
