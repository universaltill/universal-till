package cloudsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3015: the snapshot carries each item's sell-screen state as
// "sell_screen": "hidden" | "removed" so my. can show it; the key is absent
// for an item on the sell screen, which is also what an older till sends.
func TestSnapshotItemSellScreen(t *testing.T) {
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
	for _, id := range []string{"it-visible", "it-hidden", "it-removed", "it-both"} {
		testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: id, SKU: id, Name: id, BasePrice: 100, IsActive: true})
	}
	for _, q := range []string{
		`UPDATE items SET sell_screen_hidden = 1 WHERE id = 'it-hidden'`,
		`UPDATE items SET sell_screen_removed = 1 WHERE id = 'it-removed'`,
		`UPDATE items SET sell_screen_hidden = 1, sell_screen_removed = 1 WHERE id = 'it-both'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"it-hidden": "hidden", "it-removed": "removed", "it-both": "removed"}
	for _, it := range cloud.snapshots[0]["items"].([]any) {
		m := it.(map[string]any)
		raw, _ := json.Marshal(m)
		id, _ := m["id"].(string)
		got, ok := m["sell_screen"]
		if w, flagged := want[id]; flagged {
			if got != w {
				t.Errorf("%s = %s, want sell_screen %q", id, raw, w)
			}
		} else if ok {
			t.Errorf("%s is visible and must have no sell_screen key, got %s", id, raw)
		}
	}

	b, _ := json.Marshal(snapshotItemRow{})
	if strings.Contains(string(b), "sell_screen") {
		t.Errorf("empty row JSON carries sell_screen: %s", b)
	}
}
