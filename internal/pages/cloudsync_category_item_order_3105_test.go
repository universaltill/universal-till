package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// seedItemOrderCatalogue: two categories whose items interleave in the
// sale-screen grid (all implicit tiles, so name order): Apple(a1) Banana(b1)
// Cherry(a2) Date(b2) Egg(a3). Codes are the SKUs.
func seedItemOrderCatalogue(t *testing.T, dp *common.Deps) *ui.ButtonStore {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO categories(id, name, parent_id, sort_order) VALUES ('io-a','IO Alpha',NULL,1),('io-b','IO Beta',NULL,2)`,
		`INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
			('ioa1','IOA1','Apple IO',100,'io-a',1),
			('iob1','IOB1','Banana IO',100,'io-b',1),
			('ioa2','IOA2','Cherry IO',100,'io-a',1),
			('iob2','IOB2','Date IO',100,'io-b',1),
			('ioa3','IOA3','Egg IO',100,'io-a',1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return ui.NewButtonStore(dp.Db)
}

// ioCodes is the codes of the seeded tiles, in sale-screen grid order.
func ioCodes(t *testing.T, store *ui.ButtonStore) string {
	t.Helper()
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range btns {
		if strings.HasPrefix(b.Code, "IO") {
			out = append(out, b.Code)
		}
	}
	return strings.Join(out, ",")
}

func cloudCategoryItemOrderAudits(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_category_item_order_set' AND actor_id = 'system' AND entity_id = 'io-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// §3.14 (ut-docs#3105): set_category_item_order deals the category's items
// into the sale-screen slots its tiles already hold, audits a real change,
// and replays to the same text with nothing written.
func TestCloudSetCategoryItemOrder_AppliesAuditsAndReplays(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	store := seedItemOrderCatalogue(t, dp)
	hooks := buildCloudHooks(dp, nil)
	if hooks.SetCategoryItemOrder == nil {
		t.Fatal("SetCategoryItemOrder hook not wired")
	}
	if got := ioCodes(t, store); got != "IOA1,IOB1,IOA2,IOB2,IOA3" {
		t.Fatalf("starting order = %s", got)
	}
	msg, err := hooks.SetCategoryItemOrder(ctx, "io-a", []string{"ioa3", "ioa1", "ioa2"})
	if err != nil || msg != "item order applied to 3 items in category IO Alpha" {
		t.Fatalf("apply: %q %v", msg, err)
	}
	// Beta's slots (1, 3) hold; Alpha's slots (0, 2, 4) take the new order.
	if got := ioCodes(t, store); got != "IOA3,IOB1,IOA1,IOB2,IOA2" {
		t.Fatalf("order = %s, want IOA3,IOB1,IOA1,IOB2,IOA2", got)
	}
	if n := cloudCategoryItemOrderAudits(t, dp); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'cloud_category_item_order_set'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"category_id":"io-a"`) || !strings.Contains(payload, `"item_ids":["ioa3","ioa1","ioa2"]`) {
		t.Fatalf("audit payload = %s", payload)
	}
	// Lost-result replay: same text, nothing written, no second audit row.
	if msg, err := hooks.SetCategoryItemOrder(ctx, "io-a", []string{"ioa3", "ioa1", "ioa2"}); err != nil || msg != "item order applied to 3 items in category IO Alpha" {
		t.Fatalf("replay: %q %v", msg, err)
	}
	if got := ioCodes(t, store); got != "IOA3,IOB1,IOA1,IOB2,IOA2" {
		t.Fatalf("replay order = %s", got)
	}
	if n := cloudCategoryItemOrderAudits(t, dp); n != 1 {
		t.Fatalf("a no-op replay added audit rows: %d", n)
	}
}

// §3.14: every refusal is decided before anything is written.
func TestCloudSetCategoryItemOrder_Refusals(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	store := seedItemOrderCatalogue(t, dp)
	hooks := buildCloudHooks(dp, nil)
	for _, c := range []struct {
		cat  string
		ids  []string
		want string
	}{
		{"io-gone", []string{"ioa1"}, "category io-gone is not on this till"},
		{"io-a", []string{"ioa3", "ghost", "ioa1"}, "item ghost is not on this till"},
		{"io-a", []string{"ioa3", "iob1", "ioa1"}, "item iob1 is not in category io-a on this till"},
		{"io-a", []string{"ioa1", "ioa1"}, "duplicate item id ioa1"},
		{"io-a", []string{"ioa1", " "}, "blank item id"},
		{"io-a", nil, "missing item_ids"},
	} {
		_, err := hooks.SetCategoryItemOrder(ctx, c.cat, c.ids)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s %v: err = %v, want %q", c.cat, c.ids, err, c.want)
		}
	}
	if got := ioCodes(t, store); got != "IOA1,IOB1,IOA2,IOB2,IOA3" {
		t.Fatalf("a refused order changed the till: %s", got)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shortcut_buttons`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused order wrote %d shortcut rows (err %v)", n, err)
	}
	if n := cloudCategoryItemOrderAudits(t, dp); n != 0 {
		t.Fatalf("a refused order wrote %d audit rows", n)
	}
	// Listed items with no tile at all: nothing to order.
	if _, err := dp.Db.Exec(`UPDATE items SET sell_screen_removed = 1 WHERE id = 'ioa2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.SetCategoryItemOrder(ctx, "io-a", []string{"ioa2"}); err == nil || err.Error() != "none of these items has a sale-screen tile" {
		t.Fatalf("no-tile err = %v", err)
	}
	setReplica(t, dp)
	if _, err := buildCloudHooks(dp, nil).SetCategoryItemOrder(ctx, "io-a", []string{"ioa1", "ioa3"}); err == nil {
		t.Fatal("set_category_item_order must be refused on a replica")
	}
}

// §3.7: the wired ItemTilePositions hook reports the grid index.
func TestCloudItemTilePositions_Wired(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	store := seedItemOrderCatalogue(t, dp)
	hooks := buildCloudHooks(dp, nil)
	if hooks.ItemTilePositions == nil {
		t.Fatal("ItemTilePositions hook not wired")
	}
	got, err := hooks.ItemTilePositions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	btns, _ := store.Load()
	for i, b := range btns {
		if b.ItemID == "ioa3" && got["ioa3"] != i {
			t.Fatalf("ioa3 position = %d, want %d", got["ioa3"], i)
		}
	}
	if _, ok := got["ioa3"]; !ok {
		t.Fatal("ioa3 has no position")
	}
}
