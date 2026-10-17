package catalogpull

import (
	"errors"
	"strings"
	"testing"
)

// ut-docs#4522: an option set the cloud tombstones while an active item
// still uses it is deactivated — it must not keep its name (a later cloud
// set of that name would be refused) nor be orphaned once the item drops
// it. An option or value that moves between groups or sets in one version
// must apply whatever the rows' cloud_id order.

// shirtUsing is an item row whose option sets are sets.
func shirtUsing(v int64, sets ...string) feedRow {
	in := itemRowData("Shirt", 1500)
	if sets == nil {
		sets = []string{}
	}
	in["option_set_cloud_ids"] = sets
	return feedRow{Kind: "item", CloudID: itemID(1), Version: v, Data: in}
}

func TestPullRetiredOptionSetFreesItsName_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Size", val(1, "S")))
	h.feed.add(shirtUsing(2, cSet))
	h.mustRun()
	h.feed.add(feedRow{Kind: "option_set", CloudID: cSet, Version: 3, Deleted: true})
	h.mustRun()
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ? AND is_active = 0 AND name <> 'Size'`, cSet); n != 1 {
		t.Fatalf("the set in use is not deactivated under a freed name: %s", h.list(`SELECT name || ':' || is_active FROM option_sets`))
	}
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ? AND name LIKE 'Size (%)'`, cSet); n != 1 {
		t.Fatalf("freed name = %q, want it to start with the old name", h.list(`SELECT name FROM option_sets WHERE id = ?`, cSet))
	}
	if n := h.count(`SELECT COUNT(*) FROM item_option_sets WHERE option_set_id = ?`, cSet); n != 1 {
		t.Fatal("retiring the set dropped the item's link")
	}

	h.feed.add(setRow(cSet2, 4, "Size", val(2, "S")))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 1 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("new set of the retired name: result %+v, failures %+v", res, fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ? AND name = 'Size' AND is_active = 1`, cSet2); n != 1 {
		t.Fatal("new set of the retired name not created")
	}
}

func TestPullRetiredOptionSetDeletedOnceUnused_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Size", val(1, "S")))
	h.feed.add(shirtUsing(2, cSet))
	h.mustRun()
	h.feed.add(feedRow{Kind: "option_set", CloudID: cSet, Version: 3, Deleted: true})
	h.mustRun()
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ?`, cSet); n != 1 {
		t.Fatal("set in use deleted")
	}

	// A later version drops the set from the item: nothing in the feed
	// names the set again, so the till must delete it by itself.
	h.feed.add(shirtUsing(4))
	res := h.mustRun()
	if res.Refused != 0 {
		t.Fatalf("result = %+v", res)
	}
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ?`, cSet); n != 0 {
		t.Fatal("retired set orphaned once unused")
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_retired`); n != 0 {
		t.Fatal("retired record kept after the delete")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_catalog_option_set_deleted' AND entity_id = ?`, cSet); n != 1 {
		t.Fatal("the deferred delete is not audited")
	}
}

// The usual shape: the cloud deletes a set and drops it from its last item
// in one transaction. The set's tombstone applies after the item, so the
// set is deleted outright — never deactivated and renamed first.
func TestPullOptionSetTombstoneWithItsLastUserInOneVersion_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Size", val(1, "S")))
	h.feed.add(shirtUsing(2, cSet))
	h.mustRun()
	h.feed.add(feedRow{Kind: "option_set", CloudID: cSet, Version: 3, Deleted: true})
	h.feed.add(shirtUsing(3))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		t.Fatalf("result = %+v", res)
	}
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ?`, cSet); n != 0 {
		t.Fatal("set not deleted")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_catalog_option_set_deactivated'`); n != 0 {
		t.Fatal("set deactivated before its last user dropped it")
	}
}

func TestPullOptionMovedBetweenGroupsInOneVersion_4522(t *testing.T) {
	h := newHarness(t)
	// cGroup sorts before cLateGroup, so in version 2 the group the option
	// moves INTO comes first, while the option is still the other's.
	h.feed.add(groupRow(cGroup, 1, "Milk"))
	h.feed.add(groupRow(cLateGroup, 1, "Syrup", opt(1, "Oat", 40, true)))
	h.mustRun()
	h.feed.add(groupRow(cGroup, 2, "Milk", opt(1, "Oat", 40, true)))
	h.feed.add(groupRow(cLateGroup, 2, "Syrup"))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM item_modifier_options WHERE id = ? AND group_id = ?`, optID(1), cGroup); n != 1 {
		t.Fatal("option not moved to its new group")
	}
	if fails, _ := applyFailures(h.db); len(fails) != 0 {
		t.Fatalf("failures = %+v, want none", fails)
	}
}

func TestPullValueMovedBetweenOptionSetsInOneVersion_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Size"))
	h.feed.add(setRow(cLateSet, 1, "Cup", val(1, "Large")))
	h.mustRun()
	h.feed.add(setRow(cSet, 2, "Size", val(1, "Large")))
	h.feed.add(setRow(cLateSet, 2, "Cup"))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM option_set_values WHERE id = ? AND option_set_id = ?`, valID(1), cSet); n != 1 {
		t.Fatal("value not moved to its new set")
	}
}

// A row that is still refused on its retry is recorded and counted once.
func TestPullRetriedRefusalCountedOnce_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "", val(1, "S")))
	h.feed.add(setRow(cSet2, 1, "Colour", val(2, "Red")))
	res := h.mustRun()
	if res.Refused != 1 || res.Applied != 1 {
		t.Fatalf("result = %+v, want 1 refused and 1 applied", res)
	}
	if fails, _ := applyFailures(h.db); len(fails) != 1 {
		t.Fatalf("failures = %+v, want 1", fails)
	}
}

func TestOrderGroupPutsOptionSetTombstonesAfterTheirUsers_4522(t *testing.T) {
	group := []Row{
		{Kind: "option_set", CloudID: cSet, Version: 1, Deleted: true},
		{Kind: "item_barcode", CloudID: itemID(4), Version: 1},
		{Kind: "item_variant", CloudID: itemID(3), Version: 1},
		{Kind: "item", CloudID: itemID(1), Version: 1},
		{Kind: "option_set", CloudID: cSet2, Version: 1},
	}
	var got []string
	for _, r := range orderGroup(group) {
		k := r.Kind
		if r.Deleted {
			k += "-"
		}
		got = append(got, k)
	}
	if want := "option_set,item,item_variant,option_set-,item_barcode"; strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

// Review finding: a retired set the shop reactivated on the till is its own
// again — the retry drops the record and never deletes the set.
func TestPullRetiredOptionSetReactivatedLocallyIsKept_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Size", val(1, "S")))
	h.feed.add(shirtUsing(2, cSet))
	h.mustRun()
	h.feed.add(feedRow{Kind: "option_set", CloudID: cSet, Version: 3, Deleted: true})
	h.mustRun()
	if _, err := h.db.Exec(`UPDATE option_sets SET is_active = 1 WHERE id = ?`, cSet); err != nil {
		t.Fatal(err)
	}
	h.feed.add(shirtUsing(4))
	h.mustRun()
	if n := h.count(`SELECT COUNT(*) FROM option_sets WHERE id = ? AND is_active = 1`, cSet); n != 1 {
		t.Fatal("a retired set the shop reactivated was deleted")
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_retired`); n != 0 {
		t.Fatal("retired record kept for a reactivated set")
	}
}

// The cursor never passes a version whose retry is still pending: a chunk
// that commits between a row's first attempt and its retry stores the
// previous version, so a crash there re-applies the whole version.
func TestPullMidGroupCommitWithRetryPendingKeepsCursor_4522(t *testing.T) {
	h := newHarness(t)
	h.feed.add(groupRow(cGroup, 1, "Milk"))
	h.feed.add(groupRow(cLateGroup, 1, "Syrup", opt(1, "Oat", 40, true)))
	h.mustRun()
	restore := setCommitChunk(1)
	defer restore()
	h.feed.add(groupRow(cGroup, 2, "Milk", opt(1, "Oat", 40, true)))
	h.feed.add(groupRow(cLateGroup, 2, "Syrup"))
	h.feed.add(itemFeedRow(1, 3, "Latte", 300))
	calls := 0
	undo := setBeforeChunkCommit(func() error {
		calls++
		if calls == 2 { // after cLateGroup, before cGroup's retry
			return errors.New("power cut")
		}
		return nil
	})
	if _, err := h.run(); err == nil {
		t.Fatal("Run survived the injected crash")
	}
	undo()
	if h.cursor() != 1 {
		t.Fatalf("cursor after a crash mid-version = %d, want 1", h.cursor())
	}
	res := h.mustRun()
	if res.Refused != 0 || h.cursor() != 3 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("resume: result %+v cursor %d, failures %+v", res, h.cursor(), fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM item_modifier_options WHERE id = ? AND group_id = ?`, optID(1), cGroup); n != 1 {
		t.Fatal("option not moved after the resume")
	}
}
