package data

// Drift guards for the per-country defaults (ut-docs#659).
//
// The shipped defaults exist in two places by necessity: migration 041 seeds a
// fresh database, and builtinCountryDefaults is what Delete restores a builtin
// country to. Neither can be removed — a migration cannot call Go, and a
// restore cannot re-run a migration — so the honest protection is a test that
// makes them impossible to diverge silently.

import (
	"context"
	"testing"
)

func TestBuiltinDefaultsMatchMigrationSeed(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	seeded, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byCode := make(map[string]CountrySetting, len(seeded))
	for _, c := range seeded {
		byCode[c.Code] = c
	}

	defaults := BuiltinCountryDefaults()
	if len(defaults) != len(seeded) {
		t.Fatalf("builtinCountryDefaults has %d entries, migration 041 seeded %d — they must match exactly",
			len(defaults), len(seeded))
	}

	for _, want := range defaults {
		got, ok := byCode[want.Code]
		if !ok {
			t.Errorf("%s is in builtinCountryDefaults but not seeded by migration 041", want.Code)
			continue
		}
		if got.NameKey != want.NameKey ||
			got.Currency != want.Currency ||
			got.CurrencySymbol != want.CurrencySymbol ||
			got.TaxRateBP != want.TaxRateBP ||
			got.TaxInclusive != want.TaxInclusive ||
			got.ArchiveMinDays != want.ArchiveMinDays ||
			!got.IsBuiltin {
			t.Errorf("%s drifted between migration 041 and builtinCountryDefaults:\n  seeded=%+v\n  go    =%+v", want.Code, got, want)
		}
	}
}

// The floor constant and the migration's seeded value are written independently
// (Go const vs SQL literal), so pin them together too.
func TestSeededRetentionMatchesGlobalFloorConstant(t *testing.T) {
	_, repo := openCountryTestDB(t)
	seeded, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range seeded {
		if c.ArchiveMinDays != GlobalArchiveMinDays {
			t.Fatalf("migration 041 seeds %s at %d days but GlobalArchiveMinDays is %d — update both",
				c.Code, c.ArchiveMinDays, GlobalArchiveMinDays)
		}
	}
}
