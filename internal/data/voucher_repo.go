package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Voucher liability (ut-docs#1008): a multi-purpose voucher's issue is a 0%
// VAT liability, not revenue — VAT arises only at redemption, against the
// redeemed goods' own rates. These tables deliberately carry NO foreign key
// to sales (only soft, informational sale ids), and are deliberately NOT in
// reset_archive_repo.go's resetArchiveTables: a transaction-history reset
// (ADR-0042) archives the sale a voucher was issued in, but the voucher's
// outstanding balance is a real liability to a real customer that must
// survive the reset — same soft-reference reasoning as
// worker_allocations.source_id (ADR-0063).
//
// Only voucher_type 'multi_purpose' exists in this card; single-purpose
// vouchers (VAT at issue) are ut-docs#1037.

// ErrVoucherNotFound is returned when a voucher id has no vouchers row.
var ErrVoucherNotFound = errors.New("voucher not found")

// ErrVoucherNotActive is returned when a redemption targets a voucher whose
// status is not 'active' (already fully redeemed, or voided).
var ErrVoucherNotActive = errors.New("voucher is not active")

// ErrVoucherInsufficientBalance is returned when a redemption would take the
// voucher's balance negative — overspend is rejected outright (ut-docs#1008
// explicitly defers partial-voucher/partial-cash split logic).
var ErrVoucherInsufficientBalance = errors.New("voucher balance does not cover the tendered amount")

// Voucher is one vouchers row — the per-voucher outstanding liability, keyed
// by the stable voucher identifier a shop prints on the physical voucher.
// JSON tags are snake_case per this repo's API convention; amounts are raw
// minor units at this DB/DTO boundary (internal/money.Money everywhere else).
type Voucher struct {
	ID                  string `json:"id"`
	HolderLabel         string `json:"holder_label"`
	OriginalAmountMinor int64  `json:"original_amount"`
	BalanceMinor        int64  `json:"balance"`
	Currency            string `json:"currency"`
	VoucherType         string `json:"voucher_type"`
	Status              string `json:"status"`
	IssuedSaleID        string `json:"issued_sale_id"`
	CreatedAt           string `json:"created_at"`
}

// VoucherTransaction is one voucher_transactions row — a single issue or
// redemption event against a voucher's balance.
type VoucherTransaction struct {
	ID          string `json:"id"`
	VoucherID   string `json:"voucher_id"`
	SaleID      string `json:"sale_id"`
	Type        string `json:"type"` // "issue" | "redemption"
	AmountMinor int64  `json:"amount"`
	CreatedAt   string `json:"created_at"`
}

// CreateVoucher writes one vouchers row. tx may be nil (direct exec) or the
// caller's open transaction — pos.CompleteSale passes its sale transaction so
// the liability lands atomically with the sale that issued it.
func (r *POSRepo) CreateVoucher(ctx context.Context, tx *sql.Tx, v Voucher) error {
	if v.ID == "" {
		return fmt.Errorf("create voucher: id is required")
	}
	if v.OriginalAmountMinor <= 0 {
		return fmt.Errorf("create voucher: amount must be > 0")
	}
	if v.VoucherType == "" {
		v.VoucherType = "multi_purpose"
	}
	if v.Status == "" {
		v.Status = "active"
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO vouchers (id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, v.ID, nullIfEmpty(v.HolderLabel), v.OriginalAmountMinor, v.BalanceMinor, v.Currency, v.VoucherType, v.Status, nullIfEmpty(v.IssuedSaleID), v.CreatedAt)
	if err != nil {
		return fmt.Errorf("create voucher: %w", err)
	}
	return nil
}

// RecordVoucherTransaction writes one voucher_transactions row (an issue or a
// redemption). It records the event only — debiting the balance is
// DebitVoucherForRedemption's job, so the two must be called together (in the
// same transaction) for a redemption.
func (r *POSRepo) RecordVoucherTransaction(ctx context.Context, tx *sql.Tx, t VoucherTransaction) error {
	if t.Type != "issue" && t.Type != "redemption" {
		return fmt.Errorf("record voucher transaction: invalid type %q", t.Type)
	}
	if t.AmountMinor <= 0 {
		return fmt.Errorf("record voucher transaction: amount must be > 0")
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, t.ID, t.VoucherID, nullIfEmpty(t.SaleID), t.Type, t.AmountMinor, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("record voucher transaction: %w", err)
	}
	return nil
}

// GetVoucherBalance returns one voucher's row — the outstanding liability
// (BalanceMinor) plus the stable identifier and holder label the acceptance
// criteria require to be queryable. ErrVoucherNotFound for an unknown id.
func (r *POSRepo) GetVoucherBalance(ctx context.Context, id string) (Voucher, error) {
	var v Voucher
	var holder, issuedSale sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at
FROM vouchers WHERE id = ?`, id).
		Scan(&v.ID, &holder, &v.OriginalAmountMinor, &v.BalanceMinor, &v.Currency, &v.VoucherType, &v.Status, &issuedSale, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return v, fmt.Errorf("voucher %q: %w", id, ErrVoucherNotFound)
	}
	if err != nil {
		return v, fmt.Errorf("get voucher balance: %w", err)
	}
	v.HolderLabel = holder.String
	v.IssuedSaleID = issuedSale.String
	return v, nil
}

// DebitVoucherForRedemption validates and debits one voucher's balance by
// amountMinor inside the caller's transaction (tx may be nil for a direct
// exec, e.g. in tests). Fail-closed, in order: unknown id ->
// ErrVoucherNotFound; status != 'active' -> ErrVoucherNotActive; balance <
// amountMinor -> ErrVoucherInsufficientBalance (overspend rejected outright,
// never clamped). Draining the balance to exactly zero flips status to
// 'redeemed'.
func (r *POSRepo) DebitVoucherForRedemption(ctx context.Context, tx *sql.Tx, voucherID string, amountMinor int64) error {
	if amountMinor <= 0 {
		return fmt.Errorf("debit voucher: amount must be > 0")
	}
	var balance int64
	var status string
	err := r.exec(tx).QueryRowContext(ctx, `SELECT balance, status FROM vouchers WHERE id = ?`, voucherID).Scan(&balance, &status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("voucher %q: %w", voucherID, ErrVoucherNotFound)
	}
	if err != nil {
		return fmt.Errorf("debit voucher: %w", err)
	}
	if status != "active" {
		return fmt.Errorf("voucher %q (status %s): %w", voucherID, status, ErrVoucherNotActive)
	}
	if balance < amountMinor {
		return fmt.Errorf("voucher %q (balance %d, tendered %d): %w", voucherID, balance, amountMinor, ErrVoucherInsufficientBalance)
	}
	// The guard predicates (balance >= ?, status = 'active') are repeated in
	// the UPDATE itself so a concurrent debit between the read above and this
	// write can never take the balance negative — the affected-rows check
	// turns a lost race into a clean refusal instead of a silent overdraw.
	res, err := r.exec(tx).ExecContext(ctx, `
UPDATE vouchers
SET balance = balance - ?,
    status = CASE WHEN balance - ? = 0 THEN 'redeemed' ELSE status END
WHERE id = ? AND status = 'active' AND balance >= ?`,
		amountMinor, amountMinor, voucherID, amountMinor)
	if err != nil {
		return fmt.Errorf("debit voucher: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("voucher %q: %w", voucherID, ErrVoucherInsufficientBalance)
	}
	return nil
}

// VoucherRangeSummary is the day-close voucher section (ut-docs#1008):
// how many vouchers were issued and redeemed in the window, and for how
// much (minor units) — reported DISTINCTLY from article revenue, never
// summed into it.
type VoucherRangeSummary struct {
	IssuedCount   int   `json:"issued_count"`
	IssuedMinor   int64 `json:"issued"`
	RedeemedCount int   `json:"redeemed_count"`
	RedeemedMinor int64 `json:"redeemed"`
}

// VouchersIssuedRedeemedForRange aggregates voucher_transactions over
// [from, to] inclusive of both ends, matched on the shop's LOCAL calendar day
// — the same date(created_at, 'localtime') BETWEEN date(?) AND date(?)
// convention dateRangeSummary and WorkerAllocationsSummary use (ut-docs#869:
// a bare UTC date() match silently aggregates the wrong calendar day on any
// non-UTC host, and this feature's pilot market — Germany — is non-UTC).
func (r *POSRepo) VouchersIssuedRedeemedForRange(ctx context.Context, from, to string) (VoucherRangeSummary, error) {
	var out VoucherRangeSummary
	rows, err := r.db.QueryContext(ctx, `
SELECT type, COUNT(*), COALESCE(SUM(amount), 0)
FROM voucher_transactions
WHERE date(created_at, 'localtime') BETWEEN date(?) AND date(?)
GROUP BY type`, from, to)
	if err != nil {
		return out, fmt.Errorf("vouchers issued/redeemed for range: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var txType string
		var count int
		var amount int64
		if err := rows.Scan(&txType, &count, &amount); err != nil {
			return out, fmt.Errorf("vouchers issued/redeemed for range: scan: %w", err)
		}
		switch txType {
		case "issue":
			out.IssuedCount, out.IssuedMinor = count, amount
		case "redemption":
			out.RedeemedCount, out.RedeemedMinor = count, amount
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("vouchers issued/redeemed for range: %w", err)
	}
	return out, nil
}
