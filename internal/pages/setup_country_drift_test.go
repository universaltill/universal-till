package pages

// Drift guard between the wizard's hardcoded setupCountries slice and the
// country_settings defaults it was moved into (ut-docs#659).
//
// #659 deliberately does NOT change the wizard — that is #660. So until #660
// lands, the slice below and the seeded table are two live copies of the same
// data, and an edit to one would silently change what a real shop is prefilled
// with. This test fails the moment they disagree.
//
// When #660 removes setupCountries, delete this file with it.

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func TestSetupCountriesMatchCountrySettingsDefaults(t *testing.T) {
	defaults := data.BuiltinCountryDefaults()
	if len(setupCountries) != len(defaults) {
		t.Fatalf("setupCountries has %d entries, data.BuiltinCountryDefaults() has %d — ut-docs#659 requires they stay identical until #660 removes the slice",
			len(setupCountries), len(defaults))
	}

	byCode := make(map[string]data.CountrySetting, len(defaults))
	for _, d := range defaults {
		byCode[d.Code] = d
	}

	for _, sc := range setupCountries {
		want, ok := byCode[sc.Code]
		if !ok {
			t.Errorf("wizard country %s has no country_settings default", sc.Code)
			continue
		}
		if sc.NameKey != want.NameKey {
			t.Errorf("%s name key: wizard %q, defaults %q", sc.Code, sc.NameKey, want.NameKey)
		}
		if sc.Currency != want.Currency {
			t.Errorf("%s currency: wizard %q, defaults %q", sc.Code, sc.Currency, want.Currency)
		}
		if sc.TaxInclusive != want.TaxInclusive {
			t.Errorf("%s tax-inclusive: wizard %v, defaults %v", sc.Code, sc.TaxInclusive, want.TaxInclusive)
		}
		// The wizard carries percent; the table carries basis points.
		if int64(sc.TaxRatePct)*100 != want.TaxRateBP {
			t.Errorf("%s tax rate: wizard %d%% (= %d bp), defaults %d bp",
				sc.Code, sc.TaxRatePct, int64(sc.TaxRatePct)*100, want.TaxRateBP)
		}
	}
}
