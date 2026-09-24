package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2500: a category carries one image path (a built-in icon's
// /public/assets/category-icons/... path or an uploaded photo's
// /public/assets/categories/<id>/thumb.png). SetCategoryImage writes it,
// "" clears it back to NULL, and all three category readers carry it.
func TestSetCategoryImage_RoundTripAndClear(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	const icon = "/public/assets/category-icons/coffee.svg"
	if err := repo.SetCategoryImage(ctx, id, icon); err != nil {
		t.Fatalf("SetCategoryImage: %v", err)
	}

	all, err := repo.ListCategories(ctx)
	if err != nil || len(all) != 1 || all[0].ImagePath != icon {
		t.Fatalf("ListCategories = %+v err=%v, want ImagePath %q", all, err, icon)
	}
	active, err := repo.ListActiveCategories(ctx)
	if err != nil || len(active) != 1 || active[0].ImagePath != icon {
		t.Fatalf("ListActiveCategories = %+v err=%v, want ImagePath %q", active, err, icon)
	}
	admin, err := repo.ListCategoriesForAdmin(ctx)
	if err != nil || len(admin) != 1 || admin[0].ImagePath != icon {
		t.Fatalf("ListCategoriesForAdmin = %+v err=%v, want ImagePath %q", admin, err, icon)
	}

	if err := repo.SetCategoryImage(ctx, id, ""); err != nil {
		t.Fatalf("SetCategoryImage clear: %v", err)
	}
	var isNull bool
	if err := db.QueryRow(`SELECT image_path IS NULL FROM categories WHERE id = ?`, id).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("clearing must store NULL: isNull=%v err=%v", isNull, err)
	}
	admin, _ = repo.ListCategoriesForAdmin(ctx)
	if admin[0].ImagePath != "" {
		t.Fatalf("cleared image reads back %q, want empty", admin[0].ImagePath)
	}
}

func TestSetCategoryImage_UnknownIDNotFound(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	if err := repo.SetCategoryImage(context.Background(), "nope", "/public/x.svg"); !errors.Is(err, data.ErrCategoryNotFound) {
		t.Fatalf("SetCategoryImage on unknown id: err=%v, want ErrCategoryNotFound", err)
	}
}
