package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestChunkStrings pins ChunkStrings' boundary behavior directly (no DB) —
// the empty case, an exact multiple of the chunk size, a remainder, and a
// chunk size bigger than the whole input.
func TestChunkStrings(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		size int
		want [][]string
	}{
		{"empty", nil, 3, nil},
		{"smaller than one chunk", []string{"a", "b"}, 5, [][]string{{"a", "b"}}},
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, [][]string{{"a", "b"}, {"c", "d"}}},
		{"remainder", []string{"a", "b", "c", "d", "e"}, 2, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}},
		{"size one", []string{"a", "b"}, 1, [][]string{{"a"}, {"b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkStrings(tc.ids, tc.size)
			if len(got) != len(tc.want) {
				t.Fatalf("ChunkStrings(%v, %d) = %v, want %v", tc.ids, tc.size, got, tc.want)
			}
			for i := range got {
				if strings.Join(got[i], ",") != strings.Join(tc.want[i], ",") {
					t.Fatalf("ChunkStrings(%v, %d)[%d] = %v, want %v", tc.ids, tc.size, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMergeMapInto pins the generic merge helper for both value types
// LoadAllActive actually uses it for.
func TestMergeMapInto(t *testing.T) {
	dst := map[string]bool{"a": true}
	MergeMapInto(dst, map[string]bool{"b": true, "c": false})
	if len(dst) != 3 || !dst["a"] || !dst["b"] || dst["c"] {
		t.Fatalf("unexpected merged bool map: %+v", dst)
	}

	prices := map[string]int64{"x": 100}
	MergeMapInto(prices, map[string]int64{"y": 200})
	if len(prices) != 2 || prices["x"] != 100 || prices["y"] != 200 {
		t.Fatalf("unexpected merged int64 map: %+v", prices)
	}
}

// TestLoadAllActive_BeyondSQLiteBindLimit reproduces ut-docs#2318 for real:
// ItemIDsWithModifiers binds 2 SQL args per item id, so an UNCHUNKED call
// with the whole active-item id set starts failing once the id count
// crosses SQLite's SQLITE_MAX_VARIABLE_NUMBER ceiling (empirically 32766 on
// this repo's modernc.org/sqlite build, i.e. the call breaks past 16,383
// ids). Before the fix, LoadAllActive passed the WHOLE id set to all three
// lookups unchunked, and ItemIDsWithModifiers' error was silently discarded
// (`_`) — so the failure was invisible: no error returned from
// LoadAllActive, but every tile silently lost its modifier prompt (verified:
// this test's own HasModifiers assertion fails against the pre-fix code at
// n=16,400, with the real error swallowed). ItemIDsWithVariants and
// ItemCurrentPrices bind only 1 arg/id, so they don't actually break until
// past ~32,766 items — at n=16,400 they still succeed unchunked; their
// markers below are forward regression guards against a future,
// larger-catalog break (or a change that adds a second bind arg to either
// query), not a second reproduction of THIS specific failure.
//
// This seeds 16,400 active items (comfortably past the 16,384 break point
// that actually bites at this scale — ItemIDsWithModifiers) and plants
// three "marker" items — the first, one at the first chunk boundary, and
// the very last (a short final chunk) — each wired to a real modifier
// group, a real variant, and a real price-history override respectively,
// so the test proves actual correctness at every chunk position, not just
// the absence of an error.
func TestLoadAllActive_BeyondSQLiteBindLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-catalog test in -short mode")
	}
	const n = 16400 // > 16,384: the point ItemIDsWithModifiers' 2-args-per-id starts hitting SQLite's bind-variable ceiling unchunked

	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })

	// Bulk-insert with literal (non-parameterized) values: test-controlled,
	// generated ids/names, and this sidesteps hitting the very bind limit
	// this test is about during its own setup. Batched into statements of
	// 1000 rows each to keep any single SQL string a reasonable size.
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
	for i := 0; i < n; i++ {
		if rowsInBatch > 0 {
			sb.WriteString(",")
		}
		id := itemIDFor(i)
		fmt.Fprintf(&sb, "('%s','SKU%05d','Item %05d',100,1)", id, i, i)
		rowsInBatch++
		if rowsInBatch == insertBatch {
			flush()
		}
	}
	flush()

	// Marker 1: first item (chunk 0, position 0) — real reachable modifier
	// group via the item-direct link.
	markerMod := itemIDFor(0)
	mustExec(t, db, `INSERT INTO item_modifier_groups(id, name, is_active) VALUES ('grp1', 'Size', 1)`)
	mustExec(t, db, `INSERT INTO item_modifier_group_links(item_id, group_id) VALUES (?, 'grp1')`, markerMod)

	// Marker 2: item 499 — last item of the first 500-id chunk — a real
	// active, sellable variant (has a barcode, so ItemIDsWithVariants
	// counts it).
	markerVariant := itemIDFor(499)
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES ('var1', ?, 'VARSKU1', 'Large', 150, 1)`, markerVariant)
	mustExec(t, db, `INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES ('VARBC1', 'var1', 1)`)

	// Marker 3: the very last item (index n-1, in a short final chunk since
	// 16400 is not a multiple of 500) — an active promotional price
	// override below its base_price of 100.
	markerPrice := itemIDFor(n - 1)
	mustExec(t, db, `INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES ('ph1', ?, 42, datetime('now','-1 hour'), datetime('now','+1 hour'))`, markerPrice)

	store := NewButtonStore(db)
	buttons, err := store.LoadAllActive(context.Background())
	if err != nil {
		t.Fatalf("LoadAllActive with %d active items: %v", n, err)
	}
	if len(buttons) != n {
		t.Fatalf("LoadAllActive returned %d buttons, want %d", len(buttons), n)
	}

	byID := make(map[string]Button, len(buttons))
	for _, b := range buttons {
		byID[b.ItemID] = b
	}

	if b := byID[markerMod]; !b.HasModifiers {
		t.Fatalf("expected %s (first chunk, index 0) to resolve HasModifiers=true, got %+v", markerMod, b)
	}
	if b := byID[markerVariant]; !b.HasVariants {
		t.Fatalf("expected %s (index 499, first chunk boundary) to resolve HasVariants=true, got %+v", markerVariant, b)
	}
	if b := byID[markerPrice]; b.Price != 42 {
		t.Fatalf("expected %s (last item, final short chunk) to resolve the price-history override (42), got price=%d", markerPrice, b.Price)
	}

	// A plain item with no modifier/variant/price-override row must still
	// resolve to its own base price and no false-positive flags, regardless
	// of which chunk it falls in.
	plain := itemIDFor(8000) // chunk 16 (0-indexed), nowhere near a marker
	b, ok := byID[plain]
	if !ok {
		t.Fatalf("expected plain item %s to be present", plain)
	}
	if b.HasModifiers || b.HasVariants {
		t.Fatalf("expected plain item %s to have no modifier/variant flags, got %+v", plain, b)
	}
	if b.Price != 100 {
		t.Fatalf("expected plain item %s to fall back to its base_price (100), got %d", plain, b.Price)
	}
}

func itemIDFor(i int) string {
	return "bulk-" + zeroPad(i)
}

func zeroPad(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}
