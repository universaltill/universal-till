package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ut-docs#7021 (ADR-0044 D1 amendment): the till-local journal of every
// fiscal.sign.ask that was sent. Written before the ask, deleted when the
// sale commits, kept (signed_no_sale / warned) when it did not. All SQL for
// the table lives here. No card data is stored.

// Fiscal sign journal states.
const (
	FiscalSignAsked        = "asked"
	FiscalSignSignedNoSale = "signed_no_sale"
	FiscalSignWarned       = "warned"

	// FiscalSignReasonRestart is the reason boot writes on a row it warns about.
	FiscalSignReasonRestart = "restart"
)

// ErrFiscalSignJournalExists: the sale id was already journaled.
var ErrFiscalSignJournalExists = errors.New("fiscal sign journal: sale already asked")

// FiscalSignEntry is one fiscal_sign_journal row.
type FiscalSignEntry struct {
	SaleID, SaleType, State, Outcome string
	// AmountMinor is the sale total in minor units (DB/DTO boundary).
	AmountMinor                       int64
	Currency, ActorID                 string
	EvidenceJSON, ReceiptJSON, Reason string
	AskedAt, UpdatedAt                time.Time
}

// FiscalSignJournalRepo owns all SQL for fiscal_sign_journal.
type FiscalSignJournalRepo struct{ db *sql.DB }

func NewFiscalSignJournalRepo(db *sql.DB) *FiscalSignJournalRepo {
	return &FiscalSignJournalRepo{db: db}
}

const fsjCols = `sale_id, sale_type, state, outcome, amount_minor, currency, actor_id, evidence_json, receipt_json, reason, asked_at, updated_at`

// Begin inserts the row in state asked.
func (r *FiscalSignJournalRepo) Begin(ctx context.Context, e FiscalSignEntry) error {
	now := time.Now()
	if e.AskedAt.IsZero() {
		e.AskedAt = now
	}
	var exists int
	switch err := r.db.QueryRowContext(ctx, `SELECT 1 FROM fiscal_sign_journal WHERE sale_id = ?`, e.SaleID).Scan(&exists); {
	case err == nil:
		return ErrFiscalSignJournalExists
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("fiscal sign journal: check %s: %w", e.SaleID, err)
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO fiscal_sign_journal (`+fsjCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.SaleID, e.SaleType, FiscalSignAsked, "", e.AmountMinor, e.Currency, e.ActorID, "", "", "", legTS(e.AskedAt), legTS(now)); err != nil {
		return fmt.Errorf("fiscal sign journal: begin %s: %w", e.SaleID, err)
	}
	return nil
}

// MarkSignedNoSale records the signer's answer on an asked row after the sale
// failed to save.
func (r *FiscalSignJournalRepo) MarkSignedNoSale(ctx context.Context, e FiscalSignEntry) error {
	res, err := r.db.ExecContext(ctx, `UPDATE fiscal_sign_journal SET state = ?, outcome = ?, evidence_json = ?, receipt_json = ?, reason = ?, updated_at = ?
		WHERE sale_id = ? AND state = ?`,
		FiscalSignSignedNoSale, e.Outcome, e.EvidenceJSON, e.ReceiptJSON, e.Reason, legTS(time.Now()), e.SaleID, FiscalSignAsked)
	if err != nil {
		return fmt.Errorf("fiscal sign journal: mark signed_no_sale %s: %w", e.SaleID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("fiscal sign journal: %s is not in state asked", e.SaleID)
	}
	return nil
}

// MarkWarned turns an asked row into warned (boot found no sale for it).
func (r *FiscalSignJournalRepo) MarkWarned(ctx context.Context, saleID, reason string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE fiscal_sign_journal SET state = ?, reason = ?, updated_at = ? WHERE sale_id = ? AND state = ?`,
		FiscalSignWarned, reason, legTS(time.Now()), saleID, FiscalSignAsked); err != nil {
		return fmt.Errorf("fiscal sign journal: mark warned %s: %w", saleID, err)
	}
	return nil
}

// Delete removes an asked row; an open (signed_no_sale / warned) row is never
// removed here.
func (r *FiscalSignJournalRepo) Delete(ctx context.Context, saleID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM fiscal_sign_journal WHERE sale_id = ? AND state = ?`, saleID, FiscalSignAsked); err != nil {
		return fmt.Errorf("fiscal sign journal: delete %s: %w", saleID, err)
	}
	return nil
}

func (r *FiscalSignJournalRepo) list(ctx context.Context, where string, limit int, args ...any) ([]FiscalSignEntry, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+fsjCols+` FROM fiscal_sign_journal WHERE `+where+` ORDER BY asked_at, sale_id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("fiscal sign journal: list: %w", err)
	}
	defer rows.Close()
	var out []FiscalSignEntry
	for rows.Next() {
		e, err := scanFiscalSign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAskedBefore returns asked rows written before cutoff, oldest first.
func (r *FiscalSignJournalRepo) ListAskedBefore(ctx context.Context, cutoff time.Time, limit int) ([]FiscalSignEntry, error) {
	return r.list(ctx, `state = ? AND asked_at < ?`, limit, FiscalSignAsked, legTS(cutoff))
}

// ListOpen returns the signed_no_sale and warned rows, oldest first.
func (r *FiscalSignJournalRepo) ListOpen(ctx context.Context, limit int) ([]FiscalSignEntry, error) {
	return r.list(ctx, `state IN (?, ?)`, limit, FiscalSignSignedNoSale, FiscalSignWarned)
}

// Get returns one row.
func (r *FiscalSignJournalRepo) Get(ctx context.Context, saleID string) (FiscalSignEntry, bool, error) {
	rows, err := r.list(ctx, `sale_id = ?`, 1, saleID)
	if err != nil || len(rows) == 0 {
		return FiscalSignEntry{}, false, err
	}
	return rows[0], true, nil
}

// SaleExists reports whether a sales row with this id exists.
func (r *FiscalSignJournalRepo) SaleExists(ctx context.Context, saleID string) (bool, error) {
	var one int
	switch err := r.db.QueryRowContext(ctx, `SELECT 1 FROM sales WHERE id = ?`, saleID).Scan(&one); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("fiscal sign journal: sale lookup %s: %w", saleID, err)
	}
}

func scanFiscalSign(rows *sql.Rows) (FiscalSignEntry, error) {
	var e FiscalSignEntry
	var asked, updated string
	if err := rows.Scan(&e.SaleID, &e.SaleType, &e.State, &e.Outcome, &e.AmountMinor, &e.Currency, &e.ActorID, &e.EvidenceJSON, &e.ReceiptJSON, &e.Reason, &asked, &updated); err != nil {
		return e, fmt.Errorf("fiscal sign journal: scan: %w", err)
	}
	e.AskedAt, _ = time.Parse(time.RFC3339Nano, asked)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return e, nil
}
