package data

import (
	"context"
	"fmt"
	"time"
)

// ADR-0147 (ut-docs#574, ADR-0040 card 4): the till side of the report
// archive cloud upload. The un-acked report_archive rows ARE the upload
// queue (cloud_acked_at, 001_init.sql) — no separate ledger — and the
// prune predicates of ADR-0040 §2 as amended by ADR-0147 §4 live here too.

// Settings keys shared by internal/pages (the mode picker, the prune step,
// the status chip) and internal/cloudsync (the uploader). They live in this
// package, not internal/pages/common, because cloudsync must not import a
// page package; common.KeyReportRetentionMode aliases the first one.
const (
	// ReportRetentionModeKey is the per-shop retention destination
	// (ADR-0040 §1 as amended by ADR-0147 §1): "till" | "cloud" | "both",
	// empty meaning "till". Shop-wide ("store." prefix, #2997), written at
	// the till only, reported read-only to the cloud (#3390).
	ReportRetentionModeKey = "store.report_retention_mode"
	// ReportArchiveCloudRefusedAtKey is set (UTC RFC3339) when the cloud
	// refused a report upload with 402 subscription_inactive, and cleared by
	// the next accepted upload or when the mode goes back to till. It drives
	// the settings warning and the status-bar chip. The "cloudsync." prefix
	// makes it per-till (PerTillSettingPrefixes): only the main till
	// uploads, and a replica must never inherit the main till's refusal.
	ReportArchiveCloudRefusedAtKey = "cloudsync.report_archive_refused_at"
)

// Retention modes (ReportRetentionModeKey values).
const (
	ReportRetentionModeTill  = "till"
	ReportRetentionModeCloud = "cloud"
	ReportRetentionModeBoth  = "both"
)

// UploadsReportArchives reports whether a retention mode sends report
// archives to the cloud (ADR-0147 §2): cloud and both do, till (and any
// unknown value) does not.
func UploadsReportArchives(mode string) bool {
	return mode == ReportRetentionModeCloud || mode == ReportRetentionModeBoth
}

// PendingReportArchive is one un-acked report_archive row, as the uploader
// sends it.
type PendingReportArchive struct {
	ID      string
	Kind    string
	Period  string
	Content string
}

// ListUnackedReportArchives returns up to limit rows the cloud has not yet
// acknowledged, oldest close first (created_at, then insertion order for
// legacy rows stamped in the same second).
func (r *POSRepo) ListUnackedReportArchives(ctx context.Context, limit int) ([]PendingReportArchive, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, content_json FROM report_archive
WHERE cloud_acked_at IS NULL
ORDER BY created_at, rowid
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unacked report archives: %w", err)
	}
	defer rows.Close()
	var out []PendingReportArchive
	for rows.Next() {
		var p PendingReportArchive
		if err := rows.Scan(&p.ID, &p.Kind, &p.Period, &p.Content); err != nil {
			return nil, fmt.Errorf("scan unacked report archive: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountUnackedReportArchives counts the rows still waiting for a cloud ack.
func (r *POSRepo) CountUnackedReportArchives(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM report_archive WHERE cloud_acked_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unacked report archives: %w", err)
	}
	return n, nil
}

// MarkReportArchiveAcked records that the cloud accepted row id. Only an
// un-acked row is updated, so the first ack time is kept; false means the
// row was already acked or no longer exists (pruned meanwhile).
func (r *POSRepo) MarkReportArchiveAcked(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE report_archive SET cloud_acked_at = ? WHERE id = ? AND cloud_acked_at IS NULL`,
		at.UTC().Format(time.RFC3339), id)
	if err != nil {
		return false, fmt.Errorf("mark report archive acked: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// reportArchiveKeepClause is ANDed into every prune (ADR-0147 §4): never
// delete the row holding its kind's newest created_at, nor the row holding
// its kind's highest z_number. ArchiveReport numbers the next close from
// MAX(z_number) and links prev_closed_at to the highest-Z row;
// LatestArchivedAt (the next close's window start) and the once-a-day guard
// read the newest created_at. Emptying a kind would restart Z at 1 and
// widen the next close's window to the till's whole history.
//
// Correlated on report_archive.kind inside the same DELETE: SQLite
// evaluates the WHERE for every candidate row before deleting any (a
// subquery reading the table being deleted from forces its two-pass
// delete), so this stays one statement, never read-then-delete. Ties on
// created_at (legacy rows stamped in the same second) break on rowid,
// i.e. insertion order. IS NOT, not !=: a kind with no numbered rows yields
// NULL for the second subquery, which must protect nothing rather than make
// the whole predicate NULL.
const reportArchiveKeepClause = `
  AND id IS NOT (SELECT k.id FROM report_archive k
                 WHERE k.kind = report_archive.kind
                 ORDER BY k.created_at DESC, k.rowid DESC LIMIT 1)
  AND id IS NOT (SELECT k.id FROM report_archive k
                 WHERE k.kind = report_archive.kind AND k.z_number IS NOT NULL
                 ORDER BY k.z_number DESC LIMIT 1)`

// PruneReportArchiveAcked deletes every acked row (mode cloud on the main
// till, ADR-0147 §4) except each kind's newest and highest-Z row.
func (r *POSRepo) PruneReportArchiveAcked(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM report_archive WHERE cloud_acked_at IS NOT NULL`+reportArchiveKeepClause)
	if err != nil {
		return 0, fmt.Errorf("prune acked report archive: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneReportArchiveAckedOlderThan deletes rows that are acked AND whose
// period is before cutoff (mode both on the main till, ADR-0147 §4), with
// the same keep rule. cutoff compares as text exactly like
// PruneReportArchiveOlderThan.
func (r *POSRepo) PruneReportArchiveAckedOlderThan(ctx context.Context, cutoff string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM report_archive WHERE cloud_acked_at IS NOT NULL AND period < ?`+reportArchiveKeepClause, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune acked old report archive: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
