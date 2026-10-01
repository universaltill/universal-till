package pages

import (
	"context"
	"sync"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// engineConfigMu serializes whole applyEngineConfig batches (ut-docs#3085).
// Package-level rather than a Deps field so common.Deps stays copyable.
var engineConfigMu sync.Mutex

// applyEngineConfig pushes the config the saved settings call for to the
// cashier Engine, the separate KioskEngine (ut-docs#449) and every live
// table-QR session (ADR-0103), in that order, as one atomic batch. Without
// engineConfigMu two concurrent settings saves could interleave and leave
// the engines on different configs. pos.SessionBasketManager.SetConfig's
// setConfigMu (ut-docs#2488) is the per-manager analogue of this
// cross-engine lock. KioskEngine and SelfOrderSessions may be nil
// (SelfOrderSessions.SetConfig is nil-safe).
//
// The config is read from the settings store inside the lock, never passed
// in (ut-docs#3245): a caller that built it from state read before the lock
// could apply an older save's rate after a newer one, and every engine
// would then agree on a rate the DB no longer holds. Call it after the save.
// On an additional till whose best-effort local mirror of a forwarded save
// failed, that means the engines stay on the old rate until the next sync
// pull lands the change (settings_sync_proxy.go saveShopSettings).
// A batch whose config both engines already hold is skipped, so a re-derive
// that changed nothing does not recompute live baskets.
func applyEngineConfig(ctx context.Context, d *common.Deps) {
	engineConfigMu.Lock()
	defer engineConfigMu.Unlock()
	// WithoutCancel: a client that disconnects right after its save must not
	// cancel the read. A read that fails anyway leaves the engines on their
	// last good config rather than LoadState's defaults; the next save or
	// sync pull re-applies.
	st, err := common.LoadStateChecked(context.WithoutCancel(ctx), d.Settings, d.Cfg)
	if err != nil {
		logging.L().Errorf("engine config: settings not re-applied to the engines: %v", err)
		return
	}
	cfg := engineConfigFor(st)
	if d.Engine != nil && d.Engine.Config() == cfg &&
		(d.KioskEngine == nil || d.KioskEngine.Config() == cfg) {
		return
	}
	if d.Engine != nil {
		d.Engine.SetConfig(cfg)
	}
	if d.KioskEngine != nil {
		d.KioskEngine.SetConfig(cfg)
	}
	d.SelfOrderSessions.SetConfig(cfg)
}

// engineConfigFor is the pos.Config every engine runs for st.
func engineConfigFor(st common.RuntimeState) pos.Config {
	return pos.Config{
		TaxInclusive:                 st.TaxInclusive,
		TaxRateBasisPoints:           st.TaxRateBP,
		ServiceChargeRateBasisPoints: common.EffectiveServiceChargeRateBP(st),
		ChargesForbidden:             common.ServiceChargeForbidden(st.Country),
	}
}
