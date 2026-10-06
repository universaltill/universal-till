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

// ut-docs#3504: the snapshot carries a pre-packed item's net quantity as
// {"value": <int>, "unit": "g"|"ml"|"ea"} so my. can show and edit it; the
// key is absent for an item with none (both columns NULL) or an invalid
// stored pair.
func TestSnapshotItemNetQuantity(t *testing.T) {
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
	for _, id := range []string{"it-500g", "it-none", "it-bad", "it-zero", "it-kg"} {
		testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: id, SKU: id, Name: id, BasePrice: 100, IsActive: true})
	}
	if _, err := db.Exec(`UPDATE items SET net_quantity_value = 500, net_quantity_unit = 'g' WHERE id = 'it-500g'`); err != nil {
		t.Fatal(err)
	}
	// Half a pair (the test schema has no CHECK) must not reach the wire.
	if _, err := db.Exec(`UPDATE items SET net_quantity_value = 250 WHERE id = 'it-bad'`); err != nil {
		t.Fatal(err)
	}

	// A full pair that catalogtypes.ValidNetQuantity rejects (value ≤ 0, an
	// unknown unit) must not reach the wire either.
	if _, err := db.Exec(`UPDATE items SET net_quantity_value = 0, net_quantity_unit = 'g' WHERE id = 'it-zero'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE items SET net_quantity_value = 2, net_quantity_unit = 'kg' WHERE id = 'it-kg'`); err != nil {
		t.Fatal(err)
	}

	if err := pushSnapshotIfChanged(context.Background(), testCfg(srv.URL), db); err != nil {
		t.Fatal(err)
	}
	for _, it := range cloud.snapshots[0]["items"].([]any) {
		m := it.(map[string]any)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		switch m["id"] {
		case "it-500g":
			if !strings.Contains(string(raw), `"net_quantity":{"unit":"g","value":500}`) {
				t.Errorf("it-500g = %s, want net_quantity 500 g", raw)
			}
		default:
			if _, ok := m["net_quantity"]; ok {
				t.Errorf("%v must have no net_quantity key, got %s", m["id"], raw)
			}
		}
	}

	// The wire bytes themselves, in the struct's field order.
	row := snapshotItemRow{NetQuantity: &snapshotNetQuantity{Value: 500, Unit: "g"}}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"net_quantity":{"value":500,"unit":"g"}`) {
		t.Errorf("row JSON = %s", b)
	}
	b, _ = json.Marshal(snapshotItemRow{})
	if strings.Contains(string(b), "net_quantity") {
		t.Errorf("empty row JSON carries net_quantity: %s", b)
	}
}
