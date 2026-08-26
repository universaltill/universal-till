package data

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// JournalQuarantineEntry is one poison LAN-sync journal entry the primary
// skipped rather than applied (ut-docs#1127, ADR-0065) -- see
// internal/db/migrations/074_sync_journal_quarantine.sql for the table this
// mirrors and why it exists (the durable, queryable record of a quarantined
// entry; the Problems-panel Warnf next to InsertJournalQuarantine's call
// site is the immediate operator signal, this is what survives past the
// 50-entry Problems ring and a restart).
type JournalQuarantineEntry struct {
	ID            string
	TillID        string
	SaleID        string
	ReceiptNo     string
	Reason        string
	PayloadJSON   string
	QuarantinedAt string
}

// InsertJournalQuarantine records one quarantined journal entry. Idempotent
// on sale_id (ON CONFLICT DO NOTHING, matching the table's UNIQUE(sale_id)):
// the LAN-sync cursor is designed to advance past a quarantined entry on the
// very next successful batch response, so a second insert for the same sale
// should never happen in practice -- this is defense in depth, not the
// primary safeguard, so it silently keeps the first-recorded reason rather
// than erroring the caller for what would otherwise re-poison the batch this
// mechanism exists to unblock.
func (r *POSRepo) InsertJournalQuarantine(ctx context.Context, e JournalQuarantineEntry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO sync_journal_quarantine (id, till_id, sale_id, receipt_no, reason, payload_json, quarantined_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (sale_id) DO NOTHING
`, e.ID, e.TillID, e.SaleID, e.ReceiptNo, e.Reason, e.PayloadJSON, e.QuarantinedAt)
	if err != nil {
		return fmt.Errorf("insert sync_journal_quarantine: %w", err)
	}
	return nil
}

// CountJournalQuarantine reports how many quarantined entries exist, total
// -- cheap enough (COUNT(*), no row materialization) for a per-page-render
// banner (ut-docs#1133: the Settings page's Tills card, and the primary-side
// sync chip) to poll without paying ListJournalQuarantine's row-scan cost
// just to learn a length.
func (r *POSRepo) CountJournalQuarantine(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_journal_quarantine`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sync_journal_quarantine: %w", err)
	}
	return n, nil
}

// ListJournalQuarantine returns quarantined entries newest-first, capped at
// limit -- the queryable side of ADR-0065's "record it somewhere queryable"
// requirement. Read by the /sync-quarantine admin page (ut-docs#1133,
// ADR-0065's own "Not decided here" follow-up) and by the regression tests
// below.
func (r *POSRepo) ListJournalQuarantine(ctx context.Context, limit int) ([]JournalQuarantineEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, till_id, sale_id, receipt_no, reason, payload_json, quarantined_at
FROM sync_journal_quarantine
ORDER BY quarantined_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sync_journal_quarantine: %w", err)
	}
	defer rows.Close()

	var out []JournalQuarantineEntry
	for rows.Next() {
		var e JournalQuarantineEntry
		if err := rows.Scan(&e.ID, &e.TillID, &e.SaleID, &e.ReceiptNo, &e.Reason, &e.PayloadJSON, &e.QuarantinedAt); err != nil {
			return nil, fmt.Errorf("scan sync_journal_quarantine: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
