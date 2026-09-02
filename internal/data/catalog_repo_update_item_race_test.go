package data_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// TestUpdateItemReturningWasActiveConcurrentRace exercises the read-then-write
// race in the catalog item-update path (ut-docs#1399, follow-up to
// ut-docs#1365): the caller decides whether to APPEND a new row or update one
// in place from the item's previous is_active state. Pre-fix (a plain
// GetItem read followed by a separate UpdateItem write), two genuinely
// concurrent updates on the SAME item could both read the pre-update state
// before either write landed — both deciding APPEND and both emitting a row,
// duplicating it. Exactly one caller may ever see wasActive=false (the
// legitimate single append); every other concurrent caller must see the
// already-updated state and choose in-place.
//
// This deliberately uses a REAL migrated file-backed database via db.Open
// (not an in-memory DSN, which gives each pooled connection its own isolated
// database and can't exercise multi-connection locking at all) — same
// reasoning as TestAddBarcodeConcurrentRace in
// catalog_repo_concurrency_test.go.
func TestUpdateItemReturningWasActiveConcurrentRace(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()

	repo := data.NewCatalogRepo(dbh.DB)
	ctx := context.Background()

	itemID, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		ID: "race-update-item", SKU: "RACE-UPD", Name: "Race Update Item", BasePrice: 100, IsActive: false,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	// A single run can miss the race window by chance, so run the
	// n-goroutine race repeatedly and assert the invariant every time.
	const rounds = 15
	const n = 8
	for round := 0; round < rounds; round++ {
		// Each round starts the item inactive again so every round
		// re-creates the same "reactivating from inactive" race.
		if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
			ID: itemID, SKU: "RACE-UPD", Name: "Race Update Item", BasePrice: 100, IsActive: false,
		}); err != nil {
			t.Fatalf("round %d: reset to inactive: %v", round, err)
		}

		var wasActive [n]bool
		var errs [n]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < n; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start // all released together to maximise the race window
				wasActive[j], errs[j] = repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{
					ID: itemID, SKU: "RACE-UPD", Name: "Race Update Item", BasePrice: 100, IsActive: true,
				})
			}(j)
		}
		close(start)
		wg.Wait()

		appendCount := 0
		for j := 0; j < n; j++ {
			if errs[j] != nil {
				t.Fatalf("round %d call %d: unexpected error: %v", round, j, errs[j])
			}
			if !wasActive[j] {
				appendCount++
			}
		}
		if appendCount != 1 {
			t.Fatalf("round %d: expected exactly 1 of %d concurrent updates to see wasActive=false (the single legitimate append), got %d — duplicate-row race", round, n, appendCount)
		}

		var active bool
		if err := dbh.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = ?`, itemID).Scan(&active); err != nil {
			t.Fatalf("round %d: read final is_active: %v", round, err)
		}
		if !active {
			t.Fatalf("round %d: item should be active after all updates committed", round)
		}
	}
}

// TestUpdateItemReturningWasActiveSemantics pins the single-caller behaviour
// the race test doesn't speak to, so a future refactor can't quietly change
// what the OOB-mode decision is derived from. Each case must match what the
// pre-fix handler (a GetItem read followed by a separate UpdateItem) did.
func TestUpdateItemReturningWasActiveSemantics(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()

	repo := data.NewCatalogRepo(dbh.DB)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		ID: "sem-item", SKU: "SEM-1", Name: "Sem Item", BasePrice: 100, IsActive: false,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	update := catalogtypes.ItemInput{ID: id, SKU: "SEM-1", Name: "Sem Item", BasePrice: 100, IsActive: true}

	// Previously inactive -> the caller must append (wasActive=false).
	wasActive, err := repo.UpdateItemReturningWasActive(ctx, update)
	if err != nil {
		t.Fatalf("update (was inactive): %v", err)
	}
	if wasActive {
		t.Fatal("item was inactive before the update; want wasActive=false so the row is appended")
	}

	// Now already active -> in-place (wasActive=true), and the write landed.
	wasActive, err = repo.UpdateItemReturningWasActive(ctx, update)
	if err != nil {
		t.Fatalf("update (was active): %v", err)
	}
	if !wasActive {
		t.Fatal("item was active before the update; want wasActive=true so the row updates in place")
	}
	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("read back item: ok=%v err=%v", ok, err)
	}
	if !got.IsActive {
		t.Fatal("update must have committed is_active=1")
	}

	// Unknown id: GetItem finds nothing, the UPDATE matches no row, and the
	// conservative default stands — exactly the pre-fix behaviour.
	wasActive, err = repo.UpdateItemReturningWasActive(ctx,
		catalogtypes.ItemInput{ID: "no-such-item", Name: "Ghost", BasePrice: 1, IsActive: true})
	if err != nil {
		t.Fatalf("update (unknown id) must not error: %v", err)
	}
	if !wasActive {
		t.Fatal("unknown id must fall back to the conservative wasActive=true default")
	}

	// Invalid input is rejected (and, post-review, before any transaction
	// is opened) with the same error text UpdateItem returns.
	if _, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{ID: "", Name: "x"}); err == nil {
		t.Fatal("empty id must be rejected")
	}
	if _, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{ID: id, Name: ""}); err == nil {
		t.Fatal("empty name must be rejected")
	}
}
