package catalogpull

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6071 (contract cloud-catalog-changes.md 1.2.0): the cloud masters
// the item and category icon. icon uploads like the other mastered fields;
// until a row's icon upload is answered, the till's icon is a pending edit
// (its base is "", the value the cloud held while it never wrote the
// column) and the pull keeps it.

const (
	pendingItemIcon = "lucide:beer"
	pendingCatIcon  = "lucide:coffee"
)

// setIcon is a till-side icon edit (the update trigger marks it on a
// cloud-master main till, as the editor's save does).
func (h *pushHarness) setIcon(table, id, icon string) {
	h.t.Helper()
	h.exec(`UPDATE `+table+` SET icon = ? WHERE id = ?`, icon, id)
}

func (h *pushHarness) icon(table, id string) string {
	h.t.Helper()
	return h.list(`SELECT COALESCE(icon,'') FROM `+table+` WHERE id = ?`, id)
}

// seedCatAndItem pulls a category and an item (both icon "", as the cloud
// served them before it mastered icons).
func (h *pushHarness) seedCatAndItem() string {
	h.t.Helper()
	h.feed.add(catRow(cCat, h.feed.high+1, "Coffee", nil))
	return h.seedPulled(1, "Latte", 300)
}

// AC4: a linked item and category whose till icon is pending (base "" and
// a mark) keep it when the pull serves "" with a cloud-side edit.
func TestPullKeepsPendingIcon_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, pendingItemIcon)
	h.setIcon("categories", cCat, pendingCatIcon)
	if h.marks() != 2 {
		t.Fatalf("marks = %d, want one per icon edit", h.marks())
	}
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	h.myEdit("category", cCat, map[string]any{"name": "Hot drinks"})
	h.mustRun()
	if it, _ := h.item(id); it.Price != 350 {
		t.Fatalf("price = %d, the cloud's edit must still apply", it.Price)
	}
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q, want the pending till icon kept", got)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q, want the pending till icon kept", got)
	}
	if got := h.list(`SELECT name FROM categories WHERE id = ?`, cCat); got != "Hot drinks" {
		t.Fatalf("category name = %q, the cloud's edit must still apply", got)
	}
}

// An accepted answer applies the cloud's current row: a category icon
// upload and an item price edit answered in one push both end with the
// uploaded icon in place.
func TestPullKeepsIconAcrossAnAcceptedAnswer_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("categories", cCat, pendingCatIcon)
	h.exec(`DELETE FROM cloud_outbox WHERE entity = 'item'`)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(320))})
	h.myEdit("category", cCat, map[string]any{"sort_order": 9})
	// The category's icon upload is answered before the item's price edit
	// is looked at: both accepted, and the cloud then holds the icon.
	p, _ := h.tick()
	if p.Accepted != 2 {
		t.Fatalf("push = %+v, want both rows accepted", p)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q after the accepted answer", got)
	}
	if row := h.cloudRow("category", cCat); row.Data["icon"] != pendingCatIcon {
		t.Fatalf("cloud category icon = %v, want the uploaded icon", row.Data["icon"])
	}
}

// AC1/AC2/AC3: once the cloud holds the icon (base = the icon, no mark) a
// cloud-side edit serves it and the pull applies it; the item's photo and
// its file stay, and clearing the photo shows the icon.
func TestPullAppliesMasteredIcon_6071(t *testing.T) {
	h := newPushHarness(t)
	h.feed.add(catRow(cCat, h.feed.high+1, "Coffee", nil))
	it := itemRowData("Latte", 300)
	it["icon"] = pendingItemIcon
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: h.feed.high + 1, Data: it})
	h.mustRun()
	id := itemID(1)
	photo := "/public/assets/items/" + id + "/thumb.png"
	h.exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-6071', ?, ?, 'thumbnail')`, id, photo)

	// A cloud-side price change serves the same icon.
	d := itemRowData("Latte", 350)
	d["icon"] = pendingItemIcon
	h.feed.add(feedRow{Kind: "item", CloudID: id, Version: h.feed.high + 1, Data: d})
	h.mustRun()
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q after a price pull", got)
	}
	if got := h.list(`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id); got != photo {
		t.Fatalf("photo = %q, want kept", got)
	}
	// A new icon served by the cloud is applied over the old one.
	d2 := itemRowData("Latte", 350)
	d2["icon"] = "lucide:wine"
	h.feed.add(feedRow{Kind: "item", CloudID: id, Version: h.feed.high + 1, Data: d2})
	h.mustRun()
	if got := h.icon("items", id); got != "lucide:wine" {
		t.Fatalf("item icon = %q, want the cloud's new icon", got)
	}
	// AC2: the photo clears, the icon is what the sale screen shows, and it
	// survives a further cloud edit.
	h.exec(`DELETE FROM item_images WHERE item_id = ?`, id)
	d3 := itemRowData("Latte", 360)
	d3["icon"] = "lucide:wine"
	h.feed.add(feedRow{Kind: "item", CloudID: id, Version: h.feed.high + 1, Data: d3})
	h.mustRun()
	if got := h.icon("items", id); got != "lucide:wine" {
		t.Fatalf("item icon = %q after the photo cleared", got)
	}

	// AC3: a category.
	c := catRow(cCat, h.feed.high+1, "Coffee", nil)
	c.Data["icon"] = pendingCatIcon
	h.feed.add(c)
	h.mustRun()
	c2 := catRow(cCat, h.feed.high+1, "Hot coffee", nil)
	c2.Data["icon"] = pendingCatIcon
	h.feed.add(c2)
	h.mustRun()
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q after a rename pull", got)
	}
}

// AC6: a mastered clear. The base holds the icon (the cloud accepted it) and
// there is no mark: a served "" clears the till icon; the photo stays.
func TestPullAppliesMasteredIconClear_6071(t *testing.T) {
	h := newPushHarness(t)
	h.feed.add(catRow(cCat, h.feed.high+1, "Coffee", nil))
	it := itemRowData("Latte", 300)
	it["icon"] = pendingItemIcon
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: h.feed.high + 1, Data: it})
	c := catRow(cCat, h.feed.high+1, "Coffee", nil)
	c.Data["icon"] = pendingCatIcon
	h.feed.add(c)
	h.mustRun()
	id := itemID(1)
	photo := "/public/assets/items/" + id + "/thumb.png"
	h.exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-6071c', ?, ?, 'thumbnail')`, id, photo)
	if h.marks() != 0 {
		t.Fatalf("marks = %d", h.marks())
	}
	h.feed.add(feedRow{Kind: "item", CloudID: id, Version: h.feed.high + 1, Data: itemRowData("Latte", 300)})
	h.feed.add(catRow(cCat, h.feed.high+1, "Coffee", nil))
	h.mustRun()
	if got := h.icon("items", id); got != "" {
		t.Fatalf("item icon = %q, want the mastered clear applied", got)
	}
	if got := h.icon("categories", cCat); got != "" {
		t.Fatalf("category icon = %q, want the mastered clear applied", got)
	}
	if got := h.list(`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id); got != photo {
		t.Fatalf("photo = %q, want kept", got)
	}
}

// AC5: a create applies the served icon, "" gives none.
func TestPullCreateAppliesIconAsServed_6071(t *testing.T) {
	h := newPushHarness(t)
	c := catRow(cCat, 1, "Coffee", nil)
	c.Data["icon"] = pendingCatIcon
	h.feed.add(c)
	it := itemRowData("Latte", 300)
	it["icon"] = pendingItemIcon
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 2, Data: it})
	h.feed.add(itemFeedRow(2, 3, "Mocha", 400))
	h.mustRun()
	if got := h.icon("items", itemID(1)); got != pendingItemIcon {
		t.Fatalf("created item icon = %q", got)
	}
	if got := h.icon("items", itemID(2)); got != "" {
		t.Fatalf("an icon-less create got icon %q", got)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("created category icon = %q", got)
	}
	if h.marks() != 0 {
		t.Fatalf("marks = %d: a pull's own writes must never upload", h.marks())
	}
}

// AC7: a till icon edit uploads as changed_fields ["icon"] with the base ""
// as before, for an item and a category; the cloud then serves it.
func TestPushUploadsIconEdit_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, pendingItemIcon)
	h.setIcon("categories", cCat, pendingCatIcon)
	p := h.push()
	if p.Accepted != 2 {
		t.Fatalf("push = %+v", p)
	}
	byKind := map[string]Change{}
	for _, ch := range h.sentChanges() {
		byKind[ch.Kind] = ch
	}
	for kind, want := range map[string]string{"item": pendingItemIcon, "category": pendingCatIcon} {
		ch, ok := byKind[kind]
		if !ok {
			t.Fatalf("no %s change sent", kind)
		}
		if !reflect.DeepEqual(ch.ChangedFields, []string{"icon"}) {
			t.Fatalf("%s changed_fields = %v", kind, ch.ChangedFields)
		}
		if got := norm(ch.Before["icon"]); got != `""` {
			t.Fatalf("%s before icon = %s, want the seeded base", kind, got)
		}
		if got := norm(ch.After["icon"]); got != `"`+want+`"` {
			t.Fatalf("%s after icon = %s", kind, got)
		}
	}
	if row := h.cloudRow("item", id); row.Data["icon"] != pendingItemIcon {
		t.Fatalf("cloud item icon = %v", row.Data["icon"])
	}
	h.mustRun()
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q after the echo", got)
	}
	if p, _ := h.tick(); p.Sent != 0 {
		t.Fatalf("settled tick sent %d", p.Sent)
	}
}

// A malformed local icon never uploads: it cannot 400 or conflict. It reads
// as unchanged ("").
func TestPushNeverUploadsMalformedIcon_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, "Not An Icon!")
	h.setIcon("categories", cCat, "x:"+string(make([]byte, 80)))
	p := h.push()
	if p.Sent != 0 || len(h.sentChanges()) != 0 {
		t.Fatalf("push = %+v, sent %+v; a malformed icon must not upload", p, h.sentChanges())
	}
	if h.marks() != 0 {
		t.Fatalf("marks = %d, want the empty marks dropped", h.marks())
	}
}

func TestIconOut_6071(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "lucide:beer": "lucide:beer", "Lucide:Beer": "", "nocolon": "", "a:b:c": "",
	} {
		if got := iconOut(in); got != want {
			t.Errorf("iconOut(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pulled null icon is the cloud's "" (not a null base, which would differ
// from the local "" and upload a before:null, a 400).
func TestBaseFromRowNullIconIsEmpty(t *testing.T) {
	for _, kind := range []string{kindItem, kindCategory} {
		f := fields{"icon": rawJSON(nil)}
		got := baseFromRow(kind, f, nil)
		if norm(got["icon"]) != `""` {
			t.Fatalf("%s: base icon = %s, want \"\"", kind, got["icon"])
		}
	}
}

// An applier-3 link has no base: seeding sets its icon base to "" (never
// the local value) and marks a row with a local icon, so the first upload
// seeds the cloud and the pull keeps the till icon meanwhile.
func TestSeedBases_IconBaseEmptyAndMarked_6071(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.setIcon("items", id, pendingItemIcon)
	h.setIcon("categories", cCat, pendingCatIcon)
	h.exec(`DELETE FROM cloud_outbox`)
	h.exec(`UPDATE cloud_links SET base = NULL`)
	if err := seedBases(context.Background(), h.db); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"item", "category"} {
		var st string
		if err := h.db.QueryRow(`SELECT base FROM cloud_links WHERE entity = ?`, kind).Scan(&st); err != nil {
			t.Fatal(err)
		}
		if got := norm(parseBase(st)["icon"]); got != `""` {
			t.Fatalf("%s base icon = %s, want the cloud default", kind, got)
		}
	}
	if h.marks() != 2 {
		t.Fatalf("marks = %d, want the iconned rows marked", h.marks())
	}
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	h.mustRun()
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q, want kept while the seed upload is pending", got)
	}
	if p := h.push(); p.Accepted != 2 {
		t.Fatalf("push = %+v", p)
	}
	if row := h.cloudRow("item", id); row.Data["icon"] != pendingItemIcon {
		t.Fatalf("cloud item icon = %v", row.Data["icon"])
	}
}
