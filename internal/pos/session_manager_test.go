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
// section; proven here under `go test -race` with many goroutines racing
// to bind the identical table: exactly one must succeed, every other must
// see busy and mint nothing.
func TestSessionBasketManager_BindTable_ConcurrentSameTableBindsExactlyOnce(t *testing.T) {
	m := NewSessionBasketManager(func() *Service {
		return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
	})

	const guests = 32
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
					t.Errorf("busy result must return no token/service, got (%q, %p)", tok, svc)
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
		t.Fatalf("bound = %d, want exactly 1 — a real concurrent race must never let two mints win", bound)
	}
	if busy != guests-1 {
		t.Fatalf("busy = %d, want %d", busy, guests-1)
	}
	if n := m.Len(); n != 1 {
		t.Fatalf("live sessions after the race = %d, want 1", n)
	}
	svc, ok := m.Get(boundToken)
	if !ok || svc != boundSvc {
		t.Fatal("the one winning bind's token must resolve back to its own service")
	}
	if got := svc.TableID(); got != "table-A" {
		t.Fatalf("winning session's TableID() = %q, want %q", got, "table-A")
	}
}

// The mover case (an existing session's own table changes) is atomic the
// same way: a live session moving onto a table another live session
// already holds must see busy and stay on its original table, never lose
// its own binding or basket.
func TestSessionBasketManager_BindTable_MoverBlockedByBusyTableKeepsOwnBinding(t *testing.T) {
	m, now := newTestSessionManager(t)

	_, incumbent := m.Create()
	incumbent.SetTable("table-B", "T2")

	moverToken, mover := m.Create()
	mover.SetTable("table-A", "T1")
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
