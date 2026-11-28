package catalogpull

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6242: a replica's cloud_outbox is a byte copy of the main till's
// from the join. The main uploads those edits itself, so the promoted till
// must not act on them: PromoteReplica drops the open marks and keeps the
// sealed ones (they replay idempotently).

const inheritedMarkAge = "2020-01-01T00:00:00Z" // fixed-date:ok age of an inherited mark, outside the window

// replicaWithInheritedMark is a till that joined as a replica: item 1 linked
// with base price 100, local price 120 (LAN-synced from the main, no trigger
// mark on a replica) and the main's open mark for it.
func replicaWithInheritedMark(t *testing.T) (*pushHarness, string) {
	t.Helper()
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 100)
	h.exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.local')`)
	h.exec(`UPDATE items SET base_price = 120 WHERE id = ?`, id)
	h.exec(`INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', ?, ?)`, id, inheritedMarkAge)
	if h.marks() != 1 {
		t.Fatalf("marks = %d, want only the inherited one", h.marks())
	}
	return h, id
}

func (h *pushHarness) promote() {
	h.t.Helper()
	if err := data.NewSettingsRepo(h.db).PromoteReplica(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pushHarness) sentItemChange(id string) *Change {
	for _, ch := range h.sentChanges() {
		if ch.Kind == "item" && ch.CloudID == id {
			c := ch
			return &c
		}
	}
	return nil
}

func TestPromotedReplicaDoesNotResendInheritedEdit_6242(t *testing.T) {
	h, id := replicaWithInheritedMark(t)
	h.promote()
	h.push()
	if ch := h.sentItemChange(id); ch != nil {
		t.Fatalf("sent an inherited change: %+v", ch)
	}
	h.myEdit("item", id, map[string]any{"price_minor": 130})
	h.mustRun()
	if it, _ := h.item(id); it.Price != 130 {
		t.Fatalf("price = %d, want the cloud's 130", it.Price)
	}
	if p := h.push(); p.Accepted+p.Conflicts+p.Pending != 0 || h.sentItemChange(id) != nil {
		t.Fatalf("push = %+v after the pull; the field must not be pending", p)
	}
}

func TestPromotedReplicaNoABALostUpdate_6242(t *testing.T) {
	h, id := replicaWithInheritedMark(t)
	h.myEdit("item", id, map[string]any{"price_minor": 120})
	h.myEdit("item", id, map[string]any{"price_minor": 100})
	h.promote()
	h.push()
	h.mustRun()
	for _, ch := range h.sentChanges() {
		if ch.Kind == "item" && norm(ch.After["price_minor"]) == "120" {
			t.Fatalf("sent the stale after:120 change: %+v", ch)
		}
	}
	if got := h.cloudRow("item", id); num(got.Data["price_minor"]) != 100 {
		t.Fatalf("cloud price = %v, want 100", got.Data["price_minor"])
	}
	if it, _ := h.item(id); it.Price != 100 {
		t.Fatalf("local price = %d, want the cloud's 100", it.Price)
	}
}

func TestPromotedReplicaOldCursorAppliesLaterChanges_6242(t *testing.T) {
	h := newPushHarness(t)
	id1 := h.seedPulled(1, "Latte", 100)
	id2 := h.seedPulled(2, "Mocha", 200)
	cursor := h.cursor()
	h.exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.local')`)
	h.exec(`INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', ?, ?)`, id1, inheritedMarkAge)
	h.myEdit("item", id1, map[string]any{"price_minor": 250})
	h.feed.add(feedRow{Kind: "item", CloudID: id2, Version: h.feed.high + 1, Deleted: true})
	h.promote()
	if h.cursor() != cursor {
		t.Fatal("promotion must keep the pull cursor")
	}
	h.mustRun()
	if it, _ := h.item(id1); it.Price != 250 {
		t.Fatalf("price = %d, want the later cloud edit 250", it.Price)
	}
	if it, ok := h.item(id2); ok && it.Active {
		t.Fatalf("item 2 = %+v, want the tombstone applied", it)
	}
	if h.cursor() != h.feed.high {
		t.Fatalf("cursor = %d, want high water %d", h.cursor(), h.feed.high)
	}
}

func TestPromotedReplicaReplaysInheritedSealedChange_6242(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedPulled(1, "Latte", 100)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(150))})
	h.failUpload(true) // the cloud applied it, the answer was lost: a sealed mark
	first := onlyChange(t, h.sentChanges())
	h.myEdit("item", id, map[string]any{"price_minor": 200})
	h.exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.local')`)
	h.promote()
	var sealed int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM cloud_outbox WHERE change_id = ? AND sealed_at IS NOT NULL`, first.ChangeID).Scan(&sealed); err != nil || sealed != 1 {
		t.Fatalf("sealed rows = %d, %v; promotion must keep the in-flight change", sealed, err)
	}
	before := h.cloudRow("item", id).Version
	h.push()
	sends := 0
	for _, ch := range h.sentChanges() {
		if ch.ChangeID == first.ChangeID {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("change_id sent %d times, want the original plus one replay", sends)
	}
	if got := h.cloudRow("item", id); got.Version != before {
		t.Fatalf("cloud version %d -> %d: the replay must not apply again", before, got.Version)
	}
	var v int64
	if err := h.db.QueryRow(`SELECT version FROM cloud_links WHERE entity = 'item' AND local_id = ?`, id).Scan(&v); err != nil || v != before {
		t.Fatalf("link version = %d, %v; want the cloud's current %d", v, err, before)
	}
}

// The control: only the primary_url is removed (the pre-#6242 promotion), and
// the inherited mark uploads the stale change. Pins the bug so the tests above
// cannot pass for the wrong reason.
func TestPromotedReplicaInheritedMarkControl_6242(t *testing.T) {
	h, id := replicaWithInheritedMark(t)
	h.exec(`DELETE FROM settings WHERE key = 'sync.primary_url'`)
	h.push()
	ch := h.sentItemChange(id)
	if ch == nil || norm(ch.After["price_minor"]) != "120" {
		t.Fatalf("sent %+v, want the stale after:120 change", ch)
	}
}
