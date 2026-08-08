package data

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

func openIssueReportsTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "issuereports.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// SaveSent + ListSent round-trip: rows come back newest-captured first, with
// every field intact — the /my-reports page renders straight from this.
func TestIssueReportsSaveSentListSentRoundTrip(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	ctx := context.Background()

	older := SentReport{
		ID:         "rep-older",
		Note:       "printer jammed",
		CapturedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		HadAudio:   true,
		ImageCount: 2,
		Status:     "sent",
	}
	newer := SentReport{
		ID:         "rep-newer",
		Note:       "screen froze",
		CapturedAt: time.Date(2026, 8, 7, 15, 30, 0, 0, time.UTC),
		HadVideo:   true,
		Status:     "sent",
	}
	if err := repo.SaveSent(ctx, older); err != nil {
		t.Fatalf("SaveSent older: %v", err)
	}
	if err := repo.SaveSent(ctx, newer); err != nil {
		t.Fatalf("SaveSent newer: %v", err)
	}

	got, err := repo.ListSent(ctx, 10)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSent returned %d rows, want 2", len(got))
	}
	if got[0].ID != "rep-newer" || got[1].ID != "rep-older" {
		t.Fatalf("order = [%s, %s], want newest-captured first [rep-newer, rep-older]", got[0].ID, got[1].ID)
	}
	if got[0].Note != "screen froze" || !got[0].HadVideo || got[0].HadAudio || got[0].ImageCount != 0 {
		t.Fatalf("newer row fields wrong: %+v", got[0])
	}
	if !got[0].CapturedAt.Equal(newer.CapturedAt) {
		t.Fatalf("newer CapturedAt = %v, want %v", got[0].CapturedAt, newer.CapturedAt)
	}
	if got[1].Note != "printer jammed" || !got[1].HadAudio || got[1].HadVideo || got[1].ImageCount != 2 {
		t.Fatalf("older row fields wrong: %+v", got[1])
	}
	if got[0].Status != "sent" || got[1].Status != "sent" {
		t.Fatalf("statuses = %q/%q, want sent/sent", got[0].Status, got[1].Status)
	}
	if got[0].GithubIssueURL != "" || got[0].LastSyncedAt.Valid {
		t.Fatalf("fresh row must have no github url / last_synced_at: %+v", got[0])
	}
}

// ListSent honors its limit against the same newest-first ordering.
func TestIssueReportsListSentLimit(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	ctx := context.Background()
	for i, id := range []string{"a", "b", "c"} {
		if err := repo.SaveSent(ctx, SentReport{
			ID:         id,
			CapturedAt: time.Date(2026, 8, 1+i, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("SaveSent %s: %v", id, err)
		}
	}
	got, err := repo.ListSent(ctx, 2)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("limited list wrong: %+v", got)
	}
}

// Saving the same id twice must upsert (update in place), never duplicate —
// the upload loop can legitimately re-run for a bundle whose Discard failed.
func TestIssueReportsSaveSentUpsertsById(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	ctx := context.Background()

	rec := SentReport{
		ID:         "rep-1",
		Note:       "first note",
		CapturedAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
	if err := repo.SaveSent(ctx, rec); err != nil {
		t.Fatalf("SaveSent first: %v", err)
	}
	rec.Note = "second note"
	rec.ImageCount = 3
	if err := repo.SaveSent(ctx, rec); err != nil {
		t.Fatalf("SaveSent second: %v", err)
	}

	got, err := repo.ListSent(ctx, 10)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows after re-save of the same id, want 1", len(got))
	}
	if got[0].Note != "second note" || got[0].ImageCount != 3 {
		t.Fatalf("re-save did not update the row: %+v", got[0])
	}
}

// A record saved without a status defaults to "sent" — the caller shouldn't
// have to know the local-only sentinel value.
func TestIssueReportsSaveSentDefaultsStatus(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	ctx := context.Background()
	if err := repo.SaveSent(ctx, SentReport{
		ID:         "rep-nostatus",
		CapturedAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveSent: %v", err)
	}
	got, err := repo.ListSent(ctx, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListSent: %v (%d rows)", err, len(got))
	}
	if got[0].Status != "sent" {
		t.Fatalf("status = %q, want the %q default", got[0].Status, "sent")
	}
}

// UpdateStatus updates exactly the addressed row and stamps last_synced_at.
func TestIssueReportsUpdateStatusUpdatesRightRow(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	ctx := context.Background()
	for i, id := range []string{"rep-1", "rep-2"} {
		if err := repo.SaveSent(ctx, SentReport{
			ID:         id,
			CapturedAt: time.Date(2026, 8, 1+i, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("SaveSent %s: %v", id, err)
		}
	}

	if err := repo.UpdateStatus(ctx, "rep-1", "filed", "https://github.com/universaltill/ut-docs/issues/999"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := repo.ListSent(ctx, 10)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	byID := map[string]SentReport{}
	for _, r := range got {
		byID[r.ID] = r
	}
	if byID["rep-1"].Status != "filed" || byID["rep-1"].GithubIssueURL != "https://github.com/universaltill/ut-docs/issues/999" {
		t.Fatalf("rep-1 not updated: %+v", byID["rep-1"])
	}
	if !byID["rep-1"].LastSyncedAt.Valid {
		t.Fatalf("rep-1 last_synced_at not stamped: %+v", byID["rep-1"])
	}
	if byID["rep-2"].Status != "sent" || byID["rep-2"].GithubIssueURL != "" || byID["rep-2"].LastSyncedAt.Valid {
		t.Fatalf("rep-2 must be untouched: %+v", byID["rep-2"])
	}
}

// A status pull can reference a report id this till never retained (predates
// the feature, or was purged) — that is NOT an error, just a no-op.
func TestIssueReportsUpdateStatusUnknownIdIsNoop(t *testing.T) {
	d := openIssueReportsTestDB(t)
	repo := NewIssueReportsRepo(d.DB)
	if err := repo.UpdateStatus(context.Background(), "never-seen", "filed", ""); err != nil {
		t.Fatalf("UpdateStatus on an unknown id must be a silent no-op, got: %v", err)
	}
}
