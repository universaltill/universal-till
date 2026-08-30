package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// TestEnsureDefaultThumbnail_SetsPathForImagelessItem is the ut-docs#1189
// Phase 1 regression: an item with no thumbnail gets one item_images row
// (role=thumbnail) pointing at the given placeholder path.
func TestEnsureDefaultThumbnail_SetsPathForImagelessItem(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Cappuccino", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	if err := repo.EnsureDefaultThumbnail(ctx, id, "/public/assets/category-icons/coffee.svg"); err != nil {
		t.Fatalf("EnsureDefaultThumbnail: %v", err)
	}

	var path string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&path); err != nil {
		t.Fatalf("expected a thumbnail row for %s, got: %v", id, err)
	}
	if path != "/public/assets/category-icons/coffee.svg" {
		t.Fatalf("path = %q, want the coffee icon path", path)
	}
}

// TestEnsureDefaultThumbnail_NeverOverwritesARealImage: this is a
// placeholder-if-absent operation, not a set-unconditionally one — an
// item that already has a real (operator-uploaded) thumbnail must keep it.
func TestEnsureDefaultThumbnail_NeverOverwritesARealImage(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Cappuccino", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	const realPhoto = "/public/assets/items/" + "real-uploaded-photo.png"
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-real', ?, ?, 'thumbnail')`,
		id, realPhoto,
	); err != nil {
		t.Fatalf("seed real image: %v", err)
	}

	if err := repo.EnsureDefaultThumbnail(ctx, id, "/public/assets/category-icons/coffee.svg"); err != nil {
		t.Fatalf("EnsureDefaultThumbnail: %v", err)
	}

	var count int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 thumbnail row, got %d", count)
	}
	var path string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != realPhoto {
		t.Fatalf("real photo was overwritten: path = %q", path)
	}
}

// TestSetItemThumbnail_InsertsWhenNoneExists is SetItemThumbnail's insert
// branch: an item with no thumbnail row yet gets one.
func TestSetItemThumbnail_InsertsWhenNoneExists(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Cappuccino", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if err := repo.SetItemThumbnail(ctx, id, "/public/assets/items/"+id+"/thumb.png"); err != nil {
		t.Fatalf("SetItemThumbnail: %v", err)
	}
	var path string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&path); err != nil {
		t.Fatalf("expected a thumbnail row: %v", err)
	}
	if path != "/public/assets/items/"+id+"/thumb.png" {
		t.Fatalf("path = %q", path)
	}
}

// TestSetItemThumbnail_OverwritesAPlaceholder is the review-F2 regression:
// a real photo (manual upload, or a barcode-lookup match) must actually
// replace a placeholder icon EnsureDefaultThumbnail set earlier — not
// leave the placeholder showing forever on the surfaces that read
// item_images (POS grid, basket, self-order, suggestions).
func TestSetItemThumbnail_OverwritesAPlaceholder(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Cappuccino", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if err := repo.EnsureDefaultThumbnail(ctx, id, "/public/assets/category-icons/coffee.svg"); err != nil {
		t.Fatalf("EnsureDefaultThumbnail: %v", err)
	}

	realPhoto := "/public/assets/items/" + id + "/thumb.png"
	if err := repo.SetItemThumbnail(ctx, id, realPhoto); err != nil {
		t.Fatalf("SetItemThumbnail: %v", err)
	}

	var count int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 thumbnail row after overwrite, got %d (duplicate insert instead of update?)", count)
	}
	var path string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, id,
	).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != realPhoto {
		t.Fatalf("path = %q, want the real photo to have replaced the placeholder icon", path)
	}
}
