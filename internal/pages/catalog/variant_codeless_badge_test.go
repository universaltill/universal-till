package catalog

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// mustExecCatalog is a raw-SQL seed step for a case testsupport's own
// helpers can't express — here, a genuinely NULL sku (SeedVariant always
// writes its SKU field literally, and two blank-string skus collide on
// item_variants' UNIQUE(sku), unlike two NULLs).
func mustExecCatalog(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// TestItemVariantsPanel_FlagsCodelessVariant covers ut-docs#2230's UI
// acceptance criterion: a merchant looking at the catalog admin screen must
// be able to tell that a variant with neither a barcode nor a SKU will not
// sell, and a variant that DOES carry a code must never show that flag.
// This can only be reached today via a write path that bypasses
// CatalogRepo.CreateVariant's own blank-SKU generation (a raw seed here
// stands in for that; ut-docs#2230's own investigation found the LAN
// admin-sync path as the real one, fixed separately in
// internal/data/sync_admin_repo.go) — the badge is the last line of
// defense the card's acceptance criteria explicitly still asks for.
func TestItemVariantsPanel_FlagsCodelessVariant(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Shirt", BasePrice: 100, IsActive: true})

	// Codeless: active, blank SKU, no barcode — must show the flag. Seeded
	// via raw NULL (not testsupport.SeedVariant's literal "") since
	// item_variants.sku is UNIQUE and SQLite treats "" as a real,
	// collidable value — only NULL may repeat across rows.
	mustExecCatalog(t, db, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless', 'itm1', NULL, 'Small', 100, 1)`)
	// Has a SKU: must NOT show the flag.
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v-has-sku", ItemID: "itm1", SKU: "S1-M", Name: "Medium", Price: 110, IsActive: true})
	// Blank SKU but has a barcode: must NOT show the flag.
	mustExecCatalog(t, db, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-has-barcode', 'itm1', NULL, 'Large', 120, 1)`)
	testsupport.SeedVariantBarcode(t, db, "5012345678900", "v-has-barcode", true)
	// Codeless but INACTIVE: must NOT show the flag (never offered for sale
	// regardless of its code).
	mustExecCatalog(t, db, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-codeless-inactive', 'itm1', NULL, 'Retired', 90, 0)`)

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	badge := `data-testid="variant-codeless"`
	got := strings.Count(body, badge)
	if got != 1 {
		t.Fatalf("expected the codeless badge (%s) to render exactly once (for v-codeless only), got %d occurrences in:\n%s", badge, got, body)
	}
}

// TestItemVariantsPanel_NoCodelessBadgeWhenEveryVariantHasACode is the
// negative case: nothing in the panel should even mention the flag when
// every variant on the item can already be resolved at sale time.
func TestItemVariantsPanel_NoCodelessBadgeWhenEveryVariantHasACode(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Shirt", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-M", Name: "Medium", Price: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "itm1", SKU: "", Name: "Large", Price: 120, IsActive: true})
	testsupport.SeedVariantBarcode(t, db, "5012345678901", "v2", true)

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `data-testid="variant-codeless"`) {
		t.Fatalf("codeless badge key must not appear when every variant has a code:\n%s", rec.Body.String())
	}
}
