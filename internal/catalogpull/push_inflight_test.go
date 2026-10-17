package catalogpull

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Review findings on ut-docs#4464 (the in-flight rule, the answer for a
// row deleted meanwhile, a base-less linked row, echoes across a resync,
// the stale flag at send time). Real till database, fake cloud.

// failUpload makes the next Push fail: lost (the cloud applied the call,
// its answer never arrived) or refused (503 before applying anything).
func (h *pushHarness) failUpload(lost bool) {
	h.t.Helper()
	if lost {
		h.cloud.failAfterApply = true
	} else {
		h.cloud.status, h.cloud.code = http.StatusServiceUnavailable, "changes_unavailable"
	}
	if _, err := Push(context.Background(), h.db, h.post, h.opt); err == nil {
		h.t.Fatal("want the upload to fail")
	}
	h.cloud.status, h.cloud.code = 0, ""
}

// callOf is the index of the call that first carried changeID, -1 if none.
func (h *pushHarness) callOf(changeID string) int {
	h.cloud.mu.Lock()
	defer h.cloud.mu.Unlock()
	for i, call := range h.cloud.calls {
		for _, ch := range call {
			if ch.ChangeID == changeID {
				return i
			}
		}
	}
	return -1
}

// Finding 1: pull at 250, edit to 260, the upload fails, edit to 270,
// Push. The second edit must not be sealed against the unchanged base
// while the first is in flight (it would conflict with the first, and the
// cloud's answer would revert the user's latest edit): it uploads after
// the first is answered, as before=260 → after=270, with no conflict.
func TestPushEditWhileAnEarlierChangeIsInFlight(t *testing.T) {
	for _, lost := range []bool{true, false} {
		name := "refused"
		if lost {
			name = "answer-lost"
		}
		t.Run(name, func(t *testing.T) {
			h := newPushHarness(t)
			id := h.seedPulled(1, "Latte", 250)
			h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(260))})
			h.failUpload(lost)
			first := onlyChange(t, h.sentChanges())
			h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(270))})
			p := h.push()
			if p.Conflicts != 0 || p.Pending != 0 {
				t.Fatalf("push = %+v, want no conflict and nothing left", p)
			}
			if it, _ := h.item(id); it.Price != 270 {
				t.Fatalf("local price = %d, want the latest edit 270", it.Price)
			}
			if got := h.cloudRow("item", id); num(got.Data["price_minor"]) != 270 {
				t.Fatalf("cloud price = %v, want 270", got.Data["price_minor"])
			}
			chs := h.sentChanges()
			last := chs[len(chs)-1]
			if last.ChangeID == first.ChangeID || last.Op != opUpdate ||
				jsonOf(t, last.Before["price_minor"]).(float64) != 260 || jsonOf(t, last.After["price_minor"]).(float64) != 270 {
				t.Fatalf("second change = %+v, want update 260 → 270", last)
			}
			if h.callOf(last.ChangeID) <= h.callOf(first.ChangeID) || h.callOf(last.ChangeID) < 2 {
				t.Fatal("the second change was sent before the first was answered")
			}
			if p, _ := h.tick(); p.Sent != 0 {
				t.Fatalf("a later tick sent %d", p.Sent)
			}
			if it, _ := h.item(id); it.Price != 270 {
				t.Fatalf("local price after the pull = %d", it.Price)
			}
		})
	}
}

// Finding 1, create then edit: the edit waits for the create's answer and
// uploads as an update, not a second create (which the cloud answers with
// a value conflict against the live row, reverting the edit).
func TestPushCreateThenEditWhileTheCreateIsInFlight(t *testing.T) {
	for _, lost := range []bool{true, false} {
		t.Run(map[bool]string{true: "answer-lost", false: "refused"}[lost], func(t *testing.T) {
			h := newPushHarness(t)
			local := "till-made-1"
			h.saveItem(data.ItemPatch{ID: local, Create: true, Name: ptrTo("Scone"), PriceMinor: ptrTo(int64(300)), SKU: ptrTo("SC-1")})
			h.failUpload(lost)
			create := onlyChange(t, h.sentChanges())
			h.saveItem(data.ItemPatch{ID: local, PriceMinor: ptrTo(int64(350))})
			p := h.push()
			if p.Conflicts != 0 || p.Pending != 0 {
				t.Fatalf("push = %+v", p)
			}
			if it, _ := h.item(local); it.Price != 350 {
				t.Fatalf("local price = %d, want 350", it.Price)
			}
			if got := h.cloudRow("item", create.CloudID); num(got.Data["price_minor"]) != 350 {
				t.Fatalf("cloud price = %v, want 350", got.Data["price_minor"])
			}
			chs := h.sentChanges()
			last := chs[len(chs)-1]
			if last.Op != opUpdate || last.CloudID != create.CloudID ||
				jsonOf(t, last.Before["price_minor"]).(float64) != 300 || jsonOf(t, last.After["price_minor"]).(float64) != 350 {
				t.Fatalf("edit = %+v, want update 300 → 350", last)
			}
			if h.count(`SELECT COUNT(*) FROM items WHERE name = 'Scone'`) != 1 {
				t.Fatal("duplicate local rows")
			}
		})
	}
}

// Finding 2: create, a failed upload, delete. The delete must not be
// dropped while the create is in flight, and the create's answer must not
// bring the item back: the till uploads a delete for the accepted
// cloud_id. Final state: gone on the till, deleted in the cloud.
func TestPushCreateThenDeleteWhileTheCreateIsInFlight(t *testing.T) {
	for _, lost := range []bool{true, false} {
		t.Run(map[bool]string{true: "answer-lost", false: "refused"}[lost], func(t *testing.T) {
			h := newPushHarness(t)
			local := "till-made-2"
			h.saveItem(data.ItemPatch{ID: local, Create: true, Name: ptrTo("Bun"), PriceMinor: ptrTo(int64(90)), Barcodes: &[]string{"5000112345670"}})
			h.failUpload(lost)
			create := onlyChange(t, h.sentChanges())
			if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), local); err != nil {
				t.Fatal(err)
			}
			p := h.push()
			if p.Pending != 0 {
				t.Fatalf("push = %+v, want nothing left", p)
			}
			if _, ok := h.item(local); ok {
				t.Fatal("the create's answer brought the deleted item back")
			}
			if r := h.cloudRow("item", create.CloudID); r == nil || !r.Deleted {
				t.Fatalf("cloud row = %+v, want the item deleted in the cloud", r)
			}
			chs := h.sentChanges()
			if last := chs[len(chs)-1]; last.Op != opDelete || last.CloudID != create.CloudID {
				t.Fatalf("last change = %+v, want the delete", last)
			}
			if _, pull := h.tick(); pull.Refused != 0 {
				t.Fatalf("pull = %+v", pull)
			}
			if _, ok := h.item(local); ok {
				t.Fatal("the pull brought the deleted item back")
			}
			if h.marks() != 0 || h.count(`SELECT COUNT(*) FROM items WHERE name = 'Bun'`) != 0 {
				t.Fatal("bookkeeping or a copy left behind")
			}
		})
	}
}

// Finding 3: a row linked by an older applier has no base. Its base is
// seeded from the local row (what the till last pulled) before the
// re-pull, so a till edit made then is a pending field the re-pull leaves
// alone, and uploads with the pulled value as before.
func TestPushBaselessLinkedRowKeepsAnEditAcrossTheRepull(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	// The till as an applier-3 build left it (migration 084 adds the base
	// column NULL).
	h.exec(`UPDATE cloud_links SET base = NULL`)
	h.exec(`UPDATE settings SET value = '3' WHERE key = ?`, KeyApplierVersion)
	if p := h.push(); p.Sent != 0 {
		t.Fatalf("push = %+v", p)
	}
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(260))})
	h.myEdit("item", id, map[string]any{"name": "Café latte"})
	h.mustRun() // the applier-4 re-pull from 0
	if it, _ := h.item(id); it.Price != 260 || it.Name != "Café latte" {
		t.Fatalf("local item = %q %d, want the till's pending price and the cloud's name", it.Name, it.Price)
	}
	p := h.push()
	if p.Accepted != 1 || p.Conflicts != 0 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if len(ch.ChangedFields) != 1 || ch.ChangedFields[0] != "price_minor" || jsonOf(t, ch.Before["price_minor"]).(float64) != 250 {
		t.Fatalf("change = %+v, want price 250 → 260 only", ch)
	}
	if got := h.cloudRow("item", id); num(got.Data["price_minor"]) != 260 || got.Data["name"] != "Café latte" {
		t.Fatalf("cloud = %+v", got.Data)
	}
}

// Finding 3, the pull side alone: the bump seeds the base too (a till
// whose first tick after the upgrade is a pull).
func TestPullBumpSeedsBaselessLinks(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.exec(`UPDATE cloud_links SET base = NULL`)
	h.exec(`UPDATE settings SET value = '3' WHERE key = ?`, KeyApplierVersion)
	st, err := loadState(context.Background(), h.db, data.NewSettingsRepo(h.db))
	if err != nil {
		t.Fatal(err)
	}
	if st.cursor != 0 {
		t.Fatalf("cursor = %d", st.cursor)
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = 'item' AND cloud_id = ? AND base IS NOT NULL`, id); n != 1 {
		t.Fatal("the bump left the linked item without a base")
	}
}

// Finding 4: a 410 resync forgets every echo. A restored cloud may serve
// a different row at the version the till remembers as its own echo; the
// full pull must apply it.
func TestPullResyncForgetsEchoes(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	if p := h.push(); p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	var echo int64
	if err := h.db.QueryRow(`SELECT echo_version FROM cloud_links WHERE entity = 'item' AND cloud_id = ?`, id).Scan(&echo); err != nil || echo == 0 {
		t.Fatalf("echo = %d %v", echo, err)
	}
	h.mustRun()
	// The cloud is restored: the row at the echo's version holds other
	// values, and the cloud asks for a resync.
	h.feed.drop(id)
	h.feed.add(itemFeedRow(1, echo, "Restored latte", 300))
	h.feed.goneOnce = true
	h.mustRun()
	it, _ := h.item(id)
	if it.Price != 300 || it.Name != "Restored latte" {
		t.Fatalf("local item = %q %d, want the restored cloud's row", it.Name, it.Price)
	}
}

// Finding 5: stale is decided when a change is sent, from its created_at,
// not once at seal time: a change sealed fresh and re-sent after 30 days
// goes stale.
func TestPushStaleDecidedAtSendTime(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	h.failUpload(false)
	if onlyChange(t, h.sentChanges()).Stale {
		t.Fatal("a fresh change was sent stale")
	}
	old := pushNow
	pushNow = func() time.Time { return time.Now().UTC().Add(StaleAfter + time.Hour) }
	defer func() { pushNow = old }()
	p := h.push()
	chs := h.sentChanges()
	if len(chs) != 2 || !chs[1].Stale || chs[1].ChangeID != chs[0].ChangeID {
		t.Fatalf("re-sent change = %+v, want the same change, stale", chs)
	}
	if p.Conflicts != 1 {
		t.Fatalf("push = %+v", p)
	}
	if it, _ := h.item(id); it.Price != 250 {
		t.Fatalf("local price = %d, want the cloud's", it.Price)
	}
}
