package cloudsync

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// Review findings 3 and 5 on the manage-shop catalog directives: the
// catalog snapshot must never be re-uploaded every tick into a cloud that
// refuses it, and a satellite must not log every pending main-till-only
// directive on every tick.

// snapshotGuardFixture is a catalog DB with one item, a fake cloud whose
// catalog-snapshot answer the test controls, and a fake clock. The guard's
// package state is reset before and after.
type snapshotGuardFixture struct {
	db     *sql.DB
	srv    *httptest.Server
	posts  atomic.Int32
	status atomic.Int32
	now    time.Time
}

func newSnapshotGuardFixture(t *testing.T) *snapshotGuardFixture {
	t.Helper()
	f := &snapshotGuardFixture{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	f.status.Store(http.StatusOK)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stores/catalog-snapshot" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.posts.Add(1)
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.srv.Close)
	d := testsupport.NewCatalogTestDB(t)
	if _, err := d.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "it-1", SKU: "SKU1", Name: "Coke", BasePrice: 120, IsActive: true})
	f.db = d
	resetSnapshotGuard()
	prevNow := snapshotNow
	snapshotNow = func() time.Time { return f.now }
	t.Cleanup(func() { snapshotNow = prevNow; resetSnapshotGuard() })
	logging.ResetRecent()
	return f
}

func (f *snapshotGuardFixture) push(t *testing.T) {
	t.Helper()
	if err := pushSnapshotIfChanged(context.Background(), testCfg(f.srv.URL), f.db); err != nil {
		t.Fatalf("a refused snapshot must not fail the tick again every time: %v", err)
	}
}

func problemsContaining(sub string) int {
	n := 0
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, sub) {
			n++
		}
	}
	return n
}

// Over the 16 MiB cap (lowered here) the snapshot is not posted at all, the
// size is logged ONCE at warn level (so it reaches the heartbeat's
// problems digest), and a catalog that fits again pushes normally.
func TestSnapshotOverSizeCapIsNotPosted(t *testing.T) {
	f := newSnapshotGuardFixture(t)
	prevCap := maxSnapshotBytes
	maxSnapshotBytes = 64
	t.Cleanup(func() { maxSnapshotBytes = prevCap })

	f.push(t)
	f.push(t)
	if n := f.posts.Load(); n != 0 {
		t.Fatalf("an oversize snapshot was posted %d times", n)
	}
	if n := problemsContaining("catalog snapshot is too large"); n != 1 {
		t.Fatalf("oversize problems logged = %d, want exactly 1 (logged once, with its size)", n)
	}
	maxSnapshotBytes = prevCap
	f.push(t)
	if n := f.posts.Load(); n != 1 {
		t.Fatalf("a snapshot that fits again was posted %d times, want 1", n)
	}
}

// A 413 (an older cloud's 4 MiB cap, or an oversize catalog) backs off
// exponentially instead of re-uploading the whole body every tick, logs the
// failure once, and a success resets the backoff.
func TestSnapshot413BacksOffExponentially(t *testing.T) {
	f := newSnapshotGuardFixture(t)
	f.status.Store(http.StatusRequestEntityTooLarge)

	f.push(t) // attempt 1 -> 413, wait base
	f.push(t) // inside the backoff: no post
	if n := f.posts.Load(); n != 1 {
		t.Fatalf("posts = %d after a 413 and an immediate retry, want 1", n)
	}
	f.now = f.now.Add(snapshotBackoffBase)
	f.push(t) // attempt 2 -> 413, wait 2*base
	f.now = f.now.Add(snapshotBackoffBase)
	f.push(t) // still inside the doubled wait
	if n := f.posts.Load(); n != 2 {
		t.Fatalf("posts = %d, want 2 (the second wait must be doubled)", n)
	}
	f.now = f.now.Add(snapshotBackoffBase)
	f.push(t) // attempt 3
	if n := f.posts.Load(); n != 3 {
		t.Fatalf("posts = %d, want 3", n)
	}
	if n := problemsContaining("413"); n != 1 {
		t.Fatalf("413 problems logged = %d, want exactly 1 for one distinct failure", n)
	}

	f.status.Store(http.StatusOK)
	f.now = f.now.Add(snapshotBackoffMax)
	f.push(t) // success resets
	if n := f.posts.Load(); n != 4 {
		t.Fatalf("posts = %d, want 4 (retry after the wait)", n)
	}
	f.status.Store(http.StatusRequestEntityTooLarge)
	if _, err := f.db.Exec(`UPDATE items SET base_price = 130 WHERE id = 'it-1'`); err != nil {
		t.Fatal(err)
	}
	f.push(t) // a new change, a new 413: backoff starts again at base
	f.now = f.now.Add(snapshotBackoffBase)
	f.push(t)
	if n := f.posts.Load(); n != 6 {
		t.Fatalf("posts = %d, want 6 (success must reset the backoff to base)", n)
	}
	if n := problemsContaining("413"); n != 2 {
		t.Fatalf("413 problems logged = %d, want 2 (a failure after a success is logged again)", n)
	}
}

// The wait doubles per consecutive failure and is capped at 6 hours.
func TestSnapshotBackoffBounded(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 40; n++ {
		d := snapshotBackoff(n)
		if d < prev || d > snapshotBackoffMax {
			t.Fatalf("snapshotBackoff(%d) = %v (prev %v, cap %v)", n, d, prev, snapshotBackoffMax)
		}
		prev = d
	}
	if snapshotBackoff(1) != snapshotBackoffBase || snapshotBackoff(40) != snapshotBackoffMax || snapshotBackoffMax != 6*time.Hour {
		t.Fatalf("base %v / cap %v", snapshotBackoff(1), snapshotBackoff(40))
	}
}

// Finding 5: a satellite logs each skipped main-till-only directive once,
// not on every tick, and the remembered set stays bounded.
func TestSatelliteSkipLoggedOncePerDirective(t *testing.T) {
	resetSatelliteSkipLog()
	t.Cleanup(resetSatelliteSkipLog)
	if !firstSatelliteSkip("d1") || firstSatelliteSkip("d1") {
		t.Fatal("d1 must be reported on its first skip only")
	}
	if !firstSatelliteSkip("d2") {
		t.Fatal("a different directive must be reported")
	}
	for i := 0; i < 3*maxSatelliteSkipLogged; i++ {
		firstSatelliteSkip(fmt.Sprintf("x%d", i))
	}
	if n := satelliteSkipLoggedLen(); n > maxSatelliteSkipLogged {
		t.Fatalf("remembered %d directive ids, cap %d", n, maxSatelliteSkipLogged)
	}
}
