package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// X report data layer (ut-docs#5796, design ut-docs reference/design-5780-x-report.md).

// XReport is one shift's register figures. No JSON tags: the handler owns the DTO.
type XReport struct {
	Shift        ShiftSummary
	RegisterName string // registers.name, else the raw register id
	CashierName  string // display_name, else username, else the raw cashier id
	GeneratedAt  string // now (RFC 3339); a stamp only, never a window bound

	SalesCount          int
	SalesGross          money.Money
	ReturnsCount        int
	ReturnsTotal        money.Money // positive
	Net                 money.Money // SalesGross - ReturnsTotal
	Tax                 money.Money // sale tax - return tax
	VoidsCount          int
	VoidsTotal          money.Money
	ClearedBasketsCount int
	ClearedBasketsTotal money.Money

	Methods []XMethodLine // nil when no payments; ordered by name, then id

	Cash XCash

	NoSale          int
	NoSaleOperators []NoSaleOperator

	FirstReceipt, LastReceipt string // '' when no completed row in the window
}

// XMethodLine is one payment method's takings in the window.
type XMethodLine struct {
	MethodID, Name, Type string
	In, Refunded, Net    money.Money // In/Refunded as tenderInSQL/tenderOutSQL; Net = In - Refunded
	Tips                 money.Money // all tip_amount rows of the method (as SumCashTipsForShift)
}

// XCash is the drawer breakdown: Opening + CashSales + TipsHeldOut +
// AdjustmentsTotal == Expected (ShiftCashBreakdown, ut-docs#5801).
type XCash struct {
	Opening          money.Money
	CashSales        money.Money
	TipsHeldOut      money.Money
	AdjustmentsTotal money.Money
	Adjustments      []XAdjustment
	Expected         money.Money
}

// XAdjustment is one cash adjustment audit row of the shift.
type XAdjustment struct {
	Type   string // payout|adjustment|skim, as stored
	Reason string
	Amount money.Money // signed, as stored
	At     string      // audit_log.created_at as stored
	// AtClose: the skim pos.CloseShift stamps at_close:true when it closes
	// the shift (same predicate as CashReconciliationForLocalDay, #5841).
	AtClose bool
}

// getShiftRow reads one shift plus its raw opened_at/closed_at (the window
// bounds). Unknown or empty id: an error wrapping sql.ErrNoRows.
func (r *POSRepo) getShiftRow(ctx context.Context, shiftID string) (ShiftSummary, sql.NullString, sql.NullString, error) {
	if shiftID == "" {
		return ShiftSummary{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("get shift: %w", sql.ErrNoRows)
	}
	var s ShiftSummary
	var opened, closedAt, note sql.NullString
	var closing, expected sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, note
FROM shifts WHERE id = ?`, shiftID).Scan(
		&s.ID, &s.RegisterID, &s.CashierID, &opened, &closedAt, &s.OpeningCash, &closing, &expected, &note)
	if err != nil {
		return ShiftSummary{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("get shift: %w", err)
	}
	s.OpenedAt = opened.String
	s.ClosedAt = closedAt.String
	s.Open = !closedAt.Valid || closedAt.String == ""
	s.ClosingCash = closing.Int64
	s.Expected = expected.Int64
	s.Note = note.String
	if !s.Open {
		s.Variance = s.ClosingCash - s.Expected
	}
	return s, opened, closedAt, nil
}

// GetShift reads one shift by id, open or closed. Unknown/empty id: error
// wrapping sql.ErrNoRows.
func (r *POSRepo) GetShift(ctx context.Context, shiftID string) (ShiftSummary, error) {
	s, _, _, err := r.getShiftRow(ctx, shiftID)
	return s, err
}

// xTotalsQuery mirrors dateRangeSummaryInstant's select list over one
// register's completed rows in the shift window (D8: that function is not
// called, its expressions are copied).
func xTotalsQuery(reg string, o, c sql.NullString) (string, []any) {
	win, wargs := shiftWindow("s.completed_at", o, c)
	return `
SELECT
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN s.total END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN s.total END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale' THEN s.tax_total ELSE -s.tax_total END), 0),
  COALESCE(MIN(s.receipt_no), ''), COALESCE(MAX(s.receipt_no), '')
FROM sales s
WHERE s.status = 'completed' AND s.register_id = ? AND ` + win, append([]any{reg}, wargs...)
}

// xVoidsQuery: the unary + on register_id stops SQLite choosing the
// (register_id, datetime(completed_at)) index, which cannot bound a
// voided_at window and would walk every sale of the register; the status
// index reaches only voided rows (review M1, #5796).
func xVoidsQuery(reg string, o, c sql.NullString) (string, []any) {
	win, wargs := shiftWindow("COALESCE(s.voided_at, s.created_at)", o, c)
	return `
SELECT COUNT(*), COALESCE(SUM(s.total), 0)
FROM sales s
WHERE s.status = 'voided' AND +s.register_id = ? AND ` + win, append([]any{reg}, wargs...)
}

func xClearedQuery(reg string, o, c sql.NullString) (string, []any) {
	win, wargs := shiftWindow("created_at", o, c)
	return `
SELECT COUNT(*), COALESCE(SUM(total_minor), 0)
FROM cleared_baskets
WHERE register_id = ? AND ` + win, append([]any{reg}, wargs...)
}

func xMethodsQuery(reg string, o, c sql.NullString) (string, []any) {
	win, wargs := shiftWindow("s.completed_at", o, c)
	return `
SELECT pm.id, pm.name, pm.type, ` + tenderInSQL + `, ` + tenderOutSQL + `, COALESCE(SUM(p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
JOIN payment_methods pm ON pm.id = p.method_id
WHERE s.status = 'completed' AND s.register_id = ? AND ` + win + `
GROUP BY pm.id, pm.name, pm.type
ORDER BY pm.name, pm.id`, append([]any{reg}, wargs...)
}

// ShiftXReport reads one shift's register figures (ut-docs#5796). Read-only:
// no INSERT/UPDATE/DELETE, no audit row. Window (D6): shiftWindow on the
// shift's register, an open shift unbounded above, the same for every block
// (so Sales, Methods and Cash reconcile); now only stamps GeneratedAt.
// Not wrapped in one read transaction (as ShiftCashBreakdown): a sale
// committing mid-read can leave one report off by that sale between blocks;
// the next report is right. A failure in any block but no-sale is returned.
func (r *POSRepo) ShiftXReport(ctx context.Context, shiftID string, now time.Time) (XReport, error) {
	shift, opened, closed, err := r.getShiftRow(ctx, shiftID)
	if err != nil {
		return XReport{}, err
	}
	rep := XReport{Shift: shift}
	reg := shift.RegisterID

	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT NULLIF(name,'') FROM registers WHERE id = ?), ?)`, reg, reg).Scan(&rep.RegisterName); err != nil {
		return XReport{}, fmt.Errorf("x report register name: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT COALESCE(NULLIF(display_name,''), NULLIF(username,'')) FROM users WHERE id = ?), ?)`, shift.CashierID, shift.CashierID).Scan(&rep.CashierName); err != nil {
		return XReport{}, fmt.Errorf("x report cashier name: %w", err)
	}

	var gross, refunds, tax, voids, cleared int64
	q, args := xTotalsQuery(reg, opened, closed)
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&rep.SalesCount, &gross, &rep.ReturnsCount, &refunds, &tax, &rep.FirstReceipt, &rep.LastReceipt); err != nil {
		return XReport{}, fmt.Errorf("x report totals: %w", err)
	}
	rep.SalesGross, rep.ReturnsTotal, rep.Tax = money.FromMinor(gross), money.FromMinor(refunds), money.FromMinor(tax)
	rep.Net = rep.SalesGross.Sub(rep.ReturnsTotal)

	q, args = xVoidsQuery(reg, opened, closed)
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&rep.VoidsCount, &voids); err != nil {
		return XReport{}, fmt.Errorf("x report voids: %w", err)
	}
	rep.VoidsTotal = money.FromMinor(voids)

	q, args = xClearedQuery(reg, opened, closed)
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&rep.ClearedBasketsCount, &cleared); err != nil {
		return XReport{}, fmt.Errorf("x report cleared baskets: %w", err)
	}
	rep.ClearedBasketsTotal = money.FromMinor(cleared)

	q, args = xMethodsQuery(reg, opened, closed)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return XReport{}, fmt.Errorf("x report methods: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m XMethodLine
		var in, out, tips int64
		if err := rows.Scan(&m.MethodID, &m.Name, &m.Type, &in, &out, &tips); err != nil {
			return XReport{}, fmt.Errorf("x report methods scan: %w", err)
		}
		m.In, m.Refunded, m.Tips = money.FromMinor(in), money.FromMinor(out), money.FromMinor(tips)
		m.Net = m.In.Sub(m.Refunded)
		rep.Methods = append(rep.Methods, m)
	}
	if err := rows.Err(); err != nil {
		return XReport{}, fmt.Errorf("x report methods: %w", err)
	}
	_ = rows.Close()

	b, err := r.ShiftCashBreakdown(ctx, shiftID, shift.OpeningCash)
	if err != nil {
		return XReport{}, fmt.Errorf("x report cash: %w", err)
	}
	rep.Cash = XCash{Opening: b.Opening, CashSales: b.CashSales, TipsHeldOut: b.TipsHeldOut, AdjustmentsTotal: b.Adjustments, Expected: b.Expected}

	adj, err := r.db.QueryContext(ctx, `
SELECT COALESCE(json_extract(data_json,'$.type'),''), COALESCE(json_extract(data_json,'$.reason'),''),
       CAST(COALESCE(json_extract(data_json,'$.amount'),0) AS INTEGER), created_at,
       COALESCE(json_extract(data_json,'$.type'),'') = 'skim' AND COALESCE(json_extract(data_json,'$.at_close') = 1, 0)
FROM audit_log
WHERE entity_type = 'shift' AND entity_id = ? AND action = 'cash_adjustment'
ORDER BY datetime(created_at), rowid`, shiftID)
	if err != nil {
		return XReport{}, fmt.Errorf("x report adjustments: %w", err)
	}
	defer adj.Close()
	for adj.Next() {
		var a XAdjustment
		var amt int64
		if err := adj.Scan(&a.Type, &a.Reason, &amt, &a.At, &a.AtClose); err != nil {
			return XReport{}, fmt.Errorf("x report adjustments scan: %w", err)
		}
		a.Amount = money.FromMinor(amt)
		rep.Cash.Adjustments = append(rep.Cash.Adjustments, a)
	}
	if err := adj.Err(); err != nil {
		return XReport{}, fmt.Errorf("x report adjustments: %w", err)
	}

	// No-sale opens are informational: best-effort, as the EOD.
	if n, ops, nerr := r.NoSaleForShift(ctx, shiftID); nerr == nil {
		rep.NoSale, rep.NoSaleOperators = n, ops
	}

	rep.GeneratedAt = now.UTC().Format(time.RFC3339)
	return rep, nil
}

// OpenShift is one register's open shift with display names, for the /shifts
// X picker and Current card (ut-docs#5797). Names use ShiftXReport's fallbacks.
type OpenShift struct {
	ShiftID, RegisterID, RegisterName, CashierID, CashierName, OpenedAt string
}

// ListOpenShifts returns the newest open shift (closed_at IS NULL) of each
// register, ordered by register name then id. Read-only. A deactivated
// register's open shift is still listed: its drawer still holds cash.
func (r *POSRepo) ListOpenShifts(ctx context.Context) ([]OpenShift, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT s.id, s.register_id, COALESCE(NULLIF(rg.name,''), s.register_id),
       COALESCE(s.cashier_id,''),
       COALESCE(NULLIF(u.display_name,''), NULLIF(u.username,''), COALESCE(s.cashier_id,'')),
       s.opened_at
FROM shifts s
LEFT JOIN registers rg ON rg.id = s.register_id
LEFT JOIN users u      ON u.id  = s.cashier_id
WHERE s.closed_at IS NULL
ORDER BY COALESCE(NULLIF(rg.name,''), s.register_id), s.register_id, s.opened_at DESC, s.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list open shifts: %w", err)
	}
	defer rows.Close()
	var out []OpenShift
	seen := map[string]bool{}
	for rows.Next() {
		var o OpenShift
		if err := rows.Scan(&o.ShiftID, &o.RegisterID, &o.RegisterName, &o.CashierID, &o.CashierName, &o.OpenedAt); err != nil {
			return nil, fmt.Errorf("list open shifts: %w", err)
		}
		if seen[o.RegisterID] {
			continue // newest first: keep the first row per register
		}
		seen[o.RegisterID] = true
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open shifts: %w", err)
	}
	return out, nil
}

// ErrShiftNotClosed: ShiftCloseSlip was asked for a shift that is still open.
var ErrShiftNotClosed = errors.New("shift not closed")

// ShiftCloseSlip is one closed shift's X figures with the at-close skim moved
// out of the Cash block into the close block (ut-docs#5841).
type ShiftCloseSlip struct {
	X XReport // Cash.Adjustments/AdjustmentsTotal/Expected exclude the at-close skim
	// Stored shifts-row figures, never recomputed: Variance = Counted -
	// Expected; Skim = Counted - NewFloat (0 on a legacy close).
	Expected, Counted, Variance, Skim, NewFloat money.Money
}

// ShiftCloseSlip reads a closed shift for the "Shift close" slip. Read-only.
// Unknown or empty id: an error wrapping sql.ErrNoRows; an open shift:
// ErrShiftNotClosed.
func (r *POSRepo) ShiftCloseSlip(ctx context.Context, shiftID string, now time.Time) (ShiftCloseSlip, error) {
	x, err := r.ShiftXReport(ctx, shiftID, now)
	if err != nil {
		return ShiftCloseSlip{}, err
	}
	if x.Shift.Open {
		return ShiftCloseSlip{}, ErrShiftNotClosed
	}
	var counted, expected, newFloat int64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(closing_cash,0), COALESCE(expected_cash,0), COALESCE(new_float, closing_cash, 0) FROM shifts WHERE id = ?`, shiftID).
		Scan(&counted, &expected, &newFloat); err != nil {
		return ShiftCloseSlip{}, fmt.Errorf("shift close slip: %w", err)
	}
	s := ShiftCloseSlip{
		X: x, Expected: money.FromMinor(expected), Counted: money.FromMinor(counted),
		NewFloat: money.FromMinor(newFloat),
	}
	s.Variance = s.Counted.Sub(s.Expected)
	s.Skim = s.Counted.Sub(s.NewFloat)

	kept := make([]XAdjustment, 0, len(x.Cash.Adjustments))
	total := x.Cash.AdjustmentsTotal
	for _, a := range x.Cash.Adjustments {
		if a.AtClose {
			total = total.Sub(a.Amount)
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		kept = nil
	}
	c := &s.X.Cash
	c.Adjustments, c.AdjustmentsTotal = kept, total
	c.Expected = c.Opening.Add(c.CashSales).Add(c.TipsHeldOut).Add(total)
	return s, nil
}
