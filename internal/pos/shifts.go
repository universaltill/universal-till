package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// ShiftInput captures data for opening a shift
type ShiftInput struct {
	ShiftID     string
	RegisterID  string
	CashierID   string
	OpeningCash money.Money
}

// ShiftCloseInput captures data for closing a shift
type ShiftCloseInput struct {
	ShiftID     string
	ClosingCash money.Money
	Note        string
}

// CashAdjustmentReasonPfandrueckgabe is the fixed reason recorded for
// bottle-deposit cash payouts, so they're distinguishable in reports from
// any other free-text cash adjustment.
const CashAdjustmentReasonPfandrueckgabe = "Pfandrückgabe"

// CashAdjustmentInput captures payouts or adjustments affecting expected cash
type CashAdjustmentInput struct {
	ShiftID string
	Type    string      // payout|adjustment
	Amount  money.Money // negative for payout
	Reason  string
	ActorID string
}

// OpenShift creates a new shift record with opening cash and audit entry
func OpenShift(ctx context.Context, sqlDB *sql.DB, in ShiftInput) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
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
	existingID, err := repo.FindOpenShiftForRegister(ctx, nil, in.RegisterID)
	if err != nil {
		return "", err
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
		if err := repo.InsertShift(ctx, tx, shiftID, in.RegisterID, in.CashierID, in.OpeningCash.Minor(), now); err != nil {
			return err
		}
		payload := map[string]any{
			"register_id":  in.RegisterID,
			"cashier_id":   in.CashierID,
			"opening_cash": in.OpeningCash.Minor(),
		}
		if err := repo.InsertAudit(ctx, tx, in.CashierID, "shift", shiftID, "open", payload, now, ""); err != nil {
			return err
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
	repo := data.NewPOSRepo(sqlDB)
	if in.ShiftID == "" {
		return errors.New("shift_id required")
	}
	if in.ClosingCash < 0 {
		return errors.New("closing_cash must be >= 0")
	}

	// Verify shift exists and is open
	_, cashierID, openingCash, err := repo.LoadShiftForClose(ctx, nil, in.ShiftID)
	if err != nil {
		return err
	}

	// Calculate expected cash
	expectedCash, err := repo.ComputeExpectedCash(ctx, in.ShiftID, openingCash)
	if err != nil {
		return fmt.Errorf("compute expected cash: %w", err)
	}

	err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.UpdateShiftClose(ctx, tx, in.ShiftID, in.ClosingCash.Minor(), expectedCash, in.Note, now); err != nil {
			return err
		}
		variance := in.ClosingCash - money.FromMinor(expectedCash)
		payload := map[string]any{
			"closing_cash":  in.ClosingCash.Minor(),
			"expected_cash": expectedCash,
			"variance":      variance.Minor(),
			"note":          in.Note,
		}
		if err := repo.InsertAudit(ctx, tx, cashierID, "shift", in.ShiftID, "close", payload, now, ""); err != nil {
			return err
		}
		return nil
	})

	return err
}

// RecordCashAdjustment records a payout or adjustment for a shift
// Stores as audit_log entry with action 'cash_adjustment'
func RecordCashAdjustment(ctx context.Context, sqlDB *sql.DB, in CashAdjustmentInput) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
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
	ok, err := repo.ShiftOpenExists(ctx, nil, in.ShiftID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("shift not found or already closed")
	}

	adjustmentID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)

	payload := map[string]any{
		"shift_id": in.ShiftID,
		"type":     in.Type,
		"amount":   in.Amount.Minor(),
		"reason":   in.Reason,
	}
	if err := repo.InsertAudit(ctx, nil, in.ActorID, "shift", in.ShiftID, "cash_adjustment", payload, now, adjustmentID); err != nil {
		return "", err
	}

	return adjustmentID, nil
}

// ComputeExpectedCash calculates expected cash for a shift
// Formula: opening_cash + cash_payments + adjustments (adjustments should be stored as negative values for payouts)
func ComputeExpectedCash(ctx context.Context, sqlDB *sql.DB, shiftID string, openingCash int64) (int64, error) {
	return data.NewPOSRepo(sqlDB).ComputeExpectedCash(ctx, shiftID, openingCash)
}
