package data

import "context"

// Settings keys of the off-site encrypted backup (ADR-0199, ut-docs#5066).
// All are per till (PerTillSettingPrefixes: "backup.offsite." is listed there)
// and dropped by a restore (db.RestoreDropKeys). None holds key material: the
// per-backup key exists only in memory while one backup is sealed or opened.
const (
	// OffsiteBackupPrefix is the family prefix.
	OffsiteBackupPrefix = "backup.offsite."

	OffsiteLastSuccessAtKey = "backup.offsite.last_success_at"
	OffsiteLastSnapshotKey  = "backup.offsite.last_snapshot"
	OffsiteLastErrorKey     = "backup.offsite.last_error"
	OffsiteLastErrorAtKey   = "backup.offsite.last_error_at"
	OffsiteFailingSinceKey  = "backup.offsite.failing_since"
	OffsiteNextAttemptAtKey = "backup.offsite.next_attempt_at"
	OffsiteAttemptsKey      = "backup.offsite.attempts"
	OffsiteRefusedReasonKey = "backup.offsite.refused_reason"
	OffsitePendingIDKey     = "backup.offsite.pending_id"
	OffsitePendingFileKey   = "backup.offsite.pending_file"
	// OffsitePendingMetaKey is the sealed file's totals (JSON), kept so a
	// restart can finish the upload without re-reading the snapshot.
	OffsitePendingMetaKey = "backup.offsite.pending_meta"

	// OffsiteKeyInCloudKey is the owner's choice about a wrapped copy of the
	// backup key in the cloud (design section 15, E2): "" (not asked yet), "on"
	// or "off". Uploads run regardless; the till always holds its own key.
	OffsiteKeyInCloudKey = "backup.offsite.key_in_cloud"
	// OffsiteEscrowBlockedKey is "1" while the owner has switched the cloud
	// copy off on my. (the cloud refuses a copy: 409 escrow_blocked).
	OffsiteEscrowBlockedKey = "backup.offsite.escrow_blocked"
	// OffsiteKeyExportedAtKey is when a manager last showed or saved the key
	// (E4). Never the key.
	OffsiteKeyExportedAtKey = "backup.offsite.key_exported_at"
	// OffsiteLinkCopyKey says, while this till needs linking, whether the cloud
	// holds a wrapped copy of the key: "yes" (link via my.) or "no" (enter the
	// saved key, or start a new key). The card offers the matching actions.
	OffsiteLinkCopyKey = "backup.offsite.link_copy"
)

// Values of OffsiteKeyInCloudKey.
const (
	OffsiteKeyInCloudOn  = "on"
	OffsiteKeyInCloudOff = "off"
)

// OffsiteBackupKeys lists every key above, for tests and the restore wipe.
var OffsiteBackupKeys = []string{
	OffsiteLastSuccessAtKey, OffsiteLastSnapshotKey, OffsiteLastErrorKey, OffsiteLastErrorAtKey,
	OffsiteFailingSinceKey, OffsiteNextAttemptAtKey, OffsiteAttemptsKey, OffsiteRefusedReasonKey,
	OffsitePendingIDKey, OffsitePendingFileKey, OffsitePendingMetaKey,
	OffsiteKeyInCloudKey, OffsiteEscrowBlockedKey, OffsiteKeyExportedAtKey, OffsiteLinkCopyKey,
}

// CountCompletedSales is how many completed or voided sales this till holds:
// the cloud-restore page warns that a restore replaces them (ut-docs#5066).
func (r *POSRepo) CountCompletedSales(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE status IN ('completed', 'voided')`).Scan(&n)
	return n, err
}
