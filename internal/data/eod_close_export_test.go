package data

import (
	"context"
	"testing"
)

// TestEODClosesForExport covers the ut-docs#1005 conversion from archived
// report rows to the export payload's eod_closes: only kind="eod" rows
// qualify, each carries its own Z-number, and the Report is the archived
// content_json unmarshaled — never a fresh recomputation.
func TestEODClosesForExport(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	repo := dbx.repo

	// Two real closes with the cross-tab fields the DATEV batch reads.
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-08-20",
		[]byte(`{"day":"2026-08-20","gross":11900,"method_tax_bands":[{"method":"cash","rate_bp":1900,"net":10000,"tax":1900,"gross":11900}]}`),
		"R-1", "R-9"); err != nil {
		t.Fatalf("archive close 1: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-08-21",
		[]byte(`{"day":"2026-08-21","gross":23800,"method_tax_bands":[{"method":"card","rate_bp":1900,"net":20000,"tax":3800,"gross":23800}],"tips":[{"method":"card","count":1,"amount":320}]}`),
		"R-10", "R-20"); err != nil {
		t.Fatalf("archive close 2: %v", err)
	}
	// A non-eod archived report in the same range — must be filtered out.
	if _, err := repo.ArchiveReport(ctx, "weekly", "2026-08-21", []byte(`{"gross":1}`), "", ""); err != nil {
		t.Fatalf("archive weekly: %v", err)
	}
	// Outside the range — excluded by ArchivedReportsInRange itself.
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-09-01", []byte(`{"day":"2026-09-01"}`), "", ""); err != nil {
		t.Fatalf("archive out-of-range close: %v", err)
	}

	closes, err := repo.EODClosesForExport(ctx, "2026-08-01", "2026-08-31")
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

	if _, err := repo.ArchiveReport(ctx, "eod", "2026-08-20", []byte(`{"day":"2026-08-20"}`), "", ""); err != nil {
		t.Fatalf("archive good close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-08-21", []byte(`this is not json`), "", ""); err != nil {
		t.Fatalf("archive corrupt close: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-08-22", []byte(`{"day":"2026-08-22"}`), "", ""); err != nil {
		t.Fatalf("archive second good close: %v", err)
	}

	closes, err := repo.EODClosesForExport(ctx, "2026-08-01", "2026-08-31")
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
