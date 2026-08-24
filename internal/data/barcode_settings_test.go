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

// TestEnabledBarcodeSymbologies_NullOrEmptyRowFallsBackToDefaults: a stored
// "null" or "[]" unmarshals with NO error (unlike a genuinely corrupt row),
// so it needs its own fallback path — an all-disabling enabled set has no
// legitimate use and would silently break every scan and every untyped
// AddBarcode call (ut-docs#955). [""]/[" "] (ut-docs#959 review finding: a
// blank-string entry survives a bare length check, len==1, and would
// otherwise resolve zero real scans while reporting no error) must hit the
// same fallback. Unlike the corrupt-row case, none of these are errors: the
// caller gets the default set back with err == nil.
func TestEnabledBarcodeSymbologies_NullOrEmptyRowFallsBackToDefaults(t *testing.T) {
	for _, stored := range []string{"null", "[]", `[""]`, `[" "]`} {
		t.Run(stored, func(t *testing.T) {
			db := testsupport.NewCatalogTestDB(t)
			seedSettingsTable(t, db)
			defer db.Close()
			repo := data.NewSettingsRepo(db)
			ctx := context.Background()

			if err := repo.Set(ctx, data.BarcodeEnabledSymbologiesKey, stored); err != nil {
				t.Fatal(err)
			}
			ids, err := repo.EnabledBarcodeSymbologies(ctx)
			if err != nil {
				t.Fatalf("stored %q: unexpected error %v", stored, err)
			}
			want := data.DefaultEnabledBarcodeSymbologyIDs()
			if len(ids) != len(want) {
				t.Fatalf("stored %q fallback = %v, want the default set %v", stored, ids, want)
			}

			// The default set must still resolve a real scan/AddBarcode
			// match — not just be the right length. EAN13 is always in the
			// default set (only the two embedded-data ids default off).
			reg := barcode.Default()
			match, ok := reg.Match(ids, "4006381333931") // valid EAN-13 checksum
			if !ok || match.SymbologyID != "EAN13" {
				t.Fatalf("stored %q: default set %v must still resolve an EAN-13 scan, got match=%v ok=%v", stored, ids, match, ok)
			}
		})
	}
}

// TestSetBarcodeSymbologyEnabled_NullRowFallsBackToDefaults is the
// ut-docs#959 regression: SetBarcodeSymbologyEnabled has its own local
// corrupt/null-row recovery (separate from EnabledBarcodeSymbologies
// above), and a stored "null"/"[]" row unmarshals with NO error — it must
// not silently overwrite the "ids := defaults" starting point with a
// nil/empty slice. Toggling one symbology on starting from such a row must
// produce defaults ∪ {id}, not a single-entry [id] set.
func TestSetBarcodeSymbologyEnabled_NullRowFallsBackToDefaults(t *testing.T) {
	for _, stored := range []string{"null", "[]", `[""]`, `[" "]`} {
		t.Run(stored, func(t *testing.T) {
			db := testsupport.NewCatalogTestDB(t)
			seedSettingsTable(t, db)
			defer db.Close()
			repo := data.NewSettingsRepo(db)
			ctx := context.Background()

			if err := repo.Set(ctx, data.BarcodeEnabledSymbologiesKey, stored); err != nil {
				t.Fatal(err)
			}

			// EAN13_WEIGHT_PREFIX2X is one of the two embedded-data ids that
			// default OFF (ADR-0059 §2), so toggling it on is a genuine
			// change — unlike an id already in the default set, where a
			// no-op toggle would pass even with the bug (the pre-fix
			// [id]-only result and the correct defaults ∪ {id} result
			// would coincide).
			got, err := repo.SetBarcodeSymbologyEnabled(ctx, "EAN13_WEIGHT_PREFIX2X", true)
			if err != nil {
				t.Fatalf("stored %q: unexpected error %v", stored, err)
			}

			want := append(data.DefaultEnabledBarcodeSymbologyIDs(), "EAN13_WEIGHT_PREFIX2X")
			if len(got) != len(want) {
				t.Fatalf("stored %q: toggled result = %v, want defaults ∪ {EAN13_WEIGHT_PREFIX2X} (%d ids, got %d)", stored, got, len(want), len(got))
			}
			gotSet := map[string]bool{}
			for _, id := range got {
				gotSet[id] = true
			}
			for _, id := range want {
				if !gotSet[id] {
					t.Fatalf("stored %q: toggled result %v missing expected id %q", stored, got, id)
				}
			}
		})
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
