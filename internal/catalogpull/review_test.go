package catalogpull

import (
	"context"
	"errors"
	"testing"
)

// Independent-review findings on ut-docs#4463's first cut.

// A version group is one cloud transaction and the server orders it by
// kind then cloud_id (ut-cloud internal/catalogfeed/kinds.go), not by
// dependency: the till must resolve references within the group.
func TestPullAppliesAVersionGroupInDependencyOrder(t *testing.T) {
	const (
		cGrand = "22222222-2222-4222-8222-000000000001" // sorts before its parents
	)
	t.Run("item before its same-version tax code", func(t *testing.T) {
		h := newHarness(t)
		it := itemRowData("Cola", 250)
		it["tax_code_cloud_id"] = cTax
		h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 1, Data: it})
		h.feed.add(taxRow(1))
		res := h.mustRun()
		if res.Refused != 0 || res.Applied != 2 {
			t.Fatalf("result = %+v, want both applied", res)
		}
		if got, _ := h.item(itemID(1)); got.TaxCode != cTax {
			t.Fatalf("item tax code = %q, want %s", got.TaxCode, cTax)
		}
	})
	t.Run("child category before its same-version parent", func(t *testing.T) {
		h := newHarness(t)
		h.feed.add(catRow(cSubCat, 1, "Soft drinks", cCat))
		h.feed.add(catRow(cCat, 1, "Drinks", nil))
		res := h.mustRun()
		if res.Refused != 0 || res.Applied != 2 {
			t.Fatalf("result = %+v, want both applied", res)
		}
		if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND parent_id = ?`, cSubCat, cCat); n != 1 {
			t.Fatal("child not created under its parent")
		}
	})
	t.Run("deep chain in reverse order, split across chunks", func(t *testing.T) {
		h := newHarness(t)
		// The group (5 rows) is bigger than a chunk: the order must be
		// settled over the whole group before it is cut.
		defer setCommitChunk(2)()
		it := itemRowData("Lemonade", 200)
		it["category_cloud_id"] = cGrand
		it["tax_code_cloud_id"] = cTax
		h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 1, Data: it})
		h.feed.add(catRow(cGrand, 1, "Lemonades", cSubCat))
		h.feed.add(catRow(cSubCat, 1, "Soft drinks", cCat))
		h.feed.add(catRow(cCat, 1, "Drinks", nil))
		h.feed.add(taxRow(1))
		res := h.mustRun()
		if res.Refused != 0 || res.Applied != 5 {
			t.Fatalf("result = %+v, want all 5 applied", res)
		}
		if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND parent_id = ?`, cGrand, cSubCat); n != 1 {
			t.Fatal("grandchild not created under its parent")
		}
		if got, _ := h.item(itemID(1)); got.Category != cGrand || got.TaxCode != cTax {
			t.Fatalf("item = %+v", got)
		}
		if h.cursor() != 1 {
			t.Fatalf("cursor = %d, want 1", h.cursor())
		}
	})
	t.Run("a parent cycle falls back to feed order and is refused", func(t *testing.T) {
		h := newHarness(t)
		h.feed.add(catRow(cSubCat, 1, "A", cCat))
		h.feed.add(catRow(cCat, 1, "B", cSubCat))
		res := h.mustRun()
		if res.Refused == 0 || h.cursor() != 1 {
			t.Fatalf("result = %+v cursor %d, want a refusal and the cursor past it", res, h.cursor())
		}
	})
}

// The prune at the end of a resync removes what the full pull did not see;
// a row written in the cloud while the resync was finishing must not be
// mistaken for gone, so the prune waits for one quiet confirming page.
func TestPullResyncPruneWaitsForAQuietFeed(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Gone", 100))
	h.feed.add(itemFeedRow(2, 2, "Kept", 100))
	h.mustRun()
	h.feed.drop(itemID(1))
	h.feed.add(itemFeedRow(3, 3, "New", 100))
	h.feed.goneOnce = true

	late := false
	fetch := func(ctx context.Context, since int64, limit int) (Page, error) {
		if since == 3 && !late {
			// The cloud moves on between the resync's last page and the
			// confirming fetch.
			late = true
			h.feed.add(itemFeedRow(4, 4, "Late", 100))
		}
		return h.fetch(ctx, since, limit)
	}
	res, err := Run(context.Background(), h.db, fetch, h.opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pruned != 0 || res.Done {
		t.Fatalf("result = %+v, want no prune (and not done) while the feed moved", res)
	}
	if _, ok := h.item(itemID(1)); !ok {
		t.Fatal("pruned although the confirming page was not quiet")
	}
	if _, ok := h.item(itemID(4)); !ok {
		t.Fatal("the confirming page's rows were not applied")
	}
	if h.setting(KeyResyncGen) == "" {
		t.Fatal("deferred prune lost the resync generation")
	}

	res = h.mustRun()
	if res.Pruned != 1 || !res.Done {
		t.Fatalf("next run = %+v, want the deferred prune of 1", res)
	}
	if _, ok := h.item(itemID(1)); ok {
		t.Fatal("deferred prune did not remove the unreturned item")
	}
	for _, n := range []int{2, 3, 4} {
		if _, ok := h.item(itemID(n)); !ok {
			t.Fatalf("returned item %d pruned", n)
		}
	}
	if h.setting(KeyResyncGen) != "" {
		t.Fatal("resync still marked in progress after the prune")
	}
}

// An error that is not the till refusing the row (a hook failing, the
// context ending, the database failing) is not the row's fault: the chunk
// rolls back, the cursor stays, and nothing is recorded as refused.
func TestPullInfraErrorAbortsTheChunkNotARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(cancel context.CancelFunc) func(context.Context, string) (bool, error)
	}{
		{"hook error", func(context.CancelFunc) func(context.Context, string) (bool, error) {
			return func(context.Context, string) (bool, error) { return false, errors.New("basket lookup failed") }
		}},
		{"context cancelled mid-chunk", func(cancel context.CancelFunc) func(context.Context, string) (bool, error) {
			return func(context.Context, string) (bool, error) { cancel(); return false, nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.feed.add(itemFeedRow(1, 1, "A", 100))
			h.mustRun()
			h.feed.add(itemFeedRow(2, 2, "B", 100))
			h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 3, Deleted: true})
			h.feed.add(itemFeedRow(3, 3, "C", 100))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.opt.ItemInLiveBasket = tc.hook(cancel)
			res, err := Run(ctx, h.db, h.fetch, h.opt)
			if err == nil {
				t.Fatalf("result = %+v, want the run to stop with an error", res)
			}
			if h.cursor() != 1 {
				t.Fatalf("cursor = %d, want 1 (unchanged)", h.cursor())
			}
			if n := h.count(`SELECT COUNT(*) FROM cloud_apply_failures`); n != 0 {
				t.Fatalf("%d infrastructure failures recorded as refusals", n)
			}
			if _, ok := h.item(itemID(2)); ok {
				t.Fatal("the aborted chunk's earlier row was committed")
			}
		})
	}
}

// A page is at most the requested limit, except one version too big for a
// page, which the cloud sends whole (contract "Paging guarantees" 1).
func TestPullBoundsThePageRowCount(t *testing.T) {
	rows := func(sameVersion bool) []Row {
		out := make([]Row, PageLimit+1)
		for i := range out {
			v := int64(i + 1)
			if sameVersion {
				v = 1
			}
			out[i] = Row{Kind: "layout", CloudID: itemID(i), Version: v, Data: []byte(`{}`)}
		}
		return out
	}
	t.Run("over the limit across versions is refused", func(t *testing.T) {
		h := newHarness(t)
		r := rows(false)
		_, err := Run(context.Background(), h.db, func(context.Context, int64, int) (Page, error) {
			return Page{Rows: r, Next: int64(len(r)), HighWater: int64(len(r))}, nil
		}, Options{})
		if err == nil {
			t.Fatal("a page over the requested limit was accepted")
		}
		if h.cursor() != 0 {
			t.Fatalf("cursor = %d, want 0", h.cursor())
		}
	})
	t.Run("one oversized version is accepted", func(t *testing.T) {
		h := newHarness(t)
		r := rows(true)
		res, err := Run(context.Background(), h.db, func(context.Context, int64, int) (Page, error) {
			return Page{Rows: r, Next: 1, HighWater: 1}, nil
		}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Skipped != len(r) || h.cursor() != 1 {
			t.Fatalf("result = %+v cursor %d", res, h.cursor())
		}
	})
}

// 401/403 (device_credential_required) will not fix themselves on the next
// tick: a stop like 409, cursor unchanged, not an error to retry-log.
func TestPullAuthAnswersStop(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{401, "unauthorized"}, {403, "device_credential_required"}} {
		t.Run(tc.code, func(t *testing.T) {
			h := newHarness(t)
			h.feed.add(itemFeedRow(1, 1, "A", 100))
			h.mustRun()
			h.feed.add(itemFeedRow(2, 2, "B", 100))
			h.feed.status, h.feed.code = tc.status, tc.code
			res, err := h.run()
			if err != nil {
				t.Fatalf("a %d is a stop, not an error: %v", tc.status, err)
			}
			if res.Stopped != tc.code || res.StopStatus != tc.status {
				t.Fatalf("result = %+v, want stopped %d %s", res, tc.status, tc.code)
			}
			if h.cursor() != 1 {
				t.Fatalf("cursor = %d, want 1", h.cursor())
			}
		})
	}
}
