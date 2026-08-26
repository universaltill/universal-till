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

// ErrVoucherIDExists is returned by CreateVoucher when the id is already
// taken (vouchers.id is the operator-supplied TEXT PRIMARY KEY -- ut-docs#1127,
// ADR-0065). Distinct sentinel, same isUniqueViolation pattern already used
// for ErrPromotionCodeExists (pos_repo.go), so a caller (applyJournal's LAN-
// sync journal replay, in particular -- two tills can issue the same
// operator-typed code offline) can classify this as a permanent, non-retryable
// failure rather than surfacing a raw "UNIQUE constraint failed" string.
var ErrVoucherIDExists = errors.New("voucher id already exists")

// ErrVoucherNotActive is returned when a redemption targets a voucher whose
// status is not 'active' (already fully redeemed, or voided).
var ErrVoucherNotActive = errors.New("voucher is not active")

// ErrVoucherInsufficientBalance is returned when a redemption would take the
// voucher's balance negative — overspend is rejected outright (ut-docs#1008
// explicitly defers partial-voucher/partial-cash split logic).
var ErrVoucherInsufficientBalance = errors.New("voucher balance does not cover the tendered amount")

// ErrVoucherRedeemedCannotVoid is returned when voiding a sale whose issued
// voucher has already been partly or fully redeemed elsewhere (ut-docs#1008
// review, blocker F2): the void is refused outright — fail-closed — because
// unwinding a liability someone has already spent against needs a human
// decision, not invented semantics.
var ErrVoucherRedeemedCannotVoid = errors.New("voucher issued in this sale has already been redeemed; the sale cannot be voided")

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
		if isUniqueViolation(err) {
			return fmt.Errorf("create voucher %q: %w", v.ID, ErrVoucherIDExists)
		}
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
// ErrVoucherNotFound; status == 'void' -> ErrVoucherNotActive; balance <
// amountMinor -> ErrVoucherInsufficientBalance (overspend rejected outright,
// never clamped). Draining the balance to exactly zero flips status to
// 'redeemed'.
//
// force (ut-docs#1053) relaxes the balance check AND, necessarily, the
// status check: when true, an overdraft debits anyway and the balance goes
// negative — the LAN-sync journal replay path's genuine-offline-double-spend
// case, where the money already moved at the remote till, so rejecting here
// would poison that replica's journal forever (same reasoning as
// pos.SaleInput.AllowNegativeInventory for stock). Under force, a voucher
// whose balance was already exactly drained by an earlier replay — status
// 'redeemed', the single most likely double-spend shape (a customer spending
// the whole face value at two tills before either synced) — is still
// debitable; only 'void' stays a hard reject even under force, since a
// voided voucher is a different, worse problem than a balance/status race
// caused by replay ordering. The caller surfaces the resulting negative
// balance as a back-office Problem. ErrVoucherNotFound always stays a hard
// reject: an unknown id under force still needs a real fix (a pre-1.3.0
// voucher never journaled, or a genuinely bad id), not a silent debit.
// Every non-replay caller passes force=false, where status != 'active' still
// rejects exactly as before (a redeemed/void voucher can't be redeemed
// again from a live till).
func (r *POSRepo) DebitVoucherForRedemption(ctx context.Context, tx *sql.Tx, voucherID string, amountMinor int64, force bool) error {
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
	// force widens the acceptable pre-read status from just 'active' to
	// 'active' or 'redeemed' — 'void' is excluded either way, both here and
	// in the UPDATE predicate below, so a genuinely voided voucher can never
	// be force-debited.
	statusOK := status == "active" || (force && status == "redeemed")
	if !statusOK {
		return fmt.Errorf("voucher %q (status %s): %w", voucherID, status, ErrVoucherNotActive)
	}
	if !force && balance < amountMinor {
		return fmt.Errorf("voucher %q (balance %d, tendered %d): %w", voucherID, balance, amountMinor, ErrVoucherInsufficientBalance)
	}
	// The guard predicates (balance >= ?, status = 'active') are repeated in
	// the UPDATE itself so a concurrent debit between the read above and this
	// write can never take the balance negative — the affected-rows check
	// turns a lost race into a clean refusal instead of a silent overdraw.
	// Under force the balance predicate is dropped (a negative balance is
	// the point) and the status predicate widens to ('active','redeemed')
	// to match the pre-read check above — 'void' stays excluded either way,
	// so a concurrent void between read and write still loses cleanly.
	query := `
UPDATE vouchers
SET balance = balance - ?,
    status = CASE WHEN balance - ? = 0 THEN 'redeemed' ELSE status END
WHERE id = ? AND status = 'active' AND balance >= ?`
	args := []any{amountMinor, amountMinor, voucherID, amountMinor}
	if force {
		query = `
UPDATE vouchers
SET balance = balance - ?,
    status = CASE WHEN balance - ? = 0 THEN 'redeemed' ELSE status END
WHERE id = ? AND status IN ('active', 'redeemed')`
		args = []any{amountMinor, amountMinor, voucherID}
	}
	res, err := r.exec(tx).ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("debit voucher: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// A RowsAffected failure is a driver/DB fault, not evidence the
		// guard refused — reporting it as "insufficient balance" (the old
		// `_`-discard did exactly that) would mislabel an internal error as
		// a customer-facing business rejection (review minor F8).
		return fmt.Errorf("debit voucher: rows affected: %w", err)
	}
	if n != 1 {
		if force {
			// The only predicate left is status IN ('active','redeemed') —
			// a concurrent void won the race between the read and this
			// write (draining to 'redeemed' no longer excludes the row).
			return fmt.Errorf("voucher %q: %w", voucherID, ErrVoucherNotActive)
		}
		return fmt.Errorf("voucher %q: %w", voucherID, ErrVoucherInsufficientBalance)
	}
	return nil
}

// VoidVouchersIssuedInSale voids every voucher whose 'issue' transaction
// belongs to saleID, inside the caller's transaction — the voucher side of
// voiding the sale that sold them (ut-docs#1008 review, blocker F2). For each
// such voucher, in one guarded UPDATE (the same predicates-repeated-in-the-
// WHERE pattern DebitVoucherForRedemption uses, so a concurrent redemption
// between read and write loses cleanly): a genuinely untouched voucher
// (status 'active', balance still == original_amount) becomes status 'void'
// with balance 0. A voucher already partly or fully redeemed — or one a
// concurrent redemption touches mid-void — fails the whole call with
// ErrVoucherRedeemedCannotVoid, rolling the sale void back. A voucher already
// 'void' (the sale was voided before) is skipped: re-voiding is idempotent.
func (r *POSRepo) VoidVouchersIssuedInSale(ctx context.Context, tx *sql.Tx, saleID string) error {
	rows, err := r.exec(tx).QueryContext(ctx, `
SELECT DISTINCT voucher_id FROM voucher_transactions WHERE sale_id = ? AND type = 'issue'`, saleID)
	if err != nil {
		return fmt.Errorf("void vouchers for sale: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("void vouchers for sale: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("void vouchers for sale: %w", err)
	}
	rows.Close()

	for _, id := range ids {
		res, err := r.exec(tx).ExecContext(ctx, `
UPDATE vouchers
SET status = 'void', balance = 0
WHERE id = ? AND status = 'active' AND balance = original_amount`, id)
		if err != nil {
			return fmt.Errorf("void voucher %q: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("void voucher %q: rows affected: %w", id, err)
		}
		if n == 1 {
			continue
		}
		// The guarded UPDATE refused — find out why. Already 'void' means a
		// previous void of this sale got here first: idempotent, skip.
		// Anything else ('redeemed', or 'active' with balance <
		// original_amount) means value has left this voucher — fail closed.
		var status string
		err = r.exec(tx).QueryRowContext(ctx, `SELECT status FROM vouchers WHERE id = ?`, id).Scan(&status)
		if err == sql.ErrNoRows {
			return fmt.Errorf("void voucher %q: %w", id, ErrVoucherNotFound)
		}
		if err != nil {
			return fmt.Errorf("void voucher %q: %w", id, err)
		}
		if status == "void" {
			continue
		}
		return fmt.Errorf("voucher %q (status %s): %w", id, status, ErrVoucherRedeemedCannotVoid)
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
//
// A transaction whose own sale was VOIDED is excluded (ut-docs#1008 review,
// blocker F2): a voided sale drops out of the report's Gross/Net via the
// status filter, so still counting its voucher issue (or a redemption taken
// in a since-voided sale) here would report flows the till's takings no
// longer contain. LEFT JOIN, deliberately permissive on a MISSING sale row:
// sale_id is a soft reference with no FK (see the file header), and a sale
// archived by ResetTransactionHistory — physically gone from `sales` — was a
// real, non-voided sale whose voucher flows must keep counting; a void, by
// contrast, keeps its sales row (pos.UpdateSaleStatus never deletes), so
// `status = 'voided'` is reliably observable whenever it happened.
func (r *POSRepo) VouchersIssuedRedeemedForRange(ctx context.Context, from, to string) (VoucherRangeSummary, error) {
	var out VoucherRangeSummary
	rows, err := r.db.QueryContext(ctx, `
SELECT vt.type, COUNT(*), COALESCE(SUM(vt.amount), 0)
FROM voucher_transactions vt
LEFT JOIN sales s ON s.id = vt.sale_id
WHERE date(vt.created_at, 'localtime') BETWEEN date(?) AND date(?)
  AND (s.id IS NULL OR s.status != 'voided')
GROUP BY vt.type`, from, to)
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
