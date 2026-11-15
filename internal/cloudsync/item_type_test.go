package cloudsync

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4214: the catalog snapshot carries item_type (so my. and the
// cloud's customer-facing surfaces can tell a gift voucher item apart), and
// save_item decodes item_type presence-aware.
func TestSnapshotCarriesItemType(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testsupport.NewCatalogTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Gift voucher", BasePrice: 0, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "it-2", SKU: "SKU2", Name: "Bread", BasePrice: 200, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET item_type = 'gift_voucher' WHERE id = 'it-1'`); err != nil {
		t.Fatal(err)
	}
	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db, nil); err != nil {
		t.Fatal(err)
	}
	items := cloud.snapshots[0]["items"].([]any)
	byID := map[string]map[string]any{}
	for _, it := range items {
		row := it.(map[string]any)
		byID[row["id"].(string)] = row
	}
	if byID["it-1"]["item_type"] != "gift_voucher" || byID["it-2"]["item_type"] != "standard" {
		t.Fatalf("item_type = %v / %v", byID["it-1"]["item_type"], byID["it-2"]["item_type"])
	}
}

func TestApplySaveItem_ItemType(t *testing.T) {
	var got data.ItemPatch
	hooks := Hooks{SaveItem: func(ctx context.Context, p data.ItemPatch) (string, error) {
		got = p
		return "ok", nil
	}}
	if status, _ := apply(context.Background(), directive{Type: "save_item", Payload: map[string]any{"id": "i1", "item_type": "gift_voucher"}}, hooks); status != "applied" || got.ItemType == nil || *got.ItemType != "gift_voucher" {
		t.Fatalf("set: %q %+v", status, got.ItemType)
	}
	if status, msg := apply(context.Background(), directive{Type: "save_item", Payload: map[string]any{"id": "i1", "item_type": "deposit"}}, hooks); status != "failed" || msg != "bad item_type" {
		t.Fatalf("unknown type: %q %q", status, msg)
	}
	if status, _ := apply(context.Background(), directive{Type: "save_item", Payload: map[string]any{"id": "i1", "name": "x"}}, hooks); status != "applied" || got.ItemType != nil {
		t.Fatalf("absent must stay nil (keep): %+v", got.ItemType)
	}
}
