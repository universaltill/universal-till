package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#2501: the sell-screen tile cache (internal/ui's SellScreenCache)
// keys every entry on BOTH sync_admin_version.generation (every admin
// table — items, categories, shortcut_buttons, settings, …) AND
// sell_screen_version.generation, migration 042's counter for the two tables
// the sell screen renders from that the admin counter does NOT cover:
// price_history (never synced, ADR-0099) and item_images (photos, D2 limit)
// — and, since migration 047 (ut-docs#2765), every other catalog table the
// tiles render from too (sell_screen_catalog_version_test.go).
// A table the tiles read that neither counter sees would serve stale tiles
// until the cache's max-age safety net — these tests pin both halves.

func sellScreenGeneration(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var g int64
	if err := d.QueryRow(`SELECT generation FROM sell_screen_version WHERE id = 1`).Scan(&g); err != nil {
		t.Fatalf("read sell_screen_version: %v", err)
	}
	return g
}

func TestSellScreenVersion_TriggersBumpOnPriceHistoryAndItemImages(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-triggers.db")
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	steps := []struct{ name, sql string }{
		{"price_history insert", `INSERT INTO price_history (id, item_id, price) VALUES ('ph1', 'itm1', 99)`},
		{"price_history update", `UPDATE price_history SET ends_at = CURRENT_TIMESTAMP WHERE id = 'ph1'`},
		{"price_history delete", `DELETE FROM price_history WHERE id = 'ph1'`},
		{"item_images insert", `INSERT INTO item_images (id, item_id, role, path) VALUES ('img1', 'itm1', 'thumbnail', '/public/images/a.png')`},
		{"item_images update", `UPDATE item_images SET path = '/public/images/b.png' WHERE id = 'img1'`},
		{"item_images delete", `DELETE FROM item_images WHERE id = 'img1'`},
	}
	prev := sellScreenGeneration(t, d)
	for _, s := range steps {
		mustExec(t, d, s.sql)
		got := sellScreenGeneration(t, d)
		if got != prev+1 {
			t.Fatalf("%s: sell_screen_version %d -> %d, want %d", s.name, prev, got, prev+1)
		}
		prev = got
	}
}

func TestSellScreenRepo_VersionMovesOnItemAndPriceChanges(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-version.db")
	repo := NewSellScreenRepo(d.DB)
	ctx := context.Background()

	admin0, sell0, ok, err := repo.SellScreenVersion(ctx)
	if err != nil || !ok {
		t.Fatalf("SellScreenVersion on a migrated db = ok %v, err %v; want ok", ok, err)
	}

	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	admin1, sell1, _, _ := repo.SellScreenVersion(ctx)
	if admin1 == admin0 {
		t.Fatalf("item insert did not move the admin half of the version (%d)", admin1)
	}
	mustExec(t, d, `UPDATE items SET is_active = 0 WHERE id = 'itm1'`)
	admin2, _, _, _ := repo.SellScreenVersion(ctx)
	if admin2 == admin1 {
		t.Fatalf("item deactivate did not move the admin half of the version (%d)", admin2)
	}
	// ut-docs#2765 (migration 047): the sell half now moves on catalog
	// writes too — it is the open sale screen's live-refresh signal.
	if sell1 == sell0 {
		t.Fatalf("an item write did not move the sell half (%d); 047's items triggers should", sell1)
	}
	mustExec(t, d, `INSERT INTO price_history (id, item_id, price) VALUES ('ph1', 'itm1', 99)`)
	_, sell2, _, _ := repo.SellScreenVersion(ctx)
	if sell2 == sell1 {
		t.Fatalf("price_history insert did not move the sell half of the version (%d)", sell2)
	}
}

// A missing counter row (a hand-edited database) must read as ok=false —
// never an error that fails the render, and never a stable value the cache
// could key on forever.
func TestSellScreenRepo_MissingRowIsNotOK(t *testing.T) {
	for _, tbl := range []string{"sync_admin_version", "sell_screen_version"} {
		t.Run(tbl, func(t *testing.T) {
			d := openMigratedDB(t, "sellscreen-missing-"+tbl+".db")
			mustExec(t, d, `DELETE FROM `+tbl)
			_, _, ok, err := NewSellScreenRepo(d.DB).SellScreenVersion(context.Background())
			if err != nil {
				t.Fatalf("missing %s row: err %v, want nil", tbl, err)
			}
			if ok {
				t.Fatalf("missing %s row: ok = true, want false (never cache)", tbl)
			}
		})
	}
}

func TestSellScreenRepo_NextPriceBoundary(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-boundary.db")
	repo := NewSellScreenRepo(d.DB)
	ctx := context.Background()
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)

	got, err := repo.NextPriceBoundary(ctx)
	if err != nil {
		t.Fatalf("NextPriceBoundary (empty): %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("NextPriceBoundary with no price_history = %v, want zero time", got)
	}

	// A past, open-ended row is no boundary; a future start and a future end
	// both are — the earliest wins. starts_at/ends_at use the same
	// datetime('now') text format ItemCurrentPrices compares against.
	now := time.Now().UTC().Truncate(time.Second)
	fmtTS := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }
	mustExec(t, d, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('past', 'itm1', 90, ?)`, fmtTS(now.Add(-time.Hour)))
	mustExec(t, d, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('future-start', 'itm1', 80, ?)`, fmtTS(now.Add(2*time.Hour)))
	mustExec(t, d, `INSERT INTO price_history (id, item_id, price, starts_at, ends_at) VALUES ('future-end', 'itm1', 70, ?, ?)`, fmtTS(now.Add(-time.Minute)), fmtTS(now.Add(30*time.Minute)))

	got, err = repo.NextPriceBoundary(ctx)
	if err != nil {
		t.Fatalf("NextPriceBoundary: %v", err)
	}
	if want := now.Add(30 * time.Minute); !got.Equal(want) {
		t.Fatalf("NextPriceBoundary = %v, want %v (the earliest future starts_at/ends_at)", got, want)
	}
	// RFC3339-shaped timestamps normalize through datetime() the same way.
	mustExec(t, d, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('rfc', 'itm1', 60, ?)`, now.Add(10*time.Minute).Format(time.RFC3339))
	got, _ = repo.NextPriceBoundary(ctx)
	if want := now.Add(10 * time.Minute); !got.Equal(want) {
		t.Fatalf("NextPriceBoundary with an RFC3339 starts_at = %v, want %v", got, want)
	}
}
