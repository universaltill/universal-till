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

// PromotedUploadFields lists (entity, baseKey) for each promoted field, so
// catalogpull's test can pin each one to uploadFields.
func PromotedUploadFields() [][2]string {
	out := make([][2]string, 0, len(promotedUploadFields))
	for _, f := range promotedUploadFields {
		out = append(out, [2]string{f.entity, f.baseKey})
	}
	return out
}

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

// PromoteReplica is what POST /api/sync/promote runs (ut-docs#6240): the
// settings-only ClearReplicaIdentity plus, in the same transaction and after
// sync.primary_url is gone, MarkUnsentPromotedFields. A replica never marks,
// so migration 103 left its locally held icons unmarked; without marks the
// first catalogue pull after promotion clears them. A marking error rolls
// the whole promotion back, so a promoted till is never left unmarked.
func (r *SettingsRepo) PromoteReplica(ctx context.Context) error {
	return r.clearReplicaIdentity(ctx, func(ctx context.Context, tx SyncQuerier) error {
		_, err := NewCloudOutboxRepo().MarkUnsentPromotedFields(ctx, tx, time.Now().UTC().Format(time.RFC3339))
		return err
	})
}
