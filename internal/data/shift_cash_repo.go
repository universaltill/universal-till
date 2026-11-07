package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/universaltill/universal-till/internal/money"
)

// ShiftCash is one shift's drawer breakdown under the cash-tip rule
// (ut-docs#5801): Opening + CashSales + TipsHeldOut + Adjustments == Expected.
type ShiftCash struct {
	Opening     money.Money
	CashSales   money.Money // cash amount - change_given - tip_amount
	TipsHeldOut money.Money // cash tip_amount, same window as CashSales
	Adjustments money.Money // SumShiftAdjustments (pay-ins +, pay-outs/skims -)
	Expected    money.Money // the sum; == shifts.expected_cash at close
}

// ShiftCashBreakdown reads one shift's drawer breakdown (open shift: up to
// now; closed: up to closed_at). Expected counts cash tips (they sit in the
// drawer); CashSales never does. The X report (#5796) uses this for its Cash
// block.
func (r *POSRepo) ShiftCashBreakdown(ctx context.Context, shiftID string, openingCash int64) (ShiftCash, error) {
	if shiftID == "" {
		return ShiftCash{}, errors.New("shift_id required")
	}
	var openedAt, closedAt sql.NullString
	var registerID string
	if err := r.db.QueryRowContext(ctx, `
SELECT register_id, opened_at, closed_at
FROM shifts
WHERE id = ?
`, shiftID).Scan(&registerID, &openedAt, &closedAt); err != nil {
		return ShiftCash{}, fmt.Errorf("query shift: %w", err)
	}
	tendered, err := r.SumCashPaymentsForShift(ctx, registerID, openedAt, closedAt)
	if err != nil {
		return ShiftCash{}, err
	}
	tips, err := r.SumCashTipsForShift(ctx, registerID, openedAt, closedAt)
	if err != nil {
		return ShiftCash{}, err
	}
	adj, err := r.SumShiftAdjustments(ctx, shiftID)
	if err != nil {
		return ShiftCash{}, err
	}
	b := ShiftCash{
		Opening:     money.FromMinor(openingCash),
		CashSales:   money.FromMinor(tendered).Sub(money.FromMinor(tips)),
		TipsHeldOut: money.FromMinor(tips),
		Adjustments: money.FromMinor(adj),
	}
	b.Expected = b.Opening.Add(b.CashSales).Add(b.TipsHeldOut).Add(b.Adjustments)
	return b, nil
}
