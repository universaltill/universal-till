package data_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3087: the product owner's rule is that no catalog item exists
// without a SKU. These tests pin the generator's shape through the two
// insert paths every caller (import, cloud push, catalog form, API,
// SaveItem) goes through.

func skuOf(t *testing.T, d *sql.DB, id string) string {
	t.Helper()
	var sku sql.NullString
	if err := d.QueryRow(`SELECT sku FROM items WHERE id = ?`, id).Scan(&sku); err != nil {
		t.Fatalf("read sku of %s: %v", id, err)
	}
	if !sku.Valid {
		t.Fatalf("item %s has a NULL sku — no item may exist without a SKU (ut-docs#3087)", id)
	}
	return sku.String
}

func strPtr(s string) *string { return &s }

func newSKUTestDB(t *testing.T) (*sql.DB, *data.CatalogRepo) {
	t.Helper()
	d := testsupport.NewCatalogTestDB(t)
	return d, data.NewCatalogRepo(d)
}

func mustCategory(t *testing.T, d *sql.DB, id, name string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO categories (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatalf("seed category: %v", err)
	}
}

func mustCreate(t *testing.T, repo *data.CatalogRepo, in catalogtypes.ItemInput) string {
	t.Helper()
	in.IsActive = true
	if in.BasePrice == 0 {
		in.BasePrice = 100
	}
	id, err := repo.CreateItem(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateItem(%q): %v", in.Name, err)
	}
	return id
}

func TestCreateItem_BlankSKUFollowsCategoryNumericSequence(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCategory(t, d, "cat-kuchen", "Kuchen")
	for _, sku := range []string{"30089", "30096", "30090"} {
		mustCreate(t, repo, catalogtypes.ItemInput{Name: "K" + sku, SKU: sku, CategoryID: strPtr("cat-kuchen")})
	}
	id := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Apfelkuchen", CategoryID: strPtr("cat-kuchen")})
	if got := skuOf(t, d, id); got != "30097" {
		t.Fatalf("sku = %q, want 30097 (the category's highest number + 1)", got)
	}
	id2 := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Käsekuchen", SKU: "   ", CategoryID: strPtr("cat-kuchen")})
	if got := skuOf(t, d, id2); got != "30098" {
		t.Fatalf("whitespace sku = %q, want 30098", got)
	}
}

func TestCreateItem_NumericSequenceKeepsWidthAndSkipsTakenCodes(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCategory(t, d, "cat-drinks", "Drinks")
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Tea", SKU: "0041", CategoryID: strPtr("cat-drinks")})
	// 0042 is already another item's SKU (other category) and 0043 is a
	// barcode: the sell screen resolves both, so neither may be reused.
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Elsewhere", SKU: "0042"})
	other := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Barcoded", SKU: "B-1"})
	if _, err := d.Exec(`INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('0043', ?, 1)`, other); err != nil {
		t.Fatal(err)
	}
	id := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Coffee", CategoryID: strPtr("cat-drinks")})
	if got := skuOf(t, d, id); got != "0044" {
		t.Fatalf("sku = %q, want 0044 (width kept, 0042 SKU and 0043 barcode skipped)", got)
	}
}

func TestCreateItem_GTINLengthSKUsDoNotStartASequence(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCategory(t, d, "cat-snacks", "Snacks")
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Crisps", SKU: "4006381333931", CategoryID: strPtr("cat-snacks")})
	id := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Nuts", CategoryID: strPtr("cat-snacks")})
	if got := skuOf(t, d, id); got != "SNA-0001" {
		t.Fatalf("sku = %q, want SNA-0001 (an EAN-13 SKU + 1 could be a real product's barcode)", got)
	}
}

func TestCreateItem_BlankSKUUsesCategoryPrefixSequence(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCategory(t, d, "cat-cafe", "Café")
	mustCategory(t, d, "cat-fa", "نوشیدنی")
	a := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Latte", CategoryID: strPtr("cat-cafe")})
	b := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Mocha", CategoryID: strPtr("cat-cafe")})
	c := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Loose"})
	e := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Doogh", CategoryID: strPtr("cat-fa")})
	got := []string{skuOf(t, d, a), skuOf(t, d, b), skuOf(t, d, c), skuOf(t, d, e)}
	want := []string{"CAF-0001", "CAF-0002", "ITEM-0001", "ITEM-0002"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("generated SKUs = %v, want %v", got, want)
		}
	}
}

func TestCreateItem_PrefixSequenceContinuesPastExistingAndIgnoresOldHexShape(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Old generated", SKU: "ITEM-3F9A0C12"})
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Hand made", SKU: "ITEM-0007"})
	mustCreate(t, repo, catalogtypes.ItemInput{Name: "Variant clash holder", SKU: "V-1"})
	// ITEM-0008 is held by a variant: skipped.
	var holder string
	if err := d.QueryRow(`SELECT id FROM items WHERE sku = 'V-1'`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('v1', ?, 'ITEM-0008', 'Big', 1)`, holder); err != nil {
		t.Fatal(err)
	}
	id := mustCreate(t, repo, catalogtypes.ItemInput{Name: "New"})
	if got := skuOf(t, d, id); got != "ITEM-0009" {
		t.Fatalf("sku = %q, want ITEM-0009", got)
	}
}

func TestCreateItem_ExplicitSKUIsKeptVerbatim(t *testing.T) {
	d, repo := newSKUTestDB(t)
	id := mustCreate(t, repo, catalogtypes.ItemInput{Name: "Typed", SKU: "MY-SKU-1"})
	if got := skuOf(t, d, id); got != "MY-SKU-1" {
		t.Fatalf("sku = %q, want the operator's own MY-SKU-1", got)
	}
	if _, err := repo.CreateItem(context.Background(), catalogtypes.ItemInput{Name: "Dup", SKU: "MY-SKU-1", BasePrice: 1, IsActive: true}); err != data.ErrSKUExists {
		t.Fatalf("duplicate explicit sku err = %v, want ErrSKUExists (never silently replaced)", err)
	}
}

// The importer creates many rows in one transaction each, and each must see
// the previous rows' generated SKUs.
func TestCreateItemTx_BlankSKUsInSequence(t *testing.T) {
	d, repo := newSKUTestDB(t)
	mustCategory(t, d, "cat-kuchen", "Kuchen")
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range []string{"A", "B", "C"} {
		id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{Name: n, BasePrice: 1, IsActive: true, CategoryID: strPtr("cat-kuchen")})
		if err != nil {
			t.Fatalf("CreateItemTx(%s): %v", n, err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"KUC-0001", "KUC-0002", "KUC-0003"} {
		if got := skuOf(t, d, ids[i]); got != want {
			t.Fatalf("item %d sku = %q, want %q", i, got, want)
		}
	}
}

// Real migrated schema (the fixture could drift from it): the generated SKU
// lands through the full migration chain's constraints too.
func TestCreateItem_GeneratesSKUOnMigratedSchema(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "sku3087.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	id, err := repo.CreateItem(context.Background(), catalogtypes.ItemInput{Name: "Brownie", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if got := skuOf(t, d.DB, id); !regexp.MustCompile(`^ITEM-\d{4}$`).MatchString(got) {
		t.Fatalf("sku = %q, want ITEM-NNNN", got)
	}
}

// TestItemInsertPathsAreCovered is AC 4's guard: production code has
// exactly one items-table insert, insertItemRow (behind CreateItem and
// CreateItemTx), which the tests above prove fills a blank SKU. A new
// insert path fails here until it is routed through insertItemRow. It sees
// only literal INSERT text: sync_admin_repo.go's generic replica upsert
// builds the table name at runtime and copies the primary's SKU as-is.
func TestItemInsertPathsAreCovered(t *testing.T) {
	insertRe := regexp.MustCompile(`(?i)INSERT\s+(OR\s+\w+\s+)?INTO\s+items\s*\(`)
	// Seed SQL ships fixed, non-blank SKUs; checked by content below.
	const demoSeed = "seeddata/demo_catalogue.sql"
	// Anchor on this file's directory: other tests in the package change
	// the working directory.
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(self)
	found := map[string]int{}
	err := filepath.WalkDir(pkgDir, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		if strings.HasSuffix(path, "_test.go") || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".sql")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if n := len(insertRe.FindAll(b, -1)); n > 0 {
			rel, _ := filepath.Rel(pkgDir, path)
			found[filepath.ToSlash(rel)] += n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"catalog_repo.go": 1, demoSeed: 2}
	for f, n := range found {
		if want[f] != n {
			t.Errorf("%s has %d INSERT INTO items statement(s), want %d — route new item inserts through insertItemRow (CreateItem/CreateItemTx) so a blank SKU is always filled (ut-docs#3087)", f, n, want[f])
		}
	}
	for f, n := range want {
		if found[f] != n {
			t.Errorf("%s: found %d INSERT INTO items, want %d (update this guard if the paths moved)", f, found[f], n)
		}
	}
	seed, err := os.ReadFile(filepath.Join(pkgDir, demoSeed))
	if err != nil {
		t.Fatal(err)
	}
	// Every demo item row is ('itmNNN', '<sku>', …): the SKU must be a
	// non-empty literal, never NULL or ''.
	rowRe := regexp.MustCompile(`\('itm\d+',\s*([^,]+),`)
	rows := rowRe.FindAllSubmatch(seed, -1)
	if len(rows) == 0 {
		t.Fatal("no demo item rows matched — update this guard's pattern")
	}
	for _, m := range rows {
		s := strings.TrimSpace(string(m[1]))
		if strings.EqualFold(s, "NULL") || s == "''" {
			t.Errorf("demo item row has a blank SKU: %s", m[0])
		}
	}
}
