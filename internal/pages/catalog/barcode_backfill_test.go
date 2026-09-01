package catalog

// ut-docs#1356: bulk "backfill barcodes from SKU" — preview lists eligible
// items plus the two issue buckets ("can't derive" / "already in use");
// commit assigns and reports skips (including a forced collision); and
// commit re-derives fresh rather than trusting a stale preview.

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// seedNarrowedSymbologies restricts the shop's enabled barcode symbologies
// to EXACTLY the given ids — under the DEFAULT set (every catch-all
// enabled), virtually any non-empty SKU derives SOMETHING (CODE128/
// INTERNAL_PLU accept almost anything), so triggering DeriveNumberBarcode's
// "no symbology match" issue in a test needs a narrowed set, same
// precondition DeriveNumberBarcode's own doc comment names.
func seedNarrowedSymbologies(t *testing.T, db *sql.DB, ids string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('barcode_enabled_symbologies', ?)`, ids); err != nil {
		t.Fatalf("seed enabled symbologies: %v", err)
	}
}

// eligibleEAN13 and collisionEAN13/noMatchNumber are fixed, real,
// checksum-valid EAN-13s (eligibleEAN13/collisionEAN13 are the same values
// already used as valid EAN-13s elsewhere in this codebase's tests) so
// DeriveNumberBarcode's plain-EAN13 matcher (13 digits + valid GS1 check
// digit) accepts them deterministically under an EAN13-only enabled set.
const (
	eligibleEAN13  = "4006381333931"
	collisionEAN13 = "5449000000996"
	noMatchNumber  = "123" // not 13 digits: fails EAN13-only matching
)

func TestBarcodeBackfillPreview_ListsEligibleIssuesAndConflicts(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedNarrowedSymbologies(t, db, `["EAN13"]`)

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-elig", SKU: eligibleEAN13, Name: "Eligible Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-owner", SKU: "OWNER1", Name: "Owner Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'i-owner', 1)`, collisionEAN13); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-conf", SKU: collisionEAN13, Name: "Conflicted Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-nosym", SKU: noMatchNumber, Name: "NoSym Item", BasePrice: 100, IsActive: true})

	rec := get(t, mux, "/api/catalog/barcode-backfill")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Eligible Item") || !strings.Contains(body, eligibleEAN13) {
		t.Fatalf("preview must list the eligible item with its derived barcode: %s", body)
	}
	if !strings.Contains(body, "NoSym Item") {
		t.Fatalf("preview must list the item whose SKU can't derive a barcode: %s", body)
	}
	if !strings.Contains(body, "Conflicted Item") || !strings.Contains(body, "Owner Item") {
		t.Fatalf("preview must list the conflicted item, naming its conflicting owner: %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/catalog/barcode-backfill"`) {
		t.Fatalf("preview with at least one eligible item must offer the confirm button: %s", body)
	}
}

func TestBarcodeBackfillPreview_EmptyStateNoConfirmButton(t *testing.T) {
	mux, _ := newCatalogMux(t)
	// No items seeded at all: nothing without a barcode to consider.
	rec := get(t, mux, "/api/catalog/barcode-backfill")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Nothing to backfill") {
		t.Fatalf("empty preview must say plainly there's nothing to backfill: %s", body)
	}
	if strings.Contains(body, `hx-post="/api/catalog/barcode-backfill"`) {
		t.Fatalf("empty preview must not offer a confirm button: %s", body)
	}
}

// TestBarcodeBackfillPreview_ZeroEligibleWithIssuesStillSaysSoPlainly covers
// the OTHER zero-eligible shape (distinct from the fully-empty case above):
// every candidate has an issue (so the issue buckets render), but nothing
// is actually eligible to assign — the brief's "if zero eligible items, say
// so plainly (no confirm button)" must hold here too, not only when there
// are zero candidates at all.
func TestBarcodeBackfillPreview_ZeroEligibleWithIssuesStillSaysSoPlainly(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedNarrowedSymbologies(t, db, `["EAN13"]`)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-nosym", SKU: noMatchNumber, Name: "NoSym Only Item", BasePrice: 100, IsActive: true})

	rec := get(t, mux, "/api/catalog/barcode-backfill")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "NoSym Only Item") {
		t.Fatalf("preview must still list the issue: %s", body)
	}
	if !strings.Contains(body, "Nothing to backfill") {
		t.Fatalf("zero eligible (even with issues present) must say so plainly: %s", body)
	}
	if strings.Contains(body, `hx-post="/api/catalog/barcode-backfill"`) {
		t.Fatalf("zero eligible must not offer a confirm button, even with issues present: %s", body)
	}
}

func TestBarcodeBackfillCommit_AssignsAndReportsSkips(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedNarrowedSymbologies(t, db, `["EAN13"]`)

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-elig", SKU: eligibleEAN13, Name: "Eligible Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-owner", SKU: "OWNER1", Name: "Owner Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'i-owner', 1)`, collisionEAN13); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-conf", SKU: collisionEAN13, Name: "Conflicted Item", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/barcode-backfill", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("commit code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("commit must set HX-Refresh: true for the full catalog refresh, got %q", rec.Header().Get("HX-Refresh"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Assigned 1") {
		t.Fatalf("result must report exactly 1 assigned: %s", body)
	}
	if !strings.Contains(body, "Conflicted Item") {
		t.Fatalf("result must list the skipped conflicted item with its reason: %s", body)
	}

	var gotBarcode string
	if err := db.QueryRow(`SELECT barcode FROM item_barcodes WHERE item_id = 'i-elig'`).Scan(&gotBarcode); err != nil {
		t.Fatalf("query assigned barcode: %v", err)
	}
	if gotBarcode != eligibleEAN13 {
		t.Fatalf("assigned barcode = %q, want %q", gotBarcode, eligibleEAN13)
	}

	var conflictedCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = 'i-conf'`).Scan(&conflictedCount); err != nil {
		t.Fatalf("count conflicted item's barcodes: %v", err)
	}
	if conflictedCount != 0 {
		t.Fatalf("conflicted item must stay barcode-less (skipped), got %d barcode row(s)", conflictedCount)
	}
}

// TestBarcodeBackfillCommit_ReDerivesFreshNotStalePreview is the brief's key
// guardrail: the commit must NEVER trust a preview computed earlier — it
// re-derives eligibility against CURRENT data. This mutates state (another
// operator/import attaches the very code the preview showed as available,
// to a DIFFERENT item) strictly BETWEEN the preview call and the commit
// call, and asserts the commit reflects the new reality, not the stale one
// the preview saw.
func TestBarcodeBackfillCommit_ReDerivesFreshNotStalePreview(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedNarrowedSymbologies(t, db, `["EAN13"]`)

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-x", SKU: eligibleEAN13, Name: "Race Item", BasePrice: 100, IsActive: true})

	// 1. Preview: shows i-x as eligible for eligibleEAN13.
	previewRec := get(t, mux, "/api/catalog/barcode-backfill")
	if previewRec.Code != http.StatusOK {
		t.Fatalf("preview code = %d: %s", previewRec.Code, previewRec.Body.String())
	}
	if !strings.Contains(previewRec.Body.String(), "Race Item") {
		t.Fatalf("preview must show i-x as eligible before the race: %s", previewRec.Body.String())
	}

	// 2. Mutate BETWEEN preview and commit: a concurrent operator attaches
	// the exact same derived code to a DIFFERENT item.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-other", SKU: "OTHER1", Name: "Other Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES(?, 'i-other', 1)`, eligibleEAN13); err != nil {
		t.Fatal(err)
	}

	// 3. Commit: must re-derive fresh and see the NEW conflict, not the
	// stale "eligible" verdict from step 1's preview.
	commitRec := postForm(t, mux, "/api/catalog/barcode-backfill", "")
	if commitRec.Code != http.StatusOK {
		t.Fatalf("commit code = %d: %s", commitRec.Code, commitRec.Body.String())
	}
	if !strings.Contains(commitRec.Body.String(), "Assigned 0") {
		t.Fatalf("commit must assign nothing once the code races out from under it: %s", commitRec.Body.String())
	}
	if !strings.Contains(commitRec.Body.String(), "Race Item") {
		t.Fatalf("commit's own result must name the item the race knocked out, not just a bare zero: %s", commitRec.Body.String())
	}

	var itemXCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = 'i-x'`).Scan(&itemXCount); err != nil {
		t.Fatalf("count i-x barcodes: %v", err)
	}
	if itemXCount != 0 {
		t.Fatalf("i-x must stay barcode-less — the code it wanted was claimed first, got %d barcode row(s)", itemXCount)
	}
}
