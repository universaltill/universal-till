package data

// Kitchen station routing (universaltill/ut-docs#516): named prep stations
// with their own printers, category-level routing rules, and item-level
// overrides. ResolveKitchenStations is THE precedence algorithm — item rows
// override category rows (no union), only enabled stations are returned,
// and an unrouted item resolves to an empty slice (the caller falls back to
// the legacy default kitchen printer).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openStationTestDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "stations.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func seedStationCatalog(t *testing.T, dbo *db.DB) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbo.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO categories (id, name) VALUES ('cat-food','Food'), ('cat-drinks','Drinks')`)
	mustExec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES
		('itm-burger','BURGER','Burger',900,'cat-food',1),
		('itm-cola','COLA','Cola',250,'cat-drinks',1),
		('itm-mystery','MYST','Mystery',100,NULL,1)`)
}

func TestKitchenStationCRUD(t *testing.T) {
	_, repo := openStationTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateKitchenStation(ctx, "Grill", "192.168.1.60:9100")
	if err != nil {
		t.Fatalf("CreateKitchenStation: %v", err)
	}
	if id == "" {
		t.Fatal("CreateKitchenStation returned empty id")
	}

	list, err := repo.ListKitchenStations(ctx)
	if err != nil {
		t.Fatalf("ListKitchenStations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 station, got %d", len(list))
	}
	s := list[0]
	if s.ID != id || s.Name != "Grill" || s.PrinterAddress != "192.168.1.60:9100" {
		t.Fatalf("unexpected station: %+v", s)
	}
	if !s.Enabled {
		t.Fatal("new station must be enabled")
	}
	// This slice only creates printer stations (display is ut-docs#544).
	if s.DestinationType != "printer" {
		t.Fatalf("DestinationType = %q, want printer", s.DestinationType)
	}
	if s.CreatedAt == "" || s.UpdatedAt == "" {
		t.Fatalf("timestamps must be set: %+v", s)
	}

	if err := repo.UpdateKitchenStation(ctx, id, "Char Grill", "/dev/usb/lp1"); err != nil {
		t.Fatalf("UpdateKitchenStation: %v", err)
	}
	list, err = repo.ListKitchenStations(ctx)
	if err != nil {
		t.Fatalf("ListKitchenStations after update: %v", err)
	}
	if list[0].Name != "Char Grill" || list[0].PrinterAddress != "/dev/usb/lp1" {
		t.Fatalf("update not persisted: %+v", list[0])
	}

	if err := repo.SetKitchenStationEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetKitchenStationEnabled: %v", err)
	}
	list, _ = repo.ListKitchenStations(ctx)
	if list[0].Enabled {
		t.Fatal("station should be disabled")
	}
	if err := repo.SetKitchenStationEnabled(ctx, id, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	list, _ = repo.ListKitchenStations(ctx)
	if !list[0].Enabled {
		t.Fatal("station should be re-enabled")
	}
}

func TestKitchenStationUpdate_NotFound(t *testing.T) {
	_, repo := openStationTestDB(t)
	ctx := context.Background()
	if err := repo.UpdateKitchenStation(ctx, "nope", "X", ""); err == nil {
		t.Fatal("UpdateKitchenStation on missing id must error")
	}
	if err := repo.SetKitchenStationEnabled(ctx, "nope", false); err == nil {
		t.Fatal("SetKitchenStationEnabled on missing id must error")
	}
}

func TestSetItemStationRoutes_ReplaceAll(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")

	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill, bar}); err != nil {
		t.Fatalf("SetItemStationRoutes: %v", err)
	}
	got, err := repo.ItemStationRoutes(ctx, "itm-burger")
	if err != nil {
		t.Fatalf("ItemStationRoutes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 routes, got %v", got)
	}

	// Replace-all: a second call with a smaller set removes the rest.
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{bar}); err != nil {
		t.Fatalf("SetItemStationRoutes replace: %v", err)
	}
	got, _ = repo.ItemStationRoutes(ctx, "itm-burger")
	if len(got) != 1 || got[0] != bar {
		t.Fatalf("replace-all failed, got %v want [%s]", got, bar)
	}

	// Empty set removes the override entirely.
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", nil); err != nil {
		t.Fatalf("SetItemStationRoutes clear: %v", err)
	}
	got, _ = repo.ItemStationRoutes(ctx, "itm-burger")
	if len(got) != 0 {
		t.Fatalf("clear failed, got %v", got)
	}
}

func TestSetCategoryStationRoutes_ReplaceAll(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")

	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{grill, bar}); err != nil {
		t.Fatalf("SetCategoryStationRoutes: %v", err)
	}
	got, err := repo.CategoryStationRoutes(ctx, "cat-drinks")
	if err != nil {
		t.Fatalf("CategoryStationRoutes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 routes, got %v", got)
	}

	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = repo.CategoryStationRoutes(ctx, "cat-drinks")
	if len(got) != 1 || got[0] != bar {
		t.Fatalf("replace-all failed, got %v want [%s]", got, bar)
	}
}

func TestResolveKitchenStations_EmptyTable(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-burger", "itm-cola"})
	if err != nil {
		t.Fatalf("ResolveKitchenStations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero stations configured must resolve to an empty map, got %v", got)
	}

	// No item ids at all is fine too.
	got, err = repo.ResolveKitchenStations(ctx, nil)
	if err != nil {
		t.Fatalf("ResolveKitchenStations(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map for nil input, got %v", got)
	}
}

func TestResolveKitchenStations_ItemOverridesCategory(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")

	// Category rule: everything in Food goes to the Bar (deliberately odd,
	// so the override is unambiguous).
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{bar}); err != nil {
		t.Fatal(err)
	}
	// Item override: the burger goes to the Grill ONLY — no union with Bar.
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-burger"})
	if err != nil {
		t.Fatalf("ResolveKitchenStations: %v", err)
	}
	stations := got["itm-burger"]
	if len(stations) != 1 || stations[0].ID != grill {
		t.Fatalf("item override must beat (not union with) category: %+v", stations)
	}
}

func TestResolveKitchenStations_CategoryFallbackAndUnrouted(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-cola", "itm-burger", "itm-mystery"})
	if err != nil {
		t.Fatalf("ResolveKitchenStations: %v", err)
	}
	// itm-cola has no item rows → its category's stations apply.
	if s := got["itm-cola"]; len(s) != 1 || s[0].ID != bar || s[0].Name != "Bar" {
		t.Fatalf("category fallback failed: %+v", s)
	}
	// itm-burger's category has no rows and neither does the item → unrouted.
	if s := got["itm-burger"]; len(s) != 0 {
		t.Fatalf("unrouted item must resolve empty, got %+v", s)
	}
	// itm-mystery has no category at all → unrouted.
	if s := got["itm-mystery"]; len(s) != 0 {
		t.Fatalf("category-less item must resolve empty, got %+v", s)
	}
}

func TestResolveKitchenStations_MultiStationDuplication(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill, bar}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-burger"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["itm-burger"]) != 2 {
		t.Fatalf("item routed to 2 stations must resolve to both, got %+v", got["itm-burger"])
	}
}

// An item override pointing at ONLY a disabled station must resolve empty
// (falls back to the default kitchen printer) — it must NOT resurrect the
// item's still-enabled category rule (code review, ut-docs#516: the tier is
// decided by item-level row EXISTENCE, before the enabled filter runs).
func TestResolveKitchenStations_DisabledItemOverrideDoesNotFallBackToCategory(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")
	// itm-burger's category (Food) routes to Bar...
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{bar}); err != nil {
		t.Fatal(err)
	}
	// ...but the item itself overrides to Grill only, and Grill is disabled.
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetKitchenStationEnabled(ctx, grill, false); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-burger"})
	if err != nil {
		t.Fatal(err)
	}
	if stations, ok := got["itm-burger"]; ok {
		t.Fatalf("disabled-only item override must resolve UNROUTED, not fall back to the category rule, got %+v", stations)
	}
}

func TestResolveKitchenStations_EnabledOnly(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill, bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetKitchenStationEnabled(ctx, bar, false); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveKitchenStations(ctx, []string{"itm-burger", "itm-cola"})
	if err != nil {
		t.Fatal(err)
	}
	if s := got["itm-burger"]; len(s) != 1 || s[0].ID != grill {
		t.Fatalf("disabled station must be filtered from item routes: %+v", s)
	}
	// itm-cola's only (category) station is disabled → resolves empty →
	// falls back to the default kitchen printer at the call site. The
	// disabled station must NOT resurrect as a route.
	if s := got["itm-cola"]; len(s) != 0 {
		t.Fatalf("disabled station must be filtered from category routes: %+v", s)
	}
}

func TestListItemStationOverrides(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", "g:9100")
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListItemStationOverrides(ctx)
	if err != nil {
		t.Fatalf("ListItemStationOverrides: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 overridden item, got %d", len(got))
	}
	o := got[0]
	if o.ItemID != "itm-burger" || o.Name != "Burger" || o.SKU != "BURGER" {
		t.Fatalf("unexpected override row: %+v", o)
	}
	if len(o.StationIDs) != 1 || o.StationIDs[0] != grill {
		t.Fatalf("unexpected station ids: %+v", o.StationIDs)
	}
}

func TestAllCategoryStationRoutes(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	bar, _ := repo.CreateKitchenStation(ctx, "Bar", "b:9100")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.AllCategoryStationRoutes(ctx)
	if err != nil {
		t.Fatalf("AllCategoryStationRoutes: %v", err)
	}
	if len(got) != 1 || len(got["cat-drinks"]) != 1 || got["cat-drinks"][0] != bar {
		t.Fatalf("unexpected map: %v", got)
	}
}
