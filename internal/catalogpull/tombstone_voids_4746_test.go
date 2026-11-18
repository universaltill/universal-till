package catalogpull

import "testing"

// ut-docs#4746 (independent review): a cloud tombstone deletes a never-sold
// item that was only voided, through the same delete rule as delete_item,
// and its audit row records the detached voids ("item deleted with 1
// void") just as the delete_item directive's does.
func TestPullTombstoneOfVoidOnlyItemAuditsTheVoids_4746(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Voided once", 100))
	h.mustRun()
	if _, err := h.db.Exec(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
		VALUES ('se-1', 'void', ?, 'Voided once', 1, 100, 100, '2026-10-21T21:42:44Z')`, itemID(1)); err != nil {
		t.Fatal(err)
	}
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Deleted: true})
	h.mustRun()
	if _, ok := h.item(itemID(1)); ok {
		t.Fatal("void-only item not deleted by its tombstone")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action = 'cloud_catalog_item_deleted'
		AND json_extract(data_json, '$.voids') = 1 AND json_extract(data_json, '$.note') = 'item deleted with 1 void'`, itemID(1)); n != 1 {
		t.Fatalf("audit rows naming the voids = %d, want 1", n)
	}
}
