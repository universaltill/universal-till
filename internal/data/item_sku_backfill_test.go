package data_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3097: items created before #3087 can still have a NULL or blank
// SKU. PlanMissingItemSKUs previews and BackfillMissingItemSKUs assigns
// the same generated SKUs, through the same nextItemSKU the insert path
// uses.

func seedRawItem(t *testing.T, d *sql.DB, id, name string, sku any, categoryID any, active bool) {
	t.Helper()
	a := 0
	if active {
		a = 1
	}
	if _, err := d.Exec(`INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES(?,?,?,?,?,?)`,
		id, sku, name, 100, categoryID, a); err != nil {
		t.Fatalf("seed item %s: %v", id, err)
	}
}

func seedMissingSKUFixture(t *testing.T) (*sql.DB, *data.CatalogRepo) {
	t.Helper()
	d := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(d)
	mustCategory(t, d, "cat-kuchen", "Kuchen")
	// Kuchen already follows a plain numeric pattern.
	seedRawItem(t, d, "k-existing", "Apfelkuchen", "30089", "cat-kuchen", true)
	// Two blank Kuchen items: 30090 then 30091 (the second only if the
	// first assignment is visible inside the same transaction).
	seedRawItem(t, d, "k-a", "Bienenstich", nil, "cat-kuchen", true)
	seedRawItem(t, d, "k-b", "Käsekuchen", "", "cat-kuchen", true)
	// No category, inactive, whitespace-only SKU is not possible for two
	// rows ('' is UNIQUE) so this one is NULL.
	seedRawItem(t, d, "n-inactive", "Old Thing", nil, nil, false)
	// Untouched: already has a SKU.
	seedRawItem(t, d, "has-sku", "Coffee", "COF-1", nil, true)
	return d, repo
}

func TestBackfillMissingItemSKUs_FillsNullBlankAndInactive(t *testing.T) {
	d, repo := seedMissingSKUFixture(t)
	ctx := context.Background()

	got, err := repo.BackfillMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("BackfillMissingItemSKUs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("assigned %d SKUs, want 3: %+v", len(got), got)
	}
	want := map[string]string{"k-a": "30090", "k-b": "30091", "n-inactive": "ITEM-0001"}
	for _, a := range got {
		if w, ok := want[a.ItemID]; !ok || a.SKU != w {
			t.Errorf("assignment %+v, want sku %q", a, w)
		}
		if a.Name == "" {
			t.Errorf("assignment %+v has no name", a)
		}
	}
	for id, w := range want {
		if s := skuOf(t, d, id); s != w {
			t.Errorf("stored sku of %s = %q, want %q", id, s, w)
		}
	}
	if s := skuOf(t, d, "has-sku"); s != "COF-1" {
		t.Errorf("existing SKU changed to %q", s)
	}
	if s := skuOf(t, d, "k-existing"); s != "30089" {
		t.Errorf("existing SKU changed to %q", s)
	}
}

func TestPlanMissingItemSKUs_EqualsBackfillAndWritesNothing(t *testing.T) {
	d, repo := seedMissingSKUFixture(t)
	ctx := context.Background()

	plan, err := repo.PlanMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("PlanMissingItemSKUs: %v", err)
	}
	n, err := repo.CountItemsMissingSKU(ctx)
	if err != nil {
		t.Fatalf("CountItemsMissingSKU: %v", err)
	}
	if n != 3 {
		t.Fatalf("after a plan, %d items still miss a SKU, want 3 (plan must write nothing)", n)
	}
	var raw sql.NullString
	if err := d.QueryRow(`SELECT sku FROM items WHERE id = 'k-a'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw.Valid {
		t.Fatalf("plan wrote sku %q for k-a", raw.String)
	}

	done, err := repo.BackfillMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("BackfillMissingItemSKUs: %v", err)
	}
	if len(plan) != len(done) {
		t.Fatalf("plan %+v != backfill %+v", plan, done)
	}
	for i := range plan {
		if plan[i] != done[i] {
			t.Errorf("row %d: plan %+v != backfill %+v", i, plan[i], done[i])
		}
	}
}

func TestBackfillMissingItemSKUs_Idempotent(t *testing.T) {
	_, repo := seedMissingSKUFixture(t)
	ctx := context.Background()

	if _, err := repo.BackfillMissingItemSKUs(ctx); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	again, err := repo.BackfillMissingItemSKUs(ctx)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second backfill assigned %+v, want none", again)
	}
	n, err := repo.CountItemsMissingSKU(ctx)
	if err != nil || n != 0 {
		t.Fatalf("CountItemsMissingSKU = %d, %v; want 0", n, err)
	}
}

func TestCountItemsMissingSKU_CountsNullBlankWhitespaceAndInactive(t *testing.T) {
	d := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(d)
	seedRawItem(t, d, "a", "A", nil, nil, true)
	seedRawItem(t, d, "b", "B", "  ", nil, true)
	seedRawItem(t, d, "c", "C", nil, nil, false)
	seedRawItem(t, d, "d", "D", "D-1", nil, true)
	n, err := repo.CountItemsMissingSKU(context.Background())
	if err != nil {
		t.Fatalf("CountItemsMissingSKU: %v", err)
	}
	if n != 3 {
		t.Fatalf("CountItemsMissingSKU = %d, want 3", n)
	}
}
