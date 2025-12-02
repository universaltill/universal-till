package pos

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupHistoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestAppendPriceHistoryItem_AppendsAndEndsPrevious(t *testing.T) {
	ctx := context.Background()
	db := setupHistoryDB(t)
	defer db.Close()

	now := time.Now().UTC()
	// existing open price
	_, _ = db.Exec(`INSERT INTO price_history(id,item_id,price,starts_at) VALUES('p1','itm1',100,?)`, now.Add(-time.Hour))

	if err := AppendPriceHistoryItem(ctx, db, "itm1", 200, now); err != nil {
		t.Fatalf("AppendPriceHistoryItem error: %v", err)
	}

	var openCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE ends_at IS NULL`).Scan(&openCount)
	if openCount != 1 {
		t.Fatalf("expected 1 open price, got %d", openCount)
	}
	var price int64
	_ = db.QueryRow(`SELECT price FROM price_history WHERE ends_at IS NULL`).Scan(&price)
	if price != 200 {
		t.Fatalf("expected new price 200, got %d", price)
	}
	var endedCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE ends_at IS NOT NULL`).Scan(&endedCount)
	if endedCount != 1 {
		t.Fatalf("expected previous price to be ended")
	}
}

func TestAppendPriceHistoryVariant_AppendsAndEndsPrevious(t *testing.T) {
	ctx := context.Background()
	db := setupHistoryDB(t)
	defer db.Close()

	now := time.Now().UTC()
	_, _ = db.Exec(`INSERT INTO price_history(id,variant_id,price,starts_at) VALUES('pv1','var1',500,?)`, now.Add(-time.Hour))

	if err := AppendPriceHistoryVariant(ctx, db, "var1", 750, now); err != nil {
		t.Fatalf("AppendPriceHistoryVariant error: %v", err)
	}
	var price int64
	_ = db.QueryRow(`SELECT price FROM price_history WHERE ends_at IS NULL`).Scan(&price)
	if price != 750 {
		t.Fatalf("expected new variant price 750, got %d", price)
	}
	var endedCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE ends_at IS NOT NULL`).Scan(&endedCount)
	if endedCount != 1 {
		t.Fatalf("expected previous price to be ended")
	}
}

func TestAppendPriceHistoryItem_MultipleAppends(t *testing.T) {
	ctx := context.Background()
	db := setupHistoryDB(t)
	defer db.Close()

	now := time.Now().UTC()
	// first price
	if err := AppendPriceHistoryItem(ctx, db, "itm1", 100, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("AppendPriceHistoryItem first error: %v", err)
	}
	// second price
	if err := AppendPriceHistoryItem(ctx, db, "itm1", 200, now.Add(-time.Hour)); err != nil {
		t.Fatalf("AppendPriceHistoryItem second error: %v", err)
	}
	// third price
	if err := AppendPriceHistoryItem(ctx, db, "itm1", 300, now); err != nil {
		t.Fatalf("AppendPriceHistoryItem third error: %v", err)
	}

	var openCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE ends_at IS NULL`).Scan(&openCount)
	if openCount != 1 {
		t.Fatalf("expected exactly one open price, got %d", openCount)
	}
	var current int64
	_ = db.QueryRow(`SELECT price FROM price_history WHERE ends_at IS NULL`).Scan(&current)
	if current != 300 {
		t.Fatalf("expected latest open price 300, got %d", current)
	}
	var endedCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM price_history WHERE ends_at IS NOT NULL`).Scan(&endedCount)
	if endedCount != 2 {
		t.Fatalf("expected two ended prices, got %d", endedCount)
	}
}
