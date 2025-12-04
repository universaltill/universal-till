package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
)

// ShiftInput captures data for opening a shift
type ShiftInput struct {
	ShiftID     string
	RegisterID  string
	CashierID   string
	OpeningCash int64 // minor units
}

// ShiftCloseInput captures data for closing a shift
type ShiftCloseInput struct {
	ShiftID     string
	ClosingCash int64 // minor units
	Note        string
}

// CashAdjustmentInput captures payouts or adjustments affecting expected cash
type CashAdjustmentInput struct {
	ShiftID string
	Type    string // payout|adjustment
	Amount  int64  // minor units, negative for payout
	Reason  string
	ActorID string
}

// OpenShift creates a new shift record with opening cash and audit entry
func OpenShift(ctx context.Context, sqlDB *sql.DB, in ShiftInput) (string, error) {
	if in.RegisterID == "" {
		return "", errors.New("register_id required")
	}
	if in.CashierID == "" {
		return "", errors.New("cashier_id required")
	}
	if in.OpeningCash < 0 {
		return "", errors.New("opening_cash must be >= 0")
	}

	// Check if there's already an open shift for this register
	var existingID string
	err := sqlDB.QueryRowContext(ctx, `
SELECT id FROM shifts 
WHERE register_id = ? AND closed_at IS NULL
LIMIT 1`, in.RegisterID).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check existing shift: %w", err)
	}
	if existingID != "" {
		return "", fmt.Errorf("register %s already has an open shift: %s", in.RegisterID, existingID)
	}

	shiftID := in.ShiftID
	if shiftID == "" {
		shiftID = uuid.NewString()
	}

	err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO shifts (id, register_id, cashier_id, opened_at, opening_cash)
VALUES (?, ?, ?, ?, ?)
`, shiftID, in.RegisterID, in.CashierID, now, in.OpeningCash); err != nil {
			return fmt.Errorf("insert shift: %w", err)
		}

		// Audit log
		payload := map[string]any{
			"register_id":  in.RegisterID,
			"cashier_id":   in.CashierID,
			"opening_cash": in.OpeningCash,
		}
		payloadJSON, _ := json.Marshal(payload)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'shift', ?, 'open', ?, ?)
`, uuid.NewString(), in.CashierID, shiftID, string(payloadJSON), now); err != nil {
			return fmt.Errorf("insert audit: %w", err)
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	return shiftID, nil
}

// CloseShift closes an existing shift, calculates expected cash, and persists closing cash
func CloseShift(ctx context.Context, sqlDB *sql.DB, in ShiftCloseInput) error {
	if in.ShiftID == "" {
		return errors.New("shift_id required")
	}
	if in.ClosingCash < 0 {
		return errors.New("closing_cash must be >= 0")
	}

	// Verify shift exists and is open
	var registerID, cashierID string
	var openingCash int64
	err := sqlDB.QueryRowContext(ctx, `
SELECT register_id, cashier_id, opening_cash
FROM shifts 
WHERE id = ? AND closed_at IS NULL
`, in.ShiftID).Scan(&registerID, &cashierID, &openingCash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("shift not found or already closed")
		}
		return fmt.Errorf("query shift: %w", err)
	}

	// Calculate expected cash
	expectedCash, err := ComputeExpectedCash(ctx, sqlDB, in.ShiftID, openingCash)
	if err != nil {
		return fmt.Errorf("compute expected cash: %w", err)
	}

	err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, `
UPDATE shifts
SET closed_at = ?, closing_cash = ?, expected_cash = ?, note = ?
WHERE id = ?
`, now, in.ClosingCash, expectedCash, nullIfEmpty(in.Note), in.ShiftID); err != nil {
			return fmt.Errorf("update shift: %w", err)
		}

		// Audit log
		payload := map[string]any{
			"closing_cash":  in.ClosingCash,
			"expected_cash": expectedCash,
			"variance":      in.ClosingCash - expectedCash,
			"note":          in.Note,
		}
		payloadJSON, _ := json.Marshal(payload)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'shift', ?, 'close', ?, ?)
`, uuid.NewString(), cashierID, in.ShiftID, string(payloadJSON), now); err != nil {
			return fmt.Errorf("insert audit: %w", err)
		}

		return nil
	})

	return err
}

// RecordCashAdjustment records a payout or adjustment for a shift
// Stores as audit_log entry with action 'cash_adjustment'
func RecordCashAdjustment(ctx context.Context, sqlDB *sql.DB, in CashAdjustmentInput) (string, error) {
	if in.ShiftID == "" {
		return "", errors.New("shift_id required")
	}
	if in.Type != "payout" && in.Type != "adjustment" {
		return "", errors.New("type must be 'payout' or 'adjustment'")
	}
	if in.Amount == 0 {
		return "", errors.New("amount must be non-zero")
	}
	if in.Reason == "" {
		return "", errors.New("reason required")
	}
	if in.ActorID == "" {
		return "", errors.New("actor_id required")
	}

	// Verify shift exists and is open
	var exists bool
	err := sqlDB.QueryRowContext(ctx, `
SELECT 1 FROM shifts WHERE id = ? AND closed_at IS NULL
`, in.ShiftID).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("shift not found or already closed")
		}
		return "", fmt.Errorf("query shift: %w", err)
	}

	adjustmentID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)

	payload := map[string]any{
		"shift_id": in.ShiftID,
		"type":     in.Type,
		"amount":   in.Amount,
		"reason":   in.Reason,
	}
	payloadJSON, _ := json.Marshal(payload)

	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'shift', ?, 'cash_adjustment', ?, ?)
`, adjustmentID, in.ActorID, in.ShiftID, string(payloadJSON), now); err != nil {
		return "", fmt.Errorf("insert adjustment audit: %w", err)
	}

	return adjustmentID, nil
}

// ComputeExpectedCash calculates expected cash for a shift
// Formula: opening_cash + cash_payments + adjustments (adjustments should be stored as negative values for payouts)
func ComputeExpectedCash(ctx context.Context, sqlDB *sql.DB, shiftID string, openingCash int64) (int64, error) {
	if shiftID == "" {
		return 0, errors.New("shift_id required")
	}

	// Get shift time range
	var openedAt, closedAt sql.NullString
	var registerID string
	err := sqlDB.QueryRowContext(ctx, `
SELECT register_id, opened_at, closed_at
FROM shifts
WHERE id = ?
`, shiftID).Scan(&registerID, &openedAt, &closedAt)
	if err != nil {
		return 0, fmt.Errorf("query shift: %w", err)
	}

	// Sum cash payments during shift
	// Join sales with shift time range and filter by register
	var cashPayments int64
	timeFilter := `s.completed_at >= ?`
	args := []any{registerID, openedAt.String}
	if closedAt.Valid {
		timeFilter += ` AND s.completed_at <= ?`
		args = append(args, closedAt.String)
	}

	query := fmt.Sprintf(`
SELECT COALESCE(SUM(p.amount - p.change_given), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
JOIN payment_methods pm ON pm.id = p.method_id
WHERE pm.type = 'cash'
  AND s.status = 'completed'
  AND s.register_id = ?
  AND %s
`, timeFilter)

	err = sqlDB.QueryRowContext(ctx, query, args...).Scan(&cashPayments)
	if err != nil {
		return 0, fmt.Errorf("sum cash payments: %w", err)
	}

	// Sum cash adjustments from audit_log
	var adjustments int64
	err = sqlDB.QueryRowContext(ctx, `
SELECT COALESCE(SUM(
	CAST(json_extract(data_json, '$.amount') AS INTEGER)
), 0)
FROM audit_log
WHERE entity_type = 'shift'
  AND entity_id = ?
  AND action = 'cash_adjustment'
`, shiftID).Scan(&adjustments)
	if err != nil {
		return 0, fmt.Errorf("sum adjustments: %w", err)
	}

	// Expected = opening + cash_payments - adjustments
	// (adjustments are typically negative for payouts, positive for additions)
	expectedCash := openingCash + cashPayments + adjustments

	return expectedCash, nil
}
