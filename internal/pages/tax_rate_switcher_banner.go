package pages

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// taxRateSwitchMandatedCountries is the country → "a tax.rate.ask plugin
// is expected to answer for this country" table (ADR-0068). Small,
// explicit and reviewable by design — same convention as
// setupBasePlugins and ADR-0067's mandated-tax-plugin list — not
// inferred from any marketplace/country metadata, so adding a country
// here is a deliberate, reviewed line.
var taxRateSwitchMandatedCountries = map[string]bool{
	"DE": true,
}

// missingTaxRateSwitcher reports whether this shop's country expects a
// tax.rate.ask answerer (ADR-0068) and none is currently active and
// working. Unlike missingFiscalSigner, this is NOT gated on
// fiscal.system_of_record — VAT-rate correctness (which order-type gets
// which rate) is required regardless of fiscal-signing status; a shop
// that has declined/deferred the fiscal plugin (ADR-0025 Decision 4)
// still needs correct rates, per ADR-0025 Decision 4's own promise.
//
// This is a Settings-page visibility signal ONLY — read-only, same as
// missingFiscalSigner: it never touches tax_hook.go's actual switching
// behaviour, setupCountries, or the ut-docs#368 fail-closed gate that
// blocks checkout on a broken registered plugin. See ADR-0068.
func missingTaxRateSwitcher(ctx context.Context, d *common.Deps, country string) (bool, error) {
	if !taxRateSwitchMandatedCountries[strings.ToUpper(country)] {
		return false, nil
	}
	repo := data.NewPluginRepo(d.Db)
	_, _, found, err := repo.ActiveHookOwner(ctx, nil, taxRateAskEvent, "")
	if err != nil {
		return false, err
	}
	// Same ut-docs#368 shape as missingFiscalSigner / tax_hook.go's own
	// taxAuthorityBroken: an installed plugin can be is_active=1 on
	// itself and its hook row yet have a broken wasm module
	// (install_state='broken', left untouched by WasmRuntime.Sync) — a
	// plugin that ActiveHookOwner alone would read as "found" but that
	// cannot actually answer.
	//
	// Known, accepted gap (review finding, ut-docs#1191): unlike
	// fiscal.sign.ask (an ADR-0041 EXCLUSIVE hook — validateExclusiveHookOwnership
	// guarantees at most one owner), tax.rate.ask is not exclusive. If two
	// plugins ever hold it and only one is broken, this can fire even
	// though tax_hook.go's AskTaxRateBP is still answering correctly via
	// the working one (it only checks taxAuthorityBroken when NO answer
	// comes back, not whenever any registered plugin is broken). Accepted
	// for now: today only one tax.rate.ask plugin per mandated country
	// exists in practice; revisit if that changes.
	broken, err := repo.HasBrokenActivePluginForEvent(ctx, taxRateAskEvent)
	if err != nil {
		return false, err
	}
	return !found || broken, nil
}
