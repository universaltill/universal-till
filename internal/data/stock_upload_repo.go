package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StockUploadMaxMilli bounds an uploaded quantity (thousandths): one billion
// units, the cloud's own limit (ut-cloud internal/stockmirror.MaxOnHandMilli).
// A larger stored quantity is sent clamped, so a bad row can never make the
// cloud refuse a whole batch.
const StockUploadMaxMilli int64 = 1_000_000_000_000

// StockUploadRow is one owed on-hand row, ready for the wire: the cloud ids
// of its item (for a variant row, the parent item) and variant, the location,
// the quantity in integer thousandths and the mark's sequence.
type StockUploadRow struct {
	Seq            int64
	ItemCloudID    string
	VariantCloudID string // "" for an item's own stock
	LocationID     string
	OnHandMilli    int64
}

// StockUploadRepo reads and clears stock_upload_outbox (migration 091,
// ADR-0169 §8, ut-docs#4763). The triggers write the marks; nothing here
// runs on the sale path.
type StockUploadRepo struct{ db *sql.DB }

// NewStockUploadRepo returns the repository.
func NewStockUploadRepo(db *sql.DB) *StockUploadRepo { return &StockUploadRepo{db: db} }

// pendingSQL reads owed rows past a sequence cursor. The inventory join uses
// IS (not COALESCE(..)=COALESCE(..)) so SQLite probes ux_inventory_item
// (item_id, variant_id, location_id) instead of scanning inventory once per
// mark (ut-docs#4763 review: 12.9 s and a held write lock at 20,000 rows).
// TestStockUploadQueriesUseTheInventoryIndex pins the plan.
//
// The cloud_links lookups are INDEXED BY idx_cloud_links_local (entity,
// local_id): left to itself the planner chose the primary key (entity,
// cloud_id) and walked every link of the entity per mark (4.5 s a page at
// 20,000 random-UUID links; a till never runs ANALYZE, so it cannot learn
// better). TestStockUploadPendingPlan_NeverScansCloudLinks pins it.
const pendingSQL = `
SELECT seq, item_cloud_id, variant_cloud_id, location_id, milli FROM (
  SELECT o.seq AS seq,
         COALESCE((SELECT MIN(cloud_id) FROM cloud_links INDEXED BY idx_cloud_links_local WHERE entity = 'item' AND local_id = COALESCE(o.item_id, iv.item_id)), '') AS item_cloud_id,
         COALESCE((SELECT MIN(cloud_id) FROM cloud_links INDEXED BY idx_cloud_links_local WHERE entity = 'item_variant' AND local_id = o.variant_id), '') AS variant_cloud_id,
         o.location_id AS location_id,
         o.variant_id AS variant_id,
         CAST(MAX(MIN(ROUND(inv.quantity * 1000.0), ?), ?) AS INTEGER) AS milli
  FROM stock_upload_outbox o
  JOIN inventory inv ON inv.item_id IS o.item_id
   AND inv.variant_id IS o.variant_id
   AND inv.location_id = o.location_id
  LEFT JOIN item_variants iv ON iv.id = o.variant_id
  WHERE o.seq > ? AND o.retry_after <= ?
)
WHERE item_cloud_id <> '' AND (variant_id IS NULL OR variant_cloud_id <> '')
ORDER BY seq
LIMIT ?`

// purgeSQL deletes one bounded chunk of marks whose inventory row is gone.
const purgeSQL = `
DELETE FROM stock_upload_outbox WHERE seq IN (
  SELECT o.seq FROM stock_upload_outbox o
  WHERE NOT EXISTS (
    SELECT 1 FROM inventory inv
    WHERE inv.item_id IS o.item_id AND inv.variant_id IS o.variant_id AND inv.location_id = o.location_id)
  LIMIT ?)`

// Pending returns up to limit owed rows with a sequence above after, in
// sequence order. A mark is owed only once its item (and variant) has a cloud
// link (cloud_links): a row the cloud does not know yet waits. A row deferred
// by Defer is skipped until its retry time (now). Each row's quantity is read
// in the same statement as its mark, so a quantity always goes out under the
// sequence of the change that produced it. Quantities are converted to
// integer thousandths here, the DB boundary (stock is kept to three decimals).
func (r *StockUploadRepo) Pending(ctx context.Context, after int64, now time.Time, limit int) ([]StockUploadRow, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.QueryContext(ctx, pendingSQL,
		float64(StockUploadMaxMilli), float64(-StockUploadMaxMilli), after, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("stock upload pending: %w", err)
	}
	defer rows.Close()
	var out []StockUploadRow
	for rows.Next() {
		var x StockUploadRow
		if err := rows.Scan(&x.Seq, &x.ItemCloudID, &x.VariantCloudID, &x.LocationID, &x.OnHandMilli); err != nil {
			return nil, fmt.Errorf("stock upload pending: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// HasSendable reports whether any mark can be sent now (it has an inventory
// row, a cloud link and is not waiting out a retry delay): the cheap check
// the debounce loop makes before it does anything else. Marks that can never
// be sent (an item with no cloud link) do not keep the loop dialling.
func (r *StockUploadRepo) HasSendable(ctx context.Context, now time.Time) (bool, error) {
	rows, err := r.Pending(ctx, 0, now, 1)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// StockUploadMaxAttempts is how many times an `unknown_item` answer keeps a
// mark before it is dropped.
const StockUploadMaxAttempts = 5

// Defer keeps these marks for a later retry: attempts+1 and retry_after =
// now + 15 minutes x attempts (the check-in cadence). A mark that has reached
// StockUploadMaxAttempts is deleted instead; the count of those is returned.
// A stock change in between re-marks the row (attempts back to 0).
func (r *StockUploadRepo) Defer(ctx context.Context, seqs []int64, now time.Time) (dropped int, err error) {
	for len(seqs) > 0 {
		n := min(len(seqs), 400)
		ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
		args := make([]any, 0, n+2)
		for _, s := range seqs[:n] {
			args = append(args, s)
		}
		res, err := r.db.ExecContext(ctx, `DELETE FROM stock_upload_outbox WHERE attempts + 1 >= `+fmt.Sprint(StockUploadMaxAttempts)+` AND seq IN (`+ph+`)`, args...)
		if err != nil {
			return dropped, fmt.Errorf("stock upload defer: %w", err)
		}
		c, _ := res.RowsAffected()
		dropped += int(c)
		upd := append([]any{now.UTC().Unix()}, args...)
		if _, err := r.db.ExecContext(ctx, `UPDATE stock_upload_outbox
SET attempts = attempts + 1,
    retry_after = strftime('%Y-%m-%dT%H:%M:%SZ', ? + 900 * (attempts + 1), 'unixepoch')
WHERE seq IN (`+ph+`)`, upd...); err != nil {
			return dropped, fmt.Errorf("stock upload defer: %w", err)
		}
		seqs = seqs[n:]
	}
	return dropped, nil
}

// Ack removes the marks with exactly these sequences: a change made after
// the rows were read has a newer sequence and stays owed.
func (r *StockUploadRepo) Ack(ctx context.Context, seqs []int64) error {
	for len(seqs) > 0 {
		n := min(len(seqs), 400)
		ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
		args := make([]any, n)
		for i, s := range seqs[:n] {
			args[i] = s
		}
		if _, err := r.db.ExecContext(ctx, `DELETE FROM stock_upload_outbox WHERE seq IN (`+ph+`)`, args...); err != nil {
			return fmt.Errorf("stock upload ack: %w", err)
		}
		seqs = seqs[n:]
	}
	return nil
}

// Stream is this till's stock stream: its id and when it started.
type Stream struct {
	ID        string
	StartedAt time.Time
}

// StockUploadRotateMinInterval is the shortest time between two stream
// rotations (persisted in stock_upload_state.rotated_at).
const StockUploadRotateMinInterval = 30 * time.Minute

// StreamStartLayout is the wire form of Stream.StartedAt (RFC 3339, UTC,
// nanoseconds).
const StreamStartLayout = time.RFC3339Nano

// streamStartTolerance is how far ahead of the clock a stored stream start
// may be before it is treated as a wrong clock's and reset to now.
const streamStartTolerance = time.Minute

// Stream reads the current stream (stock_upload_state). A start more than a
// minute in the future (this till's clock was wrong when the stream began or
// was rotated) is reset to now and saved: a future start outranks every later
// stream at the cloud, and a till only rotates on a stale answer, so it would
// never recover.
func (r *StockUploadRepo) Stream(ctx context.Context) (Stream, error) {
	var id, started string
	if err := r.db.QueryRowContext(ctx, `SELECT stream_id, started_at FROM stock_upload_state WHERE one = 1`).Scan(&id, &started); err != nil {
		return Stream{}, fmt.Errorf("stock upload stream: %w", err)
	}
	t, err := time.Parse(StreamStartLayout, started)
	if err != nil {
		return Stream{}, fmt.Errorf("stock upload stream: started_at: %w", err)
	}
	if now := time.Now().UTC(); t.After(now.Add(streamStartTolerance)) {
		if err := r.ResetStreamStart(ctx); err != nil {
			return Stream{}, err
		}
		return Stream{ID: id, StartedAt: now.Truncate(time.Nanosecond)}, nil
	}
	return Stream{ID: id, StartedAt: t.UTC()}, nil
}

// ResetStreamStart sets the stream's start to now (the stream id is kept):
// the answer to a start in the future, which the cloud refuses
// (400 stream_start_in_future) or would treat as permanent.
func (r *StockUploadRepo) ResetStreamStart(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE stock_upload_state SET started_at = ? WHERE one = 1`,
		time.Now().UTC().Format(StreamStartLayout)); err != nil {
		return fmt.Errorf("stock upload stream: reset start: %w", err)
	}
	return nil
}

// RotationAllowed reports whether the stream may be rotated at now: not if it
// was rotated less than StockUploadRotateMinInterval ago. A rotated_at in the
// future (the clock went backwards) does not block.
func (r *StockUploadRepo) RotationAllowed(ctx context.Context, now time.Time) (bool, error) {
	var at string
	if err := r.db.QueryRowContext(ctx, `SELECT rotated_at FROM stock_upload_state WHERE one = 1`).Scan(&at); err != nil {
		return false, fmt.Errorf("stock upload rotation: %w", err)
	}
	last, err := time.Parse(time.RFC3339, at)
	if err != nil { // never rotated
		return true, nil
	}
	return now.Before(last) || now.Sub(last) >= StockUploadRotateMinInterval, nil
}

// RotateStream gives the till a new stream id (started now, and always
// strictly after the previous stream's start) and owes every inventory row
// again, in one transaction: the recovery when the cloud holds a sequence
// ahead of this till's, or a newer stream than this till's (a restore from a
// backup).
func (r *StockUploadRepo) RotateStream(ctx context.Context) (Stream, error) {
	now := time.Now().UTC()
	next := Stream{ID: uuid.NewString(), StartedAt: now}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT started_at FROM stock_upload_state WHERE one = 1`).Scan(&prev); err != nil && err != sql.ErrNoRows {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	// Strictly after the previous start (the cloud takes a different stream
	// only if it started later) - unless that start is a wrong clock's far
	// future, which a new stream must not inherit.
	if p, perr := time.Parse(StreamStartLayout, prev); perr == nil && !p.After(now.Add(streamStartTolerance)) && !next.StartedAt.After(p) {
		next.StartedAt = p.Add(time.Millisecond)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO stock_upload_state (one, stream_id, started_at, rotated_at) VALUES (1, ?, ?, ?)
ON CONFLICT(one) DO UPDATE SET stream_id = excluded.stream_id, started_at = excluded.started_at, rotated_at = excluded.rotated_at`,
		next.ID, next.StartedAt.Format(StreamStartLayout), now.Format(time.RFC3339)); err != nil {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_upload_outbox`); err != nil {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO stock_upload_outbox (item_id, variant_id, location_id, marked_at)
SELECT item_id, variant_id, location_id, ? FROM inventory`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Stream{}, fmt.Errorf("stock upload rotate: %w", err)
	}
	return next, nil
}

// PurgeOrphans removes up to limit marks whose inventory row no longer
// exists (an item was deleted) and reports how many: one bounded chunk, so
// the write lock is short. Call again while it returns limit.
func (r *StockUploadRepo) PurgeOrphans(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 1
	}
	res, err := r.db.ExecContext(ctx, purgeSQL, limit)
	if err != nil {
		return 0, fmt.Errorf("stock upload purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
