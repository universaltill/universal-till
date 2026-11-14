package catalogpull

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6203 (contract cloud-catalog-changes 1.1.0): a delete's before
// carries the net-quantity pair whole or not at all. A base holding only
// one half would otherwise upload a lone half, which the cloud refuses
// (400 for the whole call) and the outbox stalls.
func TestPushDeleteWithALoneNetQuantityHalfOmitsBoth_6203(t *testing.T) {
	for _, drop := range []string{"net_quantity_value", "net_quantity_unit"} {
		t.Run("base without "+drop, func(t *testing.T) {
			h := newPushHarness(t)
			id := h.seedWithItem(func(d map[string]any) { d["net_quantity_value"], d["net_quantity_unit"] = 330, "ml" })
			h.exec(`UPDATE cloud_links SET base = json_remove(base, '$.` + drop + `') WHERE entity = 'item'`)
			if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			p := h.push()
			if p.Accepted != 1 || p.Conflicts != 0 {
				t.Fatalf("push = %+v, want the delete accepted", p)
			}
			ch := onlyChange(t, h.sentChanges())
			if ch.Op != opDelete {
				t.Fatalf("change = %+v, want a delete", ch)
			}
			for _, f := range netQuantityPair {
				if _, ok := ch.Before[f]; ok {
					t.Fatalf("before carries %s, want neither net-quantity half", f)
				}
			}
			if r := h.cloudRow("item", id); !r.Deleted {
				t.Fatal("the delete did not reach the cloud")
			}
		})
	}
}

// A base holding both halves still sends both.
func TestPushDeleteWithTheWholeNetQuantityPairKeepsBoth_6203(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedWithItem(func(d map[string]any) { d["net_quantity_value"], d["net_quantity_unit"] = 330, "ml" })
	if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if p := h.push(); p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	for _, f := range netQuantityPair {
		if _, ok := ch.Before[f]; !ok {
			t.Fatalf("before lacks %s, want the whole pair", f)
		}
	}
}
