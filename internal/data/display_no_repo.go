package data

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Order numbers (ut-docs#6663, ADR-0166 amendment 2026-11-18). The order
// number (sales.display_no) is the short number a person reads out or waits
// for. It never carries a prefix: each till owns a block of orderNoBlock
// numbers ((till number - 1) * 100 .. +99; till 1 is 1..99), so two tills
// never show the same number, and the counter wraps inside the block,
// skipping numbers an open order or a held counter order still holds; when
// the whole block is held it reuses the number given out longest ago. The
// receipt number stays the permanent, prefixed identity.

// orderNoBlock is the size of one till's order-number block.
const orderNoBlock = 100

// laneKnown reports whether prefix names a till the order-number blocks can
// place: none, the first main till's 1-, a joined till's <n>-/T<n>-, or a
// replacement main till's M<8hex>-/letter prefix. Anything else (a restored
// copy's R<4hex>-, a hand-set prefix) keeps the older prefixed count.
func laneKnown(prefix string) bool {
	return prefix == "" || firstTillPrefixRe.MatchString(prefix) || tillNoOf(prefix) > 0 || IsReplacementMainTillPrefix(prefix)
}

// orderNoLane is the block of a till: the first and last number, as plain
// integers. Till 1 starts at 1 (there is no order 0), others at their base.
func orderNoLane(tillNo int) (lo, hi int64) {
	base := int64(tillNo-1) * orderNoBlock
	lo, hi = base, base+orderNoBlock-1
	if lo < 1 {
		lo = 1
	}
	return lo, hi
}

// NextDisplayNo allocates the next order number for a sale -- an ADDITIONAL
// identity to receipt_no, never a replacement (NextReceiptNo allocates that
// separately). Offline-safe: local SQLite reads and one write in the
// caller's own transaction. A crash between allocating and inserting leaves
// a gap, never a collision.
//
// The scheme (sale.display_no_scheme) is read inside exec(tx), not passed
// in, so a tx == nil caller (a test) falls back to r.db, and the "eod"
// boundary is read inline through exec rather than r.LatestArchivedAt (which
// uses r.db: a real deadlock from inside CompleteSale's own transaction).
// trading_period_reset (default) restarts at the block's first number after
// each EOD close; lifetime_no_reset only wraps.
func (r *POSRepo) NextDisplayNo(ctx context.Context, tx *sql.Tx) (string, error) {
	exec := r.exec(tx)
	prefix := receiptPrefix(ctx, exec)
	var scheme string
	_ = exec.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, SaleDisplayNoSchemeKey).Scan(&scheme)
	lifetime := scheme == DisplayNoSchemeLifetimeNoReset

	// boundary is the latest EOD close, as stored; since is the same instant
	// in sales.created_at's own shape (report_archive.created_at is
	// archiveTimestampFmt, space-separated, sales' is RFC3339: a bare string
	// compare is wrong, 'T' sorts after ' '). A parse failure leaves since ==
	// "" (fail open to unbounded), never an error.
	var boundary, since string
	if !lifetime {
		var raw sql.NullString
		_ = exec.QueryRowContext(ctx,
			`SELECT MAX(created_at) FROM report_archive WHERE kind = 'eod'`).Scan(&raw)
		if raw.Valid {
			boundary = raw.String
			if ts, err := time.Parse(archiveTimestampFmt, raw.String); err == nil {
				since = ts.UTC().Format(time.RFC3339)
			}
		}
	}

	prefix = strings.TrimSpace(prefix)
	if !laneKnown(prefix) {
		return legacyPrefixedDisplayNo(ctx, exec, prefix, since)
	}

	lo, hi := orderNoLane(TillNumberOfPrefix(prefix))
	last, period, ok, err := seriesLast(ctx, exec, numberKindOrder, "")
	if err != nil {
		return "", fmt.Errorf("next display no: %w", err)
	}
	switch {
	case !ok:
		// First use (a new till, or one upgraded from the MAX-based count):
		// continue above the numbers this period already issued in the lane.
		var top sql.NullInt64
		if err := exec.QueryRowContext(ctx, `
SELECT MAX(CAST(display_no AS INTEGER)) FROM sales
WHERE display_no GLOB '[0-9]*' AND display_no NOT GLOB '*[^0-9]*'
  AND CAST(display_no AS INTEGER) BETWEEN ? AND ? AND created_at >= ?`,
			lo, hi, since).Scan(&top); err != nil {
			return "", fmt.Errorf("next display no: %w", err)
		}
		last = lo - 1
		if top.Valid {
			last = top.Int64
		}
	case !lifetime && period != boundary, last < lo-1, last > hi:
		last = lo - 1 // a new trading period, or a pointer outside this till's block
	}

	size := hi - lo + 1
	pick, oldest, oldestAt := int64(0), int64(0), ""
	for i := int64(1); i <= size; i++ {
		cand := last + i
		if cand > hi {
			cand = lo + (cand-hi-1)%size
		}
		heldAt := displayNoHeldSince(ctx, exec, strconv.FormatInt(cand, 10), since)
		if heldAt == "" {
			pick = cand
			break
		}
		if oldestAt == "" || heldAt < oldestAt {
			oldest, oldestAt = cand, heldAt
		}
	}
	if pick == 0 {
		// Every number of the block is on an open order: reuse the one that
		// was given out longest ago. Never fail the sale. This is the normal
		// state of a shop that never marks orders collected, so it is logged
		// once per run, not per sale.
		pick = oldest
		blockFullLogOnce.Do(func() {
			logging.L().Warnf("order numbers: all %d numbers of this till's block are on open orders; reusing the oldest (%d)", size, pick)
		})
	}
	if err := storeSeries(ctx, exec, numberKindOrder, "", pick, boundary); err != nil {
		return "", fmt.Errorf("next display no: %w", err)
	}
	return strconv.FormatInt(pick, 10), nil
}

// blockFullLogOnce keeps the "block is full" warning to one line per run.
var blockFullLogOnce sync.Once

// displayNoHeldSince is when order number no was last given to an order that
// still holds it: an open sale of the current numbering period (created
// after since; "" = any time, the lifetime scheme) by the Order status
// list's own predicate, or a held counter order. "" when nothing holds it.
// The newest holder is the one that counts: with several open orders on one
// number (a shop that never marks orders collected), "oldest" must mean the
// number unused for longest, or the reuse would stick on one number. Sales
// from before the last day close do not hold a number, so the count starts
// again at the block's first number after a close. A query error reads as
// "not held": the allocation must never fail a sale.
func displayNoHeldSince(ctx context.Context, exec dbExecutor, no, since string) string {
	latest := ""
	var at sql.NullString
	if err := exec.QueryRowContext(ctx,
		`SELECT created_at FROM sales WHERE display_no = ? AND created_at >= ? AND `+openOrderPredicate+`
ORDER BY created_at DESC LIMIT 1`, no, since).Scan(&at); err == nil && at.Valid {
		latest = at.String
	}
	var held sql.NullString
	if err := exec.QueryRowContext(ctx,
		`SELECT MAX(created_at) FROM kiosk_counter_orders WHERE status = ? AND display_no = ?`,
		KioskCounterOrderStatusHeld, counterOrderDisplayNoPrefix+no).Scan(&held); err == nil && held.Valid && held.String > latest {
		latest = held.String
	}
	return latest
}

// legacyPrefixedDisplayNo is the pre-#6663 count, kept only for a restored
// copy's provisional R<4hex>- prefix: MAX within the prefix, this period.
func legacyPrefixedDisplayNo(ctx context.Context, exec dbExecutor, prefix, since string) (string, error) {
	var maxVal sql.NullInt64
	if err := exec.QueryRowContext(ctx, `
SELECT COALESCE(MAX(CAST(substr(display_no, ?) AS INTEGER)), 0)
FROM sales WHERE display_no LIKE ? || '%' AND created_at >= ?`,
		len(prefix)+1, prefix, since).Scan(&maxVal); err != nil {
		return "", fmt.Errorf("next display no: %w", err)
	}
	return fmt.Sprintf("%s%d", prefix, maxVal.Int64+1), nil
}

// DisplayNoByReceipt is the order number of the sale with receipt number
// receiptNo ("" when it has none, e.g. a sale from before order numbers).
func (r *POSRepo) DisplayNoByReceipt(ctx context.Context, receiptNo string) string {
	var no sql.NullString
	_ = r.db.QueryRowContext(ctx, `SELECT display_no FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&no)
	return strings.TrimSpace(no.String)
}
