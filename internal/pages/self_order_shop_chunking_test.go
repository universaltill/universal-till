package pages

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestLoadShopItems_BeyondSQLiteBindLimit is the kiosk-grid counterpart of
// ut-docs#2318's internal/ui.TestLoadAllActive_BeyondSQLiteBindLimit —
// loadShopItems (self_order_shop.go) has the identical unchunked-lookup
// bug ut-docs#2451 reports: ItemIDsWithModifiers binds 2 SQL args per item
// id, so an unchunked call with the WHOLE active-item id set starts failing
// once the id count crosses SQLite's bind-variable ceiling (empirically
// 32766 on this repo's modernc.org/sqlite build, i.e. past 16,383 ids) —
// worse here than in LoadAllActive's pre-fix state, since loadShopItems
// discards that error outright (`_`) rather than even logging a warning.
//
// Same shape as the ui-package test: 16,400 active items (past the
// 16,384 break point ItemIDsWithModifiers actually hits at this scale).
// ItemIDsWithVariants and ItemCurrentPrices bind only 1 arg/id, so they
// don't actually break until past ~32,766 items — at n=16,400 they still
// succeed even unchunked; their markers are forward regression guards
// against a future, larger-catalog break, not a second reproduction of
// THIS specific failure (same caveat the ui-package precedent's own
// comment makes). Two modifier markers — one in chunk 0 (where the
// bug actually reproduces pre-fix) and one in a later chunk — guard
// against a chunking bug that only breaks past the first chunk (a real,
// independently-review-caught gap: a `break` after the first chunk's
// merge left every subsequent chunk's modifier flags silently dropped,
// and only the chunk-0-only version of this test failed to catch it).
func TestLoadShopItems_BeyondSQLiteBindLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-catalog test in -short mode")
	}
	const n = 16400 // > 16,384: where ItemIDsWithModifiers' 2-args-per-id starts hitting SQLite's bind-variable ceiling unchunked

	dp, d := setupSelfOrderShopDeps(t)

	// Bulk-insert with literal (non-parameterized) values, batched into
	// statements of 1000 rows each — same technique as the ui-package
	// test, and for the same reason: sidesteps hitting the very bind
	// limit this test is about during its own setup. Every row gets a
	// SKU so loadShopItems' `code == ""` skip (self_order_shop.go) never
	// drops it.
	const insertBatch = 1000
	var sb strings.Builder
	rowsInBatch := 0
	flush := func() {
		if rowsInBatch == 0 {
			return
		}
		if _, err := d.DB.Exec("INSERT INTO items(id, sku, name, base_price, is_active) VALUES " + sb.String()); err != nil {
			t.Fatalf("bulk insert items: %v", err)
		}
		sb.Reset()
		rowsInBatch = 0
	}
	for i := 0; i < n; i++ {
		if rowsInBatch > 0 {
			sb.WriteString(",")
		}
		id := shopChunkItemIDFor(i)
		fmt.Fprintf(&sb, "('%s','SKU%05d','Item %05d',100,1)", id, i, i)
		rowsInBatch++
		if rowsInBatch == insertBatch {
			flush()
		}
	}
	flush()

	// Marker 1: first item (chunk 0, position 0) — real reachable modifier
	// group via the item-direct link.
	markerMod := shopChunkItemIDFor(0)
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_groups(id, name, is_active) VALUES ('grp1', 'Size', 1)`); err != nil {
		t.Fatalf("seed modifier group: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_group_links(item_id, group_id) VALUES (?, 'grp1')`, markerMod); err != nil {
		t.Fatalf("seed modifier link: %v", err)
	}

	// Marker 1b: item 12000 — chunk 24, well past the first chunk — a
	// second modifier link via the same group. Without this, a chunking
	// bug that only drops results past chunk 0 (e.g. a loop that merges
	// only the first chunk) would pass this test undetected.
	markerModLate := shopChunkItemIDFor(12000)
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_group_links(item_id, group_id) VALUES (?, 'grp1')`, markerModLate); err != nil {
		t.Fatalf("seed late modifier link: %v", err)
	}

	// Marker 2: item 499 — last item of the first 500-id chunk — a real
	// active, sellable variant (has a barcode, so ItemIDsWithVariants
	// counts it).
	markerVariant := shopChunkItemIDFor(499)
	if _, err := d.DB.Exec(`INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES ('var1', ?, 'VARSKU1', 'Large', 150, 1)`, markerVariant); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES ('VARBC1', 'var1', 1)`); err != nil {
		t.Fatalf("seed variant barcode: %v", err)
	}

	// Marker 3: the very last item (index n-1, in a short final chunk since
	// 16400 is not a multiple of 500) — an active promotional price
	// override below its base_price of 100.
	markerPrice := shopChunkItemIDFor(n - 1)
	if _, err := d.DB.Exec(`INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES ('ph1', ?, 42, datetime('now','-1 hour'), datetime('now','+1 hour'))`, markerPrice); err != nil {
		t.Fatalf("seed price override: %v", err)
	}

	items, err := loadShopItems(context.Background(), dp)
	if err != nil {
		t.Fatalf("loadShopItems with %d active items: %v", n, err)
	}
	if len(items) != n {
		t.Fatalf("loadShopItems returned %d items, want %d", len(items), n)
	}

	byID := make(map[string]shopItem, len(items))
	for _, it := range items {
		byID[it.ItemID] = it
	}

	if it := byID[markerMod]; !it.HasModifiers {
		t.Fatalf("expected %s (first chunk, index 0) to resolve HasModifiers=true, got %+v", markerMod, it)
	}
	if it := byID[markerModLate]; !it.HasModifiers {
		t.Fatalf("expected %s (chunk 24, index 12000) to resolve HasModifiers=true, got %+v", markerModLate, it)
	}
	if it := byID[markerVariant]; !it.HasVariants {
		t.Fatalf("expected %s (index 499, first chunk boundary) to resolve HasVariants=true, got %+v", markerVariant, it)
	}
	if it := byID[markerPrice]; it.PriceMinor != 42 {
		t.Fatalf("expected %s (last item, final short chunk) to resolve the price-history override (42), got price=%d", markerPrice, it.PriceMinor)
	}

	// A plain item with no modifier/variant/price-override row must still
	// resolve to its own base price and no false-positive flags, regardless
	// of which chunk it falls in.
	plain := shopChunkItemIDFor(8000) // chunk 16 (0-indexed), nowhere near a marker
	it, ok := byID[plain]
	if !ok {
		t.Fatalf("expected plain item %s to be present", plain)
	}
	if it.HasModifiers || it.HasVariants {
		t.Fatalf("expected plain item %s to have no modifier/variant flags, got %+v", plain, it)
	}
	if it.PriceMinor != 100 {
		t.Fatalf("expected plain item %s to fall back to its base_price (100), got %d", plain, it.PriceMinor)
	}
}

func shopChunkItemIDFor(i int) string {
	return "bulk-" + shopChunkZeroPad(i)
}

func shopChunkZeroPad(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}
