// Package housekeeping is the till's daily clean-up job (ut-docs#3092): one
// place that keeps backups, set-aside restore copies, unsent bug reports and
// update downloads from filling the device, with the retention rules written
// down in Retention() and in the manual (web/help/*/backups.md).
//
// It deletes files only, each kind through the package that owns that file
// namespace (db, issuereport, selfupdate, stagedupload). The sync-history tables are
// bounded by the code that owns them and only listed here (ut-docs#3123).
// It never opens the database —
// sales, receipts, fiscal/TSE data, the audit log and Z reports follow
// ADR-0040's retention, not this job (TestPackageNeverTouchesTheDatabase).
// The one file kind that holds that data, the pre-restore copies (each a
// full former database), is pruned only outside ADR-0040's statutory floor:
// RunWith is told that floor (minRetain) by its caller, which resolves it from
// the shop's country, and db.PrunePreRestore keeps every copy inside it
// (ut-docs#3365).
// Each till cleans its own disk; nothing here is triggered by another till.
// Selling never waits on it: it runs in the background and every failure is
// logged and skipped.
package housekeeping

import (
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/stagedupload"
)

// Retention limits. Whichever limit is hit first removes the file.
const (
	PreRestoreKeep = 3
	// DefaultBackupRetentionDays is the days of backups kept unless the shop
	// sets backup.retention_days (equals data.DefaultBackupRetentionDays).
	DefaultBackupRetentionDays = 7
	PreRestoreMaxAge           = 30 * 24 * time.Hour
	IssueReportMaxAge          = 7 * 24 * time.Hour
	UpdateDownloadMaxAge       = 7 * 24 * time.Hour
	// Interval between runs. The job runs shortly after boot and then once
	// a day; it is not pinned to a night-time hour because a till switched
	// off overnight would never run it, and deleting a few files costs a
	// running sale nothing.
	Interval = 24 * time.Hour

	// MinBackupKeep is the fewest snapshots any prune leaves: the low-disk
	// sweep and the backup-size cap stop here (ut-docs#3121).
	MinBackupKeep = 3
	// BackupSharePercent caps the snapshots together at this share of the
	// free space they could release (free + their own size), so on a
	// filling disk the oldest go before the till runs out of room
	// (ut-docs#3121 AC 4). A quarter leaves the floor plenty of headroom on
	// a 16 GB tablet while a few-hundred-MB database still keeps all 14.
	BackupSharePercent = 25
	// LowDiskMaxAge is the age past which the low-disk sweep removes unsent
	// bug reports and update downloads.
	LowDiskMaxAge = 24 * time.Hour
)

// Limits are the per-kind limits one sweep applies.
type Limits struct {
	// BackupKeep caps the snapshot count (0 = no count cap) and
	// BackupMaxAge their age (0 = no age cap); the newest snapshot always
	// stays. The daily sweep uses the age the shop set (backup.retention_days,
	// ut-docs#3092); the low-disk sweep uses the count.
	BackupKeep           int
	BackupMaxAge         time.Duration
	PreRestoreKeep       int
	PreRestoreMaxAge     time.Duration
	IssueReportMaxAge    time.Duration
	UpdateDownloadMaxAge time.Duration
}

// Normal is the daily sweep's limits — the retention table below.
var Normal = Limits{
	BackupMaxAge:         DefaultBackupRetentionDays * 24 * time.Hour,
	PreRestoreKeep:       PreRestoreKeep,
	PreRestoreMaxAge:     PreRestoreMaxAge,
	IssueReportMaxAge:    IssueReportMaxAge,
	UpdateDownloadMaxAge: UpdateDownloadMaxAge,
}

// LowDisk is each kind's minimum, applied as soon as free space drops below
// the diskspace floor (ut-docs#3121 AC 2). The statutory floor on restore
// copies still binds (RunWith's minRetain); staged uploads keep their crash
// age, because a younger file may belong to an upload still running.
var LowDisk = Limits{
	BackupKeep:           MinBackupKeep,
	PreRestoreKeep:       1,
	PreRestoreMaxAge:     LowDiskMaxAge,
	IssueReportMaxAge:    LowDiskMaxAge,
	UpdateDownloadMaxAge: LowDiskMaxAge,
}

// Kinds of stored file, as used in Retention() and Result.
const (
	KindBackups         = "backups"
	KindPreRestore      = "pre_restore_copies"
	KindIssueReports    = "unsent_bug_reports"
	KindUpdateDownloads = "update_downloads"
	KindLogs            = "logs"
	KindDiagnostics     = "diagnostics_spool"
	KindStagedUploads   = "staged_uploads"

	// Sync-history tables (ut-docs#3123). Housekeeping never opens the
	// database; each of these is bounded, or kept on purpose, by the code
	// that owns it. They are listed so the retention table is complete — a
	// row documents the bound, it never permits housekeeping to delete.
	KindSyncAggregateLedger = "sync_aggregate_upload_ledger"
	KindSyncHeldTombstones  = "sync_held_order_tombstones"
	KindSyncAssetLedger     = "sync_asset_ledger"
	KindSyncAdminVersion    = "sync_admin_version"
	KindSyncQuarantine      = "sync_quarantined_sales"
)

// Rule is one row of the retention table.
type Rule struct {
	Kind       string
	Where      string // location, relative to the data directory
	Policy     string
	EnforcedBy string
	// Args are the numbers in the merchant policy text, in the order of the
	// verbs in en.json "settings.storage.policy.<Kind>" (ut-docs#3122).
	Args []int64
}

// InDatabase reports a "database: <table>" row (sync history).
func (r Rule) InDatabase() bool { return strings.HasPrefix(r.Where, "database:") }

// Params are the per-till numbers a retention row depends on.
type Params struct {
	BackupDays     int   // backup.retention_days
	ArchiveMinDays int64 // resolved statutory floor (data.ArchiveMinDaysForPruning)
}

// Retention is the written retention table: every kind of file the till
// accumulates, and every sync-history table in its database ("database:
// <table>"), with its limit and the code that enforces it. Rows enforced
// elsewhere are listed so the table is complete. A "database:" row only
// documents how its owner bounds the table; housekeeping itself never
// deletes rows (TestPackageNeverTouchesTheDatabase). A new sync_* table fails
// TestRetentionTableCoversEverySyncHistoryTable until it has a row.
func Retention() []Rule {
	return RetentionFor(Params{BackupDays: DefaultBackupRetentionDays, ArchiveMinDays: 0})
}

// RetentionFor is the retention table with this till's numbers: its days of
// backups and its resolved statutory floor. Retention() is the same table
// with the defaults. Every number in a row's Args is a constant or a Params
// field, so the merchant text can't drift from the job (ut-docs#3122).
func RetentionFor(p Params) []Rule {
	return []Rule{
		{KindBackups, "backups/unitill-pos-*.db",
			fmt.Sprintf("the last %d days (setting backup.retention_days), pruned after every new backup, never the newest; together at most %d%% of the free space they could release; newest %d below the low-disk floor (never fewer than %d)", p.BackupDays, BackupSharePercent, MinBackupKeep, MinBackupKeep), "after each backup + housekeeping (db.PruneBackupsOlderThan, CapBackups)",
			[]int64{int64(p.BackupDays), BackupSharePercent, MinBackupKeep}},
		{KindPreRestore, "backups/pre-restore-*.db",
			fmt.Sprintf("newest %d, none older than %d days (newest 1, none older than %d day below the low-disk floor), never inside the shop's statutory retention floor (ADR-0040)", PreRestoreKeep, days(PreRestoreMaxAge), days(LowDiskMaxAge)), "housekeeping (db.PrunePreRestore)",
			[]int64{p.ArchiveMinDays, PreRestoreKeep, int64(days(PreRestoreMaxAge)), int64(LowDisk.PreRestoreKeep)}},
		{KindIssueReports, "issue-reports/pending/<id>/",
			fmt.Sprintf("deleted once uploaded, or after %d days unsent (%d day below the low-disk floor)", days(IssueReportMaxAge), days(LowDiskMaxAge)), "cloudsync upload + housekeeping (issuereport.PruneOlderThan)",
			[]int64{int64(days(IssueReportMaxAge)), int64(LowDiskMaxAge / time.Hour)}},
		{KindUpdateDownloads, "updates/attempt-*/",
			fmt.Sprintf("cleared when the next update starts, or after %d days (%d day below the low-disk floor)", days(UpdateDownloadMaxAge), days(LowDiskMaxAge)), "selfupdate + housekeeping (selfupdate.PruneStaleAttempts)",
			[]int64{int64(days(UpdateDownloadMaxAge)), int64(LowDiskMaxAge / time.Hour)}},
		{KindLogs, "logs/till.log*",
			fmt.Sprintf("%d files × %d MB, oldest dropped", logging.DefaultMaxFiles, logging.DefaultMaxFileBytes>>20), "logging.RotatingWriter",
			[]int64{logging.DefaultMaxFiles, logging.DefaultMaxFileBytes >> 20}},
		{KindDiagnostics, "diagnostics/pending/",
			fmt.Sprintf("%d batches, oldest dropped; deleted once uploaded", diagnostics.MaxPendingBatches), "diagnostics queue",
			[]int64{int64(diagnostics.MaxPendingBatches)}},
		// The one row outside the data directory: os.CreateTemp("") stages
		// into the system temp dir (ut-docs#3955).
		{KindStagedUploads, "<system temp dir>/" + strings.Join(stagedupload.Patterns(), ", "),
			fmt.Sprintf("deleted when the upload, import or import preview that made them ends; leftovers after a crash removed after %d hours", int(stagedupload.MaxAge/time.Hour)),
			"the request handlers + housekeeping (stagedupload.PruneOlderThan)",
			[]int64{int64(stagedupload.MaxAge / time.Hour)}},
		{KindSyncAggregateLedger, "database: sales_aggregate_uploads",
			"rows for business days before the upload look-back window are deleted on every upload round",
			"cloudsync (salesAggregateLookbackDays, POSRepo.PruneSalesAggregateUploads)", nil},
		{KindSyncHeldTombstones, "database: held_sales_tombstones",
			"deleted once older than the tombstone lifetime, on every held-order delete or claim",
			"data (heldSaleTombstoneTTL, held_sales_repo.go)", nil},
		{KindSyncAssetLedger, "database: sync_asset_ledger",
			"one row per photo or file this till downloaded from the main till; the row goes when the file is pruned",
			"LAN asset sync (SyncAssetLedgerRepo.Apply)", nil},
		{KindSyncAdminVersion, "database: sync_admin_version",
			"a single counter row; never grows", "migration 023 (CHECK id = 1)", nil},
		{KindSyncQuarantine, "database: sync_journal_quarantine",
			"kept, never deleted automatically; one row per sale (UNIQUE sale_id), so retries add nothing: each row is a sale this till could not apply from another till, and may be its only copy of that sale (ADR-0040, ADR-0065)",
			"nothing deletes it; shown on /sync-quarantine", nil},
	}
}

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

// Result is what one sweep did.
type Result struct {
	Kind    string
	Removed int
	Freed   int64
	Err     error
}

// RunWith applies every sweep once, at now, for the database at dbPath,
// with limits l (Normal daily, LowDisk below the free-space floor).
// minRetain is the statutory archive floor; no pre-restore copy younger
// than it is removed, whatever l says.
func RunWith(dbPath string, now time.Time, minRetain time.Duration, l Limits) []Result {
	return []Result{
		sweepBackups(dbPath, l.BackupKeep, l.BackupMaxAge, now),
		result(KindPreRestore)(db.PrunePreRestore(dbPath, l.PreRestoreKeep, l.PreRestoreMaxAge, now, minRetain)),
		result(KindIssueReports)(issuereport.PruneOlderThan(now.Add(-l.IssueReportMaxAge))),
		result(KindUpdateDownloads)(selfupdate.PruneStaleAttempts(now.Add(-l.UpdateDownloadMaxAge))),
		result(KindStagedUploads)(stagedupload.PruneOlderThan(now.Add(-stagedupload.MaxAge))),
	}
}

// CapBackups removes the oldest snapshots until the rest together use at
// most BackupSharePercent of the space they could release (free bytes on
// the disk plus their own size), never leaving fewer than MinBackupKeep
// (ut-docs#3121 AC 4). It touches only unitill-pos-* snapshots.
func CapBackups(dbPath string, free uint64) Result {
	res := Result{Kind: KindBackups}
	list, err := db.ListBackups(dbPath)
	if err != nil {
		res.Err = err
		return res
	}
	var total uint64
	for _, b := range list {
		total += uint64(max(b.Size, 0))
	}
	budget := (free + total) / 100 * BackupSharePercent
	keep, used := 0, uint64(0)
	for _, b := range list {
		used += uint64(max(b.Size, 0))
		if keep >= MinBackupKeep && used > budget {
			break
		}
		keep++
	}
	if keep == len(list) {
		return res
	}
	if err := db.PruneBackups(dbPath, keep); err != nil {
		res.Err = err
		return res
	}
	return diffBackups(dbPath, list, res)
}

func result(kind string) func(int, int64, error) Result {
	return func(n int, freed int64, err error) Result {
		return Result{Kind: kind, Removed: n, Freed: freed, Err: err}
	}
}

// sweepBackups re-applies the snapshot age and count limits (normally already
// enforced after each backup) and reports what it removed. The newest
// snapshot is never removed (db.PruneBackupsOlderThan, db.PruneBackups with
// keep >= 1).
func sweepBackups(dbPath string, keep int, maxAge time.Duration, now time.Time) Result {
	res := Result{Kind: KindBackups}
	list, err := db.ListBackups(dbPath)
	if err != nil {
		res.Err = err
		return res
	}
	if maxAge > 0 {
		if _, err := db.PruneBackupsOlderThan(dbPath, max(1, int(maxAge/(24*time.Hour))), now); err != nil {
			res.Err = err
			return res
		}
	}
	if keep > 0 {
		if err := db.PruneBackups(dbPath, keep); err != nil {
			res.Err = err
			return res
		}
	}
	return diffBackups(dbPath, list, res)
}

// diffBackups fills res with the snapshots in before that are gone now.
func diffBackups(dbPath string, before []db.BackupInfo, res Result) Result {
	after, err := db.ListBackups(dbPath)
	if err != nil {
		res.Err = err
		return res
	}
	kept := map[string]bool{}
	for _, b := range after {
		kept[b.Name] = true
	}
	for _, b := range before {
		if !kept[b.Name] {
			res.Removed++
			res.Freed += b.Size
		}
	}
	return res
}

// Schedule decides when the next run is due: at the first check after boot,
// then once Interval has passed since the last run. Not safe for concurrent
// use; the server's single background loop owns it.
type Schedule struct{ last time.Time }

// Due reports whether a run is due at now.
func (s *Schedule) Due(now time.Time) bool {
	return s.last.IsZero() || now.Sub(s.last) >= Interval
}

// Ran records a run at now.
func (s *Schedule) Ran(now time.Time) { s.last = now }
