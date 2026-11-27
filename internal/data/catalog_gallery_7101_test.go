package data_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func galleryRepo(t *testing.T) (*data.CatalogRepo, string) {
	repo, id, _ := galleryRepoDB(t)
	return repo, id
}

func galleryRepoDB(t *testing.T) (*data.CatalogRepo, string, *sql.DB) {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "gal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	id, err := repo.CreateItem(context.Background(), catalogtypes.ItemInput{Name: "Mug", BasePrice: 500, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	return repo, id, d.DB
}

// ut-docs#7101: position 1 is the thumbnail, the rest the gallery, in order, with alt text.
func TestReplaceItemGallery_PrimaryIsThumbnailRestInOrder(t *testing.T) {
	ctx := context.Background()
	repo, id := galleryRepo(t)
	imgs := []data.GalleryImage{{Path: "/a.png", Alt: "front"}, {Path: "/b.png", Alt: "back"}, {Path: "/c.png", Alt: ""}}
	if err := repo.ReplaceItemGallery(ctx, id, imgs); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := repo.ItemThumbnailPath(ctx, id); err != nil || !ok || p != "/a.png" {
		t.Fatalf("thumbnail %q %v %v", p, ok, err)
	}
	got, err := repo.ItemGallery(ctx, id)
	if err != nil || len(got) != 3 {
		t.Fatalf("gallery %+v %v", got, err)
	}
	for i, want := range imgs {
		if got[i].Path != want.Path || got[i].Alt != want.Alt || got[i].Position != i+1 {
			t.Errorf("image %d = %+v, want %+v at %d", i, got[i], want, i+1)
		}
	}
	// reorder + shrink: replaced as a whole, no leftovers
	if err := repo.ReplaceItemGallery(ctx, id, []data.GalleryImage{{Path: "/c.png"}, {Path: "/a.png", Alt: "front"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.ItemGallery(ctx, id)
	if len(got) != 2 || got[0].Path != "/c.png" || got[1].Path != "/a.png" {
		t.Fatalf("after reorder %+v", got)
	}
	if p, _, _ := repo.ItemThumbnailPath(ctx, id); p != "/c.png" {
		t.Fatalf("thumbnail after reorder %q", p)
	}
}

func TestReplaceItemGallery_EmptyClears_KeepsIcon(t *testing.T) {
	ctx := context.Background()
	repo, id := galleryRepo(t)
	if err := repo.ReplaceItemGallery(ctx, id, []data.GalleryImage{{Path: "/a.png"}, {Path: "/b.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceItemGallery(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ItemGallery(ctx, id); len(got) != 0 {
		t.Fatalf("leftover %+v", got)
	}
	if _, ok, _ := repo.ItemThumbnailPath(ctx, id); ok {
		t.Fatal("thumbnail left behind")
	}
}

func TestReplaceItemGallery_MaxTenAndUnknownItem(t *testing.T) {
	ctx := context.Background()
	repo, id := galleryRepo(t)
	var many []data.GalleryImage
	for i := 0; i < 11; i++ {
		many = append(many, data.GalleryImage{Path: "/x.png"})
	}
	if err := repo.ReplaceItemGallery(ctx, id, many); err == nil {
		t.Fatal("11 images must be refused")
	}
	if err := repo.ReplaceItemGallery(ctx, "nope", []data.GalleryImage{{Path: "/a.png"}}); err == nil {
		t.Fatal("unknown item must be refused")
	}
}

func TestReplaceVariantGallery_SeparateFromItem(t *testing.T) {
	ctx := context.Background()
	repo, id, rawDB := galleryRepoDB(t)
	if err := repo.ReplaceItemGallery(ctx, id, []data.GalleryImage{{Path: "/item.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceVariantGallery(ctx, id, "var1", nil); err == nil {
		t.Fatal("unknown variant must be refused")
	}
	if _, err := rawDB.ExecContext(ctx, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('var1', ?, 'Red', 500)`, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceVariantGallery(ctx, id, "var1", []data.GalleryImage{{Path: "/v1.png", Alt: "red"}, {Path: "/v2.png"}}); err != nil {
		t.Fatal(err)
	}
	vg, err := repo.VariantGallery(ctx, id, "var1")
	if err != nil || len(vg) != 2 || vg[0].Path != "/v1.png" || vg[0].Alt != "red" || vg[1].Position != 2 {
		t.Fatalf("variant gallery %+v %v", vg, err)
	}
	if p, _, _ := repo.ItemThumbnailPath(ctx, id); p != "/item.png" {
		t.Fatalf("a variant gallery changed the item thumbnail: %q", p)
	}
	if g, _ := repo.ItemGallery(ctx, id); len(g) != 1 {
		t.Fatalf("variant rows leaked into the item gallery: %+v", g)
	}
	if err := repo.ReplaceVariantGallery(ctx, id, "var1", nil); err != nil {
		t.Fatal(err)
	}
	if vg, _ := repo.VariantGallery(ctx, id, "var1"); len(vg) != 0 {
		t.Fatalf("leftover %+v", vg)
	}
}

// Every existing reader keeps seeing the primary only.
func TestReadersStillThumbnail(t *testing.T) {
	ctx := context.Background()
	repo, id := galleryRepo(t)
	if err := repo.ReplaceItemGallery(ctx, id, []data.GalleryImage{{Path: "/a.png"}, {Path: "/b.png"}, {Path: "/c.png"}}); err != nil {
		t.Fatal(err)
	}
	m, err := repo.ItemThumbnailsFor(ctx, []string{id})
	if err != nil || m[id] != "/a.png" {
		t.Fatalf("ItemThumbnailsFor %v %v", m, err)
	}
	if p, err := repo.ItemThumbnailFor(ctx, id); err != nil || p != "/a.png" {
		t.Fatalf("ItemThumbnailFor %q %v", p, err)
	}
	// the legacy single write still works and keeps the gallery rows
	if err := repo.SetItemPhoto(ctx, id, "/z.png"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := repo.ItemThumbnailPath(ctx, id); p != "/z.png" {
		t.Fatalf("SetItemPhoto: %q", p)
	}
}
