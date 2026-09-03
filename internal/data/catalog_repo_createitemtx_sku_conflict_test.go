package data_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCreateItemTx_DuplicateSKUIsErrSKUExists (ut-docs#1510) is
// TestCreateItem_DuplicateSKUIsErrSKUExists' counterpart for the
// tx-threaded variant the catalog importer commits through. Before this
// fix, CreateItemTx wrapped a UNIQUE(sku) violation as a plain
// "insert item: %w" error — indistinguishable from any other DB failure —
// so a caller looping over many rows (the importer) could not tell "SKU
// already in use" from "the database is on fire," and treated a genuine
// race on this row's SKU as a raw row failure instead of the same clean
// "already in catalog" skip a sequential re-import already gets via
// SKUExists.
func TestCreateItemTx_DuplicateSKUIsErrSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "DUP", Name: "First", BasePrice: 100, IsActive: true})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	if _, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Second", SKU: "DUP", BasePrice: 200, IsActive: true,
	}); !errors.Is(err, data.ErrSKUExists) {
		t.Fatalf("expected ErrSKUExists, got %v", err)
	}
}

// TestCreateItemTxConcurrentSKURace (ut-docs#1510) exercises the exact
// scenario the import commit loop hits on a double-tap: two commits racing
// to create an item under the SAME SKU, each in its own transaction (the
// commit loop's own per-row BeginTx/Commit shape — see the import commit
// loop in internal/pages/import_page.go). Exactly one must land; the loser
// must get the distinguishable ErrSKUExists, not a raw DB error, and the
// items table must end up with exactly one row for the SKU either way —
// never two.
//
// Real, file-backed DB (not testsupport's shared in-memory one) for the
// same reason TestAddBarcodeConcurrentRace uses db.Open: a ":memory:" DSN
// gives each pooled connection its own isolated database and can't
// exercise real multi-connection locking.
func TestCreateItemTxConcurrentSKURace(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()

	repo := data.NewCatalogRepo(dbh.DB)
	ctx := context.Background()

	const rounds = 15
	for i := 0; i < rounds; i++ {
		sku := fmt.Sprintf("RACE-SKU-%03d", i)
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start
				tx, terr := dbh.DB.BeginTx(ctx, nil)
				if terr != nil {
					errs[j] = terr
					return
				}
				_, cerr := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
					Name: fmt.Sprintf("Racer %d", j), SKU: sku, BasePrice: 100, IsActive: true,
				})
				if cerr != nil {
					_ = tx.Rollback()
					errs[j] = cerr
					return
				}
				errs[j] = tx.Commit()
			}(j)
		}
		close(start)
		wg.Wait()

		switch {
		case errs[0] == nil && errs[1] == nil:
			t.Fatalf("round %d: both CreateItemTx calls succeeded for SKU %q — duplicate item", i, sku)
		case errs[0] != nil && errs[1] != nil:
			t.Fatalf("round %d: both CreateItemTx calls failed for SKU %q: %v / %v", i, sku, errs[0], errs[1])
		}
		loserErr := errs[1]
		if errs[0] != nil {
			loserErr = errs[0]
		}
		if !errors.Is(loserErr, data.ErrSKUExists) {
			t.Fatalf("round %d: loser error should be ErrSKUExists, got: %v", i, loserErr)
		}
		if strings.Contains(loserErr.Error(), "SQLITE") || strings.Contains(loserErr.Error(), "syntax") {
			t.Fatalf("round %d: loser error leaked raw driver text: %v", i, loserErr)
		}

		var n int
		if err := dbh.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE sku = ?`, sku).Scan(&n); err != nil {
			t.Fatalf("round %d: count items: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("round %d: expected exactly 1 item for SKU %q, got %d", i, sku, n)
		}
	}
}
