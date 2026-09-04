package data

import (
	"context"
	"testing"
	"time"
)

// TestEODClosesForExport covers the ut-docs#1005 conversion from archived
// report rows to the export payload's eod_closes: only kind="eod" rows
// qualify, each carries its own Z-number, and the Report is the archived
// content_json unmarshaled — never a fresh recomputation.
func TestEODClosesForExport(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	repo := dbx.repo

	// ADR-0066 Decision 5: ArchivedReportsInRange (which this method calls)
	// now filters on each row's own created_at, not a period BETWEEN text
	// compare -- a zero closedAt stamps the schema default's real "now"
	// regardless of what period string is given, so a fixed 2026-08
	// literal no longer proves anything about range bounding once the
	// calendar rolls past August. Anchored on the host's own real "now"
	// (never a hardcoded year) with an explicit closedAt per row so each
	// genuinely lands on a distinct real day, same fix shape as
	// TestArchivedReportsInRange_BoundedAndOrdered.
	// LOCAL noon, never UTC (2026-09-04 review of ut-docs#1141): the range
	// filter compares datetime(created_at, 'localtime') against these same
	// LOCAL date bounds, so a UTC-anchored seed would encode UTC-day
	// semantics and flip this test's meaning on a non-UTC host — the exact
	// mistake eod_zreport_local_day_869_test.go's own doc comment records.
	// Noon keeps every same-day instant safely inside its calendar day for
	// any real IANA offset (-12..+14).
	anchor := time.Now()
	at := func(daysAgo int) time.Time {
		return time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
	}
	close1At := at(2)
	close2At := at(1)
	outOfRangeAt := at(20)

	// Two real closes with the cross-tab fields the DATEV batch reads.
	if _, err := repo.ArchiveReport(ctx, "eod", close1At.Format(time.RFC3339),
		[]byte(`{"day":"2026-08-20","gross":11900,"method_tax_bands":[{"method":"cash","rate_bp":1900,"net":10000,"tax":1900,"gross":11900}]}`),
		"R-1", "R-9", close1At); err != nil {
		t.Fatalf("archive close 1: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", close2At.Format(time.RFC3339),
		[]byte(`{"day":"2026-08-21","gross":23800,"method_tax_bands":[{"method":"card","rate_bp":1900,"net":20000,"tax":3800,"gross":23800}],"tips":[{"method":"card","count":1,"amount":320}]}`),
		"R-10", "R-20", close2At); err != nil {
		t.Fatalf("archive close 2: %v", err)
	}
	// A non-eod archived report in the same window — must be filtered out
	// by kind, not range (zero closedAt is fine here: EODClosesFromArchive
	// drops it regardless of where its real created_at lands).
	if _, err := repo.ArchiveReport(ctx, "weekly", "2026-08-21", []byte(`{"gross":1}`), "", "", time.Time{}); err != nil {
		t.Fatalf("archive weekly: %v", err)
	}
	// Outside the range — excluded by ArchivedReportsInRange itself.
	if _, err := repo.ArchiveReport(ctx, "eod", outOfRangeAt.Format(time.RFC3339), []byte(`{"day":"2026-09-01"}`), "", "", outOfRangeAt); err != nil {
		t.Fatalf("archive out-of-range close: %v", err)
	}

	from := at(3).Format("2006-01-02")
	to := at(1).Format("2006-01-02")
	closes, err := repo.EODClosesForExport(ctx, from, to)
	if err != nil {
		t.Fatalf("EODClosesForExport: %v", err)
	}
	if len(closes) != 2 {
		t.Fatalf("expected 2 eod closes in range, got %d: %+v", len(closes), closes)
	}
	// Oldest first (ArchivedReportsInRange's order), each with its own
	// Z-number — never merged.
	if closes[0].Report.Day != "2026-08-20" || closes[1].Report.Day != "2026-08-21" {
		t.Fatalf("expected period order 2026-08-20, 2026-08-21; got %q, %q", closes[0].Report.Day, closes[1].Report.Day)
	}
	if closes[0].ZNumber != 1 || closes[1].ZNumber != 2 {
		t.Fatalf("expected z_numbers 1 and 2, got %d and %d", closes[0].ZNumber, closes[1].ZNumber)
	}
	if len(closes[0].Report.MethodTaxBands) != 1 || closes[0].Report.MethodTaxBands[0].Gross != 11900 {
		t.Fatalf("close 1 should carry its archived cross-tab, got %+v", closes[0].Report.MethodTaxBands)
	}
	if len(closes[1].Report.Tips) != 1 || closes[1].Report.Tips[0].Amount != 320 {
		t.Fatalf("close 2 should carry its archived tips, got %+v", closes[1].Report.Tips)
	}
}

// TestEODClosesForExport_CorruptContentSkippedNotFatal: a single archive row
// whose content_json fails to unmarshal is skipped (logged) — it must never
// take every other close's export down with it.
func TestEODClosesForExport_CorruptContentSkippedNotFatal(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	repo := dbx.repo

	// Same real-clock anchoring fix as TestEODClosesForExport above (ADR-0066
	// Decision 5) — a fixed 2026-08 literal doesn't survive the range
	// filter moving off period onto created_at.
	// LOCAL noon, never UTC (2026-09-04 review of ut-docs#1141): the range
	// filter compares datetime(created_at, 'localtime') against these same
	// LOCAL date bounds, so a UTC-anchored seed would encode UTC-day
	// semantics and flip this test's meaning on a non-UTC host — the exact
	// mistake eod_zreport_local_day_869_test.go's own doc comment records.
	// Noon keeps every same-day instant safely inside its calendar day for
	// any real IANA offset (-12..+14).
	anchor := time.Now()
	at := func(daysAgo int) time.Time {
		return time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
	}
	closeGood1At := at(3)
	closeCorruptAt := at(2)
	closeGood2At := at(1)

	if _, err := repo.ArchiveReport(ctx, "eod", closeGood1At.Format(time.RFC3339), []byte(`{"day":"2026-08-20"}`), "", "", closeGood1At); err != nil {
		t.Fatalf("archive good close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", closeCorruptAt.Format(time.RFC3339), []byte(`this is not json`), "", "", closeCorruptAt); err != nil {
		t.Fatalf("archive corrupt close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", closeGood2At.Format(time.RFC3339), []byte(`{"day":"2026-08-22"}`), "", "", closeGood2At); err != nil {
		t.Fatalf("archive second good close: %v", err)
	}

	from := at(4).Format("2006-01-02")
	to := at(1).Format("2006-01-02")
	closes, err := repo.EODClosesForExport(ctx, from, to)
	if err != nil {
		t.Fatalf("a corrupt archive row must not fail the batch, got error: %v", err)
	}
	if len(closes) != 2 {
		t.Fatalf("expected the 2 parseable closes, got %d: %+v", len(closes), closes)
	}
	if closes[0].Report.Day != "2026-08-20" || closes[1].Report.Day != "2026-08-22" {
		t.Fatalf("expected the corrupt middle row skipped, got %q and %q", closes[0].Report.Day, closes[1].Report.Day)
	}
}

// TestEODClosesFromArchive_FiltersKind exercises the pure conversion
// directly: non-eod kinds never convert, whatever their content looks like.
func TestEODClosesFromArchive_FiltersKind(t *testing.T) {
	rows := []ArchivedReportRow{
		{ID: "a", Kind: "weekly", Period: "2026-08-20", Content: `{"day":"2026-08-20"}`, ZNumber: 5},
		{ID: "b", Kind: "eod", Period: "2026-08-21", Content: `{"day":"2026-08-21"}`, ZNumber: 6},
	}
	closes := EODClosesFromArchive(rows)
	if len(closes) != 1 {
		t.Fatalf("expected only the eod row, got %d: %+v", len(closes), closes)
	}
	if closes[0].ZNumber != 6 || closes[0].Report.Day != "2026-08-21" {
		t.Fatalf("unexpected converted close: %+v", closes[0])
	}
}
