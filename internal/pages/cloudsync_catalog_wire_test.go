package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Manage-shop catalog contract §3 (ut-docs
// reference/manage-shop-catalog-api.md): the till-side hooks behind the
// five new directives, driven through buildCloudHooks exactly as Tick
// does. Each applies through the repository write path, is refused on a
// satellite till, writes one audit row and is idempotent.

func cloudAuditCount(t *testing.T, dp *common.Deps, action, entityID string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND entity_id = ? AND actor_id = 'system'`, action, entityID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sp(s string) *string        { return &s }
func bp(b bool) *bool            { return &b }
func ip(n int64) *int64          { return &n }
func lp(ids ...string) *[]string { return &ids }
func setReplica(t *testing.T, dp *common.Deps) {
	t.Helper()
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
}

const newItemID = "0b6c9a8e-1111-4111-8111-111111111111"

func TestCloudSaveItem_CreateReplayConflictAudit(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.SaveItem == nil {
		t.Fatal("SaveItem hook not wired")
	}
	p := data.ItemPatch{ID: newItemID, Create: true, Name: sp("Oat latte"), PriceMinor: ip(390), Barcodes: lp("4006381333931")}
	msg, err := hooks.SaveItem(ctx, p)
	if err != nil || msg != "created Oat latte" {
		t.Fatalf("create: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_item_saved", newItemID); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	// The lost-result replay applies again as an update, same state.
	if msg, err := hooks.SaveItem(ctx, p); err != nil || msg != "updated Oat latte" {
		t.Fatalf("replay: %q %v", msg, err)
	}
	// A barcode another item owns fails with an owner-readable sentence
	// naming that item (seedForPages: barcode ABC belongs to Apple).
	_, err = hooks.SaveItem(ctx, data.ItemPatch{ID: newItemID, Barcodes: lp("ABC")})
	if err == nil || err.Error() != "barcode ABC is already used by Apple" {
		t.Fatalf("conflict: %v", err)
	}
	var price int64
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE id = ?`, newItemID).Scan(&price); err != nil || price != 390 {
		t.Fatalf("price = %d %v", price, err)
	}
}

func TestCloudSaveItem_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	setReplica(t, dp)
	_, err := buildCloudHooks(dp, nil).SaveItem(t.Context(), data.ItemPatch{ID: newItemID, Create: true, Name: sp("X"), PriceMinor: ip(1)})
	if err == nil || !strings.HasPrefix(err.Error(), "this data is primary-wins synced") {
		t.Fatalf("replica: %v", err)
	}
	if ok, _ := data.NewCatalogRepo(dp.Db).ItemExists(t.Context(), newItemID); ok {
		t.Fatal("a replica wrote the item")
	}
}

func TestCloudSaveAndDeleteCategory(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	msg, err := hooks.SaveCategory(ctx, data.CategorySave{ID: "cat-new", Create: true, Name: sp("Hot drinks"), Icon: sp("lucide:coffee"), ShowOnSaleScreen: bp(false)})
	if err != nil || msg != "created category Hot drinks" {
		t.Fatalf("create: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_category_saved", "cat-new"); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	if msg, err := hooks.SaveCategory(ctx, data.CategorySave{ID: "cat-new", Create: true, Name: sp("Hot drinks")}); err != nil || msg != "updated category Hot drinks" {
		t.Fatalf("replay: %q %v", msg, err)
	}
	if _, err := hooks.SaveCategory(ctx, data.CategorySave{ID: "cat-new", Icon: sp("javascript:alert(1)")}); err == nil {
		t.Fatal("a malformed icon id must fail")
	}

	msg, err = hooks.DeleteCategory(ctx, "cat-new", "")
	if err != nil || msg != "deleted category Hot drinks (0 items moved, 0 subcategories moved up)" {
		t.Fatalf("delete: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_category_deleted", "cat-new"); n != 1 {
		t.Fatalf("delete audit rows = %d", n)
	}
	if msg, err := hooks.DeleteCategory(ctx, "cat-new", ""); err != nil || msg != "already deleted" {
		t.Fatalf("delete replay: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_category_deleted", "cat-new"); n != 1 {
		t.Fatalf("a replayed delete must not add an audit row, got %d", n)
	}
}

func TestCloudCategoryHooks_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := data.NewCatalogRepo(dp.Db).SaveCategory(ctx, data.CategorySave{ID: "cat-r", Create: true, Name: sp("Food")}); err != nil {
		t.Fatal(err)
	}
	setReplica(t, dp)
	hooks := buildCloudHooks(dp, nil)
	if _, err := hooks.SaveCategory(ctx, data.CategorySave{ID: "cat-r", Name: sp("Renamed")}); err == nil {
		t.Fatal("save_category must be refused on a replica")
	}
	if _, err := hooks.DeleteCategory(ctx, "cat-r", ""); err == nil {
		t.Fatal("delete_category must be refused on a replica")
	}
}

func TestCloudSaveAndDeleteModifierGroup(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	opts := []data.ModifierOption{{ID: "o-1", Name: "Oat", PriceDeltaMinor: 40, IsActive: true}}
	msg, err := hooks.SaveModifierGroup(ctx, data.ModifierGroupSave{ID: "grp-new", Create: true, Name: sp("Milk"), Options: &opts, AttachItemIDs: []string{"itm1"}})
	if err != nil || msg != "created modifier group Milk" {
		t.Fatalf("create: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_modifier_group_saved", "grp-new"); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	msg, err = hooks.DeleteModifierGroup(ctx, "grp-new")
	if err != nil || msg != "deleted modifier group" {
		t.Fatalf("delete: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_modifier_group_deleted", "grp-new"); n != 1 {
		t.Fatalf("delete audit rows = %d", n)
	}
	if msg, err := hooks.DeleteModifierGroup(ctx, "grp-new"); err != nil || msg != "already deleted" {
		t.Fatalf("delete replay: %q %v", msg, err)
	}

	setReplica(t, dp)
	if _, err := hooks.SaveModifierGroup(ctx, data.ModifierGroupSave{ID: "grp-2", Create: true, Name: sp("Size")}); err == nil {
		t.Fatal("save_modifier_group must be refused on a replica")
	}
	if _, err := hooks.DeleteModifierGroup(ctx, "grp-2"); err == nil {
		t.Fatal("delete_modifier_group must be refused on a replica")
	}
}

// §3.6: the categories report carries icon and show_on_sale_screen, and
// DeviceExtra carries the till's UI locale for the cloud's status bar.
func TestRemoteReport_CategoryIconHiddenAndLocale(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	if _, err := data.NewCatalogRepo(dp.Db).SaveCategory(t.Context(), data.CategorySave{
		ID: "cat-ic", Create: true, Name: sp("Coffee"), Icon: sp("lucide:coffee"), ShowOnSaleScreen: bp(false),
	}); err != nil {
		t.Fatal(err)
	}
	_, decoded := remoteConfigReportJSON(t, dp)
	var cats []struct {
		ID               string `json:"id"`
		Icon             string `json:"icon"`
		ShowOnSaleScreen *bool  `json:"show_on_sale_screen"`
	}
	decodeReport(t, decoded, "categories", &cats)
	if len(cats) != 1 || cats[0].Icon != "lucide:coffee" || cats[0].ShowOnSaleScreen == nil || *cats[0].ShowOnSaleScreen {
		t.Fatalf("categories = %+v", cats)
	}
	var locale string
	decodeReport(t, decoded, "locale", &locale)
	if want := httpx.DefaultLocale(); locale != want || locale == "" {
		t.Fatalf("locale = %q, want the till's %q", locale, want)
	}
}

// categoryOrder is every category id in the till's own display order
// (ListCategories: sort_order, name), inactive ones included.
func categoryOrder(t *testing.T, dp *common.Deps) []string {
	t.Helper()
	cats, err := data.NewCatalogRepo(dp.Db).ListCategories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.ID)
	}
	return out
}

func cloudCategoryOrderAudits(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_category_order_set' AND actor_id = 'system'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// §3.8 (ut-docs#3075): set_category_order renumbers the listed categories
// in the given order, keeps every unlisted one (here an inactive category
// and the seeded ones) after them in its old relative order, audits the
// apply, and replays to the same state.
func TestCloudSetCategoryOrder_AppliesAuditsAndReplays(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	for _, c := range []struct{ id, name string }{{"cat-a", "Alpha"}, {"cat-b", "Bravo"}, {"cat-c", "Charlie"}, {"cat-x", "Xray"}} {
		if _, err := repo.SaveCategory(ctx, data.CategorySave{ID: c.id, Create: true, Name: sp(c.name)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetCategoryActive(ctx, "cat-x", false); err != nil {
		t.Fatal(err)
	}
	before := categoryOrder(t, dp)
	hooks := buildCloudHooks(dp, nil)
	if hooks.SetCategoryOrder == nil {
		t.Fatal("SetCategoryOrder hook not wired")
	}
	// Deliberately not alphabetical: a no-op write would leave name order.
	listed := []string{"cat-c", "cat-a", "cat-b"}
	msg, err := hooks.SetCategoryOrder(ctx, listed)
	if err != nil || msg != "category order applied to 3 categories" {
		t.Fatalf("apply: %q %v", msg, err)
	}
	want := append([]string{}, listed...)
	for _, id := range before {
		if id != "cat-a" && id != "cat-b" && id != "cat-c" {
			want = append(want, id)
		}
	}
	got := categoryOrder(t, dp)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	cats, _ := repo.ListCategories(ctx)
	for i, c := range cats {
		if c.SortOrder != i {
			t.Fatalf("cats[%d] %s sort_order = %d, want %d (distinct, renumbered)", i, c.ID, c.SortOrder, i)
		}
	}
	if n := cloudCategoryOrderAudits(t, dp); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'cloud_category_order_set'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"category_ids":["cat-c","cat-a","cat-b"]`) {
		t.Fatalf("audit payload = %s", payload)
	}
	// Lost-result replay: same list, same state, still applied.
	if msg, err := hooks.SetCategoryOrder(ctx, listed); err != nil || msg != "category order applied to 3 categories" {
		t.Fatalf("replay: %q %v", msg, err)
	}
	if again := categoryOrder(t, dp); strings.Join(again, ",") != strings.Join(want, ",") {
		t.Fatalf("replay order = %v, want %v", again, want)
	}
	// An unchanged replay writes nothing: no second audit row.
	if n := cloudCategoryOrderAudits(t, dp); n != 1 {
		t.Fatalf("a no-op replay added audit rows: %d", n)
	}
	// A different order applies and audits again (last write wins).
	if _, err := hooks.SetCategoryOrder(ctx, []string{"cat-a", "cat-b", "cat-c"}); err != nil {
		t.Fatal(err)
	}
	if got := categoryOrder(t, dp); got[0] != "cat-a" || got[1] != "cat-b" || got[2] != "cat-c" {
		t.Fatalf("second order = %v", got)
	}
	if n := cloudCategoryOrderAudits(t, dp); n != 2 {
		t.Fatalf("audit rows after a real change = %d, want 2", n)
	}
}

// §3.8: an unknown id, a duplicate, a blank id or an empty list fails with
// nothing written; an inactive category is a valid id; a replica refuses.
func TestCloudSetCategoryOrder_Refusals(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	for _, c := range []struct{ id, name string }{{"cat-a", "Alpha"}, {"cat-b", "Bravo"}, {"cat-x", "Xray"}} {
		if _, err := repo.SaveCategory(ctx, data.CategorySave{ID: c.id, Create: true, Name: sp(c.name)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetCategoryActive(ctx, "cat-x", false); err != nil {
		t.Fatal(err)
	}
	before := categoryOrder(t, dp)
	hooks := buildCloudHooks(dp, nil)
	for _, c := range []struct {
		ids  []string
		want string
	}{
		{[]string{"cat-b", "cat-gone", "cat-a"}, "category cat-gone is not on this till"},
		{[]string{"cat-b", "cat-a", "cat-b"}, "duplicate category id cat-b"},
		{[]string{"cat-b", ""}, "blank category id"},
		{nil, "missing category_ids"},
	} {
		_, err := hooks.SetCategoryOrder(ctx, c.ids)
		if err == nil || err.Error() != c.want {
			t.Errorf("%v: err = %v, want %q", c.ids, err, c.want)
		}
	}
	if after := categoryOrder(t, dp); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused order changed the till: %v -> %v", before, after)
	}
	if n := cloudCategoryOrderAudits(t, dp); n != 0 {
		t.Fatalf("a refused order wrote %d audit rows", n)
	}
	// An inactive category is on the till: listing it is fine.
	if _, err := hooks.SetCategoryOrder(ctx, []string{"cat-x", "cat-b", "cat-a"}); err != nil {
		t.Fatalf("inactive id refused: %v", err)
	}
	if got := categoryOrder(t, dp); got[0] != "cat-x" {
		t.Fatalf("order = %v, want cat-x first", got)
	}
	setReplica(t, dp)
	if _, err := buildCloudHooks(dp, nil).SetCategoryOrder(ctx, []string{"cat-a", "cat-b"}); err == nil {
		t.Fatal("set_category_order must be refused on a replica")
	}
}
