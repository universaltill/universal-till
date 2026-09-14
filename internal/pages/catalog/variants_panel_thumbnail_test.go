package catalog

// ut-docs#1324: catalog_variants.html's item-level thumbnail (the panel
// header, and the per-variant-row fallback shown when a variant has no
// photo of its own) used to guess a disk path
// ("/public/assets/items/<id>/thumb.png") and test it with imgExists,
// instead of resolving through item_images (role=thumbnail) like every
// other ImageURL consumer in this app (admin Catalog list #1842,
// self-order kiosk #1870, AI-identify #1875). An item whose thumbnail is a
// built-in category icon (not a manually uploaded photo) has no file at
// that guessed path, so the panel showed a blank placeholder even though
// every other surface showed the icon correctly.
//
// The variant's OWN photo stays disk-only on purpose — item_images has no
// variant_id column, so a variant simply has no row to resolve a photo
// from (the 2026-07-17 variant-images review). That review's "per-till,
// until image LAN-sync is designed" caveat is itself stale now:
// internal/pages/sync_assets.go's recursive walk of the items/ asset tree
// carries items/<item>/variants/<variant>/thumb.png to replicas too, so
// the reason this fix leaves variant photos alone is structural, not sync.
// They are not touched here; these tests pin that a variant's own photo
// still takes priority over the item-level fallback when present.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestVariantsPanel_ItemHeaderThumb_ResolvesFromItemImages(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Widget", BasePrice: 100, IsActive: true})
	// A built-in category icon, not an uploaded photo — no file exists at
	// any /public/assets/items/... path for this item, which is exactly
	// the case the old imgExists check got wrong.
	testsupport.SeedImage(t, db, "img-1", "itm1", "/public/assets/icons/coffee.svg")

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="thumb" src="/public/assets/icons/coffee.svg`) {
		t.Fatalf("expected the panel header to render the item's real (item_images) thumbnail, got:\n%s", body)
	}
}

func TestVariantsPanel_ItemHeaderThumb_NoImageShowsPlaceholder(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "S2", Name: "Plain", BasePrice: 100, IsActive: true})

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm2")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `class="thumb" src=`) {
		t.Fatalf("expected no thumbnail <img> for an item with no item_images row, got:\n%s", body)
	}
	if !strings.Contains(body, `<div class="thumb" aria-hidden="true"></div>`) {
		t.Fatalf("expected the placeholder div, got:\n%s", body)
	}
}

func TestVariantsPanel_VariantRow_FallsBackToItemImage(t *testing.T) {
	mux, db := newCatalogMux(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm3", SKU: "S3", Name: "Widget", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-3", "itm3", "/public/assets/icons/pastry.svg")
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var3", ItemID: "itm3", SKU: "S3-V1", Name: "Small", Price: 100, IsActive: true})
	// No file written for var3's own variant thumb — paths.Init points at
	// an empty temp dir, so imgExists($variantThumb) is false and the row
	// must fall back to the item's item_images-backed thumbnail.

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm3")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `src="/public/assets/icons/pastry.svg`) {
		t.Fatalf("expected the variant row to fall back to the item's real thumbnail, got:\n%s", body)
	}
	if strings.Contains(body, `thumb-ph`) {
		t.Fatalf("expected no placeholder div — the item fallback should have rendered instead, got:\n%s", body)
	}
}

func TestVariantsPanel_VariantRow_OwnPhotoTakesPriorityOverItemFallback(t *testing.T) {
	mux, db := newCatalogMux(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm4", SKU: "S4", Name: "Widget", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-4", "itm4", "/public/assets/icons/drink.svg")
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var4", ItemID: "itm4", SKU: "S4-V1", Name: "Large", Price: 150, IsActive: true})

	thumbPath := filepath.Join(paths.Data("public", "assets", "items", "itm4", "variants", "var4"), "thumb.png")
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(thumbPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("seed variant photo file: %v", err)
	}

	rec := get(t, mux, "/api/catalog/item-variants?item_id=itm4")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `src="/public/assets/items/itm4/variants/var4/thumb.png`) {
		t.Fatalf("expected the variant's own uploaded photo to take priority as the rendered src, got:\n%s", body)
	}
	// The item's thumbnail should still be present as the onerror runtime
	// fallback (unchanged behavior from before this fix — only its source
	// moved from a guessed disk path to item_images) but never as the
	// primary rendered src.
	if !strings.Contains(body, `drink.svg`) {
		t.Fatalf("expected the item's thumbnail to still be wired as the onerror fallback, got:\n%s", body)
	}
	if strings.Contains(body, `<img src="/public/assets/icons/drink.svg`) {
		t.Fatalf("expected the item fallback NOT to be the primary rendered src when the variant has its own photo, got:\n%s", body)
	}
}
