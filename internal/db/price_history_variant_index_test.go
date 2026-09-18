package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestPriceHistoryVariantIndexMakesVariantLookupsSargable is ut-docs#2259:
// price_history's only pre-032 index (idx_price_history_item, item_id-
// leading, from 001_init.sql) cannot seek a WHERE variant_id = ? predicate.
// Migration 032 adds idx_price_history_variant (variant_id, starts_at) —
// this runs the real production query shapes from
// POSRepo.lookupPriceHistory and CatalogRepo.ItemVariantsForSale's
// correlated subquery through SQLite's own planner (EXPLAIN QUERY PLAN)
// against a fresh migrated DB and confirms the new index is what the
// planner picks, not SCAN ph + USE TEMP B-TREE FOR ORDER BY.
//
// Deliberately does NOT run ANALYZE — see
// report_query_indexes_test.go's own comment for why: this product never
// calls ANALYZE in production, so the no-stats planner heuristics this
// test exercises are what a real till actually runs under.
func TestPriceHistoryVariantIndexMakesVariantLookupsSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m032-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, name, base_price) VALUES ('i1', 'Item 1', 100)`); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v1', 'i1', 'V1', 150, 1)`); err != nil {
		t.Fatalf("seed item_variants: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO price_history (id, variant_id, price, starts_at) VALUES ('ph1', 'v1', 199, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed price_history: %v", err)
	}

	cases := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{
			// POSRepo.lookupPriceHistory's variant-id shape (pos_repo.go).
			name: "lookupPriceHistory variant_id",
			query: `SELECT price
FROM price_history
WHERE variant_id = ?
  AND datetime(starts_at) <= CURRENT_TIMESTAMP
  AND (ends_at IS NULL OR datetime(ends_at) > CURRENT_TIMESTAMP)
ORDER BY datetime(starts_at) DESC, rowid DESC
LIMIT 1`,
			args:      []any{"v1"},
			wantIndex: "SEARCH price_history USING INDEX idx_price_history_variant",
		},
		{
			// CatalogRepo.ItemVariantsForSale's correlated subquery shape
			// (catalog_repo.go) — the batched, one-round-trip form. The
			// outer query also has its own SEARCH (idx_variants_item), so
			// asserting just "SEARCH ... idx_price_history_variant appears
			// somewhere" would pass even if the inner subquery itself fell
			// back to a full scan — pin the exact inner-alias plan line.
			name: "ItemVariantsForSale correlated subquery",
			query: `SELECT (SELECT ph.price FROM price_history ph
          WHERE ph.variant_id = v.id
            AND datetime(ph.starts_at) <= CURRENT_TIMESTAMP
            AND (ph.ends_at IS NULL OR datetime(ph.ends_at) > CURRENT_TIMESTAMP)
          ORDER BY datetime(ph.starts_at) DESC, ph.rowid DESC LIMIT 1)
FROM item_variants v
WHERE v.is_active = 1 AND v.item_id = ?`,
			args:      []any{"i1"},
			wantIndex: "SEARCH ph USING INDEX idx_price_history_variant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan plan row: %v", err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("plan rows: %v", err)
			}
			full := strings.Join(plan, " | ")
			// Acceptance criteria per ut-docs#2259: an index SEEK on
			// idx_price_history_variant, not a full SCAN of price_history.
			// A residual "USE TEMP B-TREE FOR ORDER BY" is expected and
			// fine here — the ORDER BY sorts on datetime(starts_at), an
			// expression the plain (non-expression) index can't also
			// satisfy, but that sort now runs over the handful of rows
			// the index seek narrowed to for this one variant_id, not
			// price_history's whole ever-growing table.
			if !strings.Contains(full, tc.wantIndex) {
				t.Fatalf("query plan = %q, want it to contain %q (sargable), not a full scan", full, tc.wantIndex)
			}
			if strings.Contains(full, "SCAN price_history") || strings.Contains(full, "SCAN ph") {
				t.Fatalf("query plan = %q, want no full scan of price_history", full)
			}
		})
	}
}
