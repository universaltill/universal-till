package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4958: a held sale snapshots each line's ImageURL, so a photo
// replaced or removed in the item editor while the sale was parked must not
// come back from the snapshot on resume -- the recalled basket shows the
// item's CURRENT item_images thumbnail, or none. End to end through the real
// hold and resume handlers (the item itself is "itm1", ringing up as "ABC").
func TestResumeHeldSale_ShowsCurrentItemPhoto_4958(t *testing.T) {
	const oldPhoto = "/public/assets/items/itm1/old-thumb.png"
	const newPhoto = "/public/assets/items/itm1/new-thumb.png"
	cases := []struct {
		name   string
		change func(t *testing.T, repo *data.CatalogRepo)
		want   string
	}{
		{"photo replaced while held", func(t *testing.T, repo *data.CatalogRepo) {
			if err := repo.SetItemThumbnail(context.Background(), "itm1", newPhoto); err != nil {
				t.Fatal(err)
			}
		}, newPhoto},
		{"photo removed while held", func(t *testing.T, repo *data.CatalogRepo) {
			if err := repo.ClearItemThumbnail(context.Background(), "itm1"); err != nil {
				t.Fatal(err)
			}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newHoldTestDeps(t)
			for _, q := range []string{
				`CREATE TABLE items (id TEXT PRIMARY KEY, icon TEXT)`,
				`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'thumbnail', path TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0)`,
				`CREATE UNIQUE INDEX ux_item_images_thumbnail_once ON item_images (item_id, role)`,
				`INSERT INTO items(id) VALUES('itm1')`,
			} {
				if _, err := dp.Db.Exec(q); err != nil {
					t.Fatalf("seed %q: %v", q, err)
				}
			}
			repo := data.NewCatalogRepo(dp.Db)
			if err := repo.SetItemThumbnail(context.Background(), "itm1", oldPhoto); err != nil {
				t.Fatal(err)
			}
			if _, err := dp.Engine.Scan("ABC"); err != nil {
				t.Fatalf("scan: %v", err)
			}
			// The resolver stub carries no image; ring-up state is what a
			// real till would have captured at scan time.
			dp.Engine.SetItemImage("itm1", oldPhoto)

			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
			var id string
			if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
				t.Fatalf("expected a held_sales row: %v", err)
			}
			var payload string
			if err := dp.Db.QueryRow(`SELECT payload FROM held_sales WHERE id = ?`, id).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload, oldPhoto) {
				t.Fatalf("test setup: the snapshot should carry the old photo, got %s", payload)
			}

			tc.change(t, repo)

			req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("resume: status %d: %s", rec.Code, rec.Body.String())
			}
			lines := dp.Engine.Basket().Lines
			if len(lines) != 1 {
				t.Fatalf("expected 1 restored line, got %d", len(lines))
			}
			if got := lines[0].ImageURL; got != tc.want {
				t.Fatalf("restored line ImageURL = %q, want %q", got, tc.want)
			}
		})
	}
}
