package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// number_series (migration 108, ut-docs#6663) stores the last number issued
// per (kind, series). All SQL on it lives here.

// Series kinds.
const (
	numberKindReceipt      = "receipt"
	numberKindInvoice      = "invoice"
	numberKindOrder        = "order"
	numberKindCounterOrder = "counter_order"
)

// seriesLast reads the stored counter; ok is false when no row exists yet.
func seriesLast(ctx context.Context, exec dbExecutor, kind, series string) (last int64, period string, ok bool, err error) {
	err = exec.QueryRowContext(ctx,
		`SELECT last, period FROM number_series WHERE kind = ? AND series = ?`, kind, series).Scan(&last, &period)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("read number series %s/%q: %w", kind, series, err)
	}
	return last, period, true, nil
}

// storeSeries upserts the counter inside the caller's transaction.
func storeSeries(ctx context.Context, exec dbExecutor, kind, series string, last int64, period string) error {
	if _, err := exec.ExecContext(ctx, `
INSERT INTO number_series (kind, series, last, period) VALUES (?, ?, ?, ?)
ON CONFLICT(kind, series) DO UPDATE SET last = excluded.last, period = excluded.period`,
		kind, series, last, period); err != nil {
		return fmt.Errorf("store number series %s/%q: %w", kind, series, err)
	}
	return nil
}

// nextInSeries returns the next number of a series and stores it, in the
// caller's own transaction (exec must be that tx's executor: a second
// connection deadlocks, see NextDisplayNo). An existing row gives last+1; no
// row seeds from seed() (live + archive rows), so a till that already
// cleared its history continues above the archive. floor is a lower bound
// for the last number issued (the restore jump); 0 for none.
func nextInSeries(ctx context.Context, exec dbExecutor, kind, series string, floor int64, seed func() (int64, error)) (int64, error) {
	last, _, ok, err := seriesLast(ctx, exec, kind, series)
	if err != nil {
		return 0, err
	}
	if !ok {
		if last, err = seed(); err != nil {
			return 0, err
		}
	}
	if floor > last {
		last = floor
	}
	next := last + 1
	if next < 1 {
		next = 1
	}
	if err := storeSeries(ctx, exec, kind, series, next, ""); err != nil {
		return 0, err
	}
	return next, nil
}

// maxReceiptSeq is the highest receipt counter under prefix over live and
// archived sales. For the empty prefix only unprefixed numbers count.
func maxReceiptSeq(ctx context.Context, exec dbExecutor, prefix string) (int64, error) {
	var top int64
	for _, tbl := range []string{"sales", "sales_archive"} {
		var v sql.NullInt64
		var err error
		if prefix == "" {
			err = exec.QueryRowContext(ctx, `SELECT MAX(CAST(receipt_no AS INTEGER)) FROM `+tbl+
				` WHERE receipt_no NOT LIKE '%-%'`).Scan(&v)
		} else {
			err = exec.QueryRowContext(ctx, `SELECT MAX(CAST(substr(receipt_no, ?) AS INTEGER)) FROM `+tbl+
				` WHERE receipt_no LIKE ? || '%'`, len(prefix)+1, prefix).Scan(&v)
		}
		if err != nil {
			return 0, fmt.Errorf("max receipt in %s: %w", tbl, err)
		}
		if v.Valid && v.Int64 > top {
			top = v.Int64
		}
	}
	return top, nil
}

// maxInvoiceSeq is the highest invoice_no of a series over live and archived
// invoices.
func maxInvoiceSeq(ctx context.Context, exec dbExecutor, series string) (int64, error) {
	var top int64
	for _, tbl := range []string{"invoices", "invoices_archive"} {
		var v sql.NullInt64
		if err := exec.QueryRowContext(ctx,
			`SELECT MAX(invoice_no) FROM `+tbl+` WHERE series = ?`, series).Scan(&v); err != nil {
			return 0, fmt.Errorf("max invoice in %s: %w", tbl, err)
		}
		if v.Valid && v.Int64 > top {
			top = v.Int64
		}
	}
	return top, nil
}

// receiptPrefix reads this till's receipt prefix (” when unset).
func receiptPrefix(ctx context.Context, exec dbExecutor) string {
	var prefix string
	_ = exec.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'sync.receipt_prefix'`).Scan(&prefix)
	return prefix
}

// restoreFloor is the restore jump floor for prefix (0 when none applies).
func restoreFloor(ctx context.Context, exec dbExecutor, prefix string) int64 {
	var floorPrefix, floorVal string
	if err := exec.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, RestoreReceiptFloorPrefixKey).Scan(&floorPrefix); err != nil || floorPrefix != prefix {
		return 0
	}
	if err := exec.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, RestoreReceiptFloorKey).Scan(&floorVal); err != nil {
		return 0
	}
	var f int64
	if _, err := fmt.Sscan(floorVal, &f); err != nil || f < 0 {
		return 0
	}
	return f
}

// ReseedReceiptCounter forgets this till's stored receipt counter so the
// next allocation re-seeds from the full live + archive scan. The sale
// retry loop calls it after a receipt_no UNIQUE conflict (its transaction
// rolled back the counter bump too), healing a row that fell behind.
func (r *POSRepo) ReseedReceiptCounter(ctx context.Context) error {
	prefix := receiptPrefix(ctx, r.db)
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM number_series WHERE kind = ? AND series = ?`, numberKindReceipt, prefix); err != nil {
		return fmt.Errorf("reseed receipt counter: %w", err)
	}
	return nil
}

// SetFirstReceiptPrefix sets this till's receipt prefix once, at the end of
// first-boot setup (ut-docs#6663): only when sync.receipt_prefix is unset or
// blank and no sale, live or archived, exists, so a till that has traded or
// already holds a prefix (a joined till, a restored copy) is never touched.
// Reports whether it wrote.
func (r *POSRepo) SetFirstReceiptPrefix(ctx context.Context, prefix string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("set first receipt prefix: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds
	if receiptPrefix(ctx, tx) != "" {
		return false, nil
	}
	for _, tbl := range []string{"sales", "sales_archive"} {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+tbl+`)`).Scan(&n); err != nil {
			return false, fmt.Errorf("set first receipt prefix: %w", err)
		}
		if n != 0 {
			return false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES ('sync.receipt_prefix', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		prefix, time.Now().UTC()); err != nil {
		return false, fmt.Errorf("set first receipt prefix: %w", err)
	}
	return true, tx.Commit()
}
