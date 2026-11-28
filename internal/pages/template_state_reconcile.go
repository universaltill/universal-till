package pages

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// templateStateInterval is how often StartTemplateStateReconcile checks that
// the template layout matches the shop's type — the same 30 s cadence as the
// replica's admin pull, so a pulled shop type lands within one more tick.
const templateStateInterval = 30 * time.Second

// StartTemplateStateReconcile keeps the shop-type template layout in step
// with shop.type and the apply-once ledger while the till runs (ut-docs#2793,
// ADR-0190). The two local shop-type handlers rebuild it themselves, but a
// type or ledger that arrives any other way — an additional till's admin pull
// from the main till, or a cloud set_setting directive — only changes the
// settings rows, so the menu would stay on the old layout until a restart.
// The check is one settings read per tick; the rebuild only touches the
// in-memory amendment lists (no plugin reload, no write, nothing mid-sale to
// protect). Same Start* shape as StartSelfOrderSessionSweep: wg.Add before the
// goroutine, wg.Done on every exit path.
func StartTemplateStateReconcile(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.templateStateReconcile")
		defer wg.Done()
		ticker := time.NewTicker(templateStateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// The shop type of a joined till can arrive with an admin pull.
				seedJoinedTillTemplate(ctx, d)
				if d.ReloadTemplateIfChanged(ctx) {
					logging.L().Infof("shop template: layout now follows the shop's current type")
				}
			}
		}
	}()
}
