// Command category_picture changes a category's picture in a RUNNING e2e
// till's database, out of band — the way a cloud directive lands while the
// sale screen is open (ut-docs#2717, category-icon-directive-2717.spec.ts).
//
//	UT_DATA_DIR=<dir> go run ./e2e/category_picture directive <category-id> <icon-id>
//	UT_DATA_DIR=<dir> go run ./e2e/category_picture legacy-path <category-id> <image-path>
//
// "directive" applies a save_category {id, icon} through
// data.CatalogRepo.SaveCategory — the repository write the cloudsync
// save_category hook (internal/pages cloudSaveCategory) performs.
// "legacy-path" stores an image_path and clears the icon, the row an older
// till's library pick left behind (ut-docs#2500); "" clears the picture.
// The e2e till has no cloud to poll and a 2-minute sync tick, so the spec
// drives the write directly rather than through a fake cloud.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" || len(os.Args) != 4 {
		fatalf("usage: UT_DATA_DIR=<dir> category_picture directive|legacy-path <category-id> <value>")
	}
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
		fmt.Printf("updated category %s (cleared image %q)\n", res.Name, res.ClearedImagePath)
	case "legacy-path":
		if err := repo.SetCategoryPicture(ctx, id, value, ""); err != nil {
			fatalf("set picture: %v", err)
		}
		fmt.Println("picture set")
	default:
		fatalf("unknown mode %q", mode)
	}
}
