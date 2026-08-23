package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Reset archive (ADR-0042, ut-docs#187): "Clear transaction history" moves
// rows into *_archive tables tagged with one reset_batches row instead of
// deleting them, and a batch is restorable — whole-batch only — as long as
// the till has not traded since. report_archive is NOT part of this
// mechanism (ADR-0040 §9): it is a separate retained legal record with its
// own retention pruning, and reset must never touch it.
//
// Permanent purge (ADR-0042 §3, gated on the ut-docs#635 retention decision)
// is DeleteResetBatch below, added by ut-docs#661 once #659 supplied
// per-country archive_min_days as real data. A batch holding real trading
// history cannot be purged until the shop's active country's retention
// window has elapsed since it was archived.

// StoreCountrySettingsKey is the settings.key a shop's chosen country lives
// under. Duplicated from internal/pages/common.KeyCountry ("store.country")
// rather than imported: internal/pages/common already imports internal/data
// (see internal/pages/common/barcode_conflict.go), so the reverse import
// would cycle. Exported (rather than a private literal re-typed in two
// places) so TestStoreCountrySettingsKeyMatchesCommon (in
// internal/pages/common) can assert the two never drift apart instead of
// each just trusting the other's copy.
const StoreCountrySettingsKey = "store.country"

// ErrShopHasTradedSinceReset is returned by RestoreResetBatch when any live
// transactional table already holds rows: post-reset trading re-occupies the
// receipt-number sequence (NextReceiptNo derives from MAX over live sales),
// so restoring on top would collide or duplicate receipt numbers. Restore is
// "return to exactly the pre-reset state", never a merge (ADR-0042 §2).
var ErrShopHasTradedSinceReset = errors.New("the till has traded since this reset: live transaction tables are not empty, so the archived batch cannot be restored")

// ErrResetBatchNotFound is returned by RestoreResetBatch for an unknown (or
// already-restored — a restored batch stops existing) batch id.
var ErrResetBatchNotFound = errors.New("reset archive batch not found")

// ErrArchiveReferencesRemoved is returned by RestoreResetBatch when the
// archived rows reference a catalog or customer record that no longer
// exists. Found in independent review (ut-docs#187): reset empties the
// live transaction tables, and three OTHER Data-management actions on the
// same Settings card — CleanupObsoleteItems, the demo-data "Remove sample
// data" seed, and EraseCustomer — decide what's safe to remove/anonymise
// by checking whether any LIVE sale/stock-movement still references a
// row. Right after a reset those checks see nothing (the real references
// are sitting in the archive, invisible to them) and proceed, so an item
// or customer an archived batch still points to can vanish before the
// batch is ever restored. Restoring would then violate a live FK
// (sale_lines.item_id -> items, sales.customer_id -> customers, etc) —
// this turns that into a clean, named refusal instead of a raw
// "FOREIGN KEY constraint failed" surfacing to a shop owner. The
// transaction is rolled back (nothing partially restored); the archive
// and reset_batches row are untouched, matching ErrShopHasTradedSinceReset's
// no-op-on-refusal behavior. See ADR-0042 Consequences: this is a known,
// stated gap in that ADR's own "every archived row is recoverable" claim,
// not a silent one — a deeper fix (teach those three predicates to also
// check the archive tables) is tracked separately, not attempted here.
var ErrArchiveReferencesRemoved = errors.New("this archive references a product, stock location or customer that was removed or erased after the reset (e.g. by \"Remove sample data\", catalog cleanup or a GDPR erasure) and can no longer be restored automatically")

// ArchiveWithinRetentionWindowError is returned by DeleteResetBatch when the
// batch holds real trading history (sales_count > 0) and the shop's active
// country's archive_min_days has not yet elapsed since the batch was
// archived. RetainedUntil is the date the batch becomes purgeable, so the
// handler can name it in the operator-facing refusal rather than returning
// a generic error (ut-docs#661's own acceptance criteria).
type ArchiveWithinRetentionWindowError struct {
	RetainedUntil time.Time
}

func (e *ArchiveWithinRetentionWindowError) Error() string {
	return fmt.Sprintf("archive batch is within its statutory retention window until %s", e.RetainedUntil.Format("2006-01-02"))
}

// isForeignKeyViolation reports whether err is a SQLite foreign-key
// constraint failure. modernc.org/sqlite (this project's driver) doesn't
// export a typed error the way mattn/go-sqlite3 does, and no other code in
// this repo yet parses driver errors -- string matching on SQLite's own
// stable "FOREIGN KEY constraint failed" text is the least-fragile option
// available without adding a driver-specific type dependency for one call
// site.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// ResetBatch is one reset event's archive header, for the Settings → Data
// list (newest first).
//
// Purgeable/RetainedUntil (ut-docs#698) are the same purge eligibility
// DeleteResetBatch itself gates on, computed read-only so the UI can show it
// BEFORE an operator goes through the confirm-and-fail motion. RetainedUntil
// is the zero time.Time whenever there was no gate to compute at all
// (SalesCount == 0, mirrors DeleteResetBatch's own no-trading-history
// carve-out) — never a meaningless "purgeable since the epoch" date.
type ResetBatch struct {
	ID            string
	CreatedAt     string
	ActorID       string
	SalesCount    int64
	Purgeable     bool
	RetainedUntil time.Time
}

// resetArchiveTable pairs a live table with the explicit column list shared
// by it and its <live>_archive twin. Explicit columns (never SELECT *) keep
// the copy robust against any column-order drift between the two tables.
type resetArchiveTable struct {
	live string
	cols string
}

// resetArchiveTables lists every table reset touches, in CHILD-BEFORE-PARENT
// order — the order archiving/deleting must run. Note two orderings differ
// from the pre-ADR-0042 DELETE loop: stock_movements and sale_line_modifiers
// must go before sale_lines (both FK-reference it — stock_movements without
// a cascade, so deleting sale_lines first fails outright on any real
// checkout's data; sale_line_modifiers WITH a cascade, which would silently
// destroy the modifier snapshots instead of archiving them).
// Restore runs this slice in REVERSE (parent-before-child) so FKs to live
// catalog/user rows and intra-batch parents are satisfied on re-insert.
var resetArchiveTables = []resetArchiveTable{
	{"invoices", "id, series, invoice_no, display_no, kind, sale_id, original_invoice_id, customer_name, customer_address, customer_vat_no, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by"},
	{"payments", "id, sale_id, method_id, amount, currency, reference, change_given, paid_at, tip_amount, masked_pan, auth_code, terminal_id, trace_id"},
	{"sale_links", "id, sale_id, original_sale_id, reason"},
	{"sale_discounts", "id, sale_id, line_id, type, value, amount, reason"},
	{"stock_movements", "id, item_id, variant_id, location_id, sale_line_id, type, quantity, cost_price, created_at"},
	{"sale_line_modifiers", "id, sale_line_id, group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor"},
	{"sale_lines", "id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax"},
	{"sales", "id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, sync_next_attempt_at, sync_last_error, register_id, cashier_id, customer_id, currency, subtotal, discount_total, tax_total, total, rounding, note, created_at, completed_at, voided_at, till_id, service_charge_amount, order_type, order_status, order_status_updated_at, kitchen_print_failed_at, receipt_print_failed_at, table_id, tracking_token"},
	{"held_sales", "id, label, total_minor, line_count, payload, created_at, table_id"},
	{"shifts", "id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, note"},
}

// restoreEmptyCheckTables are the live tables that must ALL be empty before a
// restore may run. shifts/stock_movements are included alongside sales/
// held_sales because both can be created independently of a sale (a shift
// open, a manual stock adjustment) and face the same collision class on
// their own id sequences (ADR-0042 §2).
var restoreEmptyCheckTables = []string{"sales", "held_sales", "shifts", "stock_movements"}

// ResetTransactionHistory clears ALL transactional data — sales, payments,
// invoices, shifts, held sales, stock movements and the sale-line modifier
// snapshots — for a clean start after testing (go-live). It KEEPS the
// catalog, users, settings and tills. Since ADR-0042 nothing is destroyed:
// every row is MOVED into its *_archive twin, tagged with one new
// reset_batches row, in the same transaction, and is restorable via
// RestoreResetBatch until the till trades again. report_archive is explicitly
// EXCLUDED (ADR-0040 §9): once a shop has configured a retention mode, the
// report archive is a retained legal record, not transactional/test data this
// button is meant to clear. The reset is all-or-nothing by design (it cannot
// cherry-pick individual sales, so it can't be used to hide specific takings)
// and records an audit entry with how many sales were archived and the batch
// id. Manager-gated at the handler.
//
// Known consequence of the report_archive exclusion above (ADR-0040
// Consequences section flags this explicitly as needing a check at
// implementation time — this is that check, not yet a full fix): if an EOD
// report was already generated for today BEFORE this reset runs,
// generateEOD's own (kind, period) idempotency means today's EOD cannot be
// regenerated afterward with the post-reset figures — the pre-reset
// (test-data) report for today survives permanently. A manager doing a
// go-live reset on the same day test EOD reports were run should reset
// before generating any further EOD reports that day; there is no
// automatic reconciliation. See settings.data.reset_confirm_dialog.
func (r *POSRepo) ResetTransactionHistory(ctx context.Context, actorID string) (int64, string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback() }()

	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sales`).Scan(&count); err != nil {
		return 0, "", fmt.Errorf("reset: count sales: %w", err)
	}

	batchID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO reset_batches (id, created_at, actor_id, sales_count)
VALUES (?, ?, ?, ?)`, batchID, now, nullIfEmpty(actorID), count); err != nil {
		return 0, "", fmt.Errorf("reset: insert batch: %w", err)
	}

	// Children before parents (see resetArchiveTables): copy each table into
	// its archive twin tagged with the batch, then clear the live table.
	for _, t := range resetArchiveTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s_archive (%s, reset_batch_id) SELECT %s, ? FROM %s`,
			t.live, t.cols, t.cols, t.live), batchID); err != nil {
			return 0, "", fmt.Errorf("reset: archive %s: %w", t.live, err)
		}
		// invoices self-FK (original_invoice_id → invoices.id): clear credit
		// notes before their originals so row-by-row FK enforcement can't
		// trip on a wholesale delete's arbitrary row order.
		if t.live == "invoices" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM invoices WHERE original_invoice_id IS NOT NULL`); err != nil {
				return 0, "", fmt.Errorf("reset: clear credit notes: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.live); err != nil {
			return 0, "", fmt.Errorf("reset: clear %s: %w", t.live, err)
		}
	}

	if err := r.InsertAudit(ctx, tx, actorID, "system", "transactions", "transaction_history_reset",
		map[string]any{"sales_archived": count, "batch_id": batchID}, now, ""); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return count, batchID, nil
}

// ListResetBatches lists every archived reset event, newest first, for the
// Settings → Data "Reset archives" list and GET /api/data/reset-archives.
//
// Purgeable/RetainedUntil (ut-docs#698) are computed here read-only, using
// the exact same resolveArchiveMinDays + computeRetainedUntil math
// DeleteResetBatch itself gates on -- so the UI can show a batch's
// eligibility before an operator ever attempts (and gets refused) a purge.
// The country lookup is resolved ONCE for the whole list (it depends on the
// shop's configured country, not on any individual batch), not per row.
func (r *POSRepo) ListResetBatches(ctx context.Context) ([]ResetBatch, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, created_at, COALESCE(actor_id, ''), sales_count
FROM reset_batches
ORDER BY created_at DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("list reset batches: %w", err)
	}
	defer rows.Close()
	type rawBatch struct {
		ID, CreatedAt, ActorID string
		SalesCount             int64
	}
	var raw []rawBatch
	for rows.Next() {
		var b rawBatch
		if err := rows.Scan(&b.ID, &b.CreatedAt, &b.ActorID, &b.SalesCount); err != nil {
			return nil, fmt.Errorf("scan reset batch: %w", err)
		}
		raw = append(raw, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resolved once, outside the row loop -- see the doc comment above.
	// minDaysErr != nil is fail-safe, not fatal to the listing itself
	// (ut-docs#698): every gated (SalesCount > 0) row falls back to
	// Purgeable=false rather than either failing the whole list or, worse,
	// wrongly showing a row as purgeable when the gate couldn't actually be
	// evaluated.
	minDays, minDaysErr := r.resolveArchiveMinDays(ctx, r.db)

	out := make([]ResetBatch, 0, len(raw))
	for _, b := range raw {
		batch := ResetBatch{ID: b.ID, CreatedAt: b.CreatedAt, ActorID: b.ActorID, SalesCount: b.SalesCount}
		switch {
		case b.SalesCount == 0:
			// Mirrors DeleteResetBatch's own no-trading-history carve-out:
			// no gate applies at all, so RetainedUntil stays the zero
			// time.Time (never a meaningless "purgeable since the epoch").
			batch.Purgeable = true
		case minDaysErr != nil:
			batch.Purgeable = false
		default:
			archivedAt, parseErr := time.Parse(time.RFC3339, b.CreatedAt)
			if parseErr != nil {
				// Defensive only -- created_at is always written by
				// ResetTransactionHistory as time.Now().UTC().Format(RFC3339),
				// so this should never actually trigger. Fail safe exactly
				// like the minDaysErr case above: never wrongly show a row
				// as purgeable when its eligibility couldn't be computed.
				batch.Purgeable = false
			} else {
				batch.RetainedUntil = computeRetainedUntil(archivedAt, minDays)
				batch.Purgeable = !time.Now().UTC().Before(batch.RetainedUntil)
			}
		}
		out = append(out, batch)
	}
	return out, nil
}

// RestoreResetBatch moves every archived row of one reset batch back into
// its live table, deletes the archived copies and the batch header (a
// restored batch stops existing as an archive and cannot be restored twice
// — ADR-0042 §2), and audits the restore under actorID. It returns how many
// sales were restored. Before touching anything it refuses with
// ErrShopHasTradedSinceReset unless sales, held_sales, shifts AND
// stock_movements are all empty, with ErrResetBatchNotFound for an unknown
// batch id, and — discovered in independent review — with
// ErrArchiveReferencesRemoved if a row in the batch points at a catalog/
// customer record removed after the reset (see that error's doc comment).
// Manager-gated at the handler.
func (r *POSRepo) RestoreResetBatch(ctx context.Context, batchID, actorID string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reset_batches WHERE id = ?`, batchID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("restore: look up batch: %w", err)
	}
	if exists == 0 {
		return 0, fmt.Errorf("restore batch %q: %w", batchID, ErrResetBatchNotFound)
	}

	for _, tbl := range restoreEmptyCheckTables {
		var c int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+tbl).Scan(&c); err != nil {
			return 0, fmt.Errorf("restore: check %s empty: %w", tbl, err)
		}
		if c != 0 {
			return 0, fmt.Errorf("restore batch %q: %s has %d row(s): %w", batchID, tbl, c, ErrShopHasTradedSinceReset)
		}
	}

	// Parents before children (reverse of the archive order), so re-inserted
	// rows satisfy the live tables' FKs (sales before sale_lines/payments/…,
	// sale_lines before sale_line_modifiers/stock_movements).
	var restored int64
	for i := len(resetArchiveTables) - 1; i >= 0; i-- {
		t := resetArchiveTables[i]
		// invoices self-FK (original_invoice_id → invoices.id): re-insert
		// original invoices before their credit notes so row-by-row FK
		// enforcement can't trip on the SELECT's arbitrary row order.
		if t.live == "invoices" {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO invoices (`+t.cols+`) SELECT `+t.cols+` FROM invoices_archive WHERE reset_batch_id = ? AND original_invoice_id IS NULL`,
				batchID); err != nil {
				if isForeignKeyViolation(err) {
					return 0, fmt.Errorf("restore batch %q: invoices: %w", batchID, ErrArchiveReferencesRemoved)
				}
				return 0, fmt.Errorf("restore: invoices: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO invoices (`+t.cols+`) SELECT `+t.cols+` FROM invoices_archive WHERE reset_batch_id = ? AND original_invoice_id IS NOT NULL`,
				batchID); err != nil {
				if isForeignKeyViolation(err) {
					return 0, fmt.Errorf("restore batch %q: credit notes: %w", batchID, ErrArchiveReferencesRemoved)
				}
				return 0, fmt.Errorf("restore: credit notes: %w", err)
			}
		} else {
			res, err := tx.ExecContext(ctx, fmt.Sprintf(
				`INSERT INTO %s (%s) SELECT %s FROM %s_archive WHERE reset_batch_id = ?`,
				t.live, t.cols, t.cols, t.live), batchID)
			if err != nil {
				if isForeignKeyViolation(err) {
					return 0, fmt.Errorf("restore batch %q: %s: %w", batchID, t.live, ErrArchiveReferencesRemoved)
				}
				return 0, fmt.Errorf("restore: %s: %w", t.live, err)
			}
			if t.live == "sales" {
				restored, _ = res.RowsAffected()
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s_archive WHERE reset_batch_id = ?`, t.live), batchID); err != nil {
			return 0, fmt.Errorf("restore: clear %s_archive: %w", t.live, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reset_batches WHERE id = ?`, batchID); err != nil {
		return 0, fmt.Errorf("restore: delete batch: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.InsertAudit(ctx, tx, actorID, "system", "transactions", "transaction_history_restored",
		map[string]any{"batch_id": batchID, "sales_restored": restored}, now, ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return restored, nil
}

// resolveArchiveMinDays reads the shop's chosen country (settings key
// StoreCountrySettingsKey) and returns that country's archive_min_days.
// Falls back to GlobalArchiveMinDays -- ADR-0040's global floor -- when no
// country is configured, or the configured code has no country_settings
// row. Correction (independent review, ut-docs#661): the floor is every
// jurisdiction's *minimum* admissible value, not its maximum --
// validateCountrySetting enforces every real row to be at or ABOVE it, so
// a country that has actually been raised beyond the floor is the one case
// this fallback protects less than the truth would. It is still the right
// fallback: it is the one value guaranteed valid for every jurisdiction
// with no row of its own (never "no restriction"), and matches
// common.LoadState's own "GB" default for an unconfigured shop, whose
// seeded country_settings row sits exactly at this floor today.
//
// q accepts either r.db or a caller's *sql.Tx (both satisfy queryRower,
// defined in demo_seed_repo.go) -- DeleteResetBatch passes its own tx so
// the read stays consistent with the batch lookup it gates; ListResetBatches
// (ut-docs#698) has no transaction of its own and passes r.db directly.
func (r *POSRepo) resolveArchiveMinDays(ctx context.Context, q queryRower) (int64, error) {
	var countryCode string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, StoreCountrySettingsKey).Scan(&countryCode)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("resolve archive retention: read shop country: %w", err)
	}
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if countryCode == "" {
		return GlobalArchiveMinDays, nil
	}

	var days int64
	err = q.QueryRowContext(ctx, `SELECT archive_min_days FROM country_settings WHERE code = ?`, countryCode).Scan(&days)
	if err == sql.ErrNoRows {
		return GlobalArchiveMinDays, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve archive retention: read country setting %s: %w", countryCode, err)
	}
	return days, nil
}

// computeRetainedUntil implements ADR-0040's retention math: an archived
// batch becomes purgeable minDays after the shop's own UTC midnight
// following archival, not after the exact time-of-day it was archived
// (ut-docs#699) -- both DeleteResetBatch's refusal message and the API/UI's
// display of RetainedUntil are date-only, so the date named must actually
// become purgeable from its own midnight, or a batch archived at 14:30
// would silently stay refused until 14:30 on the displayed date. Extracted
// (ut-docs#698) so the read-only ListResetBatches path and the enforcing
// DeleteResetBatch path can never compute two different answers for the
// same batch.
func computeRetainedUntil(archivedAt time.Time, minDays int64) time.Time {
	archivedDate := time.Date(archivedAt.Year(), archivedAt.Month(), archivedAt.Day(), 0, 0, 0, 0, time.UTC)
	return archivedDate.AddDate(0, 0, int(minDays))
}

// DeleteResetBatch permanently purges one archived reset-transactions
// batch — ADR-0042 §3's deletion path, left unbuilt until the retention
// decision (ut-docs#635 → #659 → this card, ut-docs#661) supplied a real
// window to gate it on.
//
// A batch with sales_count == 0 holds no completed SALES and is not
// protected — mirrors the product owner's ut-docs#187 condition, which was
// specifically about REAL (non-demo) trading history, and sales_count is
// the closest signal already on hand to "did this batch contain any."
// Named precisely (independent review, ut-docs#661): this does NOT mean
// the batch is empty. It can still hold archived shifts (opening/closing/
// expected cash), stock_movements (cost_price on manual receipts/
// adjustments) or held_sales payloads with zero sales among them — none of
// those are gated by the retention window, on the judgement that a batch
// with no completed sale is what the pre-launch "clear demo data" use case
// actually produces. A batch with sales_count > 0 may only be purged once
// resolveArchiveMinDays' window has elapsed since the batch's created_at
// (when it was archived).
//
// Returns ErrResetBatchNotFound for an unknown id, and
// *ArchiveWithinRetentionWindowError (still within the window) otherwise.
// Manager-gated at the handler, same as reset/restore.
func (r *POSRepo) DeleteResetBatch(ctx context.Context, batchID, actorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt string
	var salesCount int64
	err = tx.QueryRowContext(ctx, `SELECT created_at, sales_count FROM reset_batches WHERE id = ?`, batchID).Scan(&createdAt, &salesCount)
	if err == sql.ErrNoRows {
		return fmt.Errorf("delete batch %q: %w", batchID, ErrResetBatchNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete: look up batch: %w", err)
	}

	if salesCount > 0 {
		minDays, err := r.resolveArchiveMinDays(ctx, tx)
		if err != nil {
			return err
		}
		archivedAt, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return fmt.Errorf("delete: parse batch created_at %q: %w", createdAt, err)
		}
		retainedUntil := computeRetainedUntil(archivedAt, minDays)
		if time.Now().UTC().Before(retainedUntil) {
			return &ArchiveWithinRetentionWindowError{RetainedUntil: retainedUntil}
		}
	}

	for _, t := range resetArchiveTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s_archive WHERE reset_batch_id = ?`, t.live), batchID); err != nil {
			return fmt.Errorf("delete: clear %s_archive: %w", t.live, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reset_batches WHERE id = ?`, batchID); err != nil {
		return fmt.Errorf("delete: delete batch: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.InsertAudit(ctx, tx, actorID, "system", "transactions", "transaction_archive_purged",
		map[string]any{"batch_id": batchID, "sales_count": salesCount}, now, ""); err != nil {
		return err
	}
	return tx.Commit()
}
