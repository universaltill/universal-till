package cloudsync

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3584: the snapshot carries each item's icon id, so my. shows and
// preselects what the till draws — the stored items.icon, or the id of a
// library tile the till's own picker stored as a thumbnail path (the same
// iconid.EffectiveIcon read the category report uses). "" for a photo or
// no picture.
func TestSnapshotItemIcon(t *testing.T) {
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"it-icon", "it-tile", "it-photo", "it-none"} {
		testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: id, SKU: id, Name: id, BasePrice: 100, IsActive: true})
	}
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'it-icon'`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedImage(t, db, "img-1", "it-tile", "/public/assets/category-icons/coffee.svg")
	testsupport.SeedImage(t, db, "img-2", "it-photo", "/public/assets/items/it-photo/thumb.png")

	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, it := range cloud.snapshots[0]["items"].([]any) {
		m := it.(map[string]any)
		v, ok := m["icon"]
		if !ok {
			t.Fatalf("item %v has no icon field", m["id"])
		}
		got[m["id"].(string)] = v
	}
	want := map[string]any{"it-icon": "lucide:beer", "it-tile": "lucide:coffee", "it-photo": "", "it-none": ""}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s icon = %v, want %q", id, got[id], w)
		}
	}
}
