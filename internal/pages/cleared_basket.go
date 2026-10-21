package pages

import (
	"context"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// recordClearedBasket (ut-docs#1122) records a live cashier basket cleared
// with New sale before any tender and without a re-park. snap is what
// Engine.ResetAndSnapshot returned. An empty basket records nothing. Best-effort:
// offline-first, so a failed insert is logged and never blocks the reset.
func recordClearedBasket(ctx context.Context, d *common.Deps, r *http.Request, snap pos.BasketSnapshot) {
	lines := len(snap.Lines) + len(snap.AddByHand)
	if lines == 0 {
		return
	}
	// The basket is already gone; a client going away must not cancel the
	// record.
	ctx = context.WithoutCancel(ctx)
	repo := data.NewPOSRepo(d.Db)
	// The register a sale rung up on this device right now would record —
	// the no-sale path's rule (no_sale_api.go).
	registerID := tillRegisterIDBestEffort(ctx, d)
	if registerID == "" {
		if regID, err := repo.EnsureRegister(ctx); err == nil {
			registerID = regID
		}
	}
	if _, err := repo.InsertClearedBasket(ctx, data.ClearedBasket{
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		RegisterID: registerID,
		ActorID:    getSessionUserID(r),
		LineCount:  lines,
		Total:      snap.Total,
	}); err != nil {
		logging.L().Errorf("new sale: record cleared basket: %v", err)
	}
}
