package pages

import (
	"context"
	"errors"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// tillRegisterIDBestEffort resolves THIS till's own register identity for a
// stock-mutating write that has no per-request register of its own (kiosk
// checkout, refund, catalog import, and a cashier tender whose request
// names none — ut-docs#2067), so pos.ResolveStockLocationID can honour the
// register's assigned stock location.
//
// Best-effort, same tolerance as the Settings and Shifts pages' own
// till-register pickers: the ambiguous case (two or more active registers
// and nothing persisted, pos.ErrRegisterIdentityAmbiguous) returns "" and
// any other resolution error is logged and returns "" too. "" makes the
// caller fall back to exactly today's behaviour (Main), which is deliberate
// — unlike a shift payout (shifts_api.go, which refuses with 409 on
// ambiguity), a sale/refund/import must NOT start refusing on a
// two-register shop that never picked its till identity: that shop was
// selling from Main yesterday and keeps selling from Main today.
func tillRegisterIDBestEffort(ctx context.Context, d *common.Deps) string {
	if d.Settings == nil {
		return ""
	}
	resolved, err := pos.ResolveTillRegisterID(ctx, d.Db, d.Settings)
	if err == nil {
		return resolved
	}
	if !errors.Is(err, pos.ErrRegisterIdentityAmbiguous) {
		logging.L().Errorf("resolve till register: %v", err)
	}
	return ""
}
