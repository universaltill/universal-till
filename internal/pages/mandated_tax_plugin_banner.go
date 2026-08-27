package pages

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// missingMandatedTaxPlugin reports whether this shop's country has a
// country-MANDATED tax-rate plugin (countryTaxLocale, ADR-0067 amending
// ADR-0025 Decision 4 — today, only DE's §12 UStG dine-in/takeaway split via
// ut-plugin-tax-de) with no active, functioning plugin currently answering
// tax.rate.ask. Mirrors missingFiscalSigner's exact shape and reasoning
// (fiscal_signer_banner.go) — this is a Settings-page visibility signal, read
// only, and deliberately checks the SAME two things that function does for
// the identical reason: a plugin can be "installed" in the DB yet not
// actually running (wasm load failure flips install_state='broken' while
// leaving is_active untouched, ut-docs#368), so ActiveHookOwner alone can't
// tell a working plugin from a broken one still holding the hook.
//
// Unlike missingFiscalSigner, there is no fiscal.RequiresHardGate/
// IsSystemOfRecord gate here: the VAT dine-in/takeaway split is a rate-
// calculation obligation on every sale in a mandated country, not contingent
// on which entity is the fiscal system of record for TSE signing purposes —
// those are two different legal questions (ADR-0050's boundary test), and
// conflating their gates would hide this banner exactly when the plugin
// still matters (e.g. before TSE provisioning is ever configured).
func missingMandatedTaxPlugin(ctx context.Context, d *common.Deps, country string) (bool, error) {
	if _, mandated := countryTaxLocale[strings.ToUpper(strings.TrimSpace(country))]; !mandated {
		return false, nil
	}
	repo := data.NewPluginRepo(d.Db)
	_, _, found, err := repo.ActiveHookOwner(ctx, nil, taxRateAskEvent, "")
	if err != nil {
		return false, err
	}
	broken, err := repo.HasBrokenActivePluginForEvent(ctx, taxRateAskEvent)
	if err != nil {
		return false, err
	}
	return !found || broken, nil
}
