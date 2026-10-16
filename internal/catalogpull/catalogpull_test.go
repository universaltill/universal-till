package catalogpull

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// ADR-0162 §4 (ut-docs#4463): the main till's pull of a cloud-master
// store's catalogue, driven against a fake of the contract's feed.

func TestPullAppliesRowsFieldWiseAndPagesPastAnEmptyPage(t *testing.T) {
	h := newHarness(t)
	h.feed.limit = 2
	h.feed.add(taxRow(1))
	h.feed.add(catRow(cCat, 2, "Drinks", nil))
	// Versions 3 and 4 belong to nothing this store reads: after the page
	// that ends at version 2 the cloud answers one EMPTY page whose next
	// (4) is still below high_water — not the end.
	h.feed.add(catRow(cSubCat, 5, "Soft drinks", cCat))
	full := itemRowData("Cola", 250)
	full["sku"] = "COLA-1"
	full["description"] = "Can, 330 ml"
	full["unit"] = "can"
	full["category_cloud_id"] = cSubCat
	full["tax_code_cloud_id"] = cTax
	full["is_weighed"] = false
	full["stock_untracked"] = true
	full["age_restricted"] = true
	full["color"] = "#0f172a"
	full["sell_screen_hidden"] = true
	full["net_quantity_value"] = 330
	full["net_quantity_unit"] = "ml"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 6, Data: full})
	h.feed.add(itemFeedRow(2, 7, "Water", 120))
	h.feed.empty = map[int64]int64{2: 4}

	res := h.mustRun()
	if !res.Done || !res.Changed {
		t.Fatalf("result = %+v, want done and changed", res)
	}
	if got := h.feed.requests(); len(got) != 4 || got[0] != 0 || got[1] != 2 || got[2] != 4 || got[3] != 6 {
		t.Fatalf("requests since = %v, want [0 2 4 6]: paging on through the empty page", got)
	}
	if h.cursor() != 7 {
		t.Fatalf("cursor = %d, want high_water 7", h.cursor())
	}
	it, ok := h.item(itemID(1))
	if !ok {
		t.Fatal("item 1 not created under its cloud id")
	}
	if it.Name != "Cola" || it.SKU != "COLA-1" || it.Desc != "Can, 330 ml" || it.Unit != "can" || it.Price != 250 ||
		it.Category != cSubCat || it.TaxCode != cTax || !it.Untracked || !it.AgeRestricted || it.Color != "#0f172a" ||
		!it.Hidden || it.Removed || !it.Active || it.NQValue.Int64 != 330 || it.NQUnit.String != "ml" {
		t.Fatalf("item 1 = %+v", it)
	}
	if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND parent_id = ? AND sort_order = 3`, cSubCat, cCat); n != 1 {
		t.Fatal("sub-category not created under its parent with the cloud's sort order")
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = ? AND rate_basis_points = 2000 AND is_active = 1`, cTax); n != 1 {
		t.Fatal("tax code not created")
	}
	for _, l := range []struct{ entity, id string }{{"tax_code", cTax}, {"category", cCat}, {"category", cSubCat}, {"item", itemID(1)}, {"item", itemID(2)}} {
		if local, ok, _ := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, l.entity, l.id); !ok || local != l.id {
			t.Fatalf("link %s/%s = %q %v", l.entity, l.id, local, ok)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'cloud_catalog_%'`); n != 5 {
		t.Fatalf("audit rows = %d, want one per applied row (5)", n)
	}

	// A delta: a price change and a cleared description/net quantity. The
	// next run starts from the stored cursor.
	upd := itemRowData("Cola", 260)
	upd["category_cloud_id"] = cSubCat
	upd["tax_code_cloud_id"] = nil
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 8, Data: upd})
	h.mustRun()
	if got := h.feed.requests(); got[len(got)-1] != 7 {
		t.Fatalf("delta pull started at %d, want the cursor 7", got[len(got)-1])
	}
	it, _ = h.item(itemID(1))
	if it.Price != 260 || it.Desc != "" || it.TaxCode != "" || it.NQValue.Valid || it.NQUnit.Valid || it.SKU != "COLA-1" {
		t.Fatalf("after delta item 1 = %+v (sku must be kept: a blank cloud sku is absent)", it)
	}
}

func TestPullCursorOnlyAtCommittedVersionBoundaries(t *testing.T) {
	h := newHarness(t)
	defer setCommitChunk(2)()
	var committed []int64
	defer setAfterChunkCommit(func() { committed = append(committed, h.cursor()) })()
	h.feed.add(itemFeedRow(1, 1, "A", 100))
	h.feed.add(itemFeedRow(2, 2, "B", 100))
	// Version 3 holds three rows (one transaction in the cloud): a chunk
	// boundary falls inside it, and the cursor must not.
	h.feed.add(itemFeedRow(3, 3, "C", 100))
	h.feed.add(itemFeedRow(4, 3, "D", 100))
	h.feed.add(itemFeedRow(5, 3, "E", 100))
	h.feed.add(itemFeedRow(6, 4, "F", 100))
	h.mustRun()
	// Chunks: [A B] → cursor 2; [C D] → still 2 (inside version 3);
	// [E F] → 4 = high_water.
	want := []int64{2, 2, 4}
	if len(committed) != len(want) {
		t.Fatalf("cursor at each commit = %v, want %v", committed, want)
	}
	for i := range want {
		if committed[i] != want[i] {
			t.Fatalf("cursor at each commit = %v, want %v", committed, want)
		}
	}
}

func TestPullCrashMidPageResumesFromLastCommittedBoundary(t *testing.T) {
	h := newHarness(t)
	restore := setCommitChunk(2)
	defer restore()
	for i := 1; i <= 6; i++ {
		h.feed.add(itemFeedRow(i, int64(i), "Item "+strconv.Itoa(i), int64(100*i)))
	}
	calls := 0
	undo := setBeforeChunkCommit(func() error {
		calls++
		if calls == 2 {
			return errors.New("power cut")
		}
		return nil
	})
	if _, err := h.run(); err == nil {
		t.Fatal("Run survived the injected crash")
	}
	undo()
	if h.cursor() != 2 {
		t.Fatalf("cursor after the crash = %d, want 2 (the first committed chunk)", h.cursor())
	}
	if n := h.count(`SELECT COUNT(*) FROM items`); n != 2 {
		t.Fatalf("items after the crash = %d, want the first chunk's 2 (the second rolled back)", n)
	}
	h.mustRun()
	if h.cursor() != 6 {
		t.Fatalf("cursor after resume = %d, want 6", h.cursor())
	}
	if n := h.count(`SELECT COUNT(*) FROM items`); n != 6 {
		t.Fatalf("items after resume = %d, want 6 and no duplicates", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links`); n != 6 {
		t.Fatalf("links after resume = %d, want 6", n)
	}
	if got := h.feed.requests(); got[len(got)-1] != 2 {
		t.Fatalf("resume asked since=%d, want 2", got[len(got)-1])
	}
}

func TestPull409StopsWithCursorUnchanged(t *testing.T) {
	for _, code := range []string{"catalog_master_till", "catalog_adopting"} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t)
			h.feed.add(itemFeedRow(1, 1, "A", 100))
			h.mustRun()
			h.feed.add(itemFeedRow(2, 2, "B", 100))
			h.feed.status, h.feed.code = 409, code
			res, err := h.run()
			if err != nil {
				t.Fatalf("a 409 is a stop, not an error: %v", err)
			}
			if res.Stopped != code || res.Changed {
				t.Fatalf("result = %+v, want stopped %s", res, code)
			}
			if h.cursor() != 1 {
				t.Fatalf("cursor = %d, want 1 (unchanged)", h.cursor())
			}
			if _, ok := h.item(itemID(2)); ok {
				t.Fatal("a stopped pull applied a row")
			}
		})
	}
}

func TestPull503KeepsCursor(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "A", 100))
	h.mustRun()
	h.feed.add(itemFeedRow(2, 2, "B", 100))
	h.feed.status, h.feed.code = 503, "catalog_unavailable"
	if _, err := h.run(); err == nil {
		t.Fatal("503 must be reported as an error (logged, retried next trigger)")
	}
	if h.cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", h.cursor())
	}
}

func TestPull410ResyncPrunesLinksTheFullPullDidNotReturn(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Sold", 100))
	h.feed.add(itemFeedRow(2, 2, "Unsold", 100))
	h.feed.add(itemFeedRow(3, 3, "Kept", 100))
	h.feed.add(catRow(cCat, 4, "Gone category", nil))
	h.mustRun()
	h.sell(itemID(1))

	// The cloud purged 1, 2 and the category (their tombstones are past
	// retention) and answers the old cursor with 410.
	h.feed.drop(itemID(1))
	h.feed.drop(itemID(2))
	h.feed.drop(cCat)
	h.feed.add(itemFeedRow(4, 5, "New", 100))
	h.feed.goneOnce = true
	res := h.mustRun()
	if !res.Done || res.Pruned != 3 {
		t.Fatalf("result = %+v, want done with 3 pruned", res)
	}
	if got := h.feed.requests(); len(got) != 4 || got[1] != 4 || got[2] != 0 || got[3] != 5 {
		t.Fatalf("requests = %v, want the 410 at since=4, a full pull from 0, then the confirming fetch at high_water 5", got)
	}
	if it, ok := h.item(itemID(1)); !ok || it.Active {
		t.Fatalf("sold item = %+v %v, want kept and deactivated", it, ok)
	}
	if _, ok := h.item(itemID(2)); ok {
		t.Fatal("unsold item not deleted by the prune")
	}
	if it, ok := h.item(itemID(3)); !ok || !it.Active {
		t.Fatal("returned item pruned")
	}
	if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND is_active = 0`, cCat); n != 1 {
		t.Fatal("pruned category not soft-deleted")
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links`); n != 2 {
		t.Fatalf("links after prune = %d, want 2 (items 3 and 4)", n)
	}
	if h.setting(KeyResyncGen) != "" {
		t.Fatalf("resync still marked in progress: %q", h.setting(KeyResyncGen))
	}
}

func TestPullResyncCrashStillPrunesAtTheEnd(t *testing.T) {
	h := newHarness(t)
	defer setCommitChunk(1)()
	h.feed.add(itemFeedRow(1, 1, "Gone", 100))
	h.feed.add(itemFeedRow(2, 2, "Kept", 100))
	h.feed.add(itemFeedRow(3, 3, "Kept too", 100))
	h.mustRun()
	h.feed.drop(itemID(1))
	h.feed.goneOnce = true
	calls := 0
	undo := setBeforeChunkCommit(func() error {
		if calls++; calls == 2 {
			return errors.New("crash mid resync")
		}
		return nil
	})
	if _, err := h.run(); err == nil {
		t.Fatal("injected crash not reported")
	}
	undo()
	if h.setting(KeyResyncGen) == "" {
		t.Fatal("resync-in-progress state not persisted across the crash")
	}
	if _, ok := h.item(itemID(1)); !ok {
		t.Fatal("pruned before the full pull finished")
	}
	h.mustRun() // no 410 this time: it resumes the resync from its cursor
	if _, ok := h.item(itemID(1)); ok {
		t.Fatal("the resumed resync did not prune the unreturned item")
	}
	if _, ok := h.item(itemID(3)); !ok {
		t.Fatal("a returned item was pruned")
	}
}

func TestPullRefusedRowRecordedChunkMatesAppliedCursorAdvances(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Kept price", 100))
	h.mustRun()

	bad := itemRowData("Kept price", -5) // refused: negative price
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Data: bad})
	h.feed.add(itemFeedRow(2, 2, "Neighbour", 100))
	colour := itemRowData("Bad colour", 100)
	colour["color"] = "#123456"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(3), Version: 3, Data: colour})
	orphan := itemRowData("Orphan", 100)
	orphan["category_cloud_id"] = cCat // never pulled
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(4), Version: 3, Data: orphan})
	h.feed.add(itemFeedRow(5, 4, "After", 100))

	res := h.mustRun()
	if res.Refused != 3 {
		t.Fatalf("result = %+v, want 3 refused", res)
	}
	if h.cursor() != 4 {
		t.Fatalf("cursor = %d, want 4: refused rows never wedge the feed", h.cursor())
	}
	if it, _ := h.item(itemID(1)); it.Price != 100 {
		t.Fatalf("refused update changed the item: %+v", it)
	}
	for _, id := range []string{itemID(3), itemID(4)} {
		if _, ok := h.item(id); ok {
			t.Fatalf("refused create %s was written", id)
		}
	}
	for _, id := range []string{itemID(2), itemID(5)} {
		if _, ok := h.item(id); !ok {
			t.Fatalf("chunk-mate %s rolled back with a refused row", id)
		}
	}
	fails, err := data.NewCloudLinkRepo().Failures(context.Background(), h.db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 3 {
		t.Fatalf("failures = %+v, want 3", fails)
	}
	byID := map[string]data.CloudApplyFailure{}
	for _, f := range fails {
		byID[f.CloudID] = f
	}
	if f := byID[itemID(1)]; f.Version != 2 || f.Entity != "item" || f.Reason == "" {
		t.Fatalf("failure for item 1 = %+v", f)
	}

	// A later good version of the refused row applies and clears its failure.
	h.feed.add(itemFeedRow(1, 5, "Kept price", 150))
	h.mustRun()
	if it, _ := h.item(itemID(1)); it.Price != 150 {
		t.Fatalf("later version not applied: %+v", it)
	}
	fails, _ = data.NewCloudLinkRepo().Failures(context.Background(), h.db, 10)
	if len(fails) != 2 {
		t.Fatalf("failure of item 1 not cleared by its later version: %+v", fails)
	}
}

// A row refused AFTER its first write (the category rename applies, then
// deactivating refuses while active items are in it) rolls back whole:
// the per-row savepoint, not just pre-write validation.
func TestPullRefusalAfterAPartialWriteRollsBackTheWholeRow(t *testing.T) {
	h := newHarness(t)
	h.feed.add(catRow(cCat, 1, "Drinks", nil))
	in := itemRowData("Cola", 100)
	in["category_cloud_id"] = cCat
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Data: in})
	h.mustRun()

	off := catRow(cCat, 3, "Renamed", nil)
	off.Data["active"] = false
	h.feed.add(off)
	h.feed.add(itemFeedRow(2, 3, "Same chunk", 100))
	res := h.mustRun()
	if res.Refused != 1 || res.Applied != 1 {
		t.Fatalf("result = %+v, want 1 refused, 1 applied", res)
	}
	if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND name = 'Drinks' AND is_active = 1`, cCat); n != 1 {
		t.Fatal("the refused row's first write (the rename) survived")
	}
	if _, ok := h.item(itemID(2)); !ok {
		t.Fatal("chunk-mate rolled back")
	}
	if h.cursor() != 3 {
		t.Fatalf("cursor = %d, want 3", h.cursor())
	}
}

func TestPullTombstonesUseTheDeleteRules(t *testing.T) {
	h := newHarness(t)
	h.feed.add(taxRow(1))
	h.feed.add(catRow(cCat, 2, "Snacks", nil))
	sold := itemRowData("Sold crisps", 100)
	sold["category_cloud_id"] = cCat
	sold["tax_code_cloud_id"] = cTax
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 3, Data: sold})
	h.feed.add(itemFeedRow(2, 4, "Never sold", 100))
	h.mustRun()
	h.sell(itemID(1))

	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 5, Deleted: true})
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(2), Version: 6, Deleted: true})
	h.feed.add(feedRow{Kind: "category", CloudID: cCat, Version: 7, Deleted: true})
	h.feed.add(feedRow{Kind: "tax_code", CloudID: cTax, Version: 8, Deleted: true})
	// A tombstone for a row this till never had is harmless.
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(9), Version: 9, Deleted: true})
	res := h.mustRun()
	if res.Refused != 0 {
		t.Fatalf("result = %+v", res)
	}
	if it, ok := h.item(itemID(1)); !ok || it.Active {
		t.Fatalf("sold item = %+v %v, want deactivated, never deleted", it, ok)
	}
	if _, ok := h.item(itemID(2)); ok {
		t.Fatal("never-sold item not deleted")
	}
	if n := h.count(`SELECT COUNT(*) FROM categories WHERE id = ? AND is_active = 0`, cCat); n != 1 {
		t.Fatal("category tombstone not applied through the delete rule")
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = ? AND is_active = 0`, cTax); n != 1 {
		t.Fatal("tax code tombstone did not deactivate it")
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links`); n != 0 {
		t.Fatalf("links after tombstones = %d, want 0", n)
	}
	if h.cursor() != 9 {
		t.Fatalf("cursor = %d", h.cursor())
	}
}

func TestPullItemInLiveBasketIsDeactivatedNotDeleted(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "In basket", 100))
	h.mustRun()
	h.opt.ItemInLiveBasket = func(_ context.Context, id string) (bool, error) { return id == itemID(1), nil }
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Deleted: true})
	h.mustRun()
	if it, ok := h.item(itemID(1)); !ok || it.Active {
		t.Fatalf("item in a live basket = %+v %v, want deactivated", it, ok)
	}
}

func TestPullSkipsOtherKindsAndStillAdvances(t *testing.T) {
	h := newHarness(t)
	for i, kind := range []string{"item_variant", "item_barcode", "modifier_group", "option_set", "layout", "staff", "setting", "something_new"} {
		h.feed.add(feedRow{Kind: kind, CloudID: itemID(50 + i), Version: int64(i + 1), Data: map[string]any{"name": "x"}})
	}
	res := h.mustRun()
	if res.Skipped != 8 || res.Changed || res.Refused != 0 {
		t.Fatalf("result = %+v, want 8 skipped", res)
	}
	if h.cursor() != 8 {
		t.Fatalf("cursor = %d, want 8", h.cursor())
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links`) + h.count(`SELECT COUNT(*) FROM cloud_apply_failures`); n != 0 {
		t.Fatalf("skipped kinds left %d bookkeeping rows", n)
	}
}

func TestPullApplierVersionBumpRepullsFromZero(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "A", 100))
	h.mustRun()
	if h.setting(KeyApplierVersion) != strconv.Itoa(ApplierVersion) {
		t.Fatalf("applier version = %q, want %d", h.setting(KeyApplierVersion), ApplierVersion)
	}
	// A till whose stored applier is older than the code's.
	if err := data.NewSettingsRepo(h.db).Set(context.Background(), KeyApplierVersion, strconv.Itoa(ApplierVersion-1)); err != nil {
		t.Fatal(err)
	}
	h.mustRun()
	if got := h.feed.requests(); got[len(got)-1] != 0 {
		t.Fatalf("after an applier bump the pull asked since=%d, want 0", got[len(got)-1])
	}
	if h.setting(KeyApplierVersion) != strconv.Itoa(ApplierVersion) {
		t.Fatal("applier version not stored after the re-pull")
	}
	if n := h.count(`SELECT COUNT(*) FROM items`); n != 1 {
		t.Fatalf("re-pull duplicated: %d items", n)
	}
}

func TestPullBoundsAMisbehavingFeed(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 5, "A", 100))
	h.feed.stuck = true // empty pages, next == since, forever
	if _, err := h.run(); err == nil {
		t.Fatal("a feed that never advances must stop the run with an error")
	}
	if n := len(h.feed.requests()); n > 2 {
		t.Fatalf("stuck feed asked %d times", n)
	}

	h2 := newHarness(t)
	defer setMaxPages(3)()
	h2.feed.limit = 1
	for i := 1; i <= 10; i++ {
		h2.feed.add(itemFeedRow(i, int64(i), "I", 100))
	}
	res := h2.mustRun()
	if res.Done || len(h2.feed.requests()) != 3 || h2.cursor() != 3 {
		t.Fatalf("result = %+v, requests %v, cursor %d: want 3 pages then stop", res, h2.feed.requests(), h2.cursor())
	}
	h2.mustRun()
	h2.mustRun()
	h2.mustRun()
	if h2.cursor() != 10 {
		t.Fatalf("later runs did not continue: cursor %d", h2.cursor())
	}
}

func TestPullRefusesAMalformedPage(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 3, "A", 100))
	h.mustRun()
	// A row at or below the cursor is not this contract's page.
	if _, err := Run(context.Background(), h.db, func(context.Context, int64, int) (Page, error) {
		return Page{Rows: []Row{{Kind: "item", CloudID: itemID(2), Version: 2, Data: []byte(`{"name":"Old","price_minor":1}`)}}, Next: 4, HighWater: 4}, nil
	}, Options{}); err == nil {
		t.Fatal("a row at or below the cursor was accepted")
	}
	if h.cursor() != 3 {
		t.Fatalf("cursor moved on a malformed page: %d", h.cursor())
	}
	if _, ok := h.item(itemID(2)); ok {
		t.Fatal("malformed page applied")
	}
}

// ADR-0162 §4: "an open basket keeps the prices on its lines" — the pull
// writes the catalogue only; a line already rung up is the engine's.
func TestPullLeavesAnOpenBasketLinePriceAlone(t *testing.T) {
	h := newHarness(t)
	row := itemRowData("Tea", 250)
	row["sku"] = "TEA-1"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 1, Data: row})
	h.mustRun()

	engine := pos.NewServiceWithResolver(pos.Config{}, ui.PriceResolverAdapter{Store: ui.NewButtonStore(h.db)})
	if _, found, err := engine.ScanQtyWithResult("TEA-1", 1); err != nil || !found {
		t.Fatalf("scan: found=%v err=%v", found, err)
	}
	row = itemRowData("Tea", 300)
	row["sku"] = "TEA-1"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Data: row})
	h.mustRun()
	if lines := engine.Lines(); len(lines) != 1 || lines[0].PriceCents.Minor() != 250 {
		t.Fatalf("open basket line = %+v, want its 250 kept", lines)
	}
	fresh := pos.NewServiceWithResolver(pos.Config{}, ui.PriceResolverAdapter{Store: ui.NewButtonStore(h.db)})
	if _, found, _ := fresh.ScanQtyWithResult("TEA-1", 1); !found || fresh.Lines()[0].PriceCents.Minor() != 300 {
		t.Fatalf("next line rung up = %+v, want the new 300", fresh.Lines())
	}
}
