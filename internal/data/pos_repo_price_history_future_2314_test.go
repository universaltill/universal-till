package data

// ut-docs#2314: AppendPriceHistoryItem/AppendPriceHistoryVariant's own doc
// comments say "ends the current open price" — but pre-fix, the "end the
// current open row" UPDATE was `WHERE item_id = ? AND ends_at IS NULL`,
// which ends EVERY open row, including a future-dated SCHEDULED one
// (starts_at > now) that hasn't started yet. Editing today's price must
// never disturb a deliberately scheduled future price change. Fixed by
// adding `AND starts_at <= ?` (the same startsAt the caller passed) to that
// UPDATE, so only a row that is already active or in the past gets closed.

import (
	"context"
	"testing"
	"time"
)

func TestPOSRepo_AppendPriceHistoryItem_LeavesFutureScheduledRowUntouched(t *testing.T) {
	dbo := newBatch8DB(t, "phi-future.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-f', 'SKU-F', 'Future Widget', 500, 1)`)

	now := time.Now()
	future := now.Add(48 * time.Hour)

	// A deliberately scheduled future price change, not started yet.
	if err := repo.AppendPriceHistoryItem(ctx, "itm-f", 999, future); err != nil {
		t.Fatalf("seed future row: %v", err)
	}

	// Editing the CURRENT price must not touch the future row at all —
	// not its ends_at, not its price.
	if err := repo.AppendPriceHistoryItem(ctx, "itm-f", 600, now); err != nil {
		t.Fatalf("append current price: %v", err)
	}

	rows, err := dbo.DB.QueryContext(ctx, `SELECT price, starts_at, ends_at FROM price_history WHERE item_id = 'itm-f' ORDER BY datetime(starts_at)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type ph struct {
		price  int64
		starts string
		ends   *string
	}
	var got []ph
	for rows.Next() {
		var r ph
		if err := rows.Scan(&r.price, &r.starts, &r.ends); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 price_history rows (the current-price row + the untouched future row), got %d: %+v", len(got), got)
	}
	if got[0].price != 600 || got[0].ends != nil {
		t.Fatalf("expected the newly-appended current-price row (600) to stay open, got %+v", got[0])
	}
	if got[1].price != 999 {
		t.Fatalf("expected the future row's price to stay 999, got %+v", got[1])
	}
	if got[1].ends != nil {
		t.Fatalf("expected the future-dated scheduled row's ends_at to remain NULL (untouched by editing the CURRENT price), got ends_at=%v", *got[1].ends)
	}
	if got[1].starts != future.Format(time.RFC3339) {
		t.Fatalf("expected the future row's starts_at to be unchanged, got %q", got[1].starts)
	}
}

func TestPOSRepo_AppendPriceHistoryVariant_LeavesFutureScheduledRowUntouched(t *testing.T) {
	dbo := newBatch8DB(t, "phv-future.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-fv', 'SKU-FV', 'Future Drink', 300, 1)`)
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('var-f', 'itm-fv', 'SKU-FV-L', 'Large', 350, 1)`)

	now := time.Now()
	future := now.Add(48 * time.Hour)

	if err := repo.AppendPriceHistoryVariant(ctx, "var-f", 999, future); err != nil {
		t.Fatalf("seed future row: %v", err)
	}
	if err := repo.AppendPriceHistoryVariant(ctx, "var-f", 425, now); err != nil {
		t.Fatalf("append current price: %v", err)
	}

	rows, err := dbo.DB.QueryContext(ctx, `SELECT price, ends_at FROM price_history WHERE variant_id = 'var-f' ORDER BY datetime(starts_at)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prices []int64
	var endsNull []bool
	for rows.Next() {
		var price int64
		var ends *string
		if err := rows.Scan(&price, &ends); err != nil {
			t.Fatal(err)
		}
		prices = append(prices, price)
		endsNull = append(endsNull, ends == nil)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 || prices[0] != 425 || prices[1] != 999 {
		t.Fatalf("expected [425, 999], got %v", prices)
	}
	if !endsNull[0] {
		t.Fatalf("expected the new current-price row to stay open")
	}
	if !endsNull[1] {
		t.Fatalf("expected the future-dated scheduled row to remain untouched (ends_at still NULL)")
	}
}
