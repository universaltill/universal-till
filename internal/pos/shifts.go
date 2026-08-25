package pos

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// Skim is the cash moved from the counted drawer to the safe at close
	// (ut-docs#1006). Zero means no skim. Must be >= 0 and <= ClosingCash —
	// it's an amount being removed; the sign is handled internally (the
	// audit row records it negative, like any other cash leaving the till).
	// The skim never feeds back into expected/calculated cash: variance is
	// checked against the count BEFORE the skim is applied.
	Skim money.Money
	// SkimReason is the optional free-text reason recorded on the skim's
	// audit row; defaults to CashAdjustmentReasonSkim when empty.
	SkimReason string
	// CountProtocol is an optional denomination-count JSON blob persisted
	// with the close: a flat object keyed by denomination in minor units
	// mapping to piece count, e.g. {"5000":2,"100":13}. Must be well-formed
	// JSON when non-empty.
	CountProtocol string
}

// CashAdjustmentReasonPfandrueckgabe is the fixed reason recorded for
// bottle-deposit cash payouts, so they're distinguishable in reports from
// any other free-text cash adjustment.
const CashAdjustmentReasonPfandrueckgabe = "Pfandrückgabe"

// CashAdjustmentReasonSkim is the default reason recorded for a
// skim-to-safe when the operator gives none — reason stays free text,
// unlike Pfandrückgabe this is just a suggested default.
const CashAdjustmentReasonSkim = "Skim to safe"

// CashAdjustmentInput captures payouts or adjustments affecting expected cash
type CashAdjustmentInput struct {
	ShiftID string
	// Type is one of:
	//   payout     — cash paid out of the drawer (supplier, petty cash, …)
	//   adjustment — a correction in either direction (float top-up, …)
	//   skim       — cash moved from the drawer to the safe (ut-docs#1006)
	// No TSE/fiscal gate applies to any of these — whether cash
	// adjustments get the ADR-0048 hard-gate is ut-docs#998's open
	// question, deliberately not decided here.
	Type    string
	Amount  money.Money // negative for cash leaving the drawer (payout/skim)
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

// CloseShift closes an existing shift, calculates expected cash, and
// persists closing cash. With a Skim it also records the skim-to-safe
// (ut-docs#1006): new_float = counted closing cash minus the skim, and the
// skim lands as a cash_adjustment audit row in the SAME transaction as the
// close — written directly here rather than via RecordCashAdjustment,
// which requires the shift to still be open (it no longer is at that
// point), but in the exact shape RecordCashAdjustment writes so
// SumShiftAdjustments-style queries treat it identically. Expected cash is
// computed BEFORE the close is persisted, so the skim row can never feed
// back into the calculated figure — variance compares the count against
// takings, before any skim.
func CloseShift(ctx context.Context, sqlDB *sql.DB, in ShiftCloseInput) error {
	repo := data.NewPOSRepo(sqlDB)
	if in.ShiftID == "" {
		return errors.New("shift_id required")
	}
	if in.ClosingCash < 0 {
		return errors.New("closing_cash must be >= 0")
	}
	if in.Skim < 0 {
		return errors.New("skim must be >= 0")
	}
	if in.Skim > in.ClosingCash {
		return errors.New("skim cannot exceed the counted closing cash")
	}
	if in.CountProtocol != "" && !json.Valid([]byte(in.CountProtocol)) {
		return errors.New("count_protocol must be valid JSON")
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

	newFloat := in.ClosingCash - in.Skim

	err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.UpdateShiftClose(ctx, tx, in.ShiftID, in.ClosingCash.Minor(), expectedCash, newFloat.Minor(), in.Note, in.CountProtocol, now); err != nil {
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
		if in.Skim > 0 {
			reason := in.SkimReason
			if reason == "" {
				reason = CashAdjustmentReasonSkim
			}
			skimPayload := map[string]any{
				"shift_id": in.ShiftID,
				"type":     "skim",
				"amount":   (-in.Skim).Minor(),
				"reason":   reason,
			}
			if err := repo.InsertAudit(ctx, tx, cashierID, "shift", in.ShiftID, "cash_adjustment", skimPayload, now, ""); err != nil {
				return err
			}
		}
		return nil
	})

	return err
}

// LastClosedShiftNewFloat returns the opening float carried forward from a
// register's most recent closed shift (its new_float after any skim,
// falling back to closing_cash for a shift closed before ut-docs#1006).
// ok is false when the register has no closed shift yet.
func LastClosedShiftNewFloat(ctx context.Context, sqlDB *sql.DB, registerID string) (money.Money, bool, error) {
	if registerID == "" {
		return 0, false, errors.New("register_id required")
	}
	carry, ok, err := data.NewPOSRepo(sqlDB).LastClosedShiftCarryForward(ctx, registerID)
	if err != nil || !ok {
		return 0, false, err
	}
	return money.FromMinor(carry), true, nil
}

// RecordCashAdjustment records a payout or adjustment for a shift
// Stores as audit_log entry with action 'cash_adjustment'
func RecordCashAdjustment(ctx context.Context, sqlDB *sql.DB, in CashAdjustmentInput) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	if in.ShiftID == "" {
		return "", errors.New("shift_id required")
	}
	if in.Type != "payout" && in.Type != "adjustment" && in.Type != "skim" {
		return "", errors.New("type must be 'payout', 'adjustment' or 'skim'")
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
