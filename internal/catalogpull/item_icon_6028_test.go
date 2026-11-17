package catalogpull

import "testing"

// ut-docs#6028: a pulled non-empty item icon keeps the item's uploaded
// photo and deletes no file; icon "" still clears the icon but keeps it.
func TestPullItemIconKeepsPhoto(t *testing.T) {
	h := newHarness(t)
	var cleared []string
	h.opt.ItemPhotoReplaced = func(_, p string) { cleared = append(cleared, p) }
	h.feed.rows = append(h.feed.rows, itemFeedRow(1, 1, "Lager", 450))
	h.feed.high = 1
	h.mustRun()

	id := itemID(1)
	photo := "/public/assets/items/" + id + "/thumb.png"
	if _, err := h.db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-6028', ?, ?, 'thumbnail')`, id, photo); err != nil {
		t.Fatal(err)
	}
	state := func() (path, icon string) {
		h.t.Helper()
		_ = h.db.QueryRow(`SELECT COALESCE((SELECT path FROM item_images WHERE item_id = items.id AND role='thumbnail'),''), COALESCE(icon,'') FROM items WHERE id = ?`, id).Scan(&path, &icon)
		return
	}

	d := itemRowData("Lager", 450)
	d["icon"] = "lucide:beer"
	h.feed.rows = append(h.feed.rows, feedRow{Kind: "item", CloudID: id, Version: 2, Data: d})
	h.feed.high = 2
	h.mustRun()
	if p, ic := state(); p != photo || ic != "lucide:beer" {
		t.Fatalf("after icon pull: photo=%q icon=%q", p, ic)
	}
	if len(cleared) != 0 {
		t.Fatalf("ItemPhotoReplaced called with %v, want none", cleared)
	}

	h.feed.rows = append(h.feed.rows, feedRow{Kind: "item", CloudID: id, Version: 3, Data: itemRowData("Lager", 450)})
	h.feed.high = 3
	h.mustRun()
	if p, ic := state(); p != photo || ic != "" {
		t.Fatalf("after icon-less pull: photo=%q icon=%q, want photo kept, icon cleared", p, ic)
	}
}
