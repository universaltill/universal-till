package pages

import (
	"sync"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// engineConfigMu serializes whole applyEngineConfig batches (ut-docs#3085).
// Package-level rather than a Deps field so common.Deps stays copyable.
var engineConfigMu sync.Mutex

// applyEngineConfig pushes cfg to the cashier Engine, the separate
// KioskEngine (ut-docs#449) and every live table-QR session (ADR-0103), in
// that order, as one atomic batch. Without engineConfigMu two concurrent
// settings saves could interleave and leave the engines on different
// configs. pos.SessionBasketManager.SetConfig's setConfigMu (ut-docs#2488)
// is the per-manager analogue of this cross-engine lock. KioskEngine and
// SelfOrderSessions may be nil (SelfOrderSessions.SetConfig is nil-safe).
func applyEngineConfig(d *common.Deps, cfg pos.Config) {
	engineConfigMu.Lock()
	defer engineConfigMu.Unlock()
	if d.Engine != nil {
		d.Engine.SetConfig(cfg)
	}
	if d.KioskEngine != nil {
		d.KioskEngine.SetConfig(cfg)
	}
	d.SelfOrderSessions.SetConfig(cfg)
}
