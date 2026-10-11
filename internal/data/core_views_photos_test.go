package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// catalog.photos.v1 (ut-docs#4094): the active items that have a reference
// photo, confirmed photos first, newest first — so a camera-identify plugin
// spends its 64 item_image_open calls on items that have one, not on the
// first 64 names (an item at position 183 of 230 never reached the model).
func TestCatalogPhotosView(t *testing.T) {
	orig := paths.DataDir()
	root := t.TempDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(orig) })
	items := filepath.Join(root, "public", "assets", "items")
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	photo := func(rel string, at time.Time) {
		t.Helper()
		p := filepath.Join(items, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	dbo, err := db.Open(testsupport.MigratedDBFile(t, "catalog-photos-view.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, base_price, is_active, unit, is_weighed) VALUES
		('a', 'A', 'Apple',  100, 1, 'each', 0),
		('b', 'B', 'Banana', 100, 1, 'each', 0),
		('c', 'C', 'Cherry', 100, 1, 'each', 0),
		('d', 'D', 'Date',   100, 1, 'each', 0),
		('z', 'Z', 'Zest',   100, 1, 'each', 0),
		('x', 'X', 'Gone',   100, 0, 'each', 0)`)
	photo("a/thumb.png", base)                      // old thumbnail
	photo("z/thumb.png", base.Add(3*time.Hour))     // newest thumbnail, last by name
	photo("c/ai_ref/1.png", base.Add(time.Hour))    // confirmed photo
	photo("d/ai_ref/1.png", base.Add(2*time.Hour))  // newer confirmed photo
	photo("d/thumb.png", base.Add(5*time.Hour))     // ignored: d has an ai_ref
	photo("x/thumb.png", base.Add(9*time.Hour))     // inactive item: not listed
	photo("ghost/thumb.png", base.Add(9*time.Hour)) // no such item: not listed
	// b has no photo: not listed, so it costs the plugin no open.

	v, ok := LookupCoreView("catalog.photos.v1")
	if !ok {
		t.Fatal("catalog.photos.v1 not registered")
	}
	if v.Permission != "view:inventory" {
		t.Fatalf("permission = %q, want view:inventory", v.Permission)
	}
	args, err := v.ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if args["offset"] != 0 || args["limit"] != 500 {
		t.Fatalf("default args = %v, want offset 0, limit 500", args)
	}
	ctx := context.Background()
	out, err := RunCoreView(ctx, dbo.DB, v, args, CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	s := func(t time.Time) string { return itoa(t.Unix()) }
	want := `[` +
		`{"item_id":"d","source":"ai_ref","photo_at":` + s(base.Add(2*time.Hour)) + `},` +
		`{"item_id":"c","source":"ai_ref","photo_at":` + s(base.Add(time.Hour)) + `},` +
		`{"item_id":"z","source":"thumb","photo_at":` + s(base.Add(3*time.Hour)) + `},` +
		`{"item_id":"a","source":"thumb","photo_at":` + s(base) + `}]`
	if string(out) != want {
		t.Fatalf("result:\n got  %s\n want %s", out, want)
	}

	out, err = RunCoreView(ctx, dbo.DB, v, map[string]int{"offset": 1, "limit": 2}, CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	wantPage := `[{"item_id":"c","source":"ai_ref","photo_at":` + s(base.Add(time.Hour)) + `},` +
		`{"item_id":"z","source":"thumb","photo_at":` + s(base.Add(3*time.Hour)) + `}]`
	if string(out) != wantPage {
		t.Fatalf("page:\n got  %s\n want %s", out, wantPage)
	}
	out, err = RunCoreView(ctx, dbo.DB, v, map[string]int{"offset": 4, "limit": 500}, CoreViewMaxResult)
	if err != nil || string(out) != "[]" {
		t.Fatalf("past the end = %s, %v; want []", out, err)
	}
}

func itoa(n int64) string { return fmt.Sprint(n) }
