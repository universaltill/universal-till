package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
)

// adjust_stock count mode (ut-docs#4749, ADR-0168 §4). Items are invented.

func countReq(target float64, countedAt time.Time) cloudsync.CountStockRequest {
	return cloudsync.CountStockRequest{ItemID: "itm1", Target: target, CountedAt: countedAt}
}

func TestCloudCountStock_SetsToCountedQuantity(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	if _, err := cloudCountStock(ctx, dp, countReq(40, time.Now().Add(-time.Hour))); err != nil {
		t.Fatalf("cloudCountStock: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 40 {
		t.Fatalf("qty = %v, want 40", q)
	}
}

func TestCloudCountStock_KeepsSalesMadeAfterTheCount(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	counted := time.Now().Add(-time.Hour)
	// A movement before the count is already inside the counted number; a
	// sale after it must survive.
	seedMovementAt := func(qty float64, typ string, at time.Time) {
		if _, err := dp.Db.Exec(`INSERT INTO stock_movements(id,item_id,variant_id,location_id,type,quantity,created_at) VALUES(?,?,NULL,'loc_main',?,?,?)`,
			"mv-"+typ+at.Format("150405"), "itm1", typ, qty, at.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	seedMovementAt(-7, "sale", counted.Add(-10*time.Minute))
	seedMovementAt(-2, "sale", counted.Add(10*time.Minute))
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 48 WHERE id = 'inv1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudCountStock(ctx, dp, countReq(40, counted)); err != nil {
		t.Fatalf("cloudCountStock: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 38 {
		t.Fatalf("qty = %v, want counted 40 minus the later sale of 2 = 38", q)
	}
}

func TestCloudCountStock_SameTargetTwiceMovesNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	counted := time.Now().Add(-time.Hour)
	if _, err := cloudCountStock(ctx, dp, countReq(40, counted)); err != nil {
		t.Fatal(err)
	}
	n := stockMovementCount(t, dp.Db)
	msg, err := cloudCountStock(ctx, dp, countReq(40, counted))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := stockMovementCount(t, dp.Db); got != n {
		t.Fatalf("second apply wrote %d movements, want 0 (%s)", got-n, msg)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 40 {
		t.Fatalf("qty = %v, want 40", q)
	}
	// A later sale, then the same target again: still nothing.
	if _, err := dp.Db.Exec(`INSERT INTO stock_movements(id,item_id,variant_id,location_id,type,quantity,created_at) VALUES('mv-s','itm1',NULL,'loc_main','sale',-3,?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 37 WHERE id = 'inv1'`); err != nil {
		t.Fatal(err)
	}
	n = stockMovementCount(t, dp.Db)
	if _, err := cloudCountStock(ctx, dp, countReq(40, counted)); err != nil {
		t.Fatal(err)
	}
	if got := stockMovementCount(t, dp.Db); got != n {
		t.Fatalf("re-apply after a sale wrote %d movements, want 0", got-n)
	}
}

func TestCloudCountStock_DecimalTargets(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	before := stockMovementCount(t, dp.Db)
	_, err := cloudCountStock(ctx, dp, countReq(2.5, time.Now().Add(-time.Hour)))
	if err == nil || err.Error() != "target must be a whole number for this item" {
		t.Fatalf("decimal target on a counted item: err = %v", err)
	}
	if stockMovementCount(t, dp.Db) != before {
		t.Fatal("a refused count wrote a movement")
	}
	if _, err := dp.Db.Exec(`UPDATE items SET is_weighed = 1 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudCountStock(ctx, dp, countReq(2.5, time.Now().Add(-time.Hour))); err != nil {
		t.Fatalf("decimal target on a weighed item: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 2.5 {
		t.Fatalf("qty = %v, want 2.5", q)
	}
	if _, err := cloudCountStock(ctx, dp, countReq(1.2345, time.Now().Add(-time.Hour))); err == nil {
		t.Fatal("more than 3 decimals must be refused")
	}
}

func TestCloudCountStock_RefusesBadInput(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	ctx := t.Context()
	for name, req := range map[string]cloudsync.CountStockRequest{
		"negative":      countReq(-1, time.Now().Add(-time.Hour)),
		"no counted_at": {ItemID: "itm1", Target: 5},
		"future":        countReq(5, time.Now().Add(time.Hour)),
		"unknown item":  {ItemID: "nope", Target: 5, CountedAt: time.Now().Add(-time.Hour)},
	} {
		if _, err := cloudCountStock(ctx, dp, req); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 50 {
		t.Fatalf("qty = %v, a refused count must leave 50", q)
	}
	// Item with variants needs a variant (ADR-0043), as adjust_stock.
	dp2 := newCloudSyncTestDeps(t)
	if _, err := cloudCountStock(ctx, dp2, countReq(5, time.Now().Add(-time.Hour))); err == nil || err.Error() != "item has variants; choose a variant" {
		t.Fatalf("variant item: err = %v", err)
	}
}

func TestCloudCountStock_VariantAndClockConversion(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	now := time.Now()
	// The cloud's clock runs 10 minutes behind the till's. It counted 30
	// minutes before its own "now", i.e. 30 minutes ago on the till's clock.
	cloudNow := now.Add(-10 * time.Minute)
	counted := cloudNow.Add(-30 * time.Minute)
	// A sale 35 minutes ago on the till's clock is BEFORE the count (already
	// inside it); read without the conversion it would look later.
	if _, err := dp.Db.Exec(`INSERT INTO stock_movements(id,item_id,variant_id,location_id,type,quantity,created_at) VALUES('mv-v',NULL,'var1','loc_main','sale',-4,?)`, now.Add(-35*time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 26 WHERE id = 'inv2'`); err != nil {
		t.Fatal(err)
	}
	msg, err := cloudCountStock(ctx, dp, cloudsync.CountStockRequest{ItemID: "itm1", VariantID: "var1", Target: 20, CountedAt: counted, CloudNow: cloudNow})
	if err != nil {
		t.Fatalf("cloudCountStock: %v", err)
	}
	if q := inventoryQty(t, dp.Db, "", "var1", "loc_main"); q != 20 {
		t.Fatalf("variant qty = %v, want 20 (the earlier sale is inside the count)", q)
	}
	if !strings.Contains(msg, "clock skew") {
		t.Fatalf("10 minutes of skew must be reported, got %q", msg)
	}
	if q := inventoryQty(t, dp.Db, "itm1", "", "loc_main"); q != 50 {
		t.Fatalf("parent qty = %v, want untouched 50", q)
	}
}

func TestCloudCountStock_SmallSkewNotReported(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	now := time.Now()
	msg, err := cloudCountStock(t.Context(), dp, cloudsync.CountStockRequest{ItemID: "itm1", Target: 9, CountedAt: now.Add(-time.Hour), CloudNow: now.Add(-30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "skew") {
		t.Fatalf("30s of skew must not be reported: %q", msg)
	}
}

// The offset is the one measured when the response arrived: a directive that
// waited behind others is not skewed by the wait.
func TestCloudCountStock_OffsetIsMeasuredAtReceipt(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	now := time.Now()
	// Clocks agree; the response arrived 20 minutes "ago" by the cloud's
	// stamp only if the wait were counted. With ReceivedAt = CloudNow the
	// offset is zero, so no skew is reported.
	msg, err := cloudCountStock(t.Context(), dp, cloudsync.CountStockRequest{
		ItemID: "itm1", Target: 9, CountedAt: now.Add(-time.Hour),
		CloudNow: now.Add(-20 * time.Minute), ReceivedAt: now.Add(-20 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "skew") {
		t.Fatalf("a waited-out directive must not look skewed: %q", msg)
	}
}

func TestCloudCountStock_ReasonIsRecorded(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	deactivateSeededVariant(t, dp)
	req := countReq(41, time.Now().Add(-time.Hour))
	if _, err := cloudCountStock(t.Context(), dp, req); err != nil {
		t.Fatal(err)
	}
	var data string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'count'`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"reason":"import"`) {
		t.Fatalf("audit = %s, want reason import", data)
	}
}
