package plugins

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
// The publisher runs at full speed up to a CREDIT CAP (publishCapPerReload
// completed publishes per finished reload), and both halves of that are
// load-bearing. Full speed, because an earlier draft paced it at 50ms
// between Publish calls as a "realistic shop cadence"; measurement during
// review (#775) showed the whole reloadCount loop finishes in ~12ms, so the
// 50ms pacing let exactly ONE publish overlap the entire run — the test was
// green because nothing ever contended. Uncapped full speed produced
// hundreds of concurrent publishes under -race (tens of thousands without
// it), with Reload demonstrably parking in the busy handler rather than
// erroring. The cap, because an unthrottled publisher has no upper bound at
// all on how far it can outpace Reload when the two are scheduled unfairly,
// and ut-docs#1151 wanted that tail bounded before the next slow-CI repeat.
// Be explicit about what is and is not measured here: the "publisher
// out-polls the parked busy handler until Reload exhausts busy_timeout(5000)"
// starvation regime is the HYPOTHESISED mechanism behind the four CI
// failures, NOT something #1151 reproduced — see the #979/#1151 paragraph
// below, where the attempt to force it did not succeed. The cap is therefore
// a defensive bound on a plausible tail, not a fix for a diagnosed one. The
// cap of 100 per reload is ~10x the contention an idle -race run actually
// generates (~9/reload, measured) and ~100x the floor asserted below, so it
// leaves the deliberately pessimistic contention level of the uncapped
// version intact in the normal case while refusing to let a badly scheduled
// runner spin the publisher arbitrarily far ahead of Reload.
//
// ut-docs#979 (2026-08-24/25) + ut-docs#1151 (2026-08-26): four CI
// failures across three days, all on commits touching nothing near this
// path, all "database is locked". #979 called it runner variance without a
// measurement; #1151 tried to get one. Attempts to force a genuine
// SQLITE_BUSY reproduction under artificial CPU starvation (the test process
// plus several busy-loop CPU hogs sharing one pinned core, as a stand-in for
// a slow shared `ubuntu-latest` vCPU) did NOT reliably reproduce SQLITE_BUSY
// itself in this exercise — repeated attempts instead tripped the
// publisher-floor check below (on BOTH the pre-#1151 and post-#1151 code, so
// it is a pre-existing, separate flake mode under severe scheduling
// starvation, not something this change introduces; filed as a follow-up
// rather than fixed here, since it's a different failure signature than
// what #979/#1151 actually saw in CI). So the "5s-ish busy_timeout
// exhaustion" mechanism below is NOT backed by a direct measured
// reproduction of the real failure — it rests on #775's own independently
// reviewed analysis (this path never hits the SHARED->RESERVED
// lock-promotion gap, and genuine non-error parking up to ~2.0s was
// observed under massive synthetic load) plus the fact that every real CI
// failure recorded so far was SQLITE_BUSY, never an unrelated error, which
// is consistent with slow-runner busy_timeout exhaustion and not with an
// instant lock-class-bypass defect (ut-docs#311's own regression test,
// internal/db's TestConcurrentWriterWaitsInsteadOfInstantBusy, fails in
// ~1ms when that defect is reintroduced — nothing close to what any of the
// 4 real failures would need to explain if they were case 2). The reload
// loop below classifies BUSY by elapsed time instead of hard-failing on the
// first one so that IF a future repeat is slow (case 1, busy_timeout
// genuinely exhausted), the test tolerates it and keeps testing, while a
// FAST/instant BUSY (case 2, the real #311-shaped defect signature) still
// hard-fails immediately — see the comment on busyExhaustionFloor.
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
	// firstPublishErr keeps the first failing publish's error AND its elapsed
	// time — the same elapsed-to-BUSY discriminator ut-docs#1151 needed on
	// the Reload side (a bare count told #979's investigation nothing).
	var firstPublishErr atomic.Value
	// reloadsDone feeds the publisher's credit cap (header comment): the
	// publisher may complete at most publishCapPerReload publishes per
	// finished reload. The budget is CUMULATIVE — unspent credits carry
	// forward — so it bounds the publisher's total lead over Reload across
	// the run, not its instantaneous rate against any one stalled reload
	// (ut-docs#1151).
	var reloadsDone int64
	const publishCapPerReload = 100
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
			if atomic.LoadInt64(&publishOK) >= (atomic.LoadInt64(&reloadsDone)+1)*publishCapPerReload {
				// At the credit cap: yield briefly instead of spinning on
				// the write lock, then re-check (the next finished reload
				// extends the budget).
				time.Sleep(time.Millisecond)
				continue
			}
			pubStart := time.Now()
			if _, err := bus.Publish(ctx, "busy.event", map[string]any{"x": 1}); err != nil {
				// Record the detail BEFORE bumping the counter: the assertions
				// below run while this goroutine is still live (the drain is in
				// t.Cleanup), so publishErr!=0 must never be observable before
				// the error it refers to is readable, or the failure message
				// prints "<nil>" exactly when it is needed.
				firstPublishErr.CompareAndSwap(nil, fmt.Sprintf("first publish error after %s: %v", time.Since(pubStart), err))
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

	// ut-docs#1151: the 4 repeat CI failures (#979 x2, #1151 x2) were never
	// directly reproduced with a measured elapsed time (see the header
	// comment) — the classification below is a defensive design choice, not
	// evidence-backed forensics. Elapsed time is nonetheless the correct
	// discriminator between benign busy_timeout exhaustion and the real
	// #311-class defect (a lock class the busy handler doesn't cover fails
	// in roughly a millisecond, not seconds — see
	// internal/db.TestConcurrentWriterWaitsInsteadOfInstantBusy), so the loop
	// below classifies instead of hard-failing on the first BUSY:
	//
	//   - SQLITE_BUSY in under busyExhaustionFloor: the handler did NOT run
	//     its budget on this lock class → hard fail. This is the production
	//     defect signal; do not re-run it away.
	//   - SQLITE_BUSY at/after the floor: the handler ran in full and lost a
	//     fair-but-starved race against this test's deliberately pathological
	//     credit-capped publisher — still a load no real shop generates (the
	//     header comment's cadence analysis). Retry the same Reload, bounded,
	//     the way the operator's next tap on the plugins screen would.
	//
	// Any non-BUSY error still fails immediately. The retry budget is small
	// on purpose: persistent starvation (budget exhausted) still fails, so
	// the test cannot silently absorb a genuine throughput collapse.
	const reloadCount = 20
	// The floor is anchored to busy_timeout(5000) itself, NOT to any observed
	// latency: SQLite's busy handler only gives up after its full 5s budget,
	// so a genuine exhaustion cannot return in much under 5s, whereas a lock
	// class the handler does not cover (the #311 defect) returns in ~1ms.
	// 4.5s leaves 500ms of slop for timer granularity and scheduling.
	//
	// Do NOT lower this to the ~2.0s figure #775 recorded. That number was
	// #775's longest observed SUCCESSFUL park (it never errored; #775 itself
	// notes the budget is 5s, "margin ~2.5x"), so it says nothing about when
	// exhaustion begins — and ordinary reloads on a loaded box already reach
	// it (a passing run during the #1151 review measured a single successful
	// reload at 2.07s). A 2s floor would therefore classify the whole 2–5s
	// band as "handler ran its budget" and retry it away, which is precisely
	// the fast-BUSY-after-slow-preceding-work case this split exists to catch.
	const busyExhaustionFloor = 4500 * time.Millisecond
	const maxBusyRetries = 3
	busyRetries := 0
	var slowestReload time.Duration
	for i := 0; i < reloadCount; i++ {
		start := time.Now()
		err := m.Reload(ctx)
		elapsed := time.Since(start)
		if elapsed > slowestReload {
			slowestReload = elapsed
		}
		if err != nil {
			// Deliberately matched on "SQLITE_BUSY", which modernc.org/sqlite
			// appends ONLY for the primary result code 5 (see its conn.errstr).
			// Do NOT broaden this to "database is locked": extended codes such
			// as SQLITE_BUSY_SNAPSHOT (517) — the exact signature of the #311
			// lock-promotion defect (internal/db.Open's own comment) — do not
			// carry the suffix and so land in the hard-fail branch below, which
			// is what we want. Broadening the match would quietly reclassify
			// the defect this test guards against as a retryable flake.
			if !strings.Contains(err.Error(), "SQLITE_BUSY") {
				t.Fatalf("reload %d under publisher contention failed (non-BUSY, after %s): %v", i, elapsed, err)
			}
			if elapsed < busyExhaustionFloor {
				t.Fatalf("reload %d returned SQLITE_BUSY after only %s — busy_timeout(5000)'s handler never ran its budget on this lock class. This is the real #311-shaped production defect (ut-docs#1151 case 2), NOT runner variance; do not re-run this away: %v", i, elapsed, err)
			}
			busyRetries++
			if busyRetries > maxBusyRetries {
				t.Fatalf("reload %d: SQLITE_BUSY after %s (busy handler exhausted) and the run already spent all %d retries — starvation this persistent means the runner is pathologically slow OR the load model regressed; investigate, don't re-run (ut-docs#1151): %v", i, elapsed, maxBusyRetries, err)
			}
			t.Logf("reload %d: SQLITE_BUSY after %s — busy handler ran its full budget and was starved by the publisher (benign under this test's deliberately pathological load; ut-docs#1151 case 1); retrying (%d/%d)", i, elapsed, busyRetries, maxBusyRetries)
			i--
			continue
		}
		// Re-subscribe after each Reload, mirroring what a real wasm
		// plugin's Sync-driven resubscribe does moments after
		// ResetSubscribers() within the same Sync call.
		if _, err := bus.Subscribe(ctx, pid, []string{"busy.event"}); err != nil {
			t.Fatalf("resubscribe %d: %v", i, err)
		}
		atomic.AddInt64(&reloadsDone, 1)
	}
	t.Logf("%d reloads done (slowest %s, %d busy-exhaustion retries); publishes ok=%d err=%d", reloadCount, slowestReload, busyRetries, atomic.LoadInt64(&publishOK), atomic.LoadInt64(&publishErr))

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
		t.Fatalf("publisher hit %d errors while contending with Reload (want 0) — a publish-side SQLITE_BUSY is the same production risk #775 asks about, seen from the writer that lost the race (%v)", got, firstPublishErr.Load())
	}
}
