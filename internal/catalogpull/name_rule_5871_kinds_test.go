package catalogpull

import (
	"strings"
	"testing"
)

// ut-docs#5871 (Tester gap): the item and option-set pull tests do not reach
// the category, tax-code, modifier-group/option and variant apply paths. Each
// kind here gets a good v1, then a v2 with a bad name: v2 is refused, the
// till keeps v1's name and link version, a failure is recorded, and a
// chunk-mate still applies.
func TestPullRefusesBadNamesOnEveryNamedKind(t *testing.T) {
	const rlo = "\u202e"
	type kind struct {
		label, entity, cloudID string
		good, bad              func(v int64) feedRow
		name                   func(h *harness) string
	}
	kinds := []kind{
		{label: "tax_code", entity: "tax_code", cloudID: cTax,
			good: func(v int64) feedRow { return namedTaxRow(cTax, v, "Standard", 2000) },
			bad:  func(v int64) feedRow { return namedTaxRow(cTax, v, "Stan"+rlo+"dard", 2000) },
			name: func(h *harness) string { return h.list(`SELECT name FROM tax_codes WHERE id = ?`, cTax) }},
		{label: "category", entity: "category", cloudID: cCat,
			good: func(v int64) feedRow { return catRow(cCat, v, "Drinks", nil) },
			bad:  func(v int64) feedRow { return catRow(cCat, v, "Dri\u200bnks", nil) },
			name: func(h *harness) string { return h.list(`SELECT name FROM categories WHERE id = ?`, cCat) }},
		{label: "modifier_group name", entity: "modifier_group", cloudID: cGroup,
			good: func(v int64) feedRow { return groupRow(cGroup, v, "Milk", opt(1, "Oat", 0, true)) },
			bad:  func(v int64) feedRow { return groupRow(cGroup, v, "Mi\u2028lk", opt(1, "Oat", 0, true)) },
			name: func(h *harness) string { return h.list(`SELECT name FROM item_modifier_groups WHERE id = ?`, cGroup) }},
		{label: "modifier option name", entity: "modifier_group", cloudID: cGroup,
			good: func(v int64) feedRow { return groupRow(cGroup, v, "Milk", opt(1, "Oat", 0, true)) },
			bad: func(v int64) feedRow {
				return groupRow(cGroup, v, "Milk", opt(1, "Oat", 0, true), opt(2, "So\x1bya", 0, true))
			},
			name: func(h *harness) string {
				return h.list(`SELECT name FROM item_modifier_options WHERE group_id = ? ORDER BY sort_order`, cGroup)
			}},
	}
	for _, k := range kinds {
		t.Run(k.label, func(t *testing.T) {
			h := newHarness(t)
			h.feed.add(k.good(1))
			h.mustRun()
			before := k.name(h)
			h.feed.add(k.bad(2))
			h.feed.add(itemFeedRow(9, 2, "Neighbour", 100))
			res := h.mustRun()
			if res.Refused != 1 {
				fails, _ := applyFailures(h.db)
				t.Fatalf("result = %+v, want 1 refused; failures %+v", res, fails)
			}
			if got := k.name(h); got != before {
				t.Fatalf("name(s) = %q after a refused row, want %q", got, before)
			}
			if n := h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = ? AND cloud_id = ? AND version = 1`, k.entity, k.cloudID); n != 1 {
				t.Fatal("the refused row's link did not keep version 1")
			}
			if _, ok := h.item(itemID(9)); !ok {
				t.Fatal("a chunk-mate rolled back with the refused row")
			}
			fails, err := applyFailures(h.db)
			if err != nil {
				t.Fatal(err)
			}
			if len(fails) != 1 || fails[0].CloudID != k.cloudID || !strings.Contains(fails[0].Reason, "control or invisible") {
				t.Fatalf("failures = %+v", fails)
			}
		})
	}

	t.Run("item_variant", func(t *testing.T) {
		h := newHarness(t)
		h.shirt(1)
		h.feed.add(variantRow(variantID(1), 3, variantData(itemID(1), "Small", "", 1500, valID(1))))
		h.mustRun()
		h.feed.add(variantRow(variantID(1), 4, variantData(itemID(1), "Sm"+rlo+"all", "", 1500, valID(1))))
		h.feed.add(variantRow(variantID(2), 4, variantData(itemID(1), "Med\u2060ium", "", 1600, valID(2))))
		res := h.mustRun()
		if res.Refused != 2 {
			t.Fatalf("result = %+v, want the bad rename and the bad create refused", res)
		}
		if v, _ := h.variant(variantID(1)); v.Name != "Small" {
			t.Fatalf("variant = %+v, want the name kept", v)
		}
		if _, ok := h.variant(variantID(2)); ok {
			t.Fatal("the refused variant create was written")
		}
	})
}
