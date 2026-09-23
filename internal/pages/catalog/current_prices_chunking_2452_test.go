package catalog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestCatalogPage_CurrentPricesBeyondSQLiteBindLimit is the admin catalog
// list's own counterpart of ut-docs#2318/#2451's chunking fixes —
// ut-docs#2452 reports the same shape one call later: the /catalog list
// handler called repo.ItemCurrentPrices with the WHOLE active-item id set
// unchunked and discarded its error (`_`). ItemCurrentPrices binds 1 SQL
// arg per id (internal/data/catalog_repo.go's inPlaceholders), so it
// doesn't hit SQLite's bind-variable ceiling (empirically 32766 on this
// repo's modernc.org/sqlite build) until past ~32,766 items — later than
// ItemIDsWithModifiers' 2-args/id break point, which is why this needs a
// larger seed than the #2318/#2451 tests (16,400 items each).
//
// Pre-fix, the unchunked call fails outright past the ceiling and its
// error is silently swallowed, so EVERY item (not just ones past some
// boundary) falls back to base_price — a promotional price override
// becomes invisible catalog-wide with no visible sign anything went
// wrong. Two markers (one in the first chunk, one in the last, short
// chunk) guard against a chunking bug that only processes part of the
// id set, the same shape independent review already caught once in the
// sibling #2451 fix.
func TestCatalogPage_CurrentPricesBeyondSQLiteBindLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-catalog test in -short mode")
	}
	chdirToRepoRoot(t)
	const n = 32800 // > 32,766: where ItemCurrentPrices' 1-arg-per-id binding starts hitting SQLite's bind-variable ceiling unchunked

	db := setupCatalogPageDB(t)
	defer db.Close()

	// Bulk-insert with literal (non-parameterized) values, batched into
	// statements of 1000 rows each — same technique the #2451 sibling test
	// uses, and for the same reason: sidesteps hitting the very bind limit
	// this test is about during its own setup.
	const insertBatch = 1000
	var sb strings.Builder
	rowsInBatch := 0
	flush := func() {
		if rowsInBatch == 0 {
			return
		}
		if _, err := db.Exec("INSERT INTO items(id, sku, name, base_price, is_active) VALUES " + sb.String()); err != nil {
			t.Fatalf("bulk insert items: %v", err)
		}
		sb.Reset()
		rowsInBatch = 0
	}
	itemID := func(i int) string { return fmt.Sprintf("bulk-%05d", i) }
	for i := 0; i < n; i++ {
		if rowsInBatch > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "('%s','SKU%05d','Item %05d',100,1)", itemID(i), i, i)
		rowsInBatch++
		if rowsInBatch == insertBatch {
			flush()
		}
	}
	flush()

	// Marker A: first item (chunk 0) — a promotional price override.
	markerEarly := itemID(0)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES ('ph-early', ?, 7, datetime('now','-1 hour'), datetime('now','+1 hour'))`, markerEarly); err != nil {
		t.Fatalf("seed early price override: %v", err)
	}
	// Marker B: the very last item (final, short chunk since n isn't a
	// multiple of 500) — a second promotional price override.
	markerLate := itemID(n - 1)
	if _, err := db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES ('ph-late', ?, 42, datetime('now','-1 hour'), datetime('now','+1 hour'))`, markerLate); err != nil {
		t.Fatalf("seed late price override: %v", err)
	}

	mux := http.NewServeMux()
	dp := &common.Deps{
		Db:    db,
		State: common.RuntimeState{Theme: "default"},
		Menu:  []common.MenuItem{},
	}
	Register(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with %d active items, got %d: %.500s", n, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Every other item keeps base_price=100, so a resolved override price
	// (7, 42) is a unique substring — its presence/absence is exactly the
	// chunking fix's own effect.
	if !strings.Contains(body, `data-price="7"`) {
		t.Fatalf("expected first-chunk item %s to resolve its price_history override (7), not base_price — got no data-price=\"7\" in the response", markerEarly)
	}
	if !strings.Contains(body, `data-price="42"`) {
		t.Fatalf("expected last-chunk item %s to resolve its price_history override (42), not base_price — got no data-price=\"42\" in the response", markerLate)
	}
}
