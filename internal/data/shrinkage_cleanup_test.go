package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3394: shrinkage_events.item_id is a FOREIGN KEY to items(id) with
// no ON DELETE action (035_shrinkage_events.sql). An item that was only ever
// voided/comped/wasted has a shrinkage_events row but no sale_lines/
// stock_movements row, so it looked "never sold"; the DELETE FROM items hit
// the FK and rolled back the whole cleanup. Such an item must be KEPT and
// the rest of the purge must still go through.
func TestCleanupObsoleteItems_KeepsItemWithOnlyAShrinkageEvent(t *testing.T) {
	for _, includeActive := range []bool{false, true} {
		d, x, _ := resetTestDB(t, "cleanup-shrinkage.db")
		x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('voided','Old Voided Item',300,0)`)
		x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
		   VALUES ('se1','void','voided','Old Voided Item',1,300,300,'2026-01-01T00:00:00Z')`)
		x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs','Old Test Product',100,0)`)

		repo := data.NewPOSRepo(d.DB)
		ctx := context.Background()
		preview, err := repo.ListObsoleteItems(ctx, 100, includeActive)
		if err != nil {
			t.Fatalf("includeActive=%v ListObsoleteItems: %v", includeActive, err)
		}
		for _, it := range preview {
			if it.ID == "voided" {
				t.Fatalf("includeActive=%v: item with a shrinkage event must not be previewed as obsolete", includeActive)
			}
		}
		if _, err := repo.CleanupObsoleteItems(ctx, "", "", includeActive); err != nil {
			t.Fatalf("includeActive=%v cleanup must not fail on a shrinkage-referenced item: %v", includeActive, err)
		}
		has := func(id string) bool {
			var c int
			_ = d.DB.QueryRow(`SELECT count(*) FROM items WHERE id = ?`, id).Scan(&c)
			return c == 1
		}
		if !has("voided") {
			t.Fatalf("includeActive=%v: item referenced by a shrinkage event was removed", includeActive)
		}
		if has("obs") {
			t.Fatalf("includeActive=%v: genuinely obsolete item not removed — the purge did not go through", includeActive)
		}
	}
}
