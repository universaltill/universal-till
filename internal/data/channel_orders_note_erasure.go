package data

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Delivery-note erasure on the till (ADR-0215 Decision 9, legal review
// 2026-11-28 R1..R9, ut-docs#6988). The customer's free-text note may carry
// health data, so the till enforces the deadline itself, on its own clock and
// with no cloud call: at start-up, on a timer and before the order UI shows a
// note. A till that was switched off blanks the note when it next starts.
//
// What is erased: the order note and every line note. What stays: the order,
// its lines, totals and statuses, and an audit trail with no content
// (note_present, note_chars = length only, note_viewed_at, note_erased_at,
// note_erased_by; the accepting staff member is the order's accept action).
// The note text is never logged and never part of an audit value.

const (
	// DeliveryNoteRetentionKey is the shop setting: whole hours after the
	// order's terminal state, 1..72, default 24 (R1).
	DeliveryNoteRetentionKey = "delivery.note_retention_hours"
	// DeliveryNoteDefaultRetention and DeliveryNoteMaxRetention are the
	// default and the hard cap of that setting.
	DeliveryNoteDefaultRetention = 24 * time.Hour
	DeliveryNoteMaxRetention     = 72 * time.Hour
	// DeliveryNoteBackstop is how long after receipt the note of an
	// unscheduled order that never reached a terminal state is kept (R3).
	DeliveryNoteBackstop = 14 * 24 * time.Hour
	// DeliveryNoteScheduledBackstop is the same for a scheduled order, counted
	// from its start time, so the kitchen can still act on it (R3).
	DeliveryNoteScheduledBackstop = 72 * time.Hour

	// note_erased_by values (R9).
	NoteErasedByCap      = "job_cap"
	NoteErasedByBackstop = "job_backstop"
	// NoteErasedByRedact: the cloud erased the note at once (customer_redact,
	// shop_redact, store erasure, offboarding) and its next order update came
	// in with no note. The cloud moves a live order to a terminal state in the
	// same update (R8), which Apply records like any other status.
	NoteErasedByRedact = "redact"
	// NoteErasedByCloudEnd: the order ended (staff action, platform cancel or a
	// redact: the contract has no marker to tell them apart yet) and the cloud's
	// update carried no note; the till blanks at once, the safe direction.
	NoteErasedByCloudEnd = "cloud_end"
)

// DeliveryNoteRetention parses the setting value: whole hours 1..72. An empty
// or invalid value gives the 24 h default; a larger value is capped at 72 h
// (never above, R1).
func DeliveryNoteRetention(raw string) time.Duration {
	h, err := strconv.Atoi(strings.TrimSpace(raw))
	switch {
	case err != nil || h < 1:
		return DeliveryNoteDefaultRetention
	case time.Duration(h)*time.Hour > DeliveryNoteMaxRetention:
		return DeliveryNoteMaxRetention
	}
	return time.Duration(h) * time.Hour
}

// IsDeliveryTerminal reports a delivery status that ends the order (R2): the
// note's retention clock starts there. A partial refund is not terminal.
func IsDeliveryTerminal(status string) bool {
	switch status {
	case "collected", "rejected", "cancelled", "refunded", "expired", "failed":
		return true
	}
	return false
}

// deliveryNoteDeadline is when an order's note must be gone and which rule
// says so. Unparseable stored times fail closed: the zero time, so the note
// is erased.
func deliveryNoteDeadline(terminalAt, createdAt, scheduledStartAt string, retention time.Duration) (time.Time, string) {
	if terminalAt != "" {
		if t, err := time.Parse(time.RFC3339, terminalAt); err == nil {
			return t.Add(retention), NoteErasedByCap
		}
		return time.Time{}, NoteErasedByCap
	}
	if scheduledStartAt != "" {
		if t, err := time.Parse(time.RFC3339, scheduledStartAt); err == nil {
			return t.Add(DeliveryNoteScheduledBackstop), NoteErasedByBackstop
		}
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return t.Add(DeliveryNoteBackstop), NoteErasedByBackstop
	}
	return time.Time{}, NoteErasedByBackstop
}

// EraseExpiredDeliveryNotes blanks the notes of every delivery order whose
// deadline has passed at `now` and returns how many orders it blanked. Safe to
// run any time and as often as wanted; a failure on one order stops the run
// with an error and leaves the rest for the next one.
func (r *ChannelOrdersRepo) EraseExpiredDeliveryNotes(ctx context.Context, now time.Time, retention time.Duration) (int, error) {
	if retention <= 0 || retention > DeliveryNoteMaxRetention {
		retention = DeliveryNoteDefaultRetention
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, terminal_at, created_at, scheduled_start_at FROM channel_orders o
WHERE order_kind = 'delivery' AND (note <> '' OR EXISTS (SELECT 1 FROM channel_order_lines l WHERE l.order_id = o.id AND l.note <> ''))`)
	if err != nil {
		return 0, fmt.Errorf("find delivery notes: %w", err)
	}
	type due struct{ id, by string }
	var dues []due
	for rows.Next() {
		var id, term, created, sched string
		if err := rows.Scan(&id, &term, &created, &sched); err != nil {
			rows.Close()
			return 0, err
		}
		if deadline, by := deliveryNoteDeadline(term, created, sched, retention); !now.Before(deadline) {
			dues = append(dues, due{id, by})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(dues) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now.UTC().Format(time.RFC3339)
	for _, d := range dues {
		if err := blankNotes(ctx, tx, d.id, d.by, stamp); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit note erasure: %w", err)
	}
	r.reclaimNoteSpace(ctx)
	return len(dues), nil
}

// blankNotes erases the order note and its line notes and stamps the audit
// columns (first erasure only). No note text reaches an error or a log.
func blankNotes(ctx context.Context, tx *sql.Tx, orderID, by, stamp string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE channel_orders SET note = '',
  note_erased_at = CASE WHEN note_erased_at = '' THEN ? ELSE note_erased_at END,
  note_erased_by = CASE WHEN note_erased_at = '' THEN ? ELSE note_erased_by END
WHERE id = ?`, stamp, by, orderID); err != nil {
		return fmt.Errorf("erase order note: %w", err)
	}
	// SQLite evaluates every SET expression against the old row, so both audit
	// columns describe the first erasure.
	if _, err := tx.ExecContext(ctx, `UPDATE channel_order_lines SET note = '' WHERE order_id = ? AND note <> ''`, orderID); err != nil {
		return fmt.Errorf("erase line notes: %w", err)
	}
	return nil
}

// reclaimNoteSpace asks SQLite to fold the WAL back so the erased pages are
// not left in the log. Best effort: flash storage gives no overwrite
// guarantee either way (the sealed-key design of R6 is a separate card).
func (r *ChannelOrdersRepo) reclaimNoteSpace(ctx context.Context) {
	_, _ = r.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
}

// MarkDeliveryNoteViewed records the first time staff opened an order that
// carries a note (R9). Later views change nothing.
func (r *ChannelOrdersRepo) MarkDeliveryNoteViewed(ctx context.Context, orderID string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE channel_orders SET note_viewed_at = ? WHERE id = ? AND note_viewed_at = '' AND note_present = 1`,
		now.UTC().Format(time.RFC3339), orderID)
	if err != nil {
		return fmt.Errorf("mark note viewed: %w", err)
	}
	return nil
}

// EraseExpiredDeliveryNotesNow runs the erasure with the shop's retention
// setting (DeliveryNoteRetentionKey) at the current time. It is what start-up,
// the timer and the order screens call.
func EraseExpiredDeliveryNotesNow(ctx context.Context, db *sql.DB) (int, error) {
	raw, _, err := NewSettingsRepo(db).Get(ctx, DeliveryNoteRetentionKey)
	if err != nil {
		// An unreadable setting must not keep a note alive: the default applies.
		raw = ""
	}
	return NewChannelOrdersRepo(db).EraseExpiredDeliveryNotes(ctx, time.Now(), DeliveryNoteRetention(raw))
}
