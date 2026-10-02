// Package housekeeping is the till's daily clean-up job (ut-docs#3092): one
// place that keeps backups, set-aside restore copies, unsent bug reports and
// update downloads from filling the device, with the retention rules written
// down in Retention() and in the manual (web/help/*/backups.md).
//
// It deletes files only, each kind through the package that owns that file
// namespace (db, issuereport, selfupdate). It never opens the database —
// sales, receipts, fiscal/TSE data, the audit log and Z reports follow
// ADR-0040's retention, not this job (TestPackageNeverTouchesTheDatabase).
// The one file kind that holds that data, the pre-restore copies (each a
// full former database), is pruned only outside ADR-0040's statutory floor:
// Run is told that floor (minRetain) by its caller, which resolves it from
// the shop's country, and db.PrunePreRestore keeps every copy inside it
// (ut-docs#3365).
// Each till cleans its own disk; nothing here is triggered by another till.
// Selling never waits on it: it runs in the background and every failure is
// logged and skipped.
package housekeeping

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/selfupdate"
)

// Retention limits. Whichever limit is hit first removes the file.
const (
	PreRestoreKeep       = 3
	PreRestoreMaxAge     = 30 * 24 * time.Hour
	IssueReportMaxAge    = 7 * 24 * time.Hour
	UpdateDownloadMaxAge = 7 * 24 * time.Hour
	// Interval between runs. The job runs shortly after boot and then once
	// a day; it is not pinned to a night-time hour because a till switched
	// off overnight would never run it, and deleting a few files costs a
	// running sale nothing.
	Interval = 24 * time.Hour
)

// Kinds of stored file, as used in Retention() and Result.
const (
	KindBackups         = "backups"
	KindPreRestore      = "pre_restore_copies"
	KindIssueReports    = "unsent_bug_reports"
	KindUpdateDownloads = "update_downloads"
	KindLogs            = "logs"
	KindDiagnostics     = "diagnostics_spool"
)

// Rule is one row of the retention table.
type Rule struct {
	Kind       string
	Where      string // location, relative to the data directory
	Policy     string
	EnforcedBy string
}

// Retention is the written retention table: every kind of file the till
// accumulates, its limit, and the code that enforces it. Rows enforced
// elsewhere are listed so the table is complete.
func Retention() []Rule {
	return []Rule{
		{KindBackups, "backups/unitill-pos-*.db",
			fmt.Sprintf("newest %d snapshots", db.DefaultBackupKeep), "housekeeping + db.PruneBackups"},
		{KindPreRestore, "backups/pre-restore-*.db",
			fmt.Sprintf("newest %d, none older than %d days, never inside the shop's statutory retention floor (ADR-0040)", PreRestoreKeep, days(PreRestoreMaxAge)), "housekeeping (db.PrunePreRestore)"},
		{KindIssueReports, "issue-reports/pending/<id>/",
			fmt.Sprintf("deleted once uploaded, or after %d days unsent", days(IssueReportMaxAge)), "cloudsync upload + housekeeping (issuereport.PruneOlderThan)"},
		{KindUpdateDownloads, "updates/attempt-*/",
			fmt.Sprintf("cleared when the next update starts, or after %d days", days(UpdateDownloadMaxAge)), "selfupdate + housekeeping (selfupdate.PruneStaleAttempts)"},
		{KindLogs, "logs/till.log*",
			fmt.Sprintf("%d files × %d MB, oldest dropped", logging.DefaultMaxFiles, logging.DefaultMaxFileBytes>>20), "logging.RotatingWriter"},
		{KindDiagnostics, "diagnostics/pending/",
			"200 batches, oldest dropped; deleted once uploaded", "diagnostics queue"},
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

// Run applies every sweep once, at now, for the database at dbPath.
// minRetain is the statutory archive floor; no pre-restore copy younger
// than it is removed.
func Run(dbPath string, now time.Time, minRetain time.Duration) []Result {
	return []Result{
		sweepBackups(dbPath),
		result(KindPreRestore)(db.PrunePreRestore(dbPath, PreRestoreKeep, PreRestoreMaxAge, now, minRetain)),
		result(KindIssueReports)(issuereport.PruneOlderThan(now.Add(-IssueReportMaxAge))),
		result(KindUpdateDownloads)(selfupdate.PruneStaleAttempts(now.Add(-UpdateDownloadMaxAge))),
	}
}

func result(kind string) func(int, int64, error) Result {
	return func(n int, freed int64, err error) Result {
		return Result{Kind: kind, Removed: n, Freed: freed, Err: err}
	}
}

// sweepBackups re-applies the snapshot cap (normally already enforced after
// each backup) and reports what it removed.
func sweepBackups(dbPath string) Result {
	res := Result{Kind: KindBackups}
	list, err := db.ListBackups(dbPath)
	if err != nil {
		res.Err = err
		return res
	}
	if err := db.PruneBackups(dbPath, db.DefaultBackupKeep); err != nil {
		res.Err = err
		return res
	}
	after, err := db.ListBackups(dbPath)
	if err != nil {
		res.Err = err
		return res
	}
	kept := map[string]bool{}
	for _, b := range after {
		kept[b.Name] = true
	}
	for _, b := range list {
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
