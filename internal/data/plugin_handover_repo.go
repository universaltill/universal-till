package data

import (
	"context"
	"database/sql"
	"time"
)

// PluginHandoverRepo owns the plugin_handover table (122_plugin_handover.sql):
// the per-till record of the core->plugin data handover (ut-docs#3157).
type PluginHandoverRepo struct{ db *sql.DB }

func NewPluginHandoverRepo(db *sql.DB) *PluginHandoverRepo { return &PluginHandoverRepo{db: db} }

// HandoverCandidate is an active wasm plugin that declares the handover hook
// and still owes an answer.
type HandoverCandidate struct {
	PluginID string
	Version  string
	Attempts int
}

const handoverTime = time.RFC3339Nano

// ListDue returns the plugins to ask now: active, wasm, not broken, an active
// hook for event, no done_at, fewer than maxAttempts attempts and past their
// backoff. Attempts and backoff belong to the plugin version that was asked
// (asked_version): after an upgrade the new version starts from zero, so a
// handler fixed in a later release is asked again instead of staying
// abandoned on that till for ever.
func (r *PluginHandoverRepo) ListDue(ctx context.Context, event string, now time.Time, maxAttempts int) ([]HandoverCandidate, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.id, p.version,
       CASE WHEN COALESCE(h.asked_version, '') = p.version THEN COALESCE(h.attempts, 0) ELSE 0 END
FROM plugins p
LEFT JOIN plugin_handover h ON h.plugin_id = p.id
WHERE p.is_active = 1
  AND p.runtime = 'wasm'
  AND p.install_state <> 'broken'
  AND EXISTS (SELECT 1 FROM plugin_hooks k WHERE k.plugin_id = p.id AND k.is_active = 1 AND k.event = ?)
  AND h.done_at IS NULL
  AND (COALESCE(h.asked_version, '') <> p.version
       OR (COALESCE(h.attempts, 0) < ? AND (h.next_attempt_at IS NULL OR h.next_attempt_at <= ?)))
ORDER BY p.id`, event, maxAttempts, now.UTC().Format(handoverTime))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoverCandidate
	for rows.Next() {
		var c HandoverCandidate
		if err := rows.Scan(&c.PluginID, &c.Version, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordAttempt counts one real attempt that did not finish and schedules the
// next one at next.
func (r *PluginHandoverRepo) RecordAttempt(ctx context.Context, pluginID, version string, now, next time.Time, lastErr string) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugin_handover (plugin_id, asked_version, attempts, last_attempt_at, next_attempt_at, last_error)
VALUES (?, ?, 1, ?, ?, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  attempts = CASE WHEN COALESCE(asked_version, '') = excluded.asked_version THEN attempts + 1 ELSE 1 END,
  asked_version = excluded.asked_version,
  last_attempt_at = excluded.last_attempt_at,
  next_attempt_at = excluded.next_attempt_at,
  last_error = excluded.last_error
WHERE done_at IS NULL`,
		pluginID, version, now.UTC().Format(handoverTime), next.UTC().Format(handoverTime), lastErr)
	return err
}

// MarkDone records the plugin's {"done":true}; it is final.
func (r *PluginHandoverRepo) MarkDone(ctx context.Context, pluginID string, now time.Time) error {
	ts := now.UTC().Format(handoverTime)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugin_handover (plugin_id, done_at, attempts, last_attempt_at)
VALUES (?, ?, 1, ?)
ON CONFLICT(plugin_id) DO UPDATE SET
  done_at = COALESCE(done_at, excluded.done_at),
  attempts = attempts + 1,
  last_attempt_at = excluded.last_attempt_at,
  next_attempt_at = NULL,
  last_error = NULL`, pluginID, ts, ts)
	return err
}

// RecordProgress notes that the plugin answered "not done, but I moved data"
// ({"done":false,"progress":true}). It does NOT count as an attempt: a plugin
// that is working through a big table must not run out of its five tries, only
// errors, timeouts and answers without progress do. The next ask is at next.
func (r *PluginHandoverRepo) RecordProgress(ctx context.Context, pluginID, version string, now, next time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugin_handover (plugin_id, asked_version, attempts, last_attempt_at, next_attempt_at, last_error)
VALUES (?, ?, 0, ?, ?, 'progress')
ON CONFLICT(plugin_id) DO UPDATE SET
  attempts = CASE WHEN COALESCE(asked_version, '') = excluded.asked_version THEN attempts ELSE 0 END,
  asked_version = excluded.asked_version,
  last_attempt_at = excluded.last_attempt_at,
  next_attempt_at = excluded.next_attempt_at,
  last_error = 'progress'
WHERE done_at IS NULL`,
		pluginID, version, now.UTC().Format(handoverTime), next.UTC().Format(handoverTime))
	return err
}
