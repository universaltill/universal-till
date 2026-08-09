package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// FindOrCreateTaxCode underpins catalog import's tax grouping (ut-docs#512):
// items group onto tax codes by their (dine-in rate, takeaway rate) pair, so
// re-running an import must resolve the same pair to the same row
// (idempotent), and two pairs that share a dine-in rate but differ in
// takeaway behaviour must NEVER collapse onto one code — that's the two
// distinct 19% groups from ut-docs#512's real café distribution: (19%, takeaway
// 7%) needs a tax.rate.ask override, (19%, no override) doesn't.
func TestFindOrCreateTaxCode_IdempotentByPair(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "tax.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	ta7 := 700

	// Create the override pair (19% dine-in, 7% takeaway).
	pair19_7, created, err := repo.FindOrCreateTaxCode(ctx, 1900, &ta7)
	if err != nil || pair19_7 == "" || !created {
		t.Fatalf("create (1900,&700): id=%q created=%v err=%v", pair19_7, created, err)
	}

	// Idempotent: the identical pair re-finds the same row, creates nothing.
	again, created, err := repo.FindOrCreateTaxCode(ctx, 1900, &ta7)
	if err != nil || again != pair19_7 || created {
		t.Fatalf("re-find (1900,&700): id=%q (want %q) created=%v err=%v", again, pair19_7, created, err)
	}

	// The two distinct 19% groups: the same dine-in rate with NO takeaway
	// override is a DIFFERENT code — they must not collide even though both
	// have rate_basis_points = 1900.
	flat19, created, err := repo.FindOrCreateTaxCode(ctx, 1900, nil)
	if err != nil || flat19 == "" {
		t.Fatalf("create (1900,nil): id=%q created=%v err=%v", flat19, created, err)
	}
	if flat19 == pair19_7 {
		t.Fatal("(1900,nil) and (1900,&700) must be two different tax codes")
	}
	// And it's idempotent on its own too.
	if got, created, err := repo.FindOrCreateTaxCode(ctx, 1900, nil); err != nil || got != flat19 || created {
		t.Fatalf("re-find (1900,nil): id=%q (want %q) created=%v err=%v", got, flat19, created, err)
	}

	// Different flat rates never collide.
	flat7, _, err := repo.FindOrCreateTaxCode(ctx, 700, nil)
	if err != nil || flat7 == "" || flat7 == flat19 {
		t.Fatalf("create (700,nil): id=%q err=%v (flat19=%q)", flat7, err, flat19)
	}

	// A zero rate is a legitimate pair, not an error (the 0% group).
	zero, _, err := repo.FindOrCreateTaxCode(ctx, 0, nil)
	if err != nil || zero == "" {
		t.Fatalf("create (0,nil): id=%q err=%v", zero, err)
	}

	// The seeded 'Standard VAT' row is (2000, NULL) — a flat 20% import must
	// reuse it rather than duplicating it (idempotent against pre-existing
	// codes, not just this import's own creations).
	std, created, err := repo.FindOrCreateTaxCode(ctx, 2000, nil)
	if err != nil || std != "tax_std" || created {
		t.Fatalf("(2000,nil) should re-find the seeded tax_std: id=%q created=%v err=%v", std, created, err)
	}

	// Generated names are human-readable and percent-formatted.
	var name string
	if err := d.DB.QueryRowContext(ctx, `SELECT name FROM tax_codes WHERE id = ?`, pair19_7).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Imported 19% (takeaway 7%)" {
		t.Errorf("override pair name = %q, want %q", name, "Imported 19% (takeaway 7%)")
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT name FROM tax_codes WHERE id = ?`, flat19).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Imported 19%" {
		t.Errorf("flat rate name = %q, want %q", name, "Imported 19%")
	}

	// Fractional rates format cleanly (1950 → "19.5", no trailing zeros).
	frac, _, err := repo.FindOrCreateTaxCode(ctx, 1950, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT name FROM tax_codes WHERE id = ?`, frac).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Imported 19.5%" {
		t.Errorf("fractional rate name = %q, want %q", name, "Imported 19.5%")
	}

	// New rows are active.
	var active int
	if err := d.DB.QueryRowContext(ctx, `SELECT is_active FROM tax_codes WHERE id = ?`, pair19_7).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Errorf("created tax code should be active, is_active = %d", active)
	}
}

// A pre-existing, manually created tax code whose name happens to collide
// with the generated "Imported N%" name — but whose (rate, takeaway) pair is
// different — must surface as an error (the UNIQUE name constraint), not a
// crash, and must not silently return the wrong-rate code.
func TestFindOrCreateTaxCode_NameCollisionWithDifferentPairErrors(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "taxcollide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	// A hand-made code named exactly like the generated name, wrong rate.
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax_manual', 'Imported 19%', 1800, 1)`); err != nil {
		t.Fatal(err)
	}

	id, created, err := repo.FindOrCreateTaxCode(ctx, 1900, nil)
	if err == nil {
		t.Fatalf("expected a name-collision error, got id=%q created=%v", id, created)
	}
	// The wrong-rate manual code must never be returned as a match.
	if id == "tax_manual" {
		t.Fatal("must not return a code whose rate does not match the requested pair")
	}
}
