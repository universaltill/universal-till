package data

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SentReport is one retained record of a bug report this till uploaded
// (universaltill/ut-docs#348). The bulky media blobs are discarded after a
// successful upload; this row is what the /my-reports page renders, with
// Status/GithubIssueURL refreshed from the cloud on the sync tick.
type SentReport struct {
	ID             string
	Note           string
	CapturedAt     time.Time
	HadAudio       bool
	HadVideo       bool
	ImageCount     int
	Status         string
	GithubIssueURL string
	LastSyncedAt   sql.NullTime
}

// IssueReportsRepo owns all SQL for the issue_reports_sent table.
type IssueReportsRepo struct {
	db *sql.DB
}

func NewIssueReportsRepo(db *sql.DB) *IssueReportsRepo {
	return &IssueReportsRepo{db: db}
}

var issueReportsObs = newRepoObservability("issue_reports")

// SaveSent upserts the retained record for an uploaded report, keyed by the
// till's own bundle id. Status defaults to "sent" — the local-only sentinel
// meaning "uploaded, no cloud status seen yet" — when the caller leaves it
// empty. Re-saving the same id updates in place (the upload loop can re-run
// for a bundle whose Discard failed) rather than duplicating.
func (r *IssueReportsRepo) SaveSent(ctx context.Context, rec SentReport) error {
	var err error
	done := issueReportsObs.trace("save_sent")
	defer func() { done(err) }()
	status := rec.Status
	if status == "" {
		status = "sent"
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO issue_reports_sent (id, note, captured_at, had_audio, had_video, image_count, status, github_issue_url)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	note = excluded.note,
	captured_at = excluded.captured_at,
	had_audio = excluded.had_audio,
	had_video = excluded.had_video,
	image_count = excluded.image_count,
	status = excluded.status,
	github_issue_url = excluded.github_issue_url
`, rec.ID, rec.Note, rec.CapturedAt.UTC().Format(time.RFC3339), rec.HadAudio, rec.HadVideo,
		rec.ImageCount, status, rec.GithubIssueURL)
	if err != nil {
		return issueReportsObs.wrapf("save_sent", "save sent report %s", err, rec.ID)
	}
	return nil
}

// ListSent returns retained sent-report records, newest-captured first.
func (r *IssueReportsRepo) ListSent(ctx context.Context, limit int) ([]SentReport, error) {
	var err error
	done := issueReportsObs.trace("list_sent")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `
SELECT id, note, captured_at, had_audio, had_video, image_count, status, github_issue_url, last_synced_at
FROM issue_reports_sent
ORDER BY captured_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, issueReportsObs.wrap("list_sent", err)
	}
	defer rows.Close()
	var out []SentReport
	for rows.Next() {
		var rec SentReport
		var capturedAt string
		var lastSynced sql.NullString
		if err = rows.Scan(&rec.ID, &rec.Note, &capturedAt, &rec.HadAudio, &rec.HadVideo,
			&rec.ImageCount, &rec.Status, &rec.GithubIssueURL, &lastSynced); err != nil {
			return nil, issueReportsObs.wrap("list_sent", err)
		}
		rec.CapturedAt = parseStoredTime(capturedAt)
		if lastSynced.Valid {
			rec.LastSyncedAt = sql.NullTime{Time: parseStoredTime(lastSynced.String), Valid: true}
		}
		out = append(out, rec)
	}
	err = rows.Err()
	if err != nil {
		return nil, issueReportsObs.wrap("list_sent", err)
	}
	return out, nil
}

// CountSent returns the TRUE total number of retained sent-report rows —
// independent of any caller's row limit — so a caller like /my-reports can
// tell a shop owner how many more exist beyond whatever ListSent capped
// itself to (ut-docs#445).
func (r *IssueReportsRepo) CountSent(ctx context.Context) (int, error) {
	var err error
	done := issueReportsObs.trace("count_sent")
	defer func() { done(err) }()
	var n int
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_reports_sent`).Scan(&n)
	if err != nil {
		return 0, issueReportsObs.wrap("count_sent", err)
	}
	return n, nil
}

// UpdateStatus applies one cloud-reported status to the retained row and
// stamps last_synced_at. A row that doesn't exist locally (0 rows affected)
// is NOT an error: a status pull can legitimately reference a report id this
// till never retained (captured before ut-docs#348 shipped, or purged some
// other way) — silently skip it rather than logging a failure every tick.
func (r *IssueReportsRepo) UpdateStatus(ctx context.Context, id, status, githubIssueURL string) error {
	var err error
	done := issueReportsObs.trace("update_status")
	defer func() { done(err) }()
	// Independent review (ut-docs#348): github_issue_url is external input
	// from the cloud, rendered on /my-reports as a trusted-looking "View on
	// GitHub" link. html/template already neutralizes a javascript: URL, but
	// a plausible-looking http(s) link to somewhere that isn't actually
	// GitHub would render fine and mislead a manager who has every reason to
	// trust it. Only ever persist a real https://github.com/... URL; drop
	// anything else rather than fail the whole status update over it.
	if githubIssueURL != "" && !strings.HasPrefix(githubIssueURL, "https://github.com/") {
		githubIssueURL = ""
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE issue_reports_sent SET status = ?, github_issue_url = ?, last_synced_at = ? WHERE id = ?
`, status, githubIssueURL, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return issueReportsObs.wrapf("update_status", "update sent report %s", err, id)
	}
	return nil
}

// parseStoredTime reads back a timestamp this repo wrote (RFC3339), also
// tolerating SQLite's own datetime('now') format — same lenient-read shape
// as AuthRepo's session-expiry parsing. A value that parses as neither
// returns the zero time rather than erroring: a display timestamp is not
// worth failing the whole listing over.
func parseStoredTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
