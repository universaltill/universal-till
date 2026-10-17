package catalogpull

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0162 §5 (ut-docs#4464): a cloud-master main till uploads its own
// catalogue edits before each pull; the cloud tests them by value and wins
// a conflict. These run a real till database against a fake cloud that
// serves the feed and the change endpoint over the same rows.

func ptrTo[T any](v T) *T { return &v }

// num reads a JSON-ish number (a decoded float64 or a Go int a test set).
func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return -1
}

// seedPulled gives the till item n as the cloud has it (one pull).
func (h *pushHarness) seedPulled(n int, name string, price int64) string {
	h.t.Helper()
	h.feed.add(itemFeedRow(n, h.feed.high+1, name, price))
	h.mustRun()
	if h.marks() != 0 {
		h.t.Fatalf("the pull left %d outbox marks: its own writes must never upload", h.marks())
	}
	return itemID(n)
}

func (h *pushHarness) saveItem(p data.ItemPatch) {
	h.t.Helper()
	if _, err := data.NewCatalogRepo(h.db).SaveItem(context.Background(), p); err != nil {
		h.t.Fatalf("SaveItem: %v", err)
	}
}

func onlyChange(t *testing.T, chs []Change) Change {
	t.Helper()
	if len(chs) != 1 {
		t.Fatalf("sent %d changes, want 1: %+v", len(chs), chs)
	}
	return chs[0]
}

func jsonOf(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

// An offline price edit uploads as an update with the pulled value as
// before; the cloud applies it; the feed's next row is the till's own echo
// and is recognised, not applied again; nothing uploads twice.
func TestPushUploadsAnEditAndRecognisesTheEcho(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	if h.marks() != 1 {
		t.Fatalf("marks = %d after a local edit, want 1", h.marks())
	}
	p, pull := h.tick()
	if p.Accepted != 1 || p.Pending != 0 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if ch.Op != opUpdate || ch.CloudID != id || len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "price_minor" ||
		jsonOf(t, ch.Before["price_minor"]).(float64) != 250 || jsonOf(t, ch.After["price_minor"]).(float64) != 280 || ch.Stale {
		t.Fatalf("change = %+v", ch)
	}
	if got := h.cloudRow("item", id); num(got.Data["price_minor"]) != 280 {
		t.Fatalf("cloud price = %v", got.Data["price_minor"])
	}
	if pull.Echoed != 1 || pull.Applied != 0 {
		t.Fatalf("pull = %+v, want the echo recognised and nothing applied", pull)
	}
	if it, _ := h.item(id); it.Price != 280 {
		t.Fatalf("local price = %d", it.Price)
	}
	p, _ = h.tick()
	if p.Sent != 0 || len(h.sentChanges()) != 1 {
		t.Fatalf("second tick sent %d (total %d), want nothing", p.Sent, len(h.sentChanges()))
	}
}

// A blank cloud SKU leaves the till's own generated SKU in place (never an
// item without SKU, ut-docs#1459). The kept SKU never uploads by itself —
// not on the pull, not with another field's edit — but an operator's SKU
// edit does, with the cloud's "" as before (ut-docs#4464 review MEDIUM-1).
func TestPushSKUEditOnAnItemPulledWithABlankSKU(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250) // the cloud row's sku is ""
	if it, _ := h.item(id); it.SKU == "" {
		t.Fatal("the pull left the item without a SKU")
	}
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	if p, _ := h.tick(); p.Accepted != 1 {
		t.Fatalf("price push = %+v", p)
	}
	if ch := onlyChange(t, h.sentChanges()); len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "price_minor" {
		t.Fatalf("price change = %+v, want price_minor only (the kept SKU must not upload)", ch)
	}
	h.saveItem(data.ItemPatch{ID: id, SKU: ptrTo("L-1")})
	p, _ := h.tick()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v, want the SKU edit accepted", p)
	}
	chs := h.sentChanges()
	if len(chs) != 2 {
		t.Fatalf("sent %d changes, want 2", len(chs))
	}
	ch := chs[1]
	if ch.Op != opUpdate || len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "sku" ||
		jsonOf(t, ch.After["sku"]) != "L-1" || jsonOf(t, ch.Before["sku"]) != "" {
		t.Fatalf("change = %+v, want sku \"\" -> \"L-1\"", ch)
	}
	if got := h.cloudRow("item", id); got.Data["sku"] != "L-1" {
		t.Fatalf("cloud sku = %v", got.Data["sku"])
	}
	if it, _ := h.item(id); it.SKU != "L-1" {
		t.Fatalf("local sku = %q", it.SKU)
	}
	if p, _ := h.tick(); p.Sent != 0 {
		t.Fatalf("a tick after the SKU echo sent %d", p.Sent)
	}
}

// Fields the till did not change are never compared: a cloud price edit
// and an offline rename both survive, uploading first and pulling after.
func TestPushRenameAndCloudPriceEditBothSurvive(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.myEdit("item", id, map[string]any{"price_minor": 300})
	h.saveItem(data.ItemPatch{ID: id, Name: ptrTo("Latte (large)")})
	p, _ := h.tick()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	it, _ := h.item(id)
	if it.Name != "Latte (large)" || it.Price != 300 {
		t.Fatalf("local item = %q %d, want both edits", it.Name, it.Price)
	}
	if got := h.cloudRow("item", id); got.Data["name"] != "Latte (large)" || num(got.Data["price_minor"]) != 300 {
		t.Fatalf("cloud item = %+v", got.Data)
	}
}

// A field the cloud changed since the till saw it: the cloud wins the
// whole change, and its row replaces the local edit in the answer itself,
// before any pull (a conflict mints no version, so the feed never would).
func TestPushConflictAppliesTheCloudRowAtOnce(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.myEdit("item", id, map[string]any{"price_minor": 300})
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), Name: ptrTo("Flat white")})
	p := h.push()
	if p.Conflicts != 1 || p.Pending != 0 {
		t.Fatalf("push = %+v", p)
	}
	it, _ := h.item(id)
	if it.Price != 300 || it.Name != "Latte" {
		t.Fatalf("local item = %q %d after a conflict, want the cloud's row", it.Name, it.Price)
	}
	if h.marks() != 0 {
		t.Fatal("the answer re-marked the row it applied")
	}
}

// The pull never overwrites a field with an unanswered local edit (it
// waits for the upload's verdict) but applies the rest of the row.
func TestPullLeavesAPendingFieldAlone(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	h.myEdit("item", id, map[string]any{"name": "Café latte"})
	h.mustRun() // a pull with no upload before it (the upload failed)
	it, _ := h.item(id)
	if it.Price != 280 || it.Name != "Café latte" {
		t.Fatalf("local item = %q %d, want the cloud's name and the till's pending price", it.Name, it.Price)
	}
	p := h.push()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "price_minor" {
		t.Fatalf("the pulled name was uploaded as a till edit: %+v", ch)
	}
	if got := h.cloudRow("item", id); got.Data["name"] != "Café latte" || num(got.Data["price_minor"]) != 280 {
		t.Fatalf("cloud = %+v", got.Data)
	}
}

// An offline create uploads under a cloud_id the till mints and links at
// once; a lost answer re-sends the SAME change, which the cloud answers
// from its first verdict — one cloud row, one local row, then the echo.
func TestPushOfflineCreateIsIdempotent(t *testing.T) {
	h := newPushHarness(t)
	local := "till-made-1"
	h.saveItem(data.ItemPatch{ID: local, Create: true, Name: ptrTo("Scone"), PriceMinor: ptrTo(int64(300)), SKU: ptrTo("SC-1")})
	h.cloud.failAfterApply = true
	if _, err := Push(context.Background(), h.db, h.post, h.opt); err == nil {
		t.Fatal("a lost answer was not an error")
	}
	first := onlyChange(t, h.sentChanges())
	if first.Op != opCreate || first.Before != nil || jsonOf(t, first.After["sku"]) != "SC-1" {
		t.Fatalf("create = %+v", first)
	}
	p, pull := h.tick()
	if p.Accepted != 1 {
		t.Fatalf("retry push = %+v", p)
	}
	chs := h.sentChanges()
	if len(chs) != 2 || chs[1].ChangeID != first.ChangeID || chs[1].CloudID != first.CloudID {
		t.Fatalf("the retry was not the same change: %+v", chs)
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE name = 'Scone'`); n != 1 {
		t.Fatalf("local Scone items = %d", n)
	}
	if got, ok, _ := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, "item", first.CloudID); !ok || got != local {
		t.Fatalf("link = %q %v, want the till's own row", got, ok)
	}
	if pull.Applied != 0 || pull.Echoed == 0 {
		t.Fatalf("pull = %+v, want the echo only", pull)
	}
}

// Rows a till creates offline that reference each other upload in
// dependency order (the category before its item) under the cloud_ids the
// till minted.
func TestPushCreatesUploadReferencedRowsFirst(t *testing.T) {
	h := newPushHarness(t)
	cat, err := data.NewCatalogRepo(h.db).SaveCategory(context.Background(), data.CategorySave{ID: "cat-local", Create: true, Name: ptrTo("Bakery")})
	if err != nil {
		t.Fatal(err)
	}
	_ = cat
	// The item is edited first so its mark is older than the category's.
	h.saveItem(data.ItemPatch{ID: "item-local", Create: true, Name: ptrTo("Bun"), PriceMinor: ptrTo(int64(90))})
	h.exec(`DELETE FROM cloud_outbox WHERE entity = 'category'`)
	h.saveItem(data.ItemPatch{ID: "item-local", CategoryID: ptrTo("cat-local")})
	p := h.push()
	if p.Accepted != 2 {
		t.Fatalf("push = %+v", p)
	}
	chs := h.sentChanges()
	if len(chs) != 2 || chs[0].Kind != "category" || chs[1].Kind != "item" {
		t.Fatalf("order = %+v, want the category first", chs)
	}
	if jsonOf(t, chs[1].After["category_cloud_id"]) != chs[0].CloudID {
		t.Fatalf("item's category_cloud_id %s, want the category's minted %s", chs[1].After["category_cloud_id"], chs[0].CloudID)
	}
}

// A local delete uploads with every field it last saw; a newer cloud edit
// wins and the cloud's row comes back to the till.
func TestPushDeletes(t *testing.T) {
	h := newPushHarness(t)
	gone := h.seedPulled(1, "Tea", 200)
	kept := h.seedPulled(2, "Coffee", 250)
	h.myEdit("item", kept, map[string]any{"price_minor": 260})
	pos := data.NewPOSRepo(h.db)
	for _, id := range []string{gone, kept} {
		if _, err := pos.DeleteUnusedItem(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	p := h.push()
	if p.Accepted != 1 || p.Conflicts != 1 {
		t.Fatalf("push = %+v", p)
	}
	for _, ch := range h.sentChanges() {
		if ch.Op != opDelete || len(ch.Before) < 7 || ch.After != nil {
			t.Fatalf("delete change = %+v", ch)
		}
	}
	if r := h.cloudRow("item", gone); !r.Deleted {
		t.Fatal("the delete did not reach the cloud")
	}
	if it, ok := h.item(kept); !ok || it.Price != 260 {
		t.Fatalf("the cloud's kept row was not restored on the till: %+v %v", it, ok)
	}
	if _, pull := h.tick(); pull.Refused != 0 {
		t.Fatalf("pull after = %+v", pull)
	}
	if _, ok := h.item(gone); ok {
		t.Fatal("the deleted item came back")
	}
}

// A cloud delete against an offline edit: the delete wins (the edit is in
// the cloud's log), and the till removes the row through its delete rules
// — deactivated, because it was sold.
func TestPushCloudDeleteWinsOverAnOfflineEdit(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Tea", 200)
	h.sell(id)
	h.feed.add(feedRow{Kind: "item", CloudID: id, Version: h.feed.high + 1, Deleted: true})
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(210))})
	h.mustRun() // the pull leaves the pending row alone, tombstone included
	if it, _ := h.item(id); !it.Active || it.Price != 210 {
		t.Fatalf("the pull applied a tombstone over a pending edit: %+v", it)
	}
	p := h.push()
	if p.Conflicts != 1 {
		t.Fatalf("push = %+v", p)
	}
	it, ok := h.item(id)
	if !ok || it.Active {
		t.Fatalf("item = %+v %v, want kept (sold) but deactivated", it, ok)
	}
	if h.marks() != 0 {
		t.Fatal("the deactivation was marked as a till edit")
	}
}

// A create the cloud refuses (a barcode taken there) is not applied in the
// cloud, so the till's copy goes too: deleted if never sold.
func TestPushRefusedCreateIsRemovedLocally(t *testing.T) {
	h := newPushHarness(t)
	h.cloud.duplicateNames["Dup"] = true
	h.saveItem(data.ItemPatch{ID: "dup-local", Create: true, Name: ptrTo("Dup"), PriceMinor: ptrTo(int64(1))})
	p := h.push()
	if p.Conflicts != 1 {
		t.Fatalf("push = %+v", p)
	}
	if _, ok := h.item("dup-local"); ok {
		t.Fatal("a create the cloud refused stayed on the till")
	}
	if h.count(`SELECT COUNT(*) FROM cloud_links WHERE local_id = 'dup-local'`) != 0 || h.marks() != 0 {
		t.Fatal("bookkeeping left behind")
	}
}

// A cloud_id the cloud cannot use: the till mints another and re-sends.
func TestPushRejectedCreateIsReMinted(t *testing.T) {
	h := newPushHarness(t)
	h.cloud.rejectNextCreate = true
	h.saveItem(data.ItemPatch{ID: "m-local", Create: true, Name: ptrTo("Muffin"), PriceMinor: ptrTo(int64(250))})
	p := h.push()
	if p.Rejected != 1 || p.Accepted != 1 {
		t.Fatalf("push = %+v, want the reject then the re-minted create", p)
	}
	chs := h.sentChanges()
	if len(chs) != 2 || chs[0].CloudID == chs[1].CloudID || chs[0].ChangeID == chs[1].ChangeID {
		t.Fatalf("changes = %+v", chs)
	}
	if got, ok, _ := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, "item", chs[1].CloudID); !ok || got != "m-local" {
		t.Fatalf("link = %q %v", got, ok)
	}
}

// An outbox row older than 30 days uploads stale: the cloud logs it and
// its own row replaces the edit.
func TestPushStaleEdit(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	old := pushNow
	pushNow = func() time.Time { return time.Now().UTC().Add(StaleAfter + time.Hour) }
	defer func() { pushNow = old }()
	p := h.push()
	if p.Conflicts != 1 || !onlyChange(t, h.sentChanges()).Stale {
		t.Fatalf("push = %+v", p)
	}
	if it, _ := h.item(id); it.Price != 250 {
		t.Fatalf("local price = %d, want the cloud's", it.Price)
	}
}

// A barcode added offline uploads as the item's whole set; the answer
// links the cloud's new barcode row at once, so the echo neither re-adds
// nor re-uploads it. A conflicting barcode edit gives the till the
// cloud's set.
func TestPushBarcodes(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Cola", 150)
	h.saveItem(data.ItemPatch{ID: id, Barcodes: &[]string{"5000112345670"}})
	p, pull := h.tick()
	if p.Accepted != 1 || pull.Refused != 0 {
		t.Fatalf("push %+v pull %+v", p, pull)
	}
	ch := onlyChange(t, h.sentChanges())
	if ch.ChangedFields[0] != "barcodes" || string(ch.Before["barcodes"]) != "[]" {
		t.Fatalf("change = %+v (%s)", ch, ch.Before["barcodes"])
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = 'item_barcode'`); n != 1 {
		t.Fatalf("barcode links = %d", n)
	}
	if p, _ = h.tick(); p.Sent != 0 {
		t.Fatalf("the barcode re-uploaded: %+v", p)
	}
	// Conflict: the cloud added another barcode meanwhile; the till replaced
	// its set offline.
	h.feed.add(feedRow{Kind: "item_barcode", CloudID: "44444444-4444-4444-8444-000000000001", Version: h.feed.high + 1, Data: map[string]any{
		"barcode": "4006381333931", "item_cloud_id": id, "variant_cloud_id": nil, "barcode_type": "EAN13", "is_primary": false,
	}})
	h.saveItem(data.ItemPatch{ID: id, Barcodes: &[]string{"96385074"}})
	if p = h.push(); p.Conflicts != 1 {
		t.Fatalf("push = %+v", p)
	}
	got := map[string]bool{}
	rows, err := h.db.Query(`SELECT barcode FROM item_barcodes WHERE item_id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		got[b] = true
	}
	if len(got) != 2 || !got["5000112345670"] || !got["4006381333931"] {
		t.Fatalf("local barcodes = %v, want the cloud's two", got)
	}
}

// Stop answers keep the outbox untouched; a call answered retry is sent
// again on the next trigger, not spun on.
func TestPushStopsAndRetries(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(260))})
	h.cloud.status, h.cloud.code = http.StatusConflict, "catalog_master_till"
	p := h.push()
	if p.Stopped != "catalog_master_till" || p.StopStatus != http.StatusConflict || p.Pending != 1 {
		t.Fatalf("push = %+v", p)
	}
	h.cloud.status = 0
	h.cloud.retry[id] = true
	if p = h.push(); p.Calls != 1 || p.Pending != 1 {
		t.Fatalf("retry push = %+v, want one call and the change kept", p)
	}
	delete(h.cloud.retry, id)
	if p = h.push(); p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	chs := h.sentChanges()
	if chs[len(chs)-1].ChangeID != chs[0].ChangeID {
		t.Fatal("a re-sent change got a new change_id")
	}
}

// A resync's prune never removes a row whose create has not been accepted
// yet, nor one with an unanswered edit (ADR-0162 §4: they upload first).
func TestResyncPruneSparesUnuploadedRows(t *testing.T) {
	h := newPushHarness(t)
	edited := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: edited, PriceMinor: ptrTo(int64(260))})
	h.saveItem(data.ItemPatch{ID: "new-local", Create: true, Name: ptrTo("Scone"), PriceMinor: ptrTo(int64(300))})
	h.cloud.status, h.cloud.code = http.StatusServiceUnavailable, "changes_unavailable"
	if _, err := Push(context.Background(), h.db, h.post, h.opt); err == nil {
		t.Fatal("want the upload to fail")
	}
	// The cloud lost the pulled row (a restore) and asks for a resync.
	h.feed.drop(edited)
	h.feed.add(itemFeedRow(9, h.feed.high+1, "Other", 1))
	h.feed.goneOnce = true
	h.mustRun()
	h.mustRun()
	if _, ok := h.item(edited); !ok {
		t.Fatal("the prune removed a row with an unanswered edit")
	}
	if _, ok := h.item("new-local"); !ok {
		t.Fatal("the prune removed an offline create")
	}
}

// ADR-0161 import at scale (AC): a 20 000-row till-side import on a
// cloud-master store marks every row and uploads them all, 100 per call,
// with no stale conflict and no depth cap.
func TestPushTwentyThousandRowImport(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("20 000-row upload: minutes under -race, skipped with -short")
	}
	h := newPushHarness(t)
	const n = 20000
	repo := data.NewCatalogRepo(h.db)
	ctx := context.Background()
	for start := 0; start < n; start += 100 {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := start; i < start+100; i++ {
			if _, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
				ID: fmt.Sprintf("imp-%05d", i), Name: fmt.Sprintf("Imported %d", i), SKU: fmt.Sprintf("IMP-%05d", i), BasePrice: 100, IsActive: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if h.marks() != n {
		t.Fatalf("marks = %d, want %d", h.marks(), n)
	}
	start := time.Now()
	p := h.push()
	if p.Accepted != n || p.Conflicts != 0 || p.Pending != 0 || p.Calls != n/MaxChangesPerCall {
		t.Fatalf("push = %+v", p)
	}
	for _, ch := range h.sentChanges() {
		if ch.Stale {
			t.Fatal("a fresh import uploaded stale")
		}
	}
	t.Logf("20 000 creates uploaded in %d calls, %s", p.Calls, time.Since(start).Round(time.Millisecond))
	if _, pull := h.tick(); pull.Applied != 0 || pull.Echoed == 0 {
		t.Fatalf("pull after the import = %+v, want echoes only", pull)
	}
}

// A suppression flag that was ever committed would silently stop every
// mark: the upload removes it before it seals anything.
func TestPushClearsACommittedSuppressFlag(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.exec(`INSERT INTO cloud_outbox_suppress (one) VALUES (1)`)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(1))})
	if h.marks() != 0 {
		t.Fatal("precondition: the stale flag should hide this edit")
	}
	h.push()
	if n := h.count(`SELECT COUNT(*) FROM cloud_outbox_suppress`); n != 0 {
		t.Fatalf("flag rows = %d after a push", n)
	}
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(2))})
	if h.marks() != 1 {
		t.Fatal("edits are still not marked after the flag was cleared")
	}
}
