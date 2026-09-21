package pos

import (
	"regexp"
	"sync"
	"testing"
	"time"
)

// newTestSessionManager builds a manager whose sessions are plain Services
// with a fixed, injectable clock (ut-docs#2261, ADR-0103) so idle-sweep tests
// never sleep for real.
func newTestSessionManager(t *testing.T) (*SessionBasketManager, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	m := NewSessionBasketManager(func() *Service {
		return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
	})
	m.clock = func() time.Time { return now }
	return m, &now
}

func TestSessionBasketManager_CreateGetRemoveRoundTrip(t *testing.T) {
	m, _ := newTestSessionManager(t)

	token, svc := m.Create()
	if svc == nil {
		t.Fatal("Create returned a nil Service")
	}
	// Same shape as internal/data's newTrackingToken: 16 random bytes as
	// lowercase hex — 32 chars, no other alphabet.
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		t.Fatalf("token %q is not 32 lowercase hex chars", token)
	}
	got, ok := m.Get(token)
	if !ok || got != svc {
		t.Fatalf("Get(%q) = (%p, %v), want the created Service %p", token, got, ok, svc)
	}
	if _, ok := m.Get("not-a-session"); ok {
		t.Fatal("Get of an unknown token must miss")
	}
	if _, ok := m.Get(""); ok {
		t.Fatal("Get of an empty token must miss")
	}
	if n := m.Len(); n != 1 {
		t.Fatalf("Len() = %d, want 1", n)
	}

	m.Remove(token)
	if _, ok := m.Get(token); ok {
		t.Fatal("Get after Remove must miss")
	}
	if n := m.Len(); n != 0 {
		t.Fatalf("Len() after Remove = %d, want 0", n)
	}
	m.Remove(token) // idempotent: removing twice must not panic
}

func TestSessionBasketManager_TokensAreUnique(t *testing.T) {
	m, _ := newTestSessionManager(t)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, _ := m.Create()
		if seen[tok] {
			t.Fatalf("duplicate token %q after %d creates", tok, i)
		}
		seen[tok] = true
	}
}

// Two sessions never share basket state — the core isolation property this
// manager exists to provide (each Create is a fresh Service from the factory).
func TestSessionBasketManager_SessionsAreIndependent(t *testing.T) {
	m, _ := newTestSessionManager(t)
	tokA, a := m.Create()
	tokB, b := m.Create()
	if a == b {
		t.Fatal("two Creates returned the same Service")
	}
	a.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 320}, 1, nil)
	a.SetTable("t1", "T1")

	gotB, _ := m.Get(tokB)
	if n := len(gotB.Lines()); n != 0 {
		t.Fatalf("session B has %d lines after adding to A, want 0", n)
	}
	if gotB.TableID() != "" {
		t.Fatalf("session B TableID = %q after binding A, want empty", gotB.TableID())
	}
	gotA, _ := m.Get(tokA)
	if n := len(gotA.Lines()); n != 1 {
		t.Fatalf("session A has %d lines, want 1", n)
	}
}

func TestSessionBasketManager_TableOwnerFindsBoundSession(t *testing.T) {
	m, _ := newTestSessionManager(t)
	tokA, a := m.Create()
	_, b := m.Create()
	a.SetTable("t1", "T1")
	b.SetTable("t2", "T2")

	tok, svc, ok := m.TableOwner("t1")
	if !ok || tok != tokA || svc != a {
		t.Fatalf("TableOwner(t1) = (%q, %p, %v), want (%q, %p, true)", tok, svc, ok, tokA, a)
	}
	if _, _, ok := m.TableOwner("t9"); ok {
		t.Fatal("TableOwner of an unbound table must miss")
	}
	if _, _, ok := m.TableOwner(""); ok {
		t.Fatal("TableOwner(\"\") must never match an unbound session")
	}
	m.Remove(tokA)
	if _, _, ok := m.TableOwner("t1"); ok {
		t.Fatal("TableOwner must not find a removed session")
	}
}

// ut-docs#2261 review finding B1: TableOwnerActive is TableOwner narrowed
// to a session touched within maxIdle of now, so the busy guard stops
// treating an abandoned session as "holding" its table well before the
// much longer Sweep threshold would ever evict it.
func TestSessionBasketManager_TableOwnerActiveIgnoresStaleSessions(t *testing.T) {
	m, now := newTestSessionManager(t)
	tok, svc := m.Create()
	svc.SetTable("t1", "T1")

	if got, gotSvc, ok := m.TableOwnerActive("t1", 10*time.Minute, *now); !ok || got != tok || gotSvc != svc {
		t.Fatalf("freshly created session: TableOwnerActive = (%q, %p, %v), want (%q, %p, true)", got, gotSvc, ok, tok, svc)
	}

	// 11 minutes later, with no intervening Get to refresh lastSeen, the
	// same 10-minute window must no longer find it...
	later := now.Add(11 * time.Minute)
	if _, _, ok := m.TableOwnerActive("t1", 10*time.Minute, later); ok {
		t.Fatal("a session idle past maxIdle must not be found by TableOwnerActive")
	}
	// ...while the plain, unfiltered TableOwner still does — the session
	// itself was never evicted, only de-prioritized for the busy guard.
	if _, _, ok := m.TableOwner("t1"); !ok {
		t.Fatal("TableOwner (unfiltered) must still find the same session")
	}
	// And it is still reachable by its own token, same as any live session.
	if _, ok := m.Get(tok); !ok {
		t.Fatal("Get must still find the session by its own token")
	}

	// An empty tableID or a nil manager never match, same as TableOwner.
	if _, _, ok := m.TableOwnerActive("", 10*time.Minute, *now); ok {
		t.Fatal("TableOwnerActive(\"\", ...) must never match")
	}
	var nilM *SessionBasketManager
	if _, _, ok := nilM.TableOwnerActive("t1", 10*time.Minute, *now); ok {
		t.Fatal("nil manager TableOwnerActive must miss")
	}
}

// Sweep evicts only sessions idle longer than maxIdle; a Get refreshes the
// idle clock so an actively used session is never swept.
func TestSessionBasketManager_SweepEvictsOnlyIdleSessions(t *testing.T) {
	m, now := newTestSessionManager(t)
	t0 := *now
	tokIdle, _ := m.Create()
	tokActive, _ := m.Create()
	tokFresh := ""

	// 90 minutes later the "active" guest touches their session, and a
	// brand-new one is minted.
	*now = t0.Add(90 * time.Minute)
	if _, ok := m.Get(tokActive); !ok {
		t.Fatal("precondition: active session must exist")
	}
	tokFresh, _ = m.Create()

	// At t0+2h30 with a 2h threshold: idle (last seen t0) is out; active
	// (last seen t0+1h30) and fresh (created t0+1h30) stay.
	n := m.Sweep(2*time.Hour, t0.Add(150*time.Minute))
	if n != 1 {
		t.Fatalf("Sweep removed %d sessions, want 1", n)
	}
	if _, ok := m.Get(tokIdle); ok {
		t.Fatal("idle session must have been swept")
	}
	if _, ok := m.Get(tokActive); !ok {
		t.Fatal("recently used session must survive the sweep")
	}
	if _, ok := m.Get(tokFresh); !ok {
		t.Fatal("recently created session must survive the sweep")
	}
	if n := m.Sweep(2*time.Hour, t0.Add(150*time.Minute)); n != 0 {
		t.Fatalf("second identical Sweep removed %d, want 0", n)
	}
	// Far enough in the future everything goes (the Gets above refreshed
	// active/fresh to t0+2h30, so t0+5h is past the threshold for all).
	if n := m.Sweep(2*time.Hour, t0.Add(5*time.Hour)); n != 2 {
		t.Fatalf("final Sweep removed %d, want 2", n)
	}
	if m.Len() != 0 {
		t.Fatalf("Len() after full sweep = %d, want 0", m.Len())
	}
}

func TestSessionBasketManager_SetConfigReachesEveryLiveSession(t *testing.T) {
	m, _ := newTestSessionManager(t)
	_, a := m.Create()
	_, b := m.Create()
	a.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 1000}, 1, nil)

	cfg := Config{TaxRateBasisPoints: 1000, TaxInclusive: false}
	m.SetConfig(cfg)
	if a.Config() != cfg || b.Config() != cfg {
		t.Fatalf("SetConfig not applied: a=%+v b=%+v", a.Config(), b.Config())
	}
	// Totals were recomputed in place, the basket survived.
	if got := a.Basket().Tax.Minor(); got != 100 {
		t.Fatalf("a.Basket().Tax = %d after a 10%% SetConfig on a 1000 line, want 100", got)
	}
}

func TestSessionBasketManager_HasItems(t *testing.T) {
	m, _ := newTestSessionManager(t)
	if m.HasItems() {
		t.Fatal("empty manager must report no items")
	}
	_, a := m.Create()
	_, _ = m.Create()
	if m.HasItems() {
		t.Fatal("two empty sessions must report no items")
	}
	a.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 320}, 1, nil)
	if !m.HasItems() {
		t.Fatal("a session holding a line must report items")
	}
}

// A nil manager is a valid "no sessions" receiver — page handlers nil-check
// common.Deps.SelfOrderSessions, but the cheap read-only methods are made
// nil-safe too so a bare-Deps caller can't panic on them.
func TestSessionBasketManager_NilReceiverIsSafe(t *testing.T) {
	var m *SessionBasketManager
	if _, ok := m.Get("x"); ok {
		t.Fatal("nil manager Get must miss")
	}
	if _, _, ok := m.TableOwner("t1"); ok {
		t.Fatal("nil manager TableOwner must miss")
	}
	if m.HasItems() || m.Len() != 0 {
		t.Fatal("nil manager must report empty")
	}
	if n := m.Sweep(time.Hour, time.Now()); n != 0 {
		t.Fatalf("nil manager Sweep = %d, want 0", n)
	}
	m.Remove("x")
	m.SetConfig(Config{})
}

// The core acceptance criterion of ut-docs#2261, proven at this layer under
// `go test -race`: many goroutines creating, resolving, mutating, sweeping
// and removing sessions concurrently must neither race nor leak state
// between sessions — every session ends up holding exactly the lines its
// own goroutine added and nothing from any other.
func TestSessionBasketManager_ConcurrentSessionsDoNotInterfere(t *testing.T) {
	m := NewSessionBasketManager(func() *Service {
		return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
	})

	const guests = 32
	const addsPerGuest = 25
	var wg sync.WaitGroup
	tokens := make([]string, guests)
	var start sync.WaitGroup
	start.Add(1)
	for g := 0; g < guests; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			start.Wait()
			tok, svc := m.Create()
			tokens[g] = tok
			svc.SetTable("table-"+string(rune('A'+g%26)), "T")
			for i := 0; i < addsPerGuest; i++ {
				// Resolve through the manager every time, the way a request
				// handler does, so Get's last-seen touch races the mutations.
				got, ok := m.Get(tok)
				if !ok {
					t.Errorf("guest %d: own session vanished", g)
					return
				}
				// Distinct SKU and ItemID per add: mergeResolved folds a
				// same-SKU/same-item add into the existing line, and this
				// test wants addsPerGuest separate lines to count.
				id := "item-" + tok[:4] + "-" + string(rune('a'+i%26))
				got.AddLineWithModifiers(BasketLine{
					SKU:        id,
					ItemID:     id,
					Name:       "line",
					PriceCents: 100,
				}, 1, nil)
				_, _, _ = m.TableOwner("table-" + string(rune('A'+g%26)))
			}
		}(g)
	}
	// A concurrent sweeper and a concurrent HasItems/SetConfig reader, both
	// touching every session while guests mutate them.
	wg.Add(1)
	go func() {
		defer wg.Done()
		start.Wait()
		for i := 0; i < 50; i++ {
			m.Sweep(time.Hour, time.Now()) // nothing is idle: must evict nothing
			_ = m.HasItems()
			m.SetConfig(Config{TaxRateBasisPoints: 2000})
		}
	}()
	start.Done()
	wg.Wait()

	if n := m.Len(); n != guests {
		t.Fatalf("Len() = %d, want %d (the concurrent Sweep must never evict a live session)", n, guests)
	}
	for g, tok := range tokens {
		svc, ok := m.Get(tok)
		if !ok {
			t.Fatalf("guest %d's session missing after the run", g)
		}
		lines := svc.Lines()
		if len(lines) != addsPerGuest {
			t.Fatalf("guest %d: %d lines, want %d", g, len(lines), addsPerGuest)
		}
		for _, l := range lines {
			if want := "item-" + tok[:4] + "-"; len(l.ItemID) < len(want) || l.ItemID[:len(want)] != want {
				t.Fatalf("guest %d's basket holds another session's line %q", g, l.ItemID)
			}
		}
	}
	// Concurrent removal, then nothing remains.
	var rm sync.WaitGroup
	for _, tok := range tokens {
		rm.Add(1)
		go func(tok string) { defer rm.Done(); m.Remove(tok) }(tok)
	}
	rm.Wait()
	if m.Len() != 0 {
		t.Fatalf("Len() after concurrent Remove = %d, want 0", m.Len())
	}
}

// ut-docs#2434 (ADR-0103 review finding N3): the busy-check and the bind
// used to be two separate manager.mu critical sections
// (TableOwnerActive, then Create+SetTable), so two goroutines could both
// observe "not busy" before either bound — a real check-then-act race, not
// just a threshold gap. BindTable collapses both into one critical
// section; proven here with many goroutines racing to bind the identical
// table: exactly one must succeed, every other must see busy and mint
// nothing.
//
// Run under an OUTER loop, fresh manager per trial (ut-docs#2434
// independent-review finding S2): `go test -race` alone does NOT prove
// this — the old two-call implementation has no data race at all (both
// halves were individually mutex-guarded), only a logic race, so `-race`
// stays silent against a reverted fix. The only signal a single trial
// gives is `bound != 1`, and that's PROBABILISTIC: re-running the old
// racy implementation against this test's exact shape (32 goroutines, one
// start gate) let two goroutines both win in 5/20 trials under `-race`
// and 2/20 without — a revert would pass this test most of the time on a
// single run. Looping drives a false pass down to effectively zero at
// negligible cost (each trial is sub-millisecond).
func TestSessionBasketManager_BindTable_ConcurrentSameTableBindsExactlyOnce(t *testing.T) {
	const trials = 30
	const guests = 32
	for trial := 0; trial < trials; trial++ {
		m := NewSessionBasketManager(func() *Service {
			return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
		})

		var wg sync.WaitGroup
		var start sync.WaitGroup
		start.Add(1)
		var resMu sync.Mutex
		bound, busy := 0, 0
		var boundToken string
		var boundSvc *Service
		for g := 0; g < guests; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start.Wait()
				tok, svc, isBusy := m.BindTable("table-A", "T1", "", nil, time.Hour, time.Now())
				resMu.Lock()
				defer resMu.Unlock()
				if isBusy {
					busy++
					if tok != "" || svc != nil {
						t.Errorf("trial %d: busy result must return no token/service, got (%q, %p)", trial, tok, svc)
					}
					return
				}
				bound++
				boundToken, boundSvc = tok, svc
			}()
		}
		start.Done()
		wg.Wait()

		if bound != 1 {
			t.Fatalf("trial %d: bound = %d, want exactly 1 — a real concurrent race must never let two mints win", trial, bound)
		}
		if busy != guests-1 {
			t.Fatalf("trial %d: busy = %d, want %d", trial, busy, guests-1)
		}
		if n := m.Len(); n != 1 {
			t.Fatalf("trial %d: live sessions after the race = %d, want 1", trial, n)
		}
		svc, ok := m.Get(boundToken)
		if !ok || svc != boundSvc {
			t.Fatalf("trial %d: the one winning bind's token must resolve back to its own service", trial)
		}
		if got := svc.TableID(); got != "table-A" {
			t.Fatalf("trial %d: winning session's TableID() = %q, want %q", trial, got, "table-A")
		}
	}
}

// The mover case (an existing session's own table changes) is atomic the
// same way: a live session moving onto a table another live session
// already holds must see busy and stay on its original table, never lose
// its own binding or basket.
func TestSessionBasketManager_BindTable_MoverBlockedByBusyTableKeepsOwnBinding(t *testing.T) {
	m, now := newTestSessionManager(t)

	// Bind both fixtures through BindTable itself, not a direct
	// svc.SetTable call — the manager's own busy-check now scans its own
	// sessionBasket.tableID record (ut-docs#2443), which only BindTable
	// keeps in sync; a direct SetTable bypasses it entirely.
	incumbentToken, incumbent := m.Create()
	if _, _, busy := m.BindTable("table-B", "T2", incumbentToken, incumbent, time.Hour, *now); busy {
		t.Fatal("setup: incumbent's own bind to table-B must not itself report busy")
	}

	moverToken, mover := m.Create()
	if _, _, busy := m.BindTable("table-A", "T1", moverToken, mover, time.Hour, *now); busy {
		t.Fatal("setup: mover's own bind to table-A must not itself report busy")
	}
	mover.AddLineWithModifiers(BasketLine{SKU: "x", ItemID: "x", Name: "line", PriceCents: 100}, 1, nil)

	tok, svc, busy := m.BindTable("table-B", "T2", moverToken, mover, time.Hour, *now)
	if !busy {
		t.Fatal("moving onto a table another live session already holds must report busy")
	}
	if tok != "" || svc != nil {
		t.Fatalf("busy result must return no token/service, got (%q, %p)", tok, svc)
	}
	if got := mover.TableID(); got != "table-A" {
		t.Fatalf("mover's TableID() after a busy-blocked move = %q, want unchanged %q", got, "table-A")
	}
	if n := len(mover.Lines()); n != 1 {
		t.Fatalf("mover's basket has %d lines after a busy-blocked move, want unchanged 1", n)
	}
	if got := incumbent.TableID(); got != "table-B" {
		t.Fatalf("incumbent's TableID() = %q, want unchanged %q", got, "table-B")
	}
}

// ut-docs#2434 independent-review finding N4: the B1 idle-recovery
// guarantee is now load-bearing for an EMPTY session too (dropping the
// item-count exception means an empty session blocks exactly like a
// non-empty one while active), but nothing previously asserted that an
// EMPTY session actually frees its table via BindTable once idle past
// maxIdle — every existing idle-recovery test seeds a non-empty session.
func TestSessionBasketManager_BindTable_EmptySessionFreesTableAfterMaxIdle(t *testing.T) {
	m, now := newTestSessionManager(t)

	// Bind through BindTable itself (ut-docs#2443) — a direct svc.SetTable
	// call would leave the manager's own sessionBasket.tableID record at
	// "", invisible to BindTable's busy-check.
	firstToken, first := m.Create()
	if _, _, busy := m.BindTable("table-A", "T1", firstToken, first, 10*time.Minute, *now); busy {
		t.Fatal("setup: first's own bind to table-A must not itself report busy")
	} // bound, zero lines

	if _, _, busy := m.BindTable("table-A", "T1", "", nil, 10*time.Minute, *now); !busy {
		t.Fatal("right after binding: a second, unrelated scan of the same table must see busy, even though the first session is empty")
	}

	later := now.Add(10*time.Minute + time.Second)
	tok, svc, busy := m.BindTable("table-A", "T1", "", nil, 10*time.Minute, later)
	if busy {
		t.Fatal("an EMPTY session idle past maxIdle must not block a new scan of its table")
	}
	if svc == nil || svc == first {
		t.Fatal("the new scan should have bound a fresh session, not the aged-out empty one")
	}
	if got := svc.TableID(); got != "table-A" {
		t.Fatalf("new session's TableID() = %q, want %q", got, "table-A")
	}
	if got, ok := m.Get(tok); !ok || got != svc {
		t.Fatal("the new token must resolve back to the new session")
	}
}

// ut-docs#2434 independent-review finding S4: bindSelfOrderTableSession
// used to special-case "resume my own current table" BEFORE the busy
// check, unconditionally. That's a gap, not just a simplification: A
// binds table T (non-empty), goes idle past maxIdle with no further
// activity, a DIFFERENT phone (B) correctly sees A as stale and legitimately
// binds T, and only THEN does A's phone wake and resume its own stale
// cookie for T — the old early-return let A resume unconditionally,
// producing two live, recently-active sessions on one table (two
// kiosk_counter_orders rows at checkout), the exact class of bug this
// card exists to close, just reached via the resume path instead of two
// concurrent mints. Fixed by routing the resume case through the SAME
// BindTable call as mint/move — this proves BindTable itself correctly
// reports busy when a stale session's own token+service (the "mover" a
// resume caller now always passes) contests a table another, currently
// more-recently-active session already holds.
func TestSessionBasketManager_BindTable_StaleResumeAfterCompetingBindSeesBusy(t *testing.T) {
	m, now := newTestSessionManager(t)

	aToken, aSvc := m.Create() // lastSeen stamped at *now via m.clock()
	// Bind through BindTable itself (ut-docs#2443) — a direct SetTable call
	// would leave the manager's own sessionBasket.tableID record at "",
	// so A would never register as busy below regardless of staleness.
	if _, _, busy := m.BindTable("table-A", "T1", aToken, aSvc, 10*time.Minute, *now); busy {
		t.Fatal("setup: A's own bind to table-A must not itself report busy")
	}
	aSvc.AddLineWithModifiers(BasketLine{SKU: "x", ItemID: "x", Name: "line", PriceCents: 100}, 1, nil)

	// Advance the fake clock past maxIdle with no further touch of A's
	// session, then bind B — B's own lastSeen is stamped at this later
	// time via the same m.clock() mechanism A's was stamped at originally.
	*now = now.Add(10*time.Minute + time.Second)
	bToken, bSvc, busy := m.BindTable("table-A", "T1", "", nil, 10*time.Minute, *now)
	if busy || bSvc == nil {
		t.Fatalf("B's fresh scan after A went stale: busy=%v svc=%v, want busy=false, a bound session", busy, bSvc)
	}
	if bToken == aToken {
		t.Fatal("B must get its OWN token, not A's")
	}

	// A's phone wakes and "resumes" a moment later — the exact call
	// bindSelfOrderTableSession now makes unconditionally, passing A's own
	// live *Service as mover.
	*now = now.Add(time.Second)
	tok, svc, aBusy := m.BindTable("table-A", "T1", aToken, aSvc, 10*time.Minute, *now)
	if !aBusy {
		t.Fatal("A's stale resume after B's legitimate bind must see busy=true")
	}
	if tok != "" || svc != nil {
		t.Fatalf("busy result must return no token/service, got (%q, %p)", tok, svc)
	}
	// B's own binding is untouched by A's blocked resume attempt.
	if got, ok := m.Get(bToken); !ok || got != bSvc || got.TableID() != "table-A" {
		t.Fatal("B's session must be unaffected by A's blocked resume")
	}
}

// slowChargeAsker blocks in AskChargePolicy until release is closed, so a
// test can hold a Service's SetTable call open for as long as it needs to
// prove some other manager operation is (or isn't) blocked behind it. The
// FIRST call is let through immediately: SetChargePolicyAsker itself
// synchronously triggers one recomputeTotals (and so one AskChargePolicy
// call) as part of installing the asker, before the test's real BindTable
// call is even made — blocking that first, setup-only call would deadlock
// the test itself rather than the thing under test.
type slowChargeAsker struct {
	mu        sync.Mutex
	calls     int
	startOnce sync.Once
	released  sync.Once
	started   chan struct{}
	release   chan struct{}
}

func newSlowChargeAsker() *slowChargeAsker {
	return &slowChargeAsker{started: make(chan struct{}), release: make(chan struct{})}
}

// releaseNow unblocks every current and future AskChargePolicy call stuck
// in this asker, exactly once. Idempotent so a test can call it both
// explicitly (once its own assertions are done) and via a t.Cleanup
// safety net (in case a t.Fatal fires first) without a double-close panic.
func (a *slowChargeAsker) releaseNow() {
	a.released.Do(func() { close(a.release) })
}

func (a *slowChargeAsker) AskChargePolicy() (ChargePolicy, bool) {
	a.mu.Lock()
	a.calls++
	isSetupCall := a.calls == 1
	a.mu.Unlock()
	if isSetupCall {
		return ChargePolicy{}, false
	}
	a.startOnce.Do(func() { close(a.started) })
	<-a.release
	return ChargePolicy{}, false
}

// ut-docs#2443 (review finding S1 on ut-docs#2434): BindTable used to call
// mover.SetTable/svc.SetTable INSIDE the manager's m.mu critical section,
// so a slow plugin charge-policy ask triggered by one guest's bind
// serialized every OTHER table's concurrent BindTable behind it for the
// ask's duration. This proves the fix: a deliberately-stuck ask on
// table-A's bind must not stop a concurrent bind on table-B from
// completing immediately.
func TestSessionBasketManager_BindTable_SlowChargePolicyAskDoesNotBlockOtherTables(t *testing.T) {
	m, now := newTestSessionManager(t)

	moverToken, mover := m.Create()
	asker := newSlowChargeAsker()
	// Safety net: if an assertion below t.Fatal's before the explicit
	// releaseNow() call further down runs, this still unblocks the
	// goroutine parked in AskChargePolicy instead of leaking it for the
	// life of the test binary (ut-docs#2443 N5).
	t.Cleanup(asker.releaseNow)
	mover.SetChargePolicyAsker(asker)

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.BindTable("table-A", "T1", moverToken, mover, time.Hour, *now)
	}()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("mover's SetTable never reached the slow charge-policy ask")
	}

	// Table-A's bind is now stuck inside the ask. A concurrent bind on a
	// completely different table must complete immediately — if it's
	// still waiting on m.mu, that mu is being held across the ask, which
	// is exactly the regression this card fixes.
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		tok, svc, busy := m.BindTable("table-B", "T2", "", nil, time.Hour, *now)
		if busy || svc == nil || tok == "" {
			t.Errorf("table-B bind while table-A's ask is stuck: busy=%v svc=%v tok=%q, want a clean bind", busy, svc, tok)
		}
	}()

	select {
	case <-otherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("table-B's BindTable is still blocked behind table-A's in-flight charge-policy ask — m.mu is being held across the ask")
	}

	asker.releaseNow()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("table-A's BindTable never returned after its ask was released")
	}

	if got := mover.TableID(); got != "table-A" {
		t.Fatalf("mover's TableID() after its bind completed = %q, want %q", got, "table-A")
	}
}

// ut-docs#2443 M1: the test above only exercises the MOVER path, where
// BindTable never calls m.factory() at all. The mint path (mover == nil,
// the common first-ever-scan case in production) has its own blocking-ask
// source — m.factory() itself installs tax/charge-policy askers, and that
// installation synchronously triggers one AskChargePolicy call — which a
// first cut of this fix left running INSIDE m.mu, defeating the card's
// point for its most common path. This proves the actual fix (building the
// fresh Service before m.mu.Lock() when mover is nil): a still-in-flight,
// deliberately-stuck ask from one mint's OWN factory call must not stop a
// concurrent, unrelated manager operation (Get, here — it only ever needs
// m.mu briefly) from completing immediately.
func TestSessionBasketManager_BindTable_MintPathFactoryAskDoesNotHoldLock(t *testing.T) {
	asker := newSlowChargeAsker()
	t.Cleanup(asker.releaseNow)
	m := NewSessionBasketManager(func() *Service {
		s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
		s.SetChargePolicyAsker(asker) // installing it synchronously recomputes once
		return s
	})
	now := time.Now()

	// A pre-existing, unrelated session: Create()'s own factory call is
	// AskChargePolicy's call #1 (the setup call slowChargeAsker always
	// lets through immediately), so this session is minted synchronously,
	// before table-A's mint below makes call #2 — the one that blocks.
	existingToken, _ := m.Create()

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.BindTable("table-A", "T1", "", nil, time.Hour, now)
	}()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("table-A's mint never reached its factory's charge-policy ask")
	}

	// Table-A's mint is now stuck inside its OWN factory's ask. A Get on a
	// completely unrelated, already-live session must complete
	// immediately — if it's still waiting on m.mu, that mu is being held
	// across m.factory() itself, not just across the later SetTable call.
	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		if _, ok := m.Get(existingToken); !ok {
			t.Error("Get on an unrelated, already-live session failed while table-A's mint was stuck in its own factory's ask")
		}
	}()

	select {
	case <-getDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Get() on an unrelated session is still blocked behind table-A's in-flight factory charge-policy ask — m.factory() is being called under m.mu")
	}

	asker.releaseNow()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("table-A's BindTable never returned after its ask was released")
	}
}
