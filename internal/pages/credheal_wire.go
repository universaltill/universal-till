package pages

import (
	"context"
	"sync"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/offsitebackup"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// StartCredHeal wires and starts the till's self-heal engine (ADR-0222): when
// the cloud stops accepting this till's credential it proves its approval key
// and gets a new one by itself, in the background. Headless: no request
// goroutine, no sale-path read. Joined by wg like the other loops.
func StartCredHeal(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	if d == nil || d.Cfg == nil || d.Settings == nil {
		return
	}
	credheal.SetHooks(credheal.Hooks{
		Probe: func(ctx context.Context) credheal.ProbeResult { return cloudsync.ProbeCredential(ctx, d.Cfg) },
		OnHealed: func() {
			cloudsync.ClearAuthLockout() // the 3-in-a-row lock-out ended with the old credential
			if d.Db != nil {
				offsitebackup.Shared(d.Cfg, d.Db).OnHealed(context.Background()) // refusal wait cleared, an upload starts
			}
			requestOperatorCheckin(d, operatorCheckinWindow) // also lets the cloud link dial again
		},
	})
	credheal.Start(ctx, credheal.Deps{Cfg: d.Cfg, KV: d.Settings}, wg)
}
