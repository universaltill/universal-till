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

	token, svc, _ := m.Create()
	if svc == nil {
		t.Fatal("Create returned a nil Service")
	}
	// Same shape as internal/data's NewOrderTrackingToken: 16 random bytes as
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
		tok, _, _ := m.Create()
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
	tokA, a, _ := m.Create()
	tokB, b, _ := m.Create()
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

// Sweep evicts only sessions idle longer than maxIdle; a Get refreshes the
// idle clock so an actively used session is never swept.
func TestSessionBasketManager_SweepEvictsOnlyIdleSessions(t *testing.T) {
	m, now := newTestSessionManager(t)
	t0 := *now
	tokIdle, _, _ := m.Create()
	tokActive, _, _ := m.Create()
	tokFresh := ""

	// 90 minutes later the "active" guest touches their session, and a
	// brand-new one is minted.
	*now = t0.Add(90 * time.Minute)
	if _, ok := m.Get(tokActive); !ok {
		t.Fatal("precondition: active session must exist")
	}
	tokFresh, _, _ = m.Create()

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
	_, a, _ := m.Create()
	_, b, _ := m.Create()
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

// ut-docs#2435 (same lock-scope class as ut-docs#2443/#2449): SetConfig used
// to call Service.SetConfig on every live session while still holding m.mu.
// Service.SetConfig triggers recomputeTotals, which — on a session with a
// charge-policy asker installed — can perform a blocking plugin round-trip.
// Held under m.mu, a stuck ask on ANY one session would serialize every
// OTHER live session's own manager operation (Get, here) behind it for the
// ask's duration. This proves the fix: a concurrent Get on an unrelated
// session must complete immediately while SetConfig is stuck in one
// session's ask, and SetConfig itself must still reach every session once
// released.
func TestSessionBasketManager_SetConfig_DoesNotBlockOtherSessions(t *testing.T) {
	m, _ := newTestSessionManager(t)

	_, stuck, _ := m.Create()
	asker := newSlowChargeAsker()
	t.Cleanup(asker.releaseNow)
	stuck.SetChargePolicyAsker(asker)

	otherToken, other, _ := m.Create()

	cfg := Config{TaxRateBasisPoints: 1000, TaxInclusive: false}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.SetConfig(cfg)
	}()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig never reached the stuck session's charge-policy ask")
	}

	// SetConfig is now stuck inside one session's ask. A Get on a completely
	// unrelated session must complete immediately — if it's still waiting on
	// m.mu, that mu is being held across the ask, which is exactly the
	// regression this card fixes.
	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		if _, ok := m.Get(otherToken); !ok {
			t.Error("Get on an unrelated session failed while SetConfig was stuck in another session's ask")
		}
	}()

	select {
	case <-getDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Get is still blocked behind SetConfig's in-flight charge-policy ask — m.mu is being held across it")
	}

	asker.releaseNow()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig never returned after its ask was released")
	}

	if other.Config() != cfg {
		t.Fatalf("other session's Config() = %+v after SetConfig, want %+v", other.Config(), cfg)
	}
	if stuck.Config() != cfg {
		t.Fatalf("stuck session's Config() = %+v after SetConfig, want %+v", stuck.Config(), cfg)
	}
}

// ut-docs#2488: SetConfig dispatches to each session AFTER releasing m.mu
// (ut-docs#2435), so two concurrent calls' dispatch loops could interleave
// and leave live sessions on different final configs. This proves the fix
// (setConfigMu serializes whole batches): while call 1 is stuck inside one
// session's charge-policy ask, call 2 must not write cfg2 to ANY session.
// Without the fix call 2 writes cfg2 to every session ahead of the stuck
// one in its map order, so the mid-flight check fails ~32/33 of the time;
// the final "cfg2 everywhere" check catches most of the remaining runs.
func TestSessionBasketManager_SetConfig_ConcurrentCallsDoNotInterleave(t *testing.T) {
	m, _ := newTestSessionManager(t)

	_, stuck, _ := m.Create()
	asker := newSlowChargeAsker()
	t.Cleanup(asker.releaseNow)
	stuck.SetChargePolicyAsker(asker)

	const others = 32
	svcs := make([]*Service, 0, others)
	for i := 0; i < others; i++ {
		_, svc, _ := m.Create()
		svcs = append(svcs, svc)
	}

	// cfg2 must differ from newTestSessionManager's factory default
	// (Config{TaxRateBasisPoints: 2000}) too, or every freshly-created
	// session would already read as cfg2 before any SetConfig call runs,
	// making the mid-flight assertion below a false positive.
	cfg1 := Config{TaxRateBasisPoints: 1000}
	cfg2 := Config{TaxRateBasisPoints: 2500}

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		m.SetConfig(cfg1)
	}()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig(cfg1) never reached the stuck session's charge-policy ask")
	}

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		m.SetConfig(cfg2)
	}()

	// Give call 2 a short window to run. With the fix it must be parked
	// behind call 1's whole batch (blocked on setConfigMu), so it cannot
	// have reached, let alone finished, any session's SetConfig yet.
	select {
	case <-done2:
		t.Fatal("SetConfig(cfg2) returned while SetConfig(cfg1) was still stuck in its batch — setConfigMu is not serializing the two calls")
	case <-time.After(200 * time.Millisecond):
	}
	for i, svc := range svcs {
		if svc.Config() == cfg2 {
			t.Fatalf("session %d already has Config() = cfg2 while call 1's batch is still in flight — the two SetConfig batches interleaved", i)
		}
	}

	asker.releaseNow()

	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig(cfg1) never returned after its ask was released")
	}
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig(cfg2) never returned")
	}

	if stuck.Config() != cfg2 {
		t.Fatalf("stuck session's Config() = %+v after both calls, want %+v (the later call must win everywhere)", stuck.Config(), cfg2)
	}
	for i, svc := range svcs {
		if svc.Config() != cfg2 {
			t.Fatalf("session %d's Config() = %+v after both calls, want %+v (the later call must win everywhere)", i, svc.Config(), cfg2)
		}
	}
}

func TestSessionBasketManager_HasItems(t *testing.T) {
	m, _ := newTestSessionManager(t)
	if m.HasItems() {
		t.Fatal("empty manager must report no items")
	}
	_, a, _ := m.Create()
	_, _, _ = m.Create()
	if m.HasItems() {
		t.Fatal("two empty sessions must report no items")
	}
	a.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 320}, 1, nil)
	if !m.HasItems() {
		t.Fatal("a session holding a line must report items")
	}
}

// ut-docs#2449 (follow-up from the ut-docs#2444 review, same lock-scope class
// as ut-docs#2443/#2444): HasItems used to decide "does this session hold an
// item" via svc.Basket().ItemCount() > 0, all under m.mu. Basket() always
// calls recomputeTotals(), which — on any session with a tax/charge-policy
// asker installed (any country plugin, ADR-0061) — can perform a blocking
// plugin round-trip. HasItems runs under the manager's own lock, so a stuck
// ask on ANY live session would serialize every other manager operation (and
// every other session's request) behind it for the ask's duration. This
// proves the fix: HasItems must not invoke the charge-policy asker at all.
func TestSessionBasketManager_HasItems_DoesNotBlockOnChargePolicyAsk(t *testing.T) {
	m, _ := newTestSessionManager(t)

	_, svc, _ := m.Create()
	asker := newSlowChargeAsker()
	t.Cleanup(asker.releaseNow)
	svc.SetChargePolicyAsker(asker)

	done := make(chan struct{})
	var got bool
	go func() {
		defer close(done)
		got = m.HasItems()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HasItems blocked on a plugin charge-policy ask — same lock-scope class as ut-docs#2443/#2444")
	}
	if got {
		t.Fatal("HasItems() = true for a session with no lines, want false")
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
			tok, svc, _ := m.Create()
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
				_, _ = m.Get(tok)
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

// ut-docs#4337 (ADR-0159 §5): a guest_order directive builds its basket
// from the session factory but never registers it as a live session — it
// is not reachable by any cookie, never swept, never counted.
func TestSessionBasketManager_NewDetachedUsesFactoryWithoutRegistering(t *testing.T) {
	built := 0
	m := NewSessionBasketManager(func() *Service {
		built++
		return NewServiceWithResolver(Config{TaxRateBasisPoints: 700}, nil)
	})
	svc, ok := m.NewDetached()
	if !ok || svc == nil || built != 1 {
		t.Fatalf("NewDetached = %v %v (factory calls %d)", svc, ok, built)
	}
	if svc.Config().TaxRateBasisPoints != 700 {
		t.Fatalf("detached basket not built by the factory: %+v", svc.Config())
	}
	if m.Len() != 0 {
		t.Fatalf("a detached basket was registered as a live session (Len %d)", m.Len())
	}
	other, _ := m.NewDetached()
	if other == svc {
		t.Fatal("NewDetached handed out the same Service twice")
	}
	var nilM *SessionBasketManager
	if s, ok := nilM.NewDetached(); ok || s != nil {
		t.Fatal("nil manager must report no basket")
	}
}
