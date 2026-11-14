package catalogpull

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Tester additions for ut-docs#6071: drive the promotion through the real
// migration 103 SQL (not the update trigger), then the full chain the card
// promises — kept while pending, seeded by the upload, served by later cloud
// edits with the photo intact, and a mastered clear in both directions.

// rerun103 replays migration 103 on the harness DB after putting it back in
// its pre-103 state (bases without an icon key, no marks): the shape an
// upgraded till is in when the migration runs.
func (h *pushHarness) rerun103() {
	h.t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("..", "db", "migrations", "103_cloud_outbox_icon.sql"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.exec(`DELETE FROM cloud_outbox`)
	h.exec(`UPDATE cloud_links SET base = json_remove(base, '$.icon') WHERE base IS NOT NULL`)
	h.exec(string(sqlText))
}

func TestMigration103SeedsThroughPullAndPush_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, pendingItemIcon)
	h.setIcon("categories", cCat, pendingCatIcon)
	photo := "/public/assets/items/" + id + "/thumb.png"
	h.exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-t6071', ?, ?, 'thumbnail')`, id, photo)
	h.rerun103()
	if h.marks() != 2 {
		t.Fatalf("migration marks = %d, want the item and the category", h.marks())
	}

	// AC4: an older/unseeded cloud serves "" with a cloud-side edit; the
	// pull (without an upload first) keeps both icons.
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	h.myEdit("category", cCat, map[string]any{"name": "Hot drinks"})
	h.mustRun()
	if it, _ := h.item(id); it.Price != 350 {
		t.Fatalf("price = %d, the cloud edit must apply", it.Price)
	}
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q, want kept while pending", got)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q, want kept while pending", got)
	}

	// The seed upload: before "" (the migrated base), after the icon.
	if p := h.push(); p.Accepted != 2 {
		t.Fatalf("push = %+v", p)
	}
	for _, ch := range h.sentChanges() {
		if !reflect.DeepEqual(ch.ChangedFields, []string{"icon"}) || norm(ch.Before["icon"]) != `""` {
			t.Fatalf("%s change = fields %v before %s", ch.Kind, ch.ChangedFields, ch.Before["icon"])
		}
	}
	h.mustRun()

	// AC1/AC3: a later cloud-side write serves the mastered icon; the till
	// keeps it and the photo.
	h.myEdit("item", id, map[string]any{"price_minor": 400})
	h.myEdit("category", cCat, map[string]any{"sort_order": 7})
	if p, _ := h.tick(); p.Sent != 0 {
		t.Fatalf("a settled tick sent %d", p.Sent)
	}
	if it, _ := h.item(id); it.Price != 400 {
		t.Fatalf("price = %d", it.Price)
	}
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q after a later cloud write", got)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q after a later cloud write", got)
	}
	if got := h.list(`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id); got != photo {
		t.Fatalf("photo = %q, want kept", got)
	}

	// AC6: a cloud-side clear now clears the till (the base holds the icon).
	h.myEdit("item", id, map[string]any{"icon": ""})
	h.myEdit("category", cCat, map[string]any{"icon": ""})
	h.mustRun()
	if got := h.icon("items", id); got != "" {
		t.Fatalf("item icon = %q, want the mastered clear", got)
	}
	if got := h.icon("categories", cCat); got != "" {
		t.Fatalf("category icon = %q, want the mastered clear", got)
	}
	if got := h.list(`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id); got != photo {
		t.Fatalf("photo = %q after the clear, want kept", got)
	}
	if h.marks() != 0 {
		t.Fatalf("marks = %d: the pull's clear must not upload", h.marks())
	}
}

// R5: a deliberate till-side clear of a mastered icon uploads before=icon,
// after="" and the cloud then serves "" (no resurrection on the next pull).
func TestPushUploadsIconClear_6071(t *testing.T) {
	h := newPushHarness(t)
	h.feed.add(catRow(cCat, h.feed.high+1, "Coffee", nil))
	it := itemRowData("Latte", 300)
	it["icon"] = pendingItemIcon
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: h.feed.high + 1, Data: it})
	h.mustRun()
	id := itemID(1)
	h.setIcon("items", id, "")
	ch := func() Change {
		h.push()
		return onlyChange(t, h.sentChanges())
	}()
	if !reflect.DeepEqual(ch.ChangedFields, []string{"icon"}) || norm(ch.Before["icon"]) != `"`+pendingItemIcon+`"` || norm(ch.After["icon"]) != `""` {
		t.Fatalf("clear change = fields %v before %s after %s", ch.ChangedFields, ch.Before["icon"], ch.After["icon"])
	}
	if row := h.cloudRow("item", id); row.Data["icon"] != "" {
		t.Fatalf("cloud icon = %v, want cleared", row.Data["icon"])
	}
	h.mustRun()
	if got := h.icon("items", id); got != "" {
		t.Fatalf("item icon = %q, the clear must not be undone", got)
	}
}

// Known limit (architect §6, deviates from BA AC4's conflict clause): a
// change carrying a pending icon that loses a by-value conflict on another
// field is answered conflict; the till applies the cloud row and the icon
// falls back to the cloud's "". This pins the documented behaviour so a
// change to it is deliberate.
func TestPushIconConflictAppliesCloudRow_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, pendingItemIcon)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(320))})
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	p, _ := h.tick()
	if p.Conflicts != 1 {
		t.Fatalf("push = %+v, want one conflict", p)
	}
	if it, _ := h.item(id); it.Price != 350 {
		t.Fatalf("price = %d, the cloud wins the conflict", it.Price)
	}
	if got := h.icon("items", id); got != "" {
		t.Fatalf("item icon = %q; the documented limit says the cloud row (\"\") applies", got)
	}
	var n int
	if err := h.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cloud_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("outbox = %d after the conflict", n)
	}
}
