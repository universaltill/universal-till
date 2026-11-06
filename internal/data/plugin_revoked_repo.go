package data

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RevokedRelease is one marketplace revocation (ut-docs#5658). Version ""
// means every version of ManifestID.
type RevokedRelease struct {
	ManifestID string
	Version    string
	ListingID  string
	Reason     string
	Action     string
}

// RevokedRelease reports whether (manifestID, version) is revoked, matching
// an exact-version record or an every-version ("") record. A nil tx reads
// through r.db.
func (r *PluginRepo) RevokedRelease(ctx context.Context, tx *sql.Tx, manifestID, version string) (RevokedRelease, bool, error) {
	var rr RevokedRelease
	err := r.executor(tx).QueryRowContext(ctx, `
SELECT manifest_id, version, listing_id, reason, action
FROM plugin_revoked_releases
WHERE manifest_id = ? AND (version = ? OR version = '')
ORDER BY version DESC
LIMIT 1`, manifestID, version).Scan(&rr.ManifestID, &rr.Version, &rr.ListingID, &rr.Reason, &rr.Action)
	if errors.Is(err, sql.ErrNoRows) {
		return RevokedRelease{}, false, nil
	}
	if err != nil {
		return RevokedRelease{}, false, pluginObs.wrap("revoked_release", err)
	}
	return rr, true, nil
}

// ReplaceRevokedReleases makes the stored set equal to rows in one
// transaction: records not in rows are deleted (lifted), rows are upserted.
// It returns how many records were removed.
func (r *PluginRepo) ReplaceRevokedReleases(ctx context.Context, rows []RevokedRelease, now time.Time) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, pluginObs.wrap("replace_revoked_releases", err)
	}
	defer tx.Rollback()

	keep := make(map[[2]string]bool, len(rows))
	for _, rr := range rows {
		keep[[2]string{rr.ManifestID, rr.Version}] = true
	}
	existing, err := tx.QueryContext(ctx, `SELECT manifest_id, version FROM plugin_revoked_releases`)
	if err != nil {
		return 0, pluginObs.wrap("replace_revoked_releases", err)
	}
	var stale [][2]string
	for existing.Next() {
		var k [2]string
		if err := existing.Scan(&k[0], &k[1]); err != nil {
			existing.Close()
			return 0, pluginObs.wrap("replace_revoked_releases", err)
		}
		if !keep[k] {
			stale = append(stale, k)
		}
	}
	if err := existing.Err(); err != nil {
		existing.Close()
		return 0, pluginObs.wrap("replace_revoked_releases", err)
	}
	existing.Close()

	for _, k := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_revoked_releases WHERE manifest_id = ? AND version = ?`, k[0], k[1]); err != nil {
			return 0, pluginObs.wrap("replace_revoked_releases", err)
		}
	}
	ts := now.UTC().Format(time.RFC3339)
	for _, rr := range rows {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_revoked_releases (manifest_id, version, listing_id, reason, action, recorded_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(manifest_id, version) DO UPDATE SET
  listing_id = excluded.listing_id, reason = excluded.reason, action = excluded.action`,
			rr.ManifestID, rr.Version, rr.ListingID, rr.Reason, rr.Action, ts); err != nil {
			return 0, pluginObs.wrap("replace_revoked_releases", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, pluginObs.wrap("replace_revoked_releases", err)
	}
	return len(stale), nil
}
