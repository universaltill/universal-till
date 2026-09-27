package db

import (
	"path/filepath"
	"testing"
)

// builtinCountryPTMigrationVersion is 051_builtin_country_pt.sql
// (ut-docs#2963, ADR-0100). Migration files are never renumbered once
// merged; TestMigration051_IsOnDisk pins this constant to that filename.
const builtinCountryPTMigrationVersion = 51

type ptCountryRow struct {
	nameKey, currency, symbol, locale, updatedAt string
	taxRateBP, archiveMinDays                    int64
	inclusive, builtin                           int
}

func readPTCountryRow(t *testing.T, d *DB) (ptCountryRow, int) {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM country_settings WHERE code = 'PT'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		return ptCountryRow{}, n
	}
	var r ptCountryRow
	if err := d.QueryRow(`SELECT name_key, currency, currency_symbol, default_locale, updated_at,
	        tax_rate_bp, archive_min_days, tax_inclusive, is_builtin
	   FROM country_settings WHERE code = 'PT'`).Scan(
		&r.nameKey, &r.currency, &r.symbol, &r.locale, &r.updatedAt,
		&r.taxRateBP, &r.archiveMinDays, &r.inclusive, &r.builtin); err != nil {
		t.Fatal(err)
	}
	return r, n
}

func replayMigration051(t *testing.T, d *DB) {
	t.Helper()
	m := loadMigrationVersion(t, builtinCountryPTMigrationVersion)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := execMigrationStatements(tx, m); err != nil {
		tx.Rollback()
		t.Fatalf("replay of 051 must not fail: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestMigration051_FreshOpenSeedsPT: a fresh database carries exactly one PT
// row with the shipped defaults, stamped with the seed's epoch updated_at so
// it never outranks an operator's own edit in admin sync.
func TestMigration051_FreshOpenSeedsPT(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m051.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	got, n := readPTCountryRow(t, d)
	if n != 1 {
		t.Fatalf("PT rows after fresh Open = %d, want 1", n)
	}
	want := ptCountryRow{
		nameKey: "setup.country.pt", currency: "EUR", symbol: "€", locale: "pt-PT",
		updatedAt: "1970-01-01T00:00:00Z", taxRateBP: 2300, archiveMinDays: 3650,
		inclusive: 1, builtin: 1,
	}
	if got != want {
		t.Fatalf("PT row = %+v, want %+v", got, want)
	}
}

// TestMigration051_PromotesOperatorCreatedPT: a till whose operator already
// created a custom PT country before this release keeps every value they
// chose. The row only becomes builtin (what CountrySettingsRepo.Upsert would
// do on its next save anyway) and a blank name_key gains the locale key.
func TestMigration051_PromotesOperatorCreatedPT(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m051-custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.Exec(`DELETE FROM country_settings WHERE code = 'PT'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO country_settings
	    (code, name_key, currency, currency_symbol, tax_rate_bp, tax_inclusive, archive_min_days, is_builtin, updated_at, default_locale)
	    VALUES ('PT', '', 'EUR', 'EUR', 600, 0, 4000, 0, '2026-09-01T10:00:00Z', 'en-GB')`); err != nil {
		t.Fatal(err)
	}

	replayMigration051(t, d)

	got, n := readPTCountryRow(t, d)
	if n != 1 {
		t.Fatalf("PT rows after replay = %d, want 1", n)
	}
	want := ptCountryRow{
		nameKey: "setup.country.pt", currency: "EUR", symbol: "EUR", locale: "en-GB",
		updatedAt: "2026-09-01T10:00:00Z", taxRateBP: 600, archiveMinDays: 4000,
		inclusive: 0, builtin: 1,
	}
	if got != want {
		t.Fatalf("operator PT row after 051 = %+v, want %+v (operator values kept, only promoted to builtin)", got, want)
	}
}

// TestMigration051_KeepsOperatorNameKey: a custom PT row that already names
// itself keeps that name.
func TestMigration051_KeepsOperatorNameKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m051-name.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.Exec(`UPDATE country_settings SET name_key = 'custom.pt', is_builtin = 0 WHERE code = 'PT'`); err != nil {
		t.Fatal(err)
	}
	replayMigration051(t, d)

	got, _ := readPTCountryRow(t, d)
	if got.nameKey != "custom.pt" || got.builtin != 1 {
		t.Fatalf("PT row = %+v, want name_key custom.pt kept and builtin=1", got)
	}
}

// TestMigration051_IsIdempotent replays 051 against a database that already
// has the row; nothing may change and nothing may fail.
func TestMigration051_IsIdempotent(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m051-idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	before, _ := readPTCountryRow(t, d)
	for i := 0; i < 2; i++ {
		replayMigration051(t, d)
	}
	after, n := readPTCountryRow(t, d)
	if n != 1 || after != before {
		t.Fatalf("after replays PT rows = %d, row = %+v, want 1 row unchanged from %+v", n, after, before)
	}
}

// TestMigration051_IsOnDisk guards the number this file's sibling tests
// hardcode.
func TestMigration051_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, builtinCountryPTMigrationVersion)
	if m.Name != "051_builtin_country_pt.sql" {
		t.Fatalf("migration %d on disk is %q, want 051_builtin_country_pt.sql", builtinCountryPTMigrationVersion, m.Name)
	}
}
