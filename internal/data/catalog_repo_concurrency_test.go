package data_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// TestAddBarcodeConcurrentRace exercises the check-then-insert race in
// AddBarcode (ut-docs#304): two callers racing to attach the SAME barcode to
// two DIFFERENT items must never both succeed — pre-fix, both could pass the
// availability check and the second ON CONFLICT DO UPDATE silently reassigned
// the barcode, so it scanned to the wrong product at the till.
//
// This deliberately uses a REAL migrated file-backed database via db.Open
// (not testsupport.NewCatalogTestDB): a ":memory:" DSN gives each pooled
// connection its own isolated database, which cannot exercise
// multi-connection locking at all.
func TestAddBarcodeConcurrentRace(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()

	repo := data.NewCatalogRepo(dbh.DB)
	ctx := context.Background()

	itemA, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		ID: "race-item-a", SKU: "RACE-A", Name: "Race Item A", BasePrice: 100, IsActive: true,
	})
	if err != nil {
		t.Fatalf("create item A: %v", err)
	}
	itemB, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		ID: "race-item-b", SKU: "RACE-B", Name: "Race Item B", BasePrice: 200, IsActive: true,
	})
	if err != nil {
		t.Fatalf("create item B: %v", err)
	}

	// A single run can miss the race window by chance, so run the
	// two-goroutine race repeatedly and assert the invariant every time.
	const rounds = 30
	for i := 0; i < rounds; i++ {
		barcode := fmt.Sprintf("RACE-%03d", i)
		items := [2]string{itemA, itemB}
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start // both released together to maximise the race window
				errs[j] = repo.AddBarcode(ctx, catalogtypes.BarcodeInput{
					Barcode: barcode, ItemID: items[j],
				})
			}(j)
		}
		close(start)
		wg.Wait()

		// Exactly one call must win; the loser must get an explicit error.
		switch {
		case errs[0] == nil && errs[1] == nil:
			t.Fatalf("round %d: both AddBarcode calls succeeded for barcode %q — silent reassignment race", i, barcode)
		case errs[0] != nil && errs[1] != nil:
			t.Fatalf("round %d: both AddBarcode calls failed for barcode %q: %v / %v", i, barcode, errs[0], errs[1])
		}
		winner, loserErr := items[0], errs[1]
		if errs[0] != nil {
			winner, loserErr = items[1], errs[0]
		}
		if !strings.Contains(loserErr.Error(), "already assigned") {
			t.Fatalf("round %d: loser error should say the barcode is already assigned, got: %v", i, loserErr)
		}

		// The barcode must be attached to EXACTLY the winner's item.
		var n int
		if err := dbh.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&n); err != nil {
			t.Fatalf("round %d: count barcode rows: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("round %d: expected exactly 1 row for barcode %q, got %d", i, barcode, n)
		}
		var owner string
		if err := dbh.QueryRowContext(ctx,
			`SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&owner); err != nil {
			t.Fatalf("round %d: read barcode owner: %v", i, err)
		}
		if owner != winner {
			t.Fatalf("round %d: barcode %q attached to %q but the winning call was for %q — reassigned", i, barcode, owner, winner)
		}
	}
}
