package pages

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// selfOrderSessionMaxIdle is how long a table-bound self-order session may
// go without any request before the sweep evicts it (ADR-0103 Decision 5):
// generous enough that a guest mid-meal (menu open, phone face-down through
// a course) is never evicted, still bounding server-side memory over a
// trading day when a guest walks away without checking out. Every request
// that resolves the session refreshes its clock (SessionBasketManager.Get),
// so an actively used session is never a candidate.
const selfOrderSessionMaxIdle = 2 * time.Hour

// selfOrderSessionSweepInterval is the sweep cadence. Coarse on purpose: the
// only cost of a late eviction is a little memory, and the sweep itself
// walks every live session under the manager's lock.
const selfOrderSessionSweepInterval = 10 * time.Minute

// StartSelfOrderSessionSweep runs the idle-session sweep until ctx ends —
// same shape as every other Start* background loop here (runSyncLoop,
// StartHeldOrderClaimReaffirm): wg.Add before the goroutine starts, wg.Done
// on every exit path, so app.Run's shutdown drain can prove it stopped. A
// Deps without a manager (bare test harnesses) registers nothing.
func StartSelfOrderSessionSweep(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	if d.SelfOrderSessions == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(selfOrderSessionSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				selfOrderSessionSweepTick(d, now)
			}
		}
	}()
}

// selfOrderSessionSweepTick is one tick of StartSelfOrderSessionSweep,
// extracted (like heldOrderClaimReaffirmTick) so tests drive it with an
// explicit now instead of waiting on the real ticker.
func selfOrderSessionSweepTick(d *common.Deps, now time.Time) {
	if n := d.SelfOrderSessions.Sweep(selfOrderSessionMaxIdle, now); n > 0 {
		logging.L().Infof("self-order: evicted %d table session(s) idle for more than %s, %d still live (ut-docs#2261)", n, selfOrderSessionMaxIdle, d.SelfOrderSessions.Len())
	}
}
