package data

// Per-country settings (universaltill/ut-docs#659). The country_settings
// table replaces the compile-time setupCountries slice as the source of
// per-jurisdiction defaults (currency, tax, archive retention).
//
// The behaviours worth pinning are the ones a UI can't be trusted to
// enforce: the retention floor holds in the REPOSITORY, deleting a builtin
// country restores it rather than removing it, and the seed reproduces the
// pre-existing hardcoded values exactly (this must be a behaviour-preserving
// move for currency/tax — only archive_min_days is new).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openCountryTestDB(t *testing.T) (*db.DB, *CountrySettingsRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "countries.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewCountrySettingsRepo(dbo.DB)
}

// The migration seeds the same 14 countries the wizard hardcoded, with their
// currency/tax values unchanged — a regression here means the wizard's
// prefill silently changed for a real country.
func TestCountrySettingsSeedPreservesWizardDefaults(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 14 {
		t.Fatalf("seeded countries = %d, want 14", len(all))
	}

	// Spot-check the values that came from setup_page.go's setupCountries,
	// including the two the German pilot depends on.
	want := map[string]struct {
		currency  string
		taxRateBP int64
		inclusive bool
	}{
		"GB": {"GBP", 2000, true},
		"DE": {"EUR", 1900, true},
		"US": {"USD", 0, false},
		"TR": {"TRY", 2000, true},
		// "OTHER" keeps the wizard's empty-currency/no-tax defaults.
		"OTHER": {"", 0, true},
	}
	for code, w := range want {
		got, ok, err := repo.Get(ctx, code)
		if err != nil {
			t.Fatalf("get %s: %v", code, err)
		}
		if !ok {
			t.Fatalf("country %s not seeded", code)
		}
		if got.Currency != w.currency || got.TaxRateBP != w.taxRateBP || got.TaxInclusive != w.inclusive {
			t.Errorf("%s = {%q %d %v}, want {%q %d %v}",
				code, got.Currency, got.TaxRateBP, got.TaxInclusive, w.currency, w.taxRateBP, w.inclusive)
		}
		if !got.IsBuiltin {
			t.Errorf("%s should be builtin", code)
		}
	}
}

// Every seeded country starts at the ADR-0040 global floor. No
// per-jurisdiction retention value is shipped — that needs an ADR-0040
// amendment and the product owner's sign-off (ut-docs#659), so a seed that
// differs by country is a compliance regression, not a feature.
func TestCountrySettingsSeedUsesUniformGlobalFloor(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range all {
		if c.ArchiveMinDays != GlobalArchiveMinDays {
			t.Errorf("%s archive_min_days = %d, want the uniform global floor %d",
				c.Code, c.ArchiveMinDays, GlobalArchiveMinDays)
		}
	}
}

// The floor is enforced where it cannot be bypassed. A UI-only check would
// leave the API and any future caller free to destroy a legal record.
func TestCountrySettingsRetentionFloorEnforcedInRepo(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	de, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatalf("get DE: %v", err)
	}

	// Below the floor: refused.
	de.ArchiveMinDays = GlobalArchiveMinDays - 1
	if err := repo.Upsert(ctx, de); err == nil {
		t.Fatal("Upsert below the global floor should be refused")
	}

	// Re-read: the refused write must not have landed.
	after, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatalf("re-get DE: %v", err)
	}
	if after.ArchiveMinDays != GlobalArchiveMinDays {
		t.Fatalf("refused Upsert still mutated the row: %d", after.ArchiveMinDays)
	}

	// Raising it is always allowed.
	de.ArchiveMinDays = GlobalArchiveMinDays + 365
	if err := repo.Upsert(ctx, de); err != nil {
		t.Fatalf("raising retention should be allowed: %v", err)
	}
	raised, _, _ := repo.Get(ctx, "DE")
	if raised.ArchiveMinDays != GlobalArchiveMinDays+365 {
		t.Errorf("archive_min_days = %d, want %d", raised.ArchiveMinDays, GlobalArchiveMinDays+365)
	}
}

// "Delete" on a builtin country must land on a legal state, so it restores
// the shipped defaults instead of removing the row. Removing it would leave
// the wizard and the purge gate with no value at all for a real jurisdiction.
func TestDeleteBuiltinCountryRestoresDefaults(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	de, _, _ := repo.Get(ctx, "DE")
	de.Currency = "XXX"
	de.TaxRateBP = 1
	de.ArchiveMinDays = GlobalArchiveMinDays + 1000
	if err := repo.Upsert(ctx, de); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.Delete(ctx, "DE"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	restored, ok, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if !ok {
		t.Fatal("deleting a builtin country removed the row; it must restore defaults")
	}
	if restored.Currency != "EUR" || restored.TaxRateBP != 1900 || restored.ArchiveMinDays != GlobalArchiveMinDays {
		t.Errorf("DE not restored to defaults: {%q %d %d}",
			restored.Currency, restored.TaxRateBP, restored.ArchiveMinDays)
	}
}

// A country the operator added themselves carries no shipped opinion, so it
// deletes outright.
func TestDeleteCustomCountryRemovesIt(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	custom := CountrySetting{
		Code:           "NO",
		Currency:       "NOK",
		CurrencySymbol: "kr",
		TaxRateBP:      2500,
		TaxInclusive:   true,
		ArchiveMinDays: GlobalArchiveMinDays,
	}
	if err := repo.Upsert(ctx, custom); err != nil {
		t.Fatalf("upsert custom: %v", err)
	}
	got, ok, _ := repo.Get(ctx, "NO")
	if !ok {
		t.Fatal("custom country not stored")
	}
	if got.IsBuiltin {
		t.Error("operator-created country must not be marked builtin")
	}

	if err := repo.Delete(ctx, "NO"); err != nil {
		t.Fatalf("delete custom: %v", err)
	}
	if _, ok, _ := repo.Get(ctx, "NO"); ok {
		t.Error("custom country should be removed by Delete")
	}
}

// Codes are the join key with settings' KeyCountry and with the wizard, so
// they are normalised on write rather than trusted.
func TestCountrySettingsNormalisesAndValidates(t *testing.T) {
	_, repo := openCountryTestDB(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, CountrySetting{Code: "  se ", Currency: "SEK", ArchiveMinDays: GlobalArchiveMinDays}); err != nil {
		t.Fatalf("upsert lowercase/padded code: %v", err)
	}
	if _, ok, _ := repo.Get(ctx, "SE"); !ok {
		t.Error("code should be trimmed and upper-cased on write")
	}
	// Get normalises too.
	if _, ok, _ := repo.Get(ctx, " se "); !ok {
		t.Error("Get should normalise the code it is given")
	}

	if err := repo.Upsert(ctx, CountrySetting{Code: "", Currency: "EUR", ArchiveMinDays: GlobalArchiveMinDays}); err == nil {
		t.Error("empty country code should be rejected")
	}
	if err := repo.Upsert(ctx, CountrySetting{Code: "DE", Currency: "EUR", TaxRateBP: -1, ArchiveMinDays: GlobalArchiveMinDays}); err == nil {
		t.Error("negative tax rate should be rejected")
	}
	if err := repo.Upsert(ctx, CountrySetting{Code: "DE", Currency: "EUR", TaxRateBP: maxTaxRateBP + 1, ArchiveMinDays: GlobalArchiveMinDays}); err == nil {
		t.Error("tax rate above 100% should be rejected")
	}

	// The code doubles as an HTML element id binding each admin row's inputs
	// to its form, so anything that would break that association is refused
	// at the repository rather than producing a silently inert row.
	for _, bad := range []string{"D E", `D"E`, "D-E", "<script>", "TOOLONGCODE"} {
		if err := repo.Upsert(ctx, CountrySetting{Code: bad, Currency: "EUR", ArchiveMinDays: GlobalArchiveMinDays}); err == nil {
			t.Errorf("country code %q should be rejected", bad)
		}
	}
}
