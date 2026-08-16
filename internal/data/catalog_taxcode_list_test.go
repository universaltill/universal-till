package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ListTaxCodes backs the takeaway-rate-overrides settings UI (ut-docs#190):
// it needs the full set of active tax codes (id, name, dine-in rate, and any
// pinned takeaway rate to suggest as a placeholder) to render one row per
// code. Only active codes are relevant to a shop owner picking overrides;
// inactive/retired codes must not clutter the editor.
func TestListTaxCodes(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "taxlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	ta7 := 700
	if _, _, err := repo.FindOrCreateTaxCode(ctx, 1900, &ta7); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax_retired', 'Retired', 500, 0)`); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListTaxCodes(ctx)
	if err != nil {
		t.Fatalf("ListTaxCodes: %v", err)
	}

	// The seeded 'tax_std' (2000, NULL) is present, active, ordered before
	// the freshly-created 1900 pair (ordered by rate_basis_points DESC).
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 active tax codes, got %d: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.ID == "tax_retired" {
			t.Fatalf("expected inactive tax code excluded, got %+v", row)
		}
	}
	// Ordering: highest rate first.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].RateBP < rows[i].RateBP {
			t.Fatalf("expected rate_basis_points DESC ordering, got %+v", rows)
		}
	}

	var found19 bool
	for _, row := range rows {
		if row.RateBP == 1900 {
			found19 = true
			if row.TakeawayRateBP == nil || *row.TakeawayRateBP != 700 {
				t.Fatalf("expected takeaway rate 700 pinned on the 19%% code, got %+v", row)
			}
		}
	}
	if !found19 {
		t.Fatalf("expected the 19%% tax code in the list, got %+v", rows)
	}

	var foundStd bool
	for _, row := range rows {
		if row.ID == "tax_std" {
			foundStd = true
			if row.Name == "" {
				t.Fatalf("expected a non-empty name for tax_std, got %+v", row)
			}
			if row.TakeawayRateBP != nil {
				t.Fatalf("expected tax_std to have no pinned takeaway rate, got %+v", row)
			}
		}
	}
	if !foundStd {
		t.Fatalf("expected seeded tax_std in the list, got %+v", rows)
	}
}
