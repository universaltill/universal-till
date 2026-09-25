package data_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0093 Amendment B (ut-docs#2712): the primary-side atomic claim a
// resume goes through, and the short-lived tombstone both primary-side
// deletions leave behind. A REAL migrated, file-backed database via db.Open
// (not ":memory:"), same reason as TestAddBarcodeConcurrentRace: a
// ":memory:" DSN gives every pooled connection its own database and cannot
// exercise two writers racing at all.
func newHeldSalesTombstoneRepo(t *testing.T) (*data.HeldSalesRepo, *db.DB) {
	t.Helper()
	dbh, err := db.Open(testsupport.MigratedDBFile(t, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	return data.NewHeldSalesRepo(dbh.DB), dbh
}

var tombstoneTestHeldSale = data.HeldSale{ID: "h1", Label: "Table 4", TotalMinor: 1250, LineCount: 3, Payload: `{"lines":[]}`}

// Claim's three answers: a present row is handed back and gone
// (claimed); a claim by ANOTHER till of the same id finds only the
// tombstone (resolved -- known); an id nothing ever claimed or deleted is
// neither (never learned of it -- trust local).
func TestHeldSalesRepo_ClaimAndTombstone_Answers(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()
	if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
		t.Fatal(err)
	}

	got, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-a")
	if err != nil || !claimed || !known {
		t.Fatalf("claiming a present row: claimed=%v known=%v err=%v", claimed, known, err)
	}
	if got.ID != "h1" || got.Payload != tombstoneTestHeldSale.Payload || got.TotalMinor != 1250 || got.Label != "Table 4" || got.CreatedAt == "" {
		t.Fatalf("the claimed row must be handed back whole, got %+v", got)
	}
	if _, ok, _ := repo.Get(ctx, "h1"); ok {
		t.Fatal("a claimed row must be deleted")
	}

	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-b"); err != nil || claimed || !known {
		t.Fatalf("another till's claim must be refused as resolved (claimed=false, known=true), got claimed=%v known=%v err=%v", claimed, known, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "never-seen", "till-b"); err != nil || claimed || known {
		t.Fatalf("an id never claimed or deleted must be claimed=false, known=false, got claimed=%v known=%v err=%v", claimed, known, err)
	}
}

// Independent review of #2712: a till's OWN tombstone never answers known.
// After a resume the same order is re-parked under the same id
// (ut-docs#1918); if that re-park fell back to local-only, the re-parking
// till holds the newest copy and its own earlier claim must not refuse it.
// Another till's tombstone for the same id still does -- and a later
// resolution by another till overrides the caller's own stamp.
func TestHeldSalesRepo_ClaimAndTombstone_OwnTombstoneIsNotKnown(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()
	if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _, err := repo.ClaimAndTombstone(ctx, "h1", "till-a"); err != nil || !claimed {
		t.Fatalf("seed claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-a"); err != nil || claimed || known {
		t.Fatalf("the same till's later claim must find nothing contradicting its own copy (claimed=false, known=false), got claimed=%v known=%v err=%v", claimed, known, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-b"); err != nil || claimed || !known {
		t.Fatalf("another till's claim must still be refused, got claimed=%v known=%v err=%v", claimed, known, err)
	}
	// The primary till's own resume stamps "": no replica ever matches it.
	if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _, err := repo.ClaimAndTombstone(ctx, "h1", ""); err != nil || !claimed {
		t.Fatalf("primary's own claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-a"); err != nil || claimed || !known {
		t.Fatalf("a replica's claim of an order the primary till resumed must be refused, got claimed=%v known=%v err=%v", claimed, known, err)
	}
	// till-a re-parks it online (row back) and till-b takes it: the tombstone
	// now carries till-b, so till-a's own copy is stale and refused.
	if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _, err := repo.ClaimAndTombstone(ctx, "h1", "till-b"); err != nil || !claimed {
		t.Fatalf("till-b's claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-a"); err != nil || claimed || !known {
		t.Fatalf("a later resolution by another till must override the caller's own stamp, got claimed=%v known=%v err=%v", claimed, known, err)
	}
}

// The plain primary-side delete (POST /api/sync/held-sales/delete) leaves
// the same tombstone, so a stale mirror of an order resolved through it
// expires the same way as one resolved through a claim.
func TestHeldSalesRepo_DeleteAndTombstone_LeavesTombstone(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()
	if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAndTombstone(ctx, "h1", "till-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.Get(ctx, "h1"); ok {
		t.Fatal("DeleteAndTombstone must delete the row")
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-b"); err != nil || claimed || !known {
		t.Fatalf("a deleted id must answer another till known=true, got claimed=%v known=%v err=%v", claimed, known, err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", "till-a"); err != nil || claimed || known {
		t.Fatalf("the deleting till's own claim must not be refused by its own tombstone, got claimed=%v known=%v err=%v", claimed, known, err)
	}
	// Idempotent, like the plain delete it replaces on the primary.
	if err := repo.DeleteAndTombstone(ctx, "h1", "till-a"); err != nil {
		t.Fatalf("a repeat delete must be a no-op, got %v", err)
	}
}

// A tombstone older than 24h is pruned on the next write and stops
// answering known -- the short TTL is what keeps this additive to ADR-0093
// Decision 4's offline-first guarantee: a till that stayed offline for days
// finds nothing and trusts its own local row, exactly as before.
func TestHeldSalesRepo_ClaimAndTombstone_PrunesTombstonesOlderThan24h(t *testing.T) {
	repo, dbh := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()
	if err := repo.DeleteAndTombstone(ctx, "old", "till-a"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAndTombstone(ctx, "fresh", "till-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbh.DB.Exec(`UPDATE held_sales_tombstones SET deleted_at = datetime('now', '-25 hours') WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	if _, claimed, known, err := repo.ClaimAndTombstone(ctx, "old", "till-b"); err != nil || claimed || known {
		t.Fatalf("a tombstone older than 24h must no longer answer known, got claimed=%v known=%v err=%v", claimed, known, err)
	}
	var n int
	if err := dbh.DB.QueryRow(`SELECT COUNT(*) FROM held_sales_tombstones WHERE id = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the expired tombstone must be pruned, got %d rows err=%v", n, err)
	}
	if _, _, known, err := repo.ClaimAndTombstone(ctx, "fresh", "till-b"); err != nil || !known {
		t.Fatalf("a fresh tombstone must survive the prune, got known=%v err=%v", known, err)
	}
}

// The concurrent-resume half of Amendment B: several tills claiming the
// same order at the same moment serialize on the primary's single writer,
// and exactly ONE ever sees a row to take; every loser is told it was
// resolved (by a different till, hence known). Run repeatedly -- one run
// can miss the race window by chance (house pattern,
// TestAddBarcodeConcurrentRace).
func TestHeldSalesRepo_ClaimAndTombstone_ExactlyOneConcurrentWinner(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()
	const rounds, racers = 20, 4
	for i := 0; i < rounds; i++ {
		if err := repo.Upsert(ctx, tombstoneTestHeldSale); err != nil {
			t.Fatal(err)
		}
		var claimedCount, knownRefusals int
		var mu sync.Mutex
		var errs []error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < racers; j++ {
			wg.Add(1)
			go func(till string) {
				defer wg.Done()
				<-start
				_, claimed, known, err := repo.ClaimAndTombstone(ctx, "h1", till)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					errs = append(errs, err)
				case claimed:
					claimedCount++
				case known:
					knownRefusals++
				}
			}(fmt.Sprintf("till-%d", j))
		}
		close(start)
		wg.Wait()
		if len(errs) > 0 {
			t.Fatalf("round %d: a racing claim errored: %v", i, errs)
		}
		if claimedCount != 1 || knownRefusals != racers-1 {
			t.Fatalf("round %d: exactly one claim must win and every other be refused as resolved, got %d claimed / %d refused", i, claimedCount, knownRefusals)
		}
	}
}
