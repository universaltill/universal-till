package data

import (
	"context"
	"fmt"
	"time"
)

// promotedUploadField is a field the cloud began to master after links
// already existed: the "promoting a field to cloud-mastered" recipe of
// contract cloud-catalog-changes.md (1.2.0). Its till-release migration ran
// the recipe on the main till (103 for icon), which marks nothing on a
// replica; MarkUnsentPromotedFields runs it again when a replica is
// promoted (ut-docs#6240). The next promoted field (ut-docs#6081) gets a
// row here, next to its migration and its move from
// catalogpull.tillOwnedFields to uploadFields.
//
// localExpr must produce the value exactly as catalogpull encodes it in the
// base: a boolean base is JSON true, which json_extract returns as 1.
// Reference fields (*_cloud_id) need a link lookup and do not fit this shape.
// A JSON-null base value counts as cloudDefault when marking (migration 103
// skips it); the till never writes one (baseFromRow turns null into "").
type promotedUploadField struct {
	entity       string // cloud_outbox / cloud_links entity ("item", "category")
	table        string // local table, aliased t in the SQL
	localExpr    string // the local value as the base encodes it, over t
	baseKey      string // the field's key in cloud_links.base
	cloudDefault any    // the value the cloud held before it mastered the field
}

var promotedUploadFields = []promotedUploadField{
	{entity: "item", table: "items", localExpr: "COALESCE(t.icon, '')", baseKey: "icon", cloudDefault: ""},
	{entity: "category", table: "categories", localExpr: "COALESCE(t.icon, '')", baseKey: "icon", cloudDefault: ""},
}

// PromotedUploadFieldKeys lists (entity, baseKey) for each promoted field, so
// catalogpull's test can pin each one to uploadFields. A variable, not a
// func: only a test reads it (guard-deadcode-baseline.sh).
var PromotedUploadFieldKeys = func() [][2]string {
	out := make([][2]string, 0, len(promotedUploadFields))
	for _, f := range promotedUploadFields {
		out = append(out, [2]string{f.entity, f.baseKey})
	}
	return out
}()

// MarkUnsentPromotedFields runs the recipe's mark-and-seed step for every
// promotedUploadFields entry and returns the number of marks it opened. The
// marks are gated like the outbox triggers (cloud or adopting catalogue
// mode, no sync.primary_url, no suppression), so it never marks on a real
// replica. NULL or damaged bases are left to catalogpull's seedBases.
// Identifiers come from package constants; every value is a bound parameter.
func (*CloudOutboxRepo) MarkUnsentPromotedFields(ctx context.Context, q SyncQuerier, createdAt string) (int64, error) {
	var total int64
	for _, f := range promotedUploadFields {
		key := "$." + f.baseKey
		mark := fmt.Sprintf(`
INSERT INTO cloud_outbox (entity, local_id, created_at)
SELECT ?, t.id, ?
  FROM %[1]s t
  JOIN cloud_links l ON l.entity = ? AND l.local_id = t.id
 WHERE %[2]s <> ?
   AND l.base IS NOT NULL
   AND json_valid(l.base)
   AND COALESCE(json_extract(l.base, ?), ?) <> %[2]s
   AND (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
   AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox o
                    WHERE o.entity = ? AND o.local_id = t.id AND o.sealed_at IS NULL)`, f.table, f.localExpr)
		res, err := q.ExecContext(ctx, mark, f.entity, createdAt, f.entity, f.cloudDefault, key, f.cloudDefault, f.entity)
		if err != nil {
			return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
		}
		n, _ := res.RowsAffected()
		total += n
		if _, err := q.ExecContext(ctx, `
UPDATE cloud_links
   SET base = json_set(base, ?, ?)
 WHERE entity = ?
   AND base IS NOT NULL
   AND json_valid(base)
   AND json_type(base, ?) IS NULL`, key, f.cloudDefault, f.entity, key); err != nil {
			return total, fmt.Errorf("seed %s %s base: %w", f.entity, f.baseKey, err)
		}
	}
	return total, nil
}

// promoteDropSettingsKeys are the push hashes a replica inherits from its
// main till's join snapshot: the hash of what the main last pushed. A
// promoted till whose data hashes equal to it would skip the push as
// "unchanged" although the cloud holds the main's later push. They are the
// first two of db.RestoreDropKeys (restore drops them for the same reason;
// internal/data cannot import internal/db, a test pins the subset). The
// other RestoreDropKeys are the till's own state and stay.
var promoteDropSettingsKeys = []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"}

// DropInheritedOpenMarks deletes every open mark (sealed_at IS NULL) and
// returns how many. On a till that was a replica each one is the main
// till's edit from before the join, which the main uploads itself; sealed
// marks are not touched (see PromoteReplica).
func (*CloudOutboxRepo) DropInheritedOpenMarks(ctx context.Context, q SyncQuerier) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM cloud_outbox WHERE sealed_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("drop inherited open marks: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PromoteReplica is what POST /api/sync/promote runs (ut-docs#6240,
// ut-docs#6242): the settings-only ClearReplicaIdentity plus, in the same
// transaction and after sync.primary_url is gone:
//
//  1. only if the till was a replica: drop the open cloud_outbox marks and
//     the two push-hash settings it inherited from its main till's join
//     snapshot (a replica never marks or pushes, so everything in them is
//     the main's, and the main uploads it itself; stale marks would freeze
//     drifted fields, upload a stale before/after - the ABA lost update -
//     and conflict-log every row). Trade-off: an edit the main made while it
//     could not reach the cloud, and never uploaded before it died, is lost
//     with its mark; recovering it is ut-docs#6241's re-base, not a stale
//     mark. A till with no primary URL keeps its own
//     marks: the handler's 409 check runs outside this transaction.
//  2. MarkUnsentPromotedFields (#6240), after the drop so its "no open
//     mark" gate no longer sees the stale marks.
//  3. queued plugin records are rejected "till_promoted" (ut-docs#3697).
//
// Sealed (in-flight) marks are kept on purpose: the cloud replays a sealed
// change_id idempotently and its answer re-bases the row, and a sealed
// create's link (Version 0, no base) is guarded by the in-flight rule.
// Residual: a replayed "rejected" create re-mints under a new cloud id (the
// main already did), so the cloud may get one duplicate row; accepted.
//
// Deliberately kept: cloud_links (the only local-to-cloud id map for rows
// the till made; the feed carries no local id; #6240 re-marks icons against
// their bases), the stale link bases (re-basing is ut-docs#6241), and
// cloudsync.catalog.cursor/gen/resync_gen (an old cursor cannot miss a
// change: the feed serves each changed row at its latest version after it,
// tombstones included, and answers 410 to resync; resetting it would make
// the first pull overwrite local values with no marks, also #6241).
// A failure rolls the whole promotion back.
func (r *SettingsRepo) PromoteReplica(ctx context.Context) error {
	return r.clearReplicaIdentity(ctx, func(ctx context.Context, tx SyncQuerier, wasReplica bool) error {
		repo := NewCloudOutboxRepo()
		if wasReplica {
			if _, err := repo.DropInheritedOpenMarks(ctx, tx); err != nil {
				return err
			}
			for _, k := range promoteDropSettingsKeys {
				if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k); err != nil {
					return fmt.Errorf("drop inherited setting %s: %w", k, err)
				}
			}
		}
		now := time.Now().UTC()
		if _, err := repo.MarkUnsentPromotedFields(ctx, tx, now.Format(time.RFC3339)); err != nil {
			return err
		}
		// ut-docs#3697: queued plugin records can never reach the old main
		// till now; the plugin's own local rows stay valid.
		return RejectAllPendingRecords(ctx, tx, RecordReasonTillPromoted, now)
	})
}
