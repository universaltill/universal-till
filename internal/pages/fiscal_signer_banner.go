package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// missingFiscalSigner reports whether this shop is declared system-of-record
// in a hard-gated market (today: DE, fiscal.RequiresHardGate) with no active,
// functioning plugin currently subscribed to fiscal.sign.ask. This is a
// Settings-page visibility signal ONLY (a real, cloud-provisioned TSE
// credential can be configured with zero plugins installed to actually sign
// anything, leaving sales completing unsigned via proceed-and-declare with
// nothing in the UI flagging it persistently) — read-only: it never touches
// EvaluateGate, KeyTSEFailingSince, or override state (ADR-0048's gate/
// override semantics are untouched by this card).
//
// country is passed in rather than read from settings here, so the banner
// always agrees with the gate on which country the shop is in — both read
// it via d.CurrentState().Country (see pos_api.go's evaluateFiscalGate).
func missingFiscalSigner(ctx context.Context, d *common.Deps, country string) (bool, error) {
	if !fiscal.RequiresHardGate(country) {
		return false, nil
	}
	sor, err := fiscal.IsSystemOfRecord(ctx, d.Settings)
	if err != nil {
		return false, err
	}
	if !sor {
		return false, nil
	}
	repo := data.NewPluginRepo(d.Db)
	_, _, found, err := repo.ActiveHookOwner(ctx, nil, fiscalSignAskEvent, "")
	if err != nil {
		return false, err
	}
	// A signer plugin can be installed AND active yet not actually running:
	// when its wasm module fails to load, WasmRuntime.Sync flips
	// install_state='broken' while deliberately leaving is_active untouched
	// (ut-docs#368), so ActiveHookOwner alone can't tell a working signer
	// from a broken one that still holds the hook. HasBrokenActivePluginForEvent
	// is the same check tax_hook.go already uses for this exact class of
	// problem.
	broken, err := repo.HasBrokenActivePluginForEvent(ctx, fiscalSignAskEvent)
	if err != nil {
		return false, err
	}
	return !found || broken, nil
}
