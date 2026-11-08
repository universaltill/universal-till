package catalogpull

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

// ut-docs#5871 (#5860 part 3): the pull applies the single-line name rule to
// every pulled name. A refused row keeps the till's previous version, is
// recorded and reported, and never wedges the feed.

func TestPullRefusesABadNameKeepsPreviousVersion(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Flat White", 100))
	h.mustRun()

	h.feed.add(itemFeedRow(1, 2, "Flat\u202eWhite", 100)) // an update with a bad name
	h.feed.add(itemFeedRow(2, 2, "Neighbour", 100))
	h.feed.add(itemFeedRow(3, 2, "a\u200bb", 100)) // a create with a bad name
	res := h.mustRun()

	if res.Refused != 2 {
		t.Fatalf("result = %+v, want 2 refused", res)
	}
	if it, _ := h.item(itemID(1)); it.Name != "Flat White" {
		t.Fatalf("refused update changed the item: %+v", it)
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = 'item' AND cloud_id = ? AND version = 1`, itemID(1)); n != 1 {
		t.Fatal("the refused row's link did not keep its previous version")
	}
	if _, ok := h.item(itemID(3)); ok {
		t.Fatal("the refused create was written")
	}
	if _, ok := h.item(itemID(2)); !ok {
		t.Fatal("a chunk-mate rolled back with a refused row")
	}
	if h.cursor() != 2 {
		t.Fatalf("cursor = %d, want 2: refused rows never wedge the feed", h.cursor())
	}
	fails, err := applyFailures(h.db)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]applyFailure{}
	for _, f := range fails {
		byID[f.CloudID] = f
	}
	if f := byID[itemID(1)]; f.Version != 2 || !strings.Contains(f.Reason, "control or invisible") {
		t.Fatalf("failure for item 1 = %+v", f)
	}
	if _, ok := byID[itemID(3)]; !ok || len(fails) != 2 {
		t.Fatalf("failures = %+v, want items 1 and 3", fails)
	}

	// A later good version applies and clears the failure.
	h.feed.add(itemFeedRow(1, 3, "Flat White Large", 100))
	h.mustRun()
	if it, _ := h.item(itemID(1)); it.Name != "Flat White Large" {
		t.Fatalf("later version not applied: %+v", it)
	}
	if fails, _ = applyFailures(h.db); len(fails) != 1 {
		t.Fatalf("failure of item 1 not cleared: %+v", fails)
	}
}

func TestPullItemNamesRangeNameCases(t *testing.T) {
	h := newHarness(t)
	type want struct {
		ok   bool
		name string
	}
	wants := map[string]want{}
	refused := 0
	for i, c := range namecases.Cases() {
		if !utf8.ValidString(c.In) {
			continue // JSON cannot carry invalid UTF-8; the till-side table test covers it
		}
		h.feed.add(itemFeedRow(10+i, 1, c.In, 100))
		wants[itemID(10+i)] = want{c.OK, c.Want}
		if !c.OK {
			refused++
		}
	}
	res := h.mustRun()
	if res.Refused != refused {
		t.Fatalf("result = %+v, want %d refused", res, refused)
	}
	fails, err := applyFailures(h.db)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, f := range fails {
		failed[f.CloudID] = true
	}
	for id, w := range wants {
		it, found := h.item(id)
		switch {
		case w.ok && (!found || it.Name != w.name || failed[id]):
			t.Errorf("item %s: found=%v name=%q failed=%v, want stored %q", id, found, it.Name, failed[id], w.name)
		case !w.ok && (found || !failed[id]):
			t.Errorf("item %s: found=%v failed=%v, want refused and recorded", id, found, failed[id])
		}
	}
	if h.cursor() == 0 {
		t.Fatal("cursor did not advance")
	}
}

func TestPullOptionSetBadNewValueRefusedWholeRow(t *testing.T) {
	h := newHarness(t)
	h.feed.add(setRow(cSet, 1, "Sizes", val(1, "Small"), val(2, "Large")))
	h.mustRun()
	h.feed.add(setRow(cSet, 2, "Sizes", val(1, "Small"), val(2, "Large"), val(3, "x\u200by")))
	res := h.mustRun()
	if res.Refused != 1 {
		t.Fatalf("result = %+v, want 1 refused", res)
	}
	if got := h.list(`SELECT value FROM option_set_values ORDER BY sort_order`); got != "Small,Large" {
		t.Fatalf("values = %q, want the old Small,Large untouched", got)
	}
}

// A till that stored a name before the rule keeps accepting my.'s resend of
// it with another field changed (#5860 R8), but refuses a different bad name.
func TestPullResendsStoredLegacyBadNameUnchanged(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Cola", 100))
	h.mustRun()
	const legacy = "Co\u202ela"
	if _, err := h.db.Exec(`UPDATE items SET name = ? WHERE id = ?`, legacy, itemID(1)); err != nil {
		t.Fatal(err)
	}

	h.feed.add(itemFeedRow(1, 2, legacy, 150))
	if res := h.mustRun(); res.Refused != 0 {
		t.Fatalf("result = %+v: the unchanged legacy name was refused", res)
	}
	if it, _ := h.item(itemID(1)); it.Price != 150 || it.Name != legacy {
		t.Fatalf("item = %+v, want price 150 and the legacy name kept", it)
	}

	h.feed.add(itemFeedRow(1, 3, "Co\u202dla", 150))
	if res := h.mustRun(); res.Refused != 1 {
		t.Fatalf("result = %+v, want the different bad name refused", res)
	}
	if it, _ := h.item(itemID(1)); it.Name != legacy {
		t.Fatalf("refused rename changed the item: %+v", it)
	}
}
