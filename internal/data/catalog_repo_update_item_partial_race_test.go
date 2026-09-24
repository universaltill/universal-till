package data_test

import (
	"context"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestUpdateItemPartialConcurrentRace is the regression test for ut-docs#2324
// review finding S1: a naive GetItem-then-UpdateItem read-modify-write (two
// separate calls, no shared transaction) lets a concurrent writer's change
// land between the read and the write, so the SECOND writer's own write
// carries a stale copy of whatever the FIRST writer just changed — silently
// reverting it. Two concurrent callers here each own a DISJOINT field
// (price vs. sku), so under the pre-fix shape either can clobber the
// other's committed change; UpdateItemPartial's single BEGIN IMMEDIATE
// transaction must serialize the two read-modify-writes so both survive
// every round.
//
// Real file-backed database (not in-memory, which gives each pooled
// connection its own isolated database and can't exercise multi-connection
// locking) — same reasoning as TestUpdateItemReturningWasActiveConcurrentRace
// in catalog_repo_update_item_race_test.go, which this test otherwise
// mirrors.
func TestUpdateItemPartialConcurrentRace(t *testing.T) {
	dbh, err := db.Open(testsupport.MigratedDBFile(t, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()

	repo := data.NewCatalogRepo(dbh.DB)
	ctx := context.Background()

	itemID, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		ID: "race-partial-item", SKU: "ORIG-SKU", Name: "Race Partial Item", BasePrice: 100, IsActive: true,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	const rounds = 15
	for round := 0; round < rounds; round++ {
		// Reset both fields at the start of each round so a prior round's
		// result can't mask this round missing the race.
		if err := repo.SetItemPrice(ctx, itemID, 100); err != nil {
			t.Fatalf("round %d: reset price: %v", round, err)
		}
		sku := "ORIG-SKU"
		if _, ok, err := repo.GetItem(ctx, itemID); err != nil || !ok {
			t.Fatalf("round %d: pre-read item: ok=%v err=%v", round, ok, err)
		}
		if ok, err := repo.UpdateItemPartial(ctx, itemID, &sku, nil, nil, nil, nil, nil); err != nil || !ok {
			t.Fatalf("round %d: reset sku: ok=%v err=%v", round, ok, err)
		}

		const newPrice = int64(9999)
		newSKU := "RACE-SKU"
		var priceErr, skuErr error
		var skuOK bool
		start := make(chan struct{})
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			priceErr = repo.SetItemPrice(ctx, itemID, newPrice)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			skuOK, skuErr = repo.UpdateItemPartial(ctx, itemID, &newSKU, nil, nil, nil, nil, nil)
		}()
		close(start)
		wg.Wait()

		if priceErr != nil {
			t.Fatalf("round %d: SetItemPrice: %v", round, priceErr)
		}
		if skuErr != nil || !skuOK {
			t.Fatalf("round %d: UpdateItemPartial: ok=%v err=%v", round, skuOK, skuErr)
		}

		after, ok, err := repo.GetItem(ctx, itemID)
		if err != nil || !ok {
			t.Fatalf("round %d: re-read item: ok=%v err=%v", round, ok, err)
		}
		if after.BasePrice != newPrice {
			t.Fatalf("round %d: price = %d, want %d — UpdateItemPartial's stale read reverted a concurrent price write", round, after.BasePrice, newPrice)
		}
		if after.SKU != newSKU {
			t.Fatalf("round %d: sku = %q, want %q — the concurrent price write's own commit was lost", round, after.SKU, newSKU)
		}
	}
}
