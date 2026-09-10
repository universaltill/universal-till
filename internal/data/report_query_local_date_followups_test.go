package data

// ut-docs#1664 review finding S3: the EXPLAIN QUERY PLAN tests in
// internal/db/report_query_local_date_followups_test.go prove the converted
// queries are SARGABLE, but seed local_date via the same date(?,'localtime')
// expression the production writer uses -- tautological with respect to
// created_at, so nothing in that file would catch a writer stamping the
// WRONG local_date. This file closes that gap: it seeds through the REAL
// production writer (InsertSale/InsertSaleLine, not raw SQL) and asserts
// each of the four converted query families actually reads the seeded sale
// back for its day -- an end-to-end correctness check, not just a plan
// shape check.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

func TestReportQueryLocalDateFollowups_ConvertedQueriesFindRealWriterSeededSale(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "m1664-writer-roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('cat-1', 'Grocery')`)
	mustExec(t, d, `INSERT INTO items (id, name, base_price, category_id) VALUES ('item-1', 'Milk', 200, 'cat-1')`)

	// today anchors both the seeded sale and every query below to ONE Go-side
	// instant (noon, not midnight -- keeps a same-day instant inside its
	// calendar day for any real IANA offset, -12..+14, same idiom as
	// b8ExpectedDay elsewhere in this package).
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	createdAt := today.UTC().Format(time.RFC3339)

	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "rt-sale-1", ReceiptNo: "RT-1", SaleType: "sale", Currency: "GBP",
		Subtotal: 200, TaxTotal: 0, Total: 200,
		CreatedAt: createdAt, TenderType: "cash", SyncStatus: "synced",
	}); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	if err := repo.InsertSaleLine(ctx, nil, "rt-line-1", "rt-sale-1", 1, "item-1", "",
		"Milk", "SKU-1", "", 1, 200, 0, 0, 0, 200, 200); err != nil {
		t.Fatalf("InsertSaleLine: %v", err)
	}

	// The day string every query below is asked about — derived the same
	// way production derives it (browser date picker / EOD close day),
	// not by reading back local_date, so this doesn't just check the
	// writer against itself.
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, createdAt).Scan(&day); err != nil {
		t.Fatalf("control day query: %v", err)
	}

	t.Run("DepartmentsForDay", func(t *testing.T) {
		rows, err := repo.DepartmentsForDay(ctx, day)
		if err != nil {
			t.Fatalf("DepartmentsForDay: %v", err)
		}
		if len(rows) != 1 || rows[0].Department != "Grocery" || rows[0].Revenue != 200 {
			t.Fatalf("DepartmentsForDay(%s) = %+v, want one Grocery row, revenue 200 -- the real-writer-seeded sale must be found via local_date", day, rows)
		}
	})

	t.Run("SalesForTaxBands", func(t *testing.T) {
		sales, err := repo.SalesForTaxBands(ctx, day, day)
		if err != nil {
			t.Fatalf("SalesForTaxBands: %v", err)
		}
		if len(sales) != 1 || sales[0].ID != "rt-sale-1" {
			t.Fatalf("SalesForTaxBands(%s, %s) = %+v, want exactly rt-sale-1", day, day, sales)
		}
	})

	t.Run("DayTotal", func(t *testing.T) {
		// daysAgo=0 against a ref of "day after today, at local noon UTC" --
		// DayTotal(ctx, 1, ref) means "the day before ref", so ref = today+1
		// targets today itself.
		ref := today.AddDate(0, 0, 1)
		total, count, err := repo.DayTotal(ctx, 1, ref)
		if err != nil {
			t.Fatalf("DayTotal: %v", err)
		}
		if total != 200 || count != 1 {
			t.Fatalf("DayTotal(1, %s) = (%d, %d), want (200, 1) -- the real-writer-seeded sale must be found via local_date", ref, total, count)
		}
	})

	t.Run("ListSalesJournal_DayFilter", func(t *testing.T) {
		entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Day: day, Limit: 10})
		if err != nil {
			t.Fatalf("ListSalesJournal: %v", err)
		}
		if len(entries) != 1 || entries[0].ReceiptNo != "RT-1" {
			t.Fatalf("ListSalesJournal(Day=%s) = %+v, want exactly RT-1", day, entries)
		}
	})
}
