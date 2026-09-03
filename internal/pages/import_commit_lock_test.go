package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
func TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// No SKU, no barcode column at all — the exact case the ticket's first
	// bullet describes as having no dedupe of any kind.
	csv := "Name,Price,Category,In stock\n" +
		"Unkeyed Widget,4.50,Snacks,0\n"

	var codes [2]int
	var bodies [2]string
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
			req := httptest.NewRequest(http.MethodPost, "/api/import", body)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			<-start
			mux.ServeHTTP(rec, req)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}
	close(start)
	wg.Wait()

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
