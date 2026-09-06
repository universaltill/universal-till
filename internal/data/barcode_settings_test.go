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

// TestEnabledBarcodeSymbologies_CachesAcrossReads is the ut-docs#1361
// regression: the setting is manager-toggled and changes essentially never,
// so it must be served from an in-process cache rather than re-hit SQLite
// and re-JSON-parsed on every call (the scan path calls this on every
// single scan). Proven here by mutating the underlying row with a raw
// UPDATE that bypasses both SettingsRepo write methods entirely — if the
// accessor were still hitting the DB on every read, it would observe this
// change immediately; a real cache must keep serving the value it read
// before the mutation.
func TestEnabledBarcodeSymbologies_CachesAcrossReads(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}
	first, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil || len(first) != 1 || first[0] != "EAN13" {
		t.Fatalf("priming read = %v, err %v, want [EAN13]", first, err)
	}

	// Bypass SettingsRepo entirely — a raw UPDATE, not Set/SetEnabled...
	// — so this only observes cache behaviour, never a write path's own
	// invalidation (covered separately below).
	if _, err := db.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`,
		`["CODE128"]`, data.BarcodeEnabledSymbologiesKey); err != nil {
		t.Fatalf("raw row mutation: %v", err)
	}

	second, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if len(second) != 1 || second[0] != "EAN13" {
		t.Fatalf("cached read = %v, want the stale cached [EAN13] (a live DB re-read would see CODE128)", second)
	}
}

// TestEnabledBarcodeSymbologies_InvalidatedBySetEnabledBarcodeSymbologies is
// the ut-docs#1361 regression for the full-list-replace write path: once
// the cache is primed, SetEnabledBarcodeSymbologies must invalidate it so
// the very next read observes the new value instead of the stale one.
func TestEnabledBarcodeSymbologies_InvalidatedBySetEnabledBarcodeSymbologies(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	if _, err := repo.EnabledBarcodeSymbologies(ctx); err != nil {
		t.Fatalf("priming read: %v", err)
	}
	if err := repo.SetEnabledBarcodeSymbologies(ctx, []string{"CODE39"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("post-write read: %v", err)
	}
	if len(got) != 1 || got[0] != "CODE39" {
		t.Fatalf("post-write read = %v, want [CODE39] (cache must be invalidated by the write)", got)
	}
}

// TestEnabledBarcodeSymbologies_InvalidatedBySetBarcodeSymbologyEnabled is
// the ut-docs#1361 regression for the real production write path (the
// settings-checklist toggle): once the cache is primed, a single toggle via
// SetBarcodeSymbologyEnabled must invalidate it too.
func TestEnabledBarcodeSymbologies_InvalidatedBySetBarcodeSymbologyEnabled(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewSettingsRepo(db)
	ctx := context.Background()

	primed, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("priming read: %v", err)
	}
	for _, id := range primed {
		if id == "EAN13_WEIGHT_PREFIX2X" {
			t.Fatal("EAN13_WEIGHT_PREFIX2X must default off for this test to be a real change")
		}
	}

	if _, err := repo.SetBarcodeSymbologyEnabled(ctx, "EAN13_WEIGHT_PREFIX2X", true); err != nil {
		t.Fatal(err)
	}
	got, err := repo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		t.Fatalf("post-toggle read: %v", err)
	}
	found := false
	for _, id := range got {
		if id == "EAN13_WEIGHT_PREFIX2X" {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-toggle read = %v, want EAN13_WEIGHT_PREFIX2X included (cache must be invalidated by the toggle)", got)
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
