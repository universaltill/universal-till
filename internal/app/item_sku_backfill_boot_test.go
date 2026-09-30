package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#3097: an upgraded primary/standalone till gives every SKU-less
// item a generated SKU once at boot; a replica never does (its catalog
// comes from the primary, and a local write would diverge from it).

type recordingLog struct{ infos, errors []string }

func (l *recordingLog) Infof(f string, a ...any)  { l.infos = append(l.infos, fmt.Sprintf(f, a...)) }
func (l *recordingLog) Errorf(f string, a ...any) { l.errors = append(l.errors, fmt.Sprintf(f, a...)) }

func openBackfillBootDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, id := range []string{"legacy-1", "legacy-2"} {
		if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES (?, NULL, ?, 100)`, id, "Legacy "+id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	return d.DB
}

func TestBackfillItemSKUsOnPrimary_FillsOnPrimary(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	lg := &recordingLog{}

	backfillItemSKUsOnPrimary(ctx, sqlDB, lg)

	n, err := data.NewCatalogRepo(sqlDB).CountItemsMissingSKU(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d items still miss a SKU after boot backfill on a primary", n)
	}
	if len(lg.errors) != 0 {
		t.Fatalf("unexpected errors: %v", lg.errors)
	}
	if len(lg.infos) != 1 || !strings.Contains(lg.infos[0], "2") {
		t.Fatalf("want one info line naming the count 2, got %v", lg.infos)
	}

	// Second boot: nothing to do, nothing logged.
	lg2 := &recordingLog{}
	backfillItemSKUsOnPrimary(ctx, sqlDB, lg2)
	if len(lg2.infos)+len(lg2.errors) != 0 {
		t.Fatalf("second boot logged %v / %v, want silence", lg2.infos, lg2.errors)
	}
}

func TestBackfillItemSKUsOnPrimary_SkipsOnReplica(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	if err := data.NewSettingsRepo(sqlDB).Set(ctx, "sync.primary_url", "http://primary.local:8080"); err != nil {
		t.Fatal(err)
	}
	lg := &recordingLog{}

	backfillItemSKUsOnPrimary(ctx, sqlDB, lg)

	n, err := data.NewCatalogRepo(sqlDB).CountItemsMissingSKU(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("replica boot changed SKUs: %d items still missing, want 2", n)
	}
	if len(lg.infos)+len(lg.errors) != 0 {
		t.Fatalf("replica boot logged %v / %v", lg.infos, lg.errors)
	}
}
