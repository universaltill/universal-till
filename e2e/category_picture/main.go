// Command category_picture changes a category's picture in a RUNNING e2e
// till's database, out of band — the way a cloud directive lands while the
// sale screen is open (ut-docs#2717, category-icon-directive-2717.spec.ts).
//
//	UT_DATA_DIR=<dir> go run ./e2e/category_picture directive <category-id> <icon-id>
//	UT_DATA_DIR=<dir> go run ./e2e/category_picture legacy-path <category-id> <image-path>
//	UT_DATA_DIR=<dir> go run ./e2e/category_picture photo <category-id> <icon-id>
//
// "directive" applies a save_category {id, icon} through
// data.CatalogRepo.SaveCategory — the repository write the cloudsync
// save_category hook (internal/pages cloudSaveCategory) performs.
// "legacy-path" stores an image_path and clears the icon, the row an older
// till's library pick left behind (ut-docs#2500); "" clears the picture.
// "photo" (ut-docs#3585) writes a real thumbnail file and stores it as
// image_path alongside the given icon id ("" for none) — the pair a
// category's own upload now keeps (internal/pages storeCategoryPhoto),
// used to prove the sale screen still shows the photo, not the icon,
// once both are stored.
// The e2e till has no cloud to poll and a 2-minute sync tick, so the spec
// drives the write directly rather than through a fake cloud.
package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/paths"
)

// categoryThumbURL / categoryThumbFile mirror internal/pages/
// categories_page.go's unexported pair: this tool runs in a separate
// `main` package (the data-access guard's test-tooling exemption,
// CLAUDE.md "Data access") so it cannot import them directly.
func categoryThumbURL(id string) string { return "/public/assets/categories/" + id + "/thumb.png" }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" || len(os.Args) != 4 {
		fatalf("usage: UT_DATA_DIR=<dir> category_picture directive|legacy-path <category-id> <value>")
	}
	paths.Init(dataDir)
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	repo := data.NewCatalogRepo(conn.DB)
	ctx := context.Background()
	mode, id, value := os.Args[1], os.Args[2], os.Args[3]
	switch mode {
	case "directive":
		res, err := repo.SaveCategory(ctx, data.CategorySave{ID: id, Icon: &value})
		if err != nil {
			fatalf("save_category: %v", err)
		}
		fmt.Printf("updated category %s\n", res.Name)
	case "legacy-path":
		if err := repo.SetCategoryPicture(ctx, id, value, ""); err != nil {
			fatalf("set picture: %v", err)
		}
		fmt.Println("picture set")
	case "photo":
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		thumbFile := paths.Data("public", "assets", "categories", id, "thumb.png")
		if err := imaging.WriteThumbPNG(img, thumbFile); err != nil {
			fatalf("write thumb: %v", err)
		}
		if err := repo.SetCategoryPicture(ctx, id, categoryThumbURL(id), value); err != nil {
			fatalf("set picture: %v", err)
		}
		fmt.Println("photo set")
	default:
		fatalf("unknown mode %q", mode)
	}
}
