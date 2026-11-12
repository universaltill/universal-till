package catalogpull

import (
	"sort"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6072: the cloud has no write path for an item's unit, colour,
// item type, stock/sell-screen flags and link lists (nor a category's
// colour, sell-screen flag and modifier groups), yet the feed always
// carries them at the schema defaults. On a row the till already has, the
// pull and an upload answer must leave the till's values alone; a create
// applies the row as served.

type tillOwnedItem struct {
	Color, Unit, ItemType      string
	Untracked, Hidden, Removed bool
}

func (h *harness) tillOwned(id string) tillOwnedItem {
	h.t.Helper()
	it, ok := h.item(id)
	if !ok {
		h.t.Fatalf("item %s missing", id)
	}
	return tillOwnedItem{it.Color, it.Unit, it.ItemType, it.Untracked, it.Hidden, it.Removed}
}

// setTillOwned is what the operator set on the till.
func (h *harness) setTillOwned(id string) tillOwnedItem {
	h.t.Helper()
	if _, err := h.db.Exec(`UPDATE items SET color = '#0f172a', unit = 'can', item_type = 'gift_voucher',
		stock_untracked = 1, sell_screen_hidden = 1, sell_screen_removed = 1 WHERE id = ?`, id); err != nil {
		h.t.Fatal(err)
	}
	return tillOwnedItem{"#0f172a", "can", "gift_voucher", true, true, true}
}

func TestPullKeepsTillOwnedItemFields_6072(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Cola", 250))
	h.mustRun()
	want := h.setTillOwned(itemID(1))

	v2 := itemRowData("Cola", 300) // every till-owned field at the cloud's default
	v2["description"] = "Can, 330 ml"
	v2["is_weighed"] = true
	v2["age_restricted"] = true
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Data: v2})
	h.mustRun()

	it, _ := h.item(itemID(1))
	if it.Price != 300 || it.Desc != "Can, 330 ml" || !it.Weighed || !it.AgeRestricted {
		t.Fatalf("mastered fields not applied: %+v", it)
	}
	if got := h.tillOwned(itemID(1)); got != want {
		t.Fatalf("till-owned fields = %+v, want the till's %+v kept", got, want)
	}
	if h.cursor() != 2 {
		t.Fatalf("cursor = %d, want high_water 2", h.cursor())
	}
}

func TestPullKeepsItemLinkLists_6072(t *testing.T) {
	h := newHarness(t)
	h.feed.add(groupRow(cGroup, 1, "Milk", opt(1, "Oat", 40, true)))
	h.feed.add(groupRow(cGroup2, 2, "Size", opt(2, "Large", 50, true)))
	h.feed.add(setRow(cSet, 3, "Cup", val(1, "Paper")))
	it := itemRowData("Latte", 300)
	it["modifier_group_cloud_ids"] = []string{cGroup}
	it["modifier_opt_out_cloud_ids"] = []string{cGroup2}
	it["option_set_cloud_ids"] = []string{cSet}
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 4, Data: it})
	h.mustRun() // a create applies the lists as served
	links := func() string {
		return h.list(`SELECT group_id FROM item_modifier_group_links WHERE item_id = ?`, itemID(1)) + "|" +
			h.list(`SELECT group_id FROM item_modifier_group_opt_outs WHERE item_id = ?`, itemID(1)) + "|" +
			h.list(`SELECT option_set_id FROM item_option_sets WHERE item_id = ?`, itemID(1))
	}
	want := cGroup + "|" + cGroup2 + "|" + cSet
	if got := links(); got != want {
		t.Fatalf("created links = %q, want %q", got, want)
	}
	h.feed.add(itemFeedRow(1, 5, "Latte", 320)) // every list []
	h.mustRun()
	if it, _ := h.item(itemID(1)); it.Price != 320 {
		t.Fatalf("price = %d, want 320 applied", it.Price)
	}
	if got := links(); got != want {
		t.Fatalf("links after the pull = %q, want the till's %q kept", got, want)
	}
}

func TestPullKeepsTillOwnedCategoryFields_6072(t *testing.T) {
	h := newHarness(t)
	h.feed.add(groupRow(cGroup, 1, "Milk", opt(1, "Oat", 40, true)))
	cat := catRow(cCat, 2, "Coffee", nil)
	cat.Data["modifier_group_cloud_ids"] = []string{cGroup}
	h.feed.add(cat)
	h.mustRun()
	if _, err := h.db.Exec(`UPDATE categories SET color = '#0f172a', sell_screen_hidden = 1 WHERE id = ?`, cCat); err != nil {
		t.Fatal(err)
	}
	h.feed.add(catRow(cCat, 3, "Hot drinks", nil))
	h.mustRun()
	var name, color string
	var hidden bool
	if err := h.db.QueryRow(`SELECT name, COALESCE(color,''), sell_screen_hidden FROM categories WHERE id = ?`, cCat).Scan(&name, &color, &hidden); err != nil {
		t.Fatal(err)
	}
	if name != "Hot drinks" {
		t.Fatalf("name = %q, want the cloud's rename applied", name)
	}
	if color != "#0f172a" || !hidden {
		t.Fatalf("category color %q hidden %v, want the till's kept", color, hidden)
	}
	if got := h.list(`SELECT group_id FROM category_modifier_group_links WHERE category_id = ?`, cCat); got != cGroup {
		t.Fatalf("category groups = %q, want %q kept", got, cGroup)
	}
}

func TestPushAnswerKeepsTillOwnedFields_6072(t *testing.T) {
	keep := func(t *testing.T, h *pushHarness, id string) {
		t.Helper()
		it, _ := h.item(id)
		if it.Color != "#0f172a" || !it.Hidden || it.ItemType != "gift_voucher" {
			t.Fatalf("till-owned fields = colour %q hidden %v type %q, want the till's kept", it.Color, it.Hidden, it.ItemType)
		}
	}
	patch := func(id string, price int64) data.ItemPatch {
		return data.ItemPatch{ID: id, PriceMinor: ptrTo(price), Color: ptrTo("#0f172a"),
			SellScreenHidden: ptrTo(true), ItemType: ptrTo("gift_voucher")}
	}

	t.Run("accepted", func(t *testing.T) {
		h := newPushHarness(t)
		id := h.seedPulled(1, "Latte", 250)
		h.saveItem(patch(id, 280))
		if p := h.push(); p.Accepted != 1 {
			t.Fatalf("push = %+v", p)
		}
		if it, _ := h.item(id); it.Price != 280 {
			t.Fatalf("price = %d, want 280", it.Price)
		}
		keep(t, h, id)
	})
	t.Run("conflict", func(t *testing.T) {
		h := newPushHarness(t)
		id := h.seedPulled(1, "Latte", 250)
		h.myEdit("item", id, map[string]any{"price_minor": 300})
		h.saveItem(patch(id, 280))
		if p := h.push(); p.Conflicts != 1 {
			t.Fatalf("push = %+v", p)
		}
		if it, _ := h.item(id); it.Price != 300 {
			t.Fatalf("price = %d, want the cloud's 300", it.Price)
		}
		keep(t, h, id)
	})
	t.Run("till create", func(t *testing.T) {
		h := newPushHarness(t)
		p := patch("till-made-1", 300)
		p.Create, p.Name, p.SKU = true, ptrTo("Voucher"), ptrTo("V-1")
		h.saveItem(p)
		if res := h.push(); res.Accepted != 1 {
			t.Fatalf("push = %+v", res)
		}
		keep(t, h, "till-made-1")
	})
}

func TestFullRePullKeepsTillOwnedFields_6072(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Cola", 250))
	h.mustRun()
	want := h.setTillOwned(itemID(1))

	// An applier bump re-pulls from 0.
	if err := data.NewSettingsRepo(h.db).Set(t.Context(), KeyApplierVersion, "3"); err != nil {
		t.Fatal(err)
	}
	h.mustRun()
	if got := h.tillOwned(itemID(1)); got != want {
		t.Fatalf("after the applier re-pull = %+v, want %+v", got, want)
	}

	// A 410 resync does the same.
	h.feed.add(itemFeedRow(1, 2, "Cola", 260))
	h.feed.goneOnce = true
	h.mustRun()
	if it, _ := h.item(itemID(1)); it.Price != 260 {
		t.Fatalf("price = %d, want 260", it.Price)
	}
	if got := h.tillOwned(itemID(1)); got != want {
		t.Fatalf("after the 410 resync = %+v, want %+v", got, want)
	}
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Every field the feed's item and category rows carry is exactly one of:
// cloud-written (applied on update), till-owned (kept), or icon (applied
// until ut-docs#6071). A new wire field must be placed.
func TestTillOwnedFieldsPartitionTheFeedRow_6072(t *testing.T) {
	check := func(kind string, row map[string]any, written []string) {
		t.Helper()
		seen := map[string]string{}
		put := func(set string, names []string) {
			for _, n := range names {
				if prev, dup := seen[n]; dup {
					t.Errorf("%s: %q is in both %s and %s", kind, n, prev, set)
				}
				seen[n] = set
			}
		}
		put("cloud-written", written)
		put("till-owned", tillOwnedFields[kind])
		put("icon (interim)", []string{"icon"})
		for _, k := range keysOf(row) {
			if _, ok := seen[k]; !ok {
				t.Errorf("%s: feed field %q is in no set: decide whether the cloud writes it", kind, k)
			}
			delete(seen, k)
		}
		for n, set := range seen {
			t.Errorf("%s: %q is in %s but not a feed field", kind, n, set)
		}
		for _, f := range tillOwnedFields[kind] {
			for _, u := range uploadFields[kind] {
				if f == u {
					t.Errorf("%s: %q is both till-owned and uploaded", kind, f)
				}
			}
		}
	}
	check(kindItem, itemRowData("x", 1), []string{"name", "sku", "description", "price_minor", "category_cloud_id",
		"tax_code_cloud_id", "active", "is_weighed", "age_restricted", "net_quantity_value", "net_quantity_unit"})
	check(kindCategory, catRow(cCat, 1, "x", nil).Data, []string{"name", "parent_cloud_id", "sort_order", "active"})
}

// icon is applied as served on an update until ut-docs#6071 decides it:
// #6071 flips these assertions.
func TestPullAppliesIconAsServedUntil6071(t *testing.T) {
	h := newHarness(t)
	h.feed.add(catRow(cCat, 1, "Coffee", nil))
	h.feed.add(itemFeedRow(1, 2, "Latte", 300))
	h.mustRun()
	cat := catRow(cCat, 3, "Coffee", nil)
	cat.Data["icon"] = "lucide:coffee"
	h.feed.add(cat)
	it := itemRowData("Latte", 300)
	it["icon"] = "lucide:coffee"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 4, Data: it})
	h.mustRun()
	if got := h.list(`SELECT COALESCE(icon,'') FROM items WHERE id = ?`, itemID(1)); got != "lucide:coffee" {
		t.Fatalf("item icon = %q, want the served icon applied", got)
	}
	if got := h.list(`SELECT COALESCE(icon,'') FROM categories WHERE id = ?`, cCat); got != "lucide:coffee" {
		t.Fatalf("category icon = %q, want the served icon applied", got)
	}
}

// AC6 (Tester): on a linked row the mastered fields' clear cases still apply
// — description null, category null, net quantity both null, tax code null —
// while the till-owned fields beside them are kept.
func TestPullStillClearsMasteredFieldsOnALinkedRow_6072(t *testing.T) {
	h := newHarness(t)
	h.feed.add(taxRow(1))
	h.feed.add(catRow(cCat, 2, "Drinks", nil))
	v1 := itemRowData("Cola", 250)
	v1["description"] = "Can, 330 ml"
	v1["category_cloud_id"] = cCat
	v1["tax_code_cloud_id"] = cTax
	v1["net_quantity_value"] = 330
	v1["net_quantity_unit"] = "ml"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 3, Data: v1})
	h.mustRun()
	if it, _ := h.item(itemID(1)); it.Category != cCat || it.TaxCode != cTax || it.Desc == "" || !it.NQValue.Valid {
		t.Fatalf("created item = %+v", it)
	}
	want := h.setTillOwned(itemID(1))

	v2 := itemRowData("Cola", 250)
	v2["description"] = nil
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 4, Data: v2})
	h.mustRun()
	it, _ := h.item(itemID(1))
	if it.Desc != "" || it.Category != "" || it.TaxCode != "" || it.NQValue.Valid || it.NQUnit.Valid {
		t.Fatalf("mastered clears not applied on a linked row: %+v", it)
	}
	if got := h.tillOwned(itemID(1)); got != want {
		t.Fatalf("till-owned fields = %+v, want %+v kept", got, want)
	}
}
