package app

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// legReconcileCap bounds the whole boot pass over the payment-leg journal.
const legReconcileCap = 5 * time.Minute

// startPaymentLegReconcile: ut-docs#6653 (ADR-0136 Decision 9.5) — after boot,
// in the background, return a card payment the last run captured but never
// turned into a sale, and warn about legs it left in doubt. It never blocks
// boot or checkout and only touches rows written before startedAt (this
// process's start). A reversal is a plugin event, so it starts after the
// plugin runtime has loaded its subscribers (plugins.Init, then pagesInit).
// Joined by wg so shutdown waits for it, bounded by the same drain as the
// other background work.
func startPaymentLegReconcile(ctx context.Context, wg *sync.WaitGroup, d *common.Deps, startedAt time.Time, log bootLogger) {
	if d == nil || d.Db == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("app.legReconcileBoot")
		defer wg.Done()
		passCtx, cancel := context.WithTimeout(ctx, legReconcileCap)
		defer cancel()
		pages.ReconcilePaymentLegs(passCtx, d, startedAt)
	}()
}
