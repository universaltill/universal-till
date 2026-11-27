package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Plugin-records channel storage (ut-docs#3697, ADR-0149 §3): the replica's
// outbox, the main till's dedupe inbox and a plugin's main-owned snapshot
// values. All SQL for the channel lives here (guard-data-access.sh). The
// tables are written only when a plugin declares records_forward, so a till
// with no such plugin never touches them.

// Bounds of the channel, one place (ut-docs#3697 §1.4); each has a test.
const (
	RecordMaxPayload      = 64 << 10            // one record / snapshot value
	RecordMaxRowsPlugin   = 2000                // pending rows per plugin
	RecordMaxBytesPlugin  = 8 << 20             // pending bytes per plugin
	RecordMaxRowsAll      = 20000               // pending rows, all plugins
	RecordPendingMaxAge   = 30 * 24 * time.Hour // older pending = rejected "expired"
	RecordRejectedKeep    = 200                 // rejected rows kept per plugin
	RecordRejectedMaxAge  = 30 * 24 * time.Hour // ... and for this long
	RecordWireBatchMax    = 20                  // records per POST
	RecordWireBatchBytes  = 1 << 20             // payload bytes per POST (body cap is 1.5 MiB)
	RecordInboxKeepAge    = 90 * 24 * time.Hour // > RecordPendingMaxAge
	RecordInboxMaxPlugin  = 200000              // inbox rows per plugin
	RecordStatusMaxRows   = 200                 // rows records_status returns
	SnapshotMaxBytes      = RecordMaxPayload    // one snapshot collection
	SnapshotMaxPerPlugin  = 8                   // collections per plugin
	SnapshotMaxPerTill    = 40                  // collections per till
	recordBackoffBase     = 30 * time.Second    // first retry delay
	recordBackoffCeil     = time.Hour           // retry delay ceiling
	recordBackoffJitterPc = 20                  // +/- percent
)

// Terminal reasons an outbox row is rejected with.
const (
	RecordReasonOverflow     = "overflow"
	RecordReasonExpired      = "expired"
	RecordReasonTillPromoted = "till_promoted"
	RecordReasonBadBatch     = "bad_batch"
)

var (
	// ErrRecordTooLarge: a record or snapshot value over its byte cap.
	ErrRecordTooLarge = errors.New("plugin record too large")
	// ErrRecordInvalid: not a JSON value of the expected kind.
	ErrRecordInvalid = errors.New("plugin record invalid")
	// ErrRecordQuota: a snapshot collection count cap.
	ErrRecordQuota = errors.New("plugin record quota exceeded")

	// RecordCollectionRe / RecordIDRe: the shapes a collection name and a
	// record id must have (also enforced on the main till, which never
	// trusts the sender).
	RecordCollectionRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	RecordIDRe         = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

type recordLimits struct {
	maxRows, maxBytes, maxRowsAll, keepRejected, inboxMax, snapPerPlugin, snapPerTill int
}

// PluginRecordsRepo is the plugin-records store.
type PluginRecordsRepo struct {
	db     *sql.DB
	limits recordLimits
}

// NewPluginRecordsRepo returns a repo with the production bounds.
func NewPluginRecordsRepo(db *sql.DB) *PluginRecordsRepo {
	return &PluginRecordsRepo{db: db, limits: recordLimits{
		maxRows: RecordMaxRowsPlugin, maxBytes: RecordMaxBytesPlugin, maxRowsAll: RecordMaxRowsAll,
		keepRejected: RecordRejectedKeep, inboxMax: RecordInboxMaxPlugin,
		snapPerPlugin: SnapshotMaxPerPlugin, snapPerTill: SnapshotMaxPerTill,
	}}
}

func recTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// OutboxRecord is one queued record.
type OutboxRecord struct {
	Seq        int64
	PluginID   string
	Collection string
	RecordID   string
	Payload    []byte
	Attempts   int
	CreatedAt  string
}

// RecordStatusRow is one row of records_status.
type RecordStatusRow struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Attempts int    `json:"attempts"`
}

// RecordBackoff is the delay before the next attempt after `attempts`
// failures: 30s * 2^attempts, capped at 1h, then jitter in [-1, 1] scaled to
// +/-20%. A caller passes jitter from its own random source (0 in tests).
func RecordBackoff(attempts int, jitter float64) time.Duration {
	d := recordBackoffBase
	for i := 0; i < attempts && d < recordBackoffCeil; i++ {
		d *= 2
	}
	if d > recordBackoffCeil {
		d = recordBackoffCeil
	}
	if jitter > 1 {
		jitter = 1
	} else if jitter < -1 {
		jitter = -1
	}
	return d + time.Duration(float64(d)*jitter*recordBackoffJitterPc/100)
}

// Enqueue stores one record for the replica push loop and returns how many
// older pending rows it had to reject to stay within the caps (the card's
// "oldest rows rejected, not grown"). The same (plugin, collection, id)
// again is a no-op. Never deletes a pending row.
func (r *PluginRecordsRepo) Enqueue(ctx context.Context, pluginID, collection, id string, payload []byte, now time.Time) (overflowed int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_record_outbox
  (plugin_id, collection, record_id, payload, bytes, next_attempt_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pluginID, collection, id, string(payload), len(payload), recTS(now), recTS(now))
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, tx.Commit()
	}
	// Per plugin: rows, then bytes; then the whole till. Oldest first.
	for {
		var rows, bytes int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM plugin_record_outbox WHERE plugin_id = ? AND status = 'pending'`,
			pluginID).Scan(&rows, &bytes); err != nil {
			return 0, err
		}
		if rows <= r.limits.maxRows && bytes <= r.limits.maxBytes {
			break
		}
		if rows <= 1 {
			break // a single row always fits (<= RecordMaxPayload < byte cap)
		}
		if err := rejectOldestPending(ctx, tx, `plugin_id = ?`, []any{pluginID}, now); err != nil {
			return 0, err
		}
		overflowed++
	}
	for {
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_record_outbox WHERE status = 'pending'`).Scan(&rows); err != nil {
			return 0, err
		}
		if rows <= r.limits.maxRowsAll || rows <= 1 {
			break
		}
		if err := rejectOldestPending(ctx, tx, `1=1`, nil, now); err != nil {
			return 0, err
		}
		overflowed++
	}
	return overflowed, tx.Commit()
}

func rejectOldestPending(ctx context.Context, tx *sql.Tx, where string, args []any, now time.Time) error {
	a := append([]any{RecordReasonOverflow, recTS(now)}, args...)
	_, err := tx.ExecContext(ctx, `
UPDATE plugin_record_outbox SET status = 'rejected', reason = ?, rejected_at = ?
WHERE seq = (SELECT MIN(seq) FROM plugin_record_outbox WHERE status = 'pending' AND `+where+`)`, a...)
	return err
}

// Due returns pending rows whose next attempt is due, oldest first, at most
// limit rows and about maxBytes of payload (always at least one row).
func (r *PluginRecordsRepo) Due(ctx context.Context, now time.Time, limit, maxBytes int) ([]OutboxRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT seq, plugin_id, collection, record_id, payload, attempts, created_at
FROM plugin_record_outbox
WHERE status = 'pending' AND next_attempt_at <= ?
ORDER BY seq LIMIT ?`, recTS(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRecord
	used := 0
	for rows.Next() {
		var o OutboxRecord
		var payload string
		if err := rows.Scan(&o.Seq, &o.PluginID, &o.Collection, &o.RecordID, &payload, &o.Attempts, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Payload = []byte(payload)
		if len(out) > 0 && used+len(o.Payload) > maxBytes {
			break
		}
		used += len(o.Payload)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete removes a delivered row.
func (r *PluginRecordsRepo) Delete(ctx context.Context, seq int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM plugin_record_outbox WHERE seq = ?`, seq)
	return err
}

// Retry schedules another attempt after the backoff for the row's attempts.
func (r *PluginRecordsRepo) Retry(ctx context.Context, seq int64, now time.Time, jitter float64) error {
	var attempts int
	if err := r.db.QueryRowContext(ctx, `SELECT attempts FROM plugin_record_outbox WHERE seq = ?`, seq).Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE plugin_record_outbox SET attempts = attempts + 1, next_attempt_at = ? WHERE seq = ? AND status = 'pending'`,
		recTS(now.Add(RecordBackoff(attempts, jitter))), seq)
	return err
}

// MarkRejected makes a pending row terminal.
func (r *PluginRecordsRepo) MarkRejected(ctx context.Context, seq int64, reason string, now time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE plugin_record_outbox SET status = 'rejected', reason = ?, rejected_at = ? WHERE seq = ? AND status = 'pending'`,
		reason, recTS(now), seq)
	return err
}

// RejectAllPendingRecords marks every pending row rejected inside the
// caller's transaction (a promoted till can never send them).
func RejectAllPendingRecords(ctx context.Context, tx SyncQuerier, reason string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE plugin_record_outbox SET status = 'rejected', reason = ?, rejected_at = ? WHERE status = 'pending'`,
		reason, recTS(now))
	return err
}

// DeletePluginRecords drops one plugin's outbox, inbox and snapshot rows
// inside the caller's transaction (an uninstall; ut-docs#3697 review M1) and
// returns how many rows went. A snapshot delete on a main bumps the admin
// generation, so replicas drop it on their next pull.
func DeletePluginRecords(ctx context.Context, tx SyncQuerier, pluginID string) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM plugin_record_outbox WHERE plugin_id = ?`,
		`DELETE FROM plugin_record_inbox WHERE plugin_id = ?`,
		`DELETE FROM plugin_snapshot WHERE plugin_id = ?`,
	} {
		res, err := tx.ExecContext(ctx, q, pluginID)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// Status lists one plugin's rows for a collection, capped: rejected rows
// first, then the rest, newest first within each group (review M3), so past
// the cap a plugin still sees what failed and what it just forwarded.
func (r *PluginRecordsRepo) Status(ctx context.Context, pluginID, collection string) ([]RecordStatusRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT record_id, status, reason, attempts FROM plugin_record_outbox
WHERE plugin_id = ? AND collection = ?
ORDER BY CASE status WHEN 'rejected' THEN 0 ELSE 1 END, seq DESC LIMIT ?`, pluginID, collection, RecordStatusMaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordStatusRow{}
	for rows.Next() {
		var s RecordStatusRow
		if err := rows.Scan(&s.ID, &s.Status, &s.Reason, &s.Attempts); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RejectedByPlugin counts rejected rows per plugin (for the Problems entry).
func (r *PluginRecordsRepo) RejectedByPlugin(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT plugin_id, COUNT(*) FROM plugin_record_outbox WHERE status = 'rejected' GROUP BY plugin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// SweepResult is what one Sweep changed.
type SweepResult struct{ Expired, Purged int }

// Sweep rejects pending rows older than RecordPendingMaxAge (reason
// "expired") and deletes rejected rows past RecordRejectedMaxAge or beyond
// the per-plugin keep count (newest kept).
func (r *PluginRecordsRepo) Sweep(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	exp, err := r.db.ExecContext(ctx, `
UPDATE plugin_record_outbox SET status = 'rejected', reason = ?, rejected_at = ?
WHERE status = 'pending' AND created_at < ?`,
		RecordReasonExpired, recTS(now), recTS(now.Add(-RecordPendingMaxAge)))
	if err != nil {
		return res, err
	}
	n, _ := exp.RowsAffected()
	res.Expired = int(n)
	old, err := r.db.ExecContext(ctx,
		`DELETE FROM plugin_record_outbox WHERE status = 'rejected' AND rejected_at < ?`, recTS(now.Add(-RecordRejectedMaxAge)))
	if err != nil {
		return res, err
	}
	n, _ = old.RowsAffected()
	res.Purged = int(n)
	over, err := r.db.ExecContext(ctx, `
DELETE FROM plugin_record_outbox WHERE status = 'rejected' AND seq IN (
  SELECT seq FROM (
    SELECT seq, ROW_NUMBER() OVER (PARTITION BY plugin_id ORDER BY seq DESC) AS rn
    FROM plugin_record_outbox WHERE status = 'rejected'
  ) WHERE rn > ?)`, r.limits.keepRejected)
	if err != nil {
		return res, err
	}
	n, _ = over.RowsAffected()
	res.Purged += int(n)
	return res, nil
}

// InboxHas reports whether the main till already applied this record.
func (r *PluginRecordsRepo) InboxHas(ctx context.Context, pluginID, collection, id string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugin_record_inbox WHERE plugin_id = ? AND collection = ? AND record_id = ?`,
		pluginID, collection, id).Scan(&n)
	return n > 0, err
}

// InboxAdd records an applied record; a repeat is a no-op.
func (r *PluginRecordsRepo) InboxAdd(ctx context.Context, pluginID, collection, id, fromTillID string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_record_inbox (plugin_id, collection, record_id, from_till_id, received_at)
VALUES (?, ?, ?, ?, ?)`, pluginID, collection, id, fromTillID, recTS(now))
	return err
}

// InboxTrim drops ledger rows older than RecordInboxKeepAge, then the oldest
// beyond the per-plugin cap.
func (r *PluginRecordsRepo) InboxTrim(ctx context.Context, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM plugin_record_inbox WHERE received_at < ?`,
		recTS(now.Add(-RecordInboxKeepAge))); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
DELETE FROM plugin_record_inbox WHERE rowid IN (
  SELECT rowid FROM (
    SELECT rowid, ROW_NUMBER() OVER (PARTITION BY plugin_id ORDER BY received_at DESC, rowid DESC) AS rn
    FROM plugin_record_inbox
  ) WHERE rn > ?)`, r.limits.inboxMax)
	return err
}

// PluginSnapshotRow is one snapshot value on the admin-bundle wire.
type PluginSnapshotRow struct {
	PluginID   string          `json:"plugin_id"`
	Collection string          `json:"collection"`
	Version    int64           `json:"version"`
	Payload    json.RawMessage `json:"payload"`
}

// SnapshotSet stores a plugin's snapshot value. changed is false (and
// nothing is written, so no generation bump) when the bytes are identical.
func (r *PluginRecordsRepo) SnapshotSet(ctx context.Context, pluginID, collection string, payload []byte, now time.Time) (changed bool, err error) {
	if len(payload) > SnapshotMaxBytes {
		return false, ErrRecordTooLarge
	}
	if !json.Valid(payload) {
		return false, ErrRecordInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var cur string
	var ver int64
	err = tx.QueryRowContext(ctx,
		`SELECT payload, version FROM plugin_snapshot WHERE plugin_id = ? AND collection = ?`, pluginID, collection).Scan(&cur, &ver)
	switch {
	case err == nil:
		if cur == string(payload) {
			return false, nil
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE plugin_snapshot SET payload = ?, version = ?, updated_at = ? WHERE plugin_id = ? AND collection = ?`,
			string(payload), ver+1, recTS(now), pluginID, collection); err != nil {
			return false, err
		}
	case errors.Is(err, sql.ErrNoRows):
		var mine, all int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_snapshot WHERE plugin_id = ?`, pluginID).Scan(&mine); err != nil {
			return false, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_snapshot`).Scan(&all); err != nil {
			return false, err
		}
		if mine >= r.limits.snapPerPlugin || all >= r.limits.snapPerTill {
			return false, ErrRecordQuota
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO plugin_snapshot (plugin_id, collection, payload, version, updated_at) VALUES (?, ?, ?, 1, ?)`,
			pluginID, collection, string(payload), recTS(now)); err != nil {
			return false, err
		}
	default:
		return false, err
	}
	return true, tx.Commit()
}

// SnapshotGet reads one stored snapshot value.
func (r *PluginRecordsRepo) SnapshotGet(ctx context.Context, pluginID, collection string) ([]byte, bool, error) {
	var p string
	err := r.db.QueryRowContext(ctx,
		`SELECT payload FROM plugin_snapshot WHERE plugin_id = ? AND collection = ?`, pluginID, collection).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(p), true, nil
}

// listSnapshots reads every snapshot for the admin bundle.
func listSnapshots(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]PluginSnapshotRow, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT plugin_id, collection, version, payload FROM plugin_snapshot ORDER BY plugin_id, collection`)
	if err != nil {
		return nil, fmt.Errorf("dump plugin_snapshot: %w", err)
	}
	defer rows.Close()
	var out []PluginSnapshotRow
	for rows.Next() {
		var s PluginSnapshotRow
		var p string
		if err := rows.Scan(&s.PluginID, &s.Collection, &s.Version, &p); err != nil {
			return nil, err
		}
		s.Payload = json.RawMessage(p)
		out = append(out, s)
	}
	return out, rows.Err()
}

// replacePluginSnapshots makes plugin_snapshot equal rows inside tx (the
// primary wins, deletes propagate). Only differing rows are written so an
// unchanged pull does not move the admin generation.
func replacePluginSnapshots(ctx context.Context, tx *sql.Tx, rows []PluginSnapshotRow, now time.Time) error {
	have := map[[2]string]PluginSnapshotRow{}
	cur, err := listSnapshots(ctx, tx)
	if err != nil {
		return err
	}
	for _, c := range cur {
		have[[2]string{c.PluginID, c.Collection}] = c
	}
	for _, row := range rows {
		if len(row.Payload) > SnapshotMaxBytes || !json.Valid(row.Payload) ||
			!RecordCollectionRe.MatchString(row.Collection) || row.PluginID == "" {
			continue // external input: a bad row is skipped, never stored
		}
		k := [2]string{row.PluginID, row.Collection}
		if c, ok := have[k]; ok && c.Version == row.Version && string(c.Payload) == string(row.Payload) {
			delete(have, k)
			continue
		}
		delete(have, k)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_snapshot (plugin_id, collection, payload, version, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(plugin_id, collection) DO UPDATE SET payload = excluded.payload, version = excluded.version, updated_at = excluded.updated_at`,
			row.PluginID, row.Collection, string(row.Payload), row.Version, recTS(now)); err != nil {
			return err
		}
	}
	for k := range have {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_snapshot WHERE plugin_id = ? AND collection = ?`, k[0], k[1]); err != nil {
			return err
		}
	}
	return nil
}

// PluginState reports whether a plugin row exists and is enabled; the main
// till tells "unknown" from "inactive" when it refuses a record.
func (r *PluginRecordsRepo) PluginState(ctx context.Context, pluginID string) (installed, active bool, err error) {
	var a int
	err = r.db.QueryRowContext(ctx, `SELECT is_active FROM plugins WHERE id = ?`, pluginID).Scan(&a)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, a == 1, nil
}

// IsJSONObject reports whether b is exactly one JSON object (not null, an
// array or a scalar): the shape of a record payload.
func IsJSONObject(b []byte) bool {
	var probe map[string]json.RawMessage
	return json.Unmarshal(b, &probe) == nil && probe != nil
}
