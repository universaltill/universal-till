package data

// ut-docs#1361 review finding: EnabledBarcodeSymbologies' read path fetches
// from SQLite UNLOCKED (only the final cache-store takes the write lock),
// so a concurrent invalidating write can land between a reader's stale
// fetch and that reader's store. Without the generation check in
// cacheEnabledSymbologies, the stale fetch would silently overwrite the
// write's own invalidation, pinning the wrong value until the NEXT write —
// which for a setting that "changes essentially never" could be months.
//
// This is a white-box (package data, not data_test) test because it drives
// the unexported generation/cache maps directly to make the race
// deterministic rather than relying on goroutine timing to (maybe) hit the
// window — the same reason internal_test.go-style files already exist
// alongside the black-box tests in this package.

import (
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCacheEnabledSymbologies_StaleGenerationIsNotStored is the direct
// regression: a fetch that started before a concurrent write must not
// clobber that write's invalidation just because it finishes later.
func TestCacheEnabledSymbologies_StaleGenerationIsNotStored(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	r := &SettingsRepo{db: db}

	// Simulate a reader starting its (unlocked) fetch: it snapshots the
	// current generation (0, nothing has invalidated yet) before "going to
	// SQLite" — nothing to actually fetch here since this test drives the
	// cache layer directly, not the DB read.
	barcodeSymbologyCacheMu.RLock()
	staleGen := barcodeSymbologyGen[db]
	barcodeSymbologyCacheMu.RUnlock()

	// While that reader is "in flight", a concurrent write commits and
	// invalidates — bumping the generation past what the reader captured.
	invalidateBarcodeSymbologyCache(db)

	// The reader now finishes its stale fetch and tries to store it, still
	// carrying the OLD generation it captured before the write.
	got := r.cacheEnabledSymbologies(staleGen, []string{"STALE"})
	if len(got) != 1 || got[0] != "STALE" {
		t.Fatalf("cacheEnabledSymbologies return value = %v, want [STALE] (the stale fetch's own result is still returned to ITS caller)", got)
	}

	// But the cache itself must NOT have been overwritten with it — the
	// write's invalidation must stick.
	barcodeSymbologyCacheMu.RLock()
	cached, ok := barcodeSymbologyCache[db]
	barcodeSymbologyCacheMu.RUnlock()
	if ok {
		t.Fatalf("cache[db] = %v, ok=%v — want no entry (a stale-generation store must be dropped, not silently undo the write's invalidation)", cached, ok)
	}

	// A fresh fetch (current generation) must still cache normally —
	// confirms the guard only blocks STALE generations, not caching itself.
	barcodeSymbologyCacheMu.RLock()
	currentGen := barcodeSymbologyGen[db]
	barcodeSymbologyCacheMu.RUnlock()
	got = r.cacheEnabledSymbologies(currentGen, []string{"FRESH"})
	if len(got) != 1 || got[0] != "FRESH" {
		t.Fatalf("cacheEnabledSymbologies return value = %v, want [FRESH]", got)
	}
	barcodeSymbologyCacheMu.RLock()
	cached, ok = barcodeSymbologyCache[db]
	barcodeSymbologyCacheMu.RUnlock()
	if !ok || len(cached) != 1 || cached[0] != "FRESH" {
		t.Fatalf("cache[db] = %v, ok=%v, want [FRESH] cached (a current-generation store must succeed)", cached, ok)
	}
}
