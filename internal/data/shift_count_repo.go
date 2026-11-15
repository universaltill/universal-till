package data

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/universaltill/universal-till/internal/money"
)

// ShiftCount is a closed shift's stored denomination count (ut-docs#1043).
type ShiftCount struct {
	ShiftID  string
	OpenedAt string
	ClosedAt string
	Counted  money.Money // shifts.closing_cash
	Protocol string      // raw shifts.count_protocol; "" when NULL
}

// ShiftCount reads one shift's count protocol. Read-only. An unknown id
// wraps sql.ErrNoRows; a shift that is still open is ErrShiftNotClosed.
func (r *POSRepo) ShiftCount(ctx context.Context, shiftID string) (ShiftCount, error) {
	var c ShiftCount
	var closedAt, protocol sql.NullString
	var counted sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, opened_at, closed_at, closing_cash, count_protocol FROM shifts WHERE id = ?`, shiftID).
		Scan(&c.ShiftID, &c.OpenedAt, &closedAt, &counted, &protocol)
	if err != nil {
		return ShiftCount{}, fmt.Errorf("shift count: %w", err)
	}
	if !closedAt.Valid {
		return ShiftCount{}, ErrShiftNotClosed
	}
	c.ClosedAt = closedAt.String
	c.Counted = money.FromMinor(counted.Int64)
	c.Protocol = protocol.String
	return c, nil
}
