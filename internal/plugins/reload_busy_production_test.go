package plugins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// TestReload_SurvivesRealisticPublisherContention is the ut-docs#775
// follow-up to ut-docs#770. #770 pinned publish_reload_race_test.go's DB to
// ONE connection specifically so *that* test's own regression goal
// (ut-docs#504, a panic race) stays deterministic — a deliberate choice
// that also means it no longer exercises real inter-connection SQLite lock
// contention. That's exactly the open question #775 asks about production:
// can Manager.Reload's write path return SQLITE_BUSY under a busy plugin
// publisher? This test restores that dimension instead of reusing #770's
// pinned config: managerTestDB opens the same production-shaped DSN
// (busy_timeout(5000) + WAL + _txlock=immediate — internal/db.Open) against
// a real on-disk file, and does NOT call SetMaxOpenConns — so Reload's
// writes and the publisher's own audit-row writes really do land on
// different pooled connections and genuinely contend, the way two real
// goroutines do in production.
//
// The publisher runs UNTHROTTLED, and that is load-bearing, not laziness.
// An earlier draft of this test paced it at 50ms between Publish calls as a
// "realistic shop cadence"; measurement during review showed the whole
// reloadCount loop finishes in ~12ms, so the 50ms pacing let exactly ONE
// publish overlap the entire run — the test was green because nothing ever
// contended, not because contention was survived. Unthrottled, the same
// loop sees hundreds of concurrent publishes under -race (tens of thousands
// without it) at no extra wall-clock cost, and Reload demonstrably parks in
// the busy handler waiting for the write lock rather than erroring. A real
// shop's event rate is far lower than either figure, so this is a
// deliberately pessimistic upper bound on production contention.
//
// ut-docs#979 (2026-08-24/25): this test failed twice in CI within ~15
// minutes, on two commits that touched no Go code, then passed on a
// single re-run each time. Investigated: this repo's ci/e2e workflows run
// on GitHub-hosted `ubuntu-latest`, not a self-hosted runner, and a clean
// local run (-race, count=5) did not reproduce. Given the deliberately
// unthrottled write load above, occasionally exceeding busy_timeout(5000)
// on a slower/shared Actions VM is consistent with runner variance, not a
// Reload regression — but a *repeat* failure (same test, multiple
// consecutive runs, or reproducible outside CI) would be real signal and
// should be investigated as one, not re-run away. See the Fatalf message
// below for what that distinction means for the next reader.
//
// A reliably green result is evidence that Reload's write path
// (SyncPluginPaymentMethods — three sequential autocommit ExecContext
// calls, no explicit Tx) doesn't hit the SHARED->RESERVED lock-promotion
// gap _txlock=immediate exists to fix (ut-docs#311): each call acquires
// the write lock fresh rather than promoting from an already-held SHARED
// lock within one transaction, so busy_timeout(5000)'s ordinary
// busy-handler retry applies in full and just waits.
//
// Scope note: this test exercises the Reload PATH end-to-end under real
// concurrent write load. It is NOT a regression test for the DSN flag
// itself — verified during review by deleting _txlock=immediate from
// internal/db.Open, which this test does not notice. internal/db's
// TestConcurrentWriterWaitsInsteadOfInstantBusy is that guard, and it fails
// instantly (~1ms, SQLITE_BUSY) when the flag goes away. The two are
// complementary; don't retire either on the strength of the other.
func TestReload_SurvivesRealisticPublisherContention(t *testing.T) {
	db := managerTestDB(t) // production DSN, deliberately UNPINNED pool
	ctx := context.Background()
	const pid = "com.test.reloadbusy"
	seedInstalledPlugin(t, db, pid, "ReloadBusy", "1.0.0", "none", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('rb1', ?, 'events:receive', 1)`, pid); err != nil {
		t.Fatalf("perm: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('rbh1', ?, 'busy.event', 'noop', 1)`, pid); err != nil {
		t.Fatalf("hook: %v", err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	bus := SharedBus(db)
	if _, err := bus.Subscribe(ctx, pid, []string{"busy.event"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// publishOK/publishErr are the contention witnesses. The publisher's own
	// audit-row INSERTs are the competing writer here, so a publish that
	// silently failed would quietly remove the very contention this test
	// exists to create — hence both are counted and asserted on below
	// rather than discarded.
	var publishOK, publishErr int64
	stop := make(chan struct{})
	var publisherWg sync.WaitGroup
	publisherWg.Add(1)
	go func() {
		defer publisherWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := bus.Publish(ctx, "busy.event", map[string]any{"x": 1}); err != nil {
				atomic.AddInt64(&publishErr, 1)
			} else {
				atomic.AddInt64(&publishOK, 1)
			}
		}
	}()
	// Drain via t.Cleanup registered right after the publisher starts (same
	// pattern as publish_reload_race_test.go, ut-docs#509/#750) so it still
	// runs even if a t.Fatalf below unwinds the test via runtime.Goexit.
	t.Cleanup(func() {
		close(stop)
		publisherWg.Wait()
	})

	const reloadCount = 20
	for i := 0; i < reloadCount; i++ {
		if err := m.Reload(ctx); err != nil {
			t.Fatalf("reload %d under publisher contention: %v (this is the ut-docs#775 production-risk signal IF it repeats — a lone failure on an unrelated commit is consistent with CI runner variance under this test's deliberately unthrottled load, per ut-docs#979; check for a repeat across re-runs/commits before treating one failure as a regression)", i, err)
		}
		// Re-subscribe after each Reload, mirroring what a real wasm
		// plugin's Sync-driven resubscribe does moments after
		// ResetSubscribers() within the same Sync call.
		if _, err := bus.Subscribe(ctx, pid, []string{"busy.event"}); err != nil {
			t.Fatalf("resubscribe %d: %v", i, err)
		}
	}

	// The publisher's writes are what make this a contention test at all. If
	// this floor ever trips, the test has decayed back into the no-op the
	// 50ms-cadence draft was — fix the contention, don't lower the floor.
	// One publish per reload is ~7x below the slowest rate observed under
	// -race (153 publishes across 20 reloads), so it has ample headroom on a
	// loaded CI box while still catching total collapse.
	if got := atomic.LoadInt64(&publishOK); got < reloadCount {
		t.Fatalf("publisher only completed %d publishes across %d reloads — too little concurrent write load for this to be a meaningful contention test (see the cadence note above)", got, reloadCount)
	}
	// The publisher side writes audit rows on the same contended DB, so it
	// answers the same #775 question from the other direction: it must not
	// be quietly eating SQLITE_BUSY either.
	if got := atomic.LoadInt64(&publishErr); got != 0 {
		t.Fatalf("publisher hit %d errors while contending with Reload (want 0) — a publish-side SQLITE_BUSY is the same production risk #775 asks about, seen from the writer that lost the race", got)
	}
}
