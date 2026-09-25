package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2717: a save_category directive whose icon replaces an uploaded
// photo, driven through buildCloudHooks exactly as Tick does. The row keeps
// one picture (the icon) and the superseded photo's file is removed, the
// same as the till's own editor does when an icon replaces an upload.
func TestCloudSaveCategory_IconReplacesUploadedPhoto(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dataDir := useTempDataDir(t)
	ctx := t.Context()
	const id = "cat-2717"
	if _, err := dp.Db.Exec(`INSERT INTO categories (id, name, image_path) VALUES (?, 'Breakfast', ?)`, id, categoryThumbURL(id)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dataDir, "public", "assets", "categories", id, "thumb.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := buildCloudHooks(dp, nil).SaveCategory(ctx, data.CategorySave{ID: id, Icon: sp("lucide:egg-fried")}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := dp.Db.QueryRow(`SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "-|lucide:egg-fried" {
		t.Fatalf("row = %q, want the photo cleared and the icon set", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the superseded upload must be removed, stat err=%v", err)
	}
}

// ut-docs#2717: the snapshot reports the icon the till actually DRAWS
// (iconid.EffectiveIcon), so my. shows what the sale screen shows — a
// library tile an older till stored as a path reports as its icon id, a
// photo reports no icon. Same "icon" key, same string type: schema 2 is
// unchanged.
func TestRemoteCategoriesReport_ReportsTheEffectiveIcon(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	if _, err := dp.Db.Exec(`DELETE FROM categories`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO categories (id, name, sort_order, image_path, icon) VALUES ('tile', 'Tile', 0, '/public/assets/category-icons/beer.svg', NULL)`,
		`INSERT INTO categories (id, name, sort_order, image_path, icon) VALUES ('both', 'Both', 1, '/public/assets/category-icons/beer.svg', 'lucide:coffee')`,
		`INSERT INTO categories (id, name, sort_order, image_path, icon) VALUES ('photo', 'Photo', 2, '/public/assets/categories/photo/thumb.png', 'lucide:soup')`,
		`INSERT INTO categories (id, name, sort_order, image_path, icon) VALUES ('icon', 'Icon', 3, NULL, 'lucide:leaf')`,
		`INSERT INTO categories (id, name, sort_order, image_path, icon) VALUES ('none', 'None', 4, NULL, NULL)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"tile": "lucide:beer", "both": "lucide:coffee", "photo": "", "icon": "lucide:leaf", "none": ""}
	got := map[string]string{}
	for _, c := range remoteCategoriesReport(t.Context(), dp) {
		got[c["id"].(string)] = c["icon"].(string)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("category %s reports icon %q, want %q", id, got[id], w)
		}
	}
}
