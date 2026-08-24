package data_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// seedSettingsTable creates the generic key/value settings table
// (001_init.sql shape) that NewCatalogTestDB deliberately omits — several
// pre-existing tests create their own copy, so testsupport cannot own it.
// The barcode scan path / AddBarcode read the shop-enabled symbology set
// (ADR-0059 §2) through it; without the table both fall back to the
// default-enabled set.
func seedSettingsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
}

// TestDefaultEnabledBarcodeSymbologyIDs pins ADR-0059 §2's default set:
// every registry entry except the two embedded-data ones, which are opt-in.
func TestDefaultEnabledBarcodeSymbologyIDs(t *testing.T) {
	got := data.DefaultEnabledBarcodeSymbologyIDs()
	want := map[string]bool{
		"EAN13": true, "EAN8": true, "UPCA": true, "UPCE": true,
		"GTIN14": true, "CODE128": true, "CODE39": true, "INTERNAL_PLU": true,
	}
	if len(got) != len(want) {
		t.Fatalf("default set = %v, want the 8 non-embedded registry ids", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("default set contains unexpected id %q (embedded-data ids must default OFF)", id)
		}
	}
	// The default set must stay a strict subset of the registry — a typo'd
	// id here would silently disable a symbology (Match skips unknown ids).
	reg := barcode.Default()
	for _, id := range got {
		if _, ok := reg.Lookup(id); !ok {
			t.Fatalf("default id %q is not a registry id", id)
		}
	}
}

// TestEnabledBarcodeSymbologies_SeedsDefaultOnFirstRead is the upgrade path
// (acceptance criterion 4's storage half): a shop with NO settings row gets
// the compatibility-preserving default seeded on first read, and later
// reads return the same stored value.
func TestEnabledBarcodeSymbologies_SeedsDefaultOnFirstRead(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	ids, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if len(ids) != len(data.DefaultEnabledBarcodeSymbologyIDs()) {
		t.Fatalf("first read = %v, want default set", ids)
	}

	// The row must now exist (GetOrCreate seeded it).
	if _, found, err := repo.Get(ctx, data.BarcodeEnabledSymbologiesKey); err != nil || !found {
		t.Fatalf("expected the setting row to be seeded, found=%v err=%v", found, err)
	}
}

func TestEnabledBarcodeSymbologies_RoundTrip(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.SetEnabledBarcodeSymbologies(ctx, []string{"INTERNAL_PLU"}); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "INTERNAL_PLU" {
		t.Fatalf("round trip = %v, want [INTERNAL_PLU]", ids)
	}
}

// TestEnabledBarcodeSymbologies_CorruptRowFallsBackToDefaults: a corrupt
// value must not brick scanning — the accessor reports the error but STILL
// hands back the default set so scan-path callers can proceed.
func TestEnabledBarcodeSymbologies_CorruptRowFallsBackToDefaults(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.Set(ctx, data.BarcodeEnabledSymbologiesKey, "not-json{"); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.EnabledBarcodeSymbologies(ctx)
	if err == nil {
		t.Fatal("expected a parse error for a corrupt row")
	}
	if len(ids) != len(data.DefaultEnabledBarcodeSymbologyIDs()) {
		t.Fatalf("corrupt row fallback = %v, want the default set", ids)
	}
}
