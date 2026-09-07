package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond (ut-docs#1510)
// reproduces the report's exact complaint: "Import" pressed directly,
// without previewing first, so there is no staged_id for
// takeStagedCatalogUpload's existing exclusivity to protect — a double-tap
// sends two independent requests carrying byte-identical bytes. The row
// itself has neither a barcode nor a SKU, so nothing at the DB layer (no
// UNIQUE constraint reaches a NULL sku) can catch a resulting duplicate —
// the reservation in import_stage.go's reserveImportCommit is the only
// thing that can.
//
// ut-docs#1725: this test used to start two goroutines via a closed channel
// and trust the OS scheduler to run them concurrently enough to collide
// inside reserveImportCommit. That trust was misplaced — nothing stopped
// one goroutine's entire handler (parse, hash, reserve, insert, release)
// from finishing before the other's ServeHTTP was even scheduled, in which
// case the second request is an ordinary, unblocked sequential re-import and
// both legitimately answer 200 (this row has no barcode/SKU, so nothing
// dedupes a genuinely sequential repeat either — see
// TestImport_CommitLockReleasedAfterRequestFinishes). That under-overlap
// reproduced 17/20 runs under GOMAXPROCS=1 and 30/30 under -race, even
// though reserveImportCommit's own mutex+map exclusivity, exercised
// directly with 1000 real concurrent calls, never once let both callers in
// — so the bug was in the test's concurrency, not the production lock. A
// first fix attempt (a barrier only synchronizing the two goroutines'
// *arrival* at reserveImportCommit) still reproduced under GOMAXPROCS=1:
// once released from the barrier there is still no guarantee the runtime
// interleaves them rather than running the "winner" to completion first.
// Fixed here by removing the scheduler dependency entirely: the first
// request is paused (via importCommitReserveSync) WHILE it holds the
// reservation, the second is sent only once that's guaranteed, and only
// then is the first allowed to finish — deterministic, no timing bet at all.
func TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// No SKU, no barcode column at all — the exact case the ticket's first
	// bullet describes as having no dedupe of any kind.
	csv := "Name,Price,Category,In stock\n" +
		"Unkeyed Widget,4.50,Snacks,0\n"

	// arrivals guards `close(reserved)`: only the FIRST caller to hold a
	// reservation pauses there — if the lock were ever broken (e.g. a
	// regression that let a second caller reserve concurrently), a second
	// call must return immediately rather than double-close the channel
	// (which would panic the whole test binary and hide the real failure)
	// or deadlock on <-proceed (which would hang instead of failing).
	reserved := make(chan struct{})
	proceed := make(chan struct{})
	var arrivals int32
	importCommitReserveSync = func() {
		if atomic.AddInt32(&arrivals, 1) == 1 {
			close(reserved)
			<-proceed
		}
	}
	t.Cleanup(func() { importCommitReserveSync = nil })

	var codes [2]int
	var bodies [2]string

	// Request A: sent first, and guaranteed (via importCommitReserveSync)
	// to be holding the reservation by the time request B is sent below.
	bodyA, ctA := multipartCSV(t, csv, map[string]string{"commit": "1"})
	reqA := httptest.NewRequest(http.MethodPost, "/api/import", bodyA)
	reqA.Header.Set("Content-Type", ctA)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqA)
		codes[0] = rec.Code
		bodies[0] = rec.Body.String()
	}()
	select {
	case <-reserved:
	case <-time.After(10 * time.Second):
		t.Fatal("request A never reached the reservation hook — did the handler change how/when it reserves?")
	}

	// Request B: sent only now, so it can only ever observe A's reservation
	// as already held — a deterministic collision, not a hoped-for one.
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	codes[1] = rec.Code
	bodies[1] = rec.Body.String()

	// Let A's now-unblocked handler (and B's, if the lock was broken and it
	// also arrived) finish before asserting anything, so a failure here
	// never leaves a goroutine blocked forever on <-proceed.
	close(proceed)
	wg.Wait()

	if got := atomic.LoadInt32(&arrivals); got != 1 {
		t.Fatalf("expected exactly 1 caller to hold the reservation while B ran, got %d — the exclusivity lock let a second caller in", got)
	}

	var okCount, conflictCount int
	for i := 0; i < 2; i++ {
		switch codes[i] {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
			if !strings.Contains(bodies[i], "already running") {
				t.Fatalf("rejected duplicate commit body = %q, want the already-in-progress message", bodies[i])
			}
		default:
			t.Fatalf("response %d: unexpected code %d, body %s", i, codes[i], bodies[i])
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("expected exactly one 200 and one 409 across the two concurrent direct commits, got codes %v", codes)
	}

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE name = ?`, "Unkeyed Widget").Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 item after two concurrent identical direct commits, got %d", n)
	}
}

// TestImport_CommitLockReleasedAfterRequestFinishes confirms the
// reservation is per-request, not permanent: a second commit of the SAME
// file, once the first has actually finished, must succeed normally (the
// existing barcode/SKU-exists checks are what make a later, non-concurrent
// re-import of the same file safe — this guard must never block that).
func TestImport_CommitLockReleasedAfterRequestFinishes(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Category,In stock\n" +
		"Sequential Widget,SEQ1,4.50,Snacks,0\n"

	for i := 0; i < 2; i++ {
		body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
		req := httptest.NewRequest(http.MethodPost, "/api/import", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("commit %d: code %d body %s", i, rec.Code, rec.Body.String())
		}
	}

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = ?`, "SEQ1").Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 item after a genuine sequential re-import (existing SKU-skip behaviour), got %d", n)
	}
}

// TestImport_ConcurrentSKURaceAcrossDifferentFilesSkipsCleanly (ut-docs#1510)
// covers the race the content-hash lock above does NOT catch on its own:
// two DIFFERENT uploads (different bytes, so each gets its own reservation
// and both proceed) that happen to introduce the SAME new SKU — e.g. two
// operators importing overlapping supplier lists at once. The loser must
// land on the same clean "SKU already in catalog" skip a sequential
// re-import gets (CreateItemTx now returns the distinguishable
// data.ErrSKUExists — see catalog_repo_createitemtx_sku_conflict_test.go),
// never the old generic "item could not be created" failure, and the table
// must end up with exactly one row for the SKU.
func TestImport_ConcurrentSKURaceAcrossDifferentFilesSkipsCleanly(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	const rounds = 5
	for r := 0; r < rounds; r++ {
		dp := newImportTestDeps(t)
		mux := http.NewServeMux()
		registerImport(mux, dp)

		sku := fmt.Sprintf("RACE-SKU-%d", r)
		csvA := fmt.Sprintf("Name,SKU,Price,Category,In stock\nFirst Racer %d,%s,4.50,Snacks,0\n", r, sku)
		csvB := fmt.Sprintf("Name,SKU,Price,Category,In stock\nSecond Racer %d,%s,5.00,Drinks,0\n", r, sku)

		var codes [2]int
		var bodies [2]string
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, csv := range []string{csvA, csvB} {
			wg.Add(1)
			go func(i int, csv string) {
				defer wg.Done()
				body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
				req := httptest.NewRequest(http.MethodPost, "/api/import", body)
				req.Header.Set("Content-Type", ct)
				rec := httptest.NewRecorder()
				<-start
				mux.ServeHTTP(rec, req)
				codes[i] = rec.Code
				bodies[i] = rec.Body.String()
			}(i, csv)
		}
		close(start)
		wg.Wait()

		// Neither request is blocked by the content lock (different bytes),
		// so both must reach the DB and answer 200 — the race is resolved
		// INSIDE the commit, as a row outcome, not as a rejected request.
		for i := 0; i < 2; i++ {
			if codes[i] != http.StatusOK {
				t.Fatalf("round %d, response %d: code %d body %s", r, i, codes[i], bodies[i])
			}
			if strings.Contains(bodies[i], "item could not be created") {
				t.Fatalf("round %d, response %d: SKU race surfaced as a raw item_failed, not a clean skip: %s", r, i, bodies[i])
			}
		}

		var n int
		if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = ?`, sku).Scan(&n); err != nil {
			t.Fatalf("round %d: count items: %v", r, err)
		}
		if n != 1 {
			t.Fatalf("round %d: expected exactly 1 item for SKU %q, got %d", r, sku, n)
		}
	}
}
