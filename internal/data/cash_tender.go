package data

import (
	"context"
	"fmt"
)

// Payment-method types core reads (payment_methods.type). Plugin rows carry
// their manifest's method_type verbatim; these are the only values core
// gives meaning to.
const (
	paymentTypeCash    = "cash"    // drawer cash: shift close, Z, aggregate
	paymentTypeUnknown = "unknown" // placeholder made by EnsurePaymentMethod (ut-docs#5804)
)

// cashMethodIDs returns the id of every payment_methods row of type 'cash',
// active or not — the one definition of a cash payment (ut-docs#5804).
// shiftCashWindow's `pm.type = 'cash'` join applies the same rule in SQL.
func (r *POSRepo) cashMethodIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM payment_methods WHERE type = ?`, paymentTypeCash)
	if err != nil {
		return nil, fmt.Errorf("list cash payment methods: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cash payment method: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// applyCashSales fills rc.CashSales / rc.TipsHeldOut from the report's own
// per-method breakdown: Σ(In−Out) and Σ tip over every cash-type method,
// CashSales net of the tips (ut-docs#1046). On error the caller omits the
// section rather than print a wrong drawer figure.
func (r *POSRepo) applyCashSales(ctx context.Context, rc *CashReconciliation, methods []EODMethod, tips []EODTip) error {
	cash, err := r.cashMethodIDs(ctx)
	if err != nil {
		return err
	}
	rc.CashSales, rc.TipsHeldOut = 0, 0
	for _, m := range methods {
		if cash[m.Method] {
			rc.CashSales += m.In - m.Out
		}
	}
	for _, tp := range tips {
		if cash[tp.Method] {
			rc.TipsHeldOut += tp.Amount
		}
	}
	rc.CashSales -= rc.TipsHeldOut
	return nil
}
