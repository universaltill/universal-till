package catalogpull

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4623 (from the ut-docs#4464 review: L-2 and delta D-1).

// L-2: a category or tax code the till hard-deletes (the demo-data
// removal, remove_demo*.sql) while it is linked to the cloud uploads as
// the contract's deactivation — an update of active, since a till never
// deletes either kind (cloud-catalog-changes.md, "op") — instead of
// being dropped, which left the cloud's row live for the next pull to
// bring back.
func TestPushLocalHardDeleteOfACategoryOrTaxCodeUploadsADeactivation(t *testing.T) {
	h := newPushHarness(t)
	h.feed.add(taxRow(1))
	h.feed.add(catRow(cCat, 2, "Drinks", nil))
	h.mustRun()
	h.exec(`DELETE FROM categories WHERE id = ?`, cCat)
	h.exec(`DELETE FROM tax_codes WHERE id = ?`, cTax)
	p := h.push()
	if p.Accepted != 2 || p.Conflicts != 0 || p.Pending != 0 {
		t.Fatalf("push = %+v, want both deactivations accepted", p)
	}
	chs := h.sentChanges()
	if len(chs) != 2 {
		t.Fatalf("sent %d changes, want 2: %+v", len(chs), chs)
	}
	for _, ch := range chs {
		if ch.Op != opUpdate || len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "active" ||
			jsonOf(t, ch.Before["active"]) != true || jsonOf(t, ch.After["active"]) != false || len(ch.After) != 1 {
			t.Fatalf("change = %+v, want update active true → false", ch)
		}
	}
	for _, k := range []struct{ kind, id string }{{"category", cCat}, {"tax_code", cTax}} {
		if r := h.cloudRow(k.kind, k.id); r == nil || r.Deleted || r.Data["active"] != false {
			t.Fatalf("cloud %s = %+v, want it deactivated", k.kind, r)
		}
	}
	h.mustRun()
	if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND is_active = 1`, cCat); n != 0 {
		t.Fatal("the pull brought the deleted category back active")
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = ? AND is_active = 1`, cTax); n != 0 {
		t.Fatal("the pull brought the deleted tax code back active")
	}
	if p, _ := h.tick(); p.Sent != 0 || h.marks() != 0 {
		t.Fatalf("a later tick sent %d (marks %d), want nothing", p.Sent, h.marks())
	}
}

// L-2: a category already inactive in the till's base has nothing to
// upload when the till hard-deletes it (the mark is dropped); one the
// cloud deactivated since the till last pulled is accepted as is.
func TestPushLocalHardDeleteOfAnInactiveCategory(t *testing.T) {
	t.Run("inactive in the base", func(t *testing.T) {
		h := newPushHarness(t)
		off := catRow(cCat, 1, "Drinks", nil)
		off.Data["active"] = false
		h.feed.add(off)
		h.mustRun()
		h.exec(`DELETE FROM categories WHERE id = ?`, cCat)
		if p := h.push(); p.Sent != 0 || p.Pending != 0 {
			t.Fatalf("push = %+v, want nothing sent and the mark dropped", p)
		}
	})
	t.Run("deactivated in the cloud since", func(t *testing.T) {
		h := newPushHarness(t)
		h.feed.add(catRow(cCat, 1, "Drinks", nil))
		h.mustRun()
		h.exec(`DELETE FROM categories WHERE id = ?`, cCat)
		h.myEdit("category", cCat, map[string]any{"active": false})
		if p := h.push(); p.Accepted != 1 || p.Conflicts != 0 || p.Pending != 0 {
			t.Fatalf("push = %+v, want the deactivation accepted", p)
		}
		if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND is_active = 1`, cCat); n != 0 {
			t.Fatal("the answer brought the category back active")
		}
	})
}

// D-1: an item an applier-3 build pulled with a blank cloud SKU holds the
// SKU the till generated, and the base seeded from it after the upgrade
// cannot tell that SKU from one the cloud sent. A SKU edit made before the
// re-pull waits for it (instead of uploading the generated SKU as before,
// which the cloud judges a false value conflict); the re-pull settles the
// base to the cloud's "" with the generated SKU kept, and the edit then
// uploads and is accepted.
func TestPushSKUEditOnASeededBaseWaitsForTheRepull(t *testing.T) {
	for _, tc := range []struct {
		name, cloudSKU, before string
	}{
		{"blank cloud SKU", "", ""},
		{"cloud SKU", "C-1", "C-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPushHarness(t)
			row := itemFeedRow(1, h.feed.high+1, "Latte", 250)
			row.Data["sku"] = tc.cloudSKU
			h.feed.add(row)
			h.mustRun()
			id := itemID(1)
			it, _ := h.item(id)
			if it.SKU == "" || (tc.cloudSKU != "" && it.SKU != tc.cloudSKU) {
				t.Fatalf("pulled SKU = %q", it.SKU)
			}
			// The till as an applier-3 build left it.
			h.exec(`UPDATE cloud_links SET base = NULL`)
			h.exec(`UPDATE settings SET value = '3' WHERE key = ?`, KeyApplierVersion)
			if p := h.push(); p.Sent != 0 {
				t.Fatalf("seeding push = %+v", p)
			}
			h.saveItem(data.ItemPatch{ID: id, SKU: ptrTo("L-1")})
			if p := h.push(); p.Sent != 0 || p.Pending != 1 {
				t.Fatalf("push before the re-pull = %+v, want the SKU edit waiting", p)
			}
			h.mustRun() // the applier-4 re-pull from 0
			if it, _ := h.item(id); it.SKU != "L-1" {
				t.Fatalf("the re-pull overwrote the pending SKU edit: %q", it.SKU)
			}
			p := h.push()
			if p.Accepted != 1 || p.Conflicts != 0 {
				t.Fatalf("push = %+v, want the SKU edit accepted, no false conflict", p)
			}
			ch := onlyChange(t, h.sentChanges())
			if len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "sku" ||
				jsonOf(t, ch.Before["sku"]) != tc.before || jsonOf(t, ch.After["sku"]) != "L-1" {
				t.Fatalf("change = %+v, want sku %q → \"L-1\"", ch, tc.before)
			}
			if got := h.cloudRow("item", id); got.Data["sku"] != "L-1" {
				t.Fatalf("cloud sku = %v", got.Data["sku"])
			}
		})
	}
}

// D-1: only a SKU change waits on a seeded base; another field's edit
// uploads at once, without the SKU.
func TestPushOtherEditOnASeededBaseDoesNotWait(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250) // blank cloud SKU
	h.exec(`UPDATE cloud_links SET base = NULL`)
	h.exec(`UPDATE settings SET value = '3' WHERE key = ?`, KeyApplierVersion)
	if p := h.push(); p.Sent != 0 {
		t.Fatalf("seeding push = %+v", p)
	}
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(260))})
	p := h.push()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	if ch := onlyChange(t, h.sentChanges()); len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "price_minor" {
		t.Fatalf("change = %+v, want price_minor only", ch)
	}
}

// D-1 on a delete (review M-1): an item deleted on the till before the
// re-pull cannot wait for it (the pull leaves a marked row the till
// deleted alone), so its delete leaves the unsettled seeded sku out of
// before — the cloud compares the fields it is sent — instead of a false
// conflict that would keep the item.
func TestPushDeleteOnASeededBaseOmitsTheSeededSKU(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250) // blank cloud SKU
	h.exec(`UPDATE cloud_links SET base = NULL`)
	h.exec(`UPDATE settings SET value = '3' WHERE key = ?`, KeyApplierVersion)
	if p := h.push(); p.Sent != 0 {
		t.Fatalf("seeding push = %+v", p)
	}
	if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	p := h.push()
	if p.Accepted != 1 || p.Conflicts != 0 {
		t.Fatalf("push = %+v, want the delete accepted", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if _, ok := ch.Before["sku"]; ch.Op != opDelete || ok || len(ch.Before) < 6 {
		t.Fatalf("change = %+v, want a delete without the seeded sku", ch)
	}
	if r := h.cloudRow("item", id); !r.Deleted {
		t.Fatal("the delete did not reach the cloud")
	}
}
