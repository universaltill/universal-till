package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0148 amendment (2026-10-14, ut-docs#4285): a lapsed till, or one that
// recorded a paid confirmation in the last 90 days, makes one check-in per
// hour (± 10 %) while it runs, so a renewal done in the cloud admin reaches
// it by itself. A 402 does not end the watch. A never-paid store (none, no
// block) stays silent.

const lapsedBlock = `{"plan":"shop","subscription_status":"lapsed","expires_at":null,"refreshed_at":"2026-10-14T10:00:00Z","cloud_link":"periodic"}`
const activeShopBlock = `{"plan":"shop","subscription_status":"active","expires_at":"2026-11-14T00:00:00Z","refreshed_at":"2026-10-14T10:00:00Z","cloud_link":"periodic"}`

// watchCloud counts sync POSTs and answers each with a 402 carrying block,
// or (active = true) a 200 with the active shop block.
type watchCloud struct {
	mu     sync.Mutex
	posts  int
	other  int
	block  string
	active bool
}

func (c *watchCloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/stores/sync" {
			c.other++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
			return
		}
		c.posts++
		if c.active {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"directives": []any{}, "entitlement": json.RawMessage(activeShopBlock),
			}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"data":{"entitlement":` + c.block + `},"error":{"code":"plan_required","message":"x"}}`))
	})
}

func (c *watchCloud) postCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.posts
}

// watchFixture: the real gate, clocks pinned to startupDay, the start-up
// check-in already spent for today (markers current), the watch armed at
// t0 with the jitter pinned to 1.0 (exactly one hour).
func watchFixture(t *testing.T, block string) (*watchCloud, *httptest.Server, *data.SettingsRepo, func(time.Time), func() int) {
	t.Helper()
	useRealSyncGate(t)
	resetTillAuth(t)
	setNow := pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	origJitter := renewalWatchJitter
	renewalWatchJitter = func() float64 { return 1.0 }
	t.Cleanup(func() { renewalWatchJitter = origJitter })
	cloud := &watchCloud{block: block}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	seedUnpaidMarkers(t, db, "0.30.20", "2026-10-05")
	armRenewalWatch()
	cfg := testCfg(srv.URL)
	tickAt := func() int {
		t.Helper()
		_, _ = tick(context.Background(), cfg, db, Hooks{})
		return cloud.postCount()
	}
	return cloud, srv, data.NewSettingsRepo(db), setNow, tickAt
}

func seedWatchState(t *testing.T, s *data.SettingsRepo, kv map[string]string) {
	t.Helper()
	if err := s.SetMany(context.Background(), kv); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func getSetting(t *testing.T, s *data.SettingsRepo, k string) string {
	t.Helper()
	v, _, err := s.Get(context.Background(), k)
	if err != nil {
		t.Fatalf("get %s: %v", k, err)
	}
	return v
}

func TestRenewalWatch_LapsedChecksInHourly(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, lapsedBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyLastConfirmedAt: startupDay.Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	if n := tickAt(); n != 0 {
		t.Fatalf("t0: %d sync POSTs, want 0 (the watch waits an hour after start)", n)
	}
	if got := getSetting(t, s, entitlement.KeyLapsedSeenAt); got == "" {
		t.Fatal("the first gated tick on a lapsed till did not record lapsed_seen_at")
	}
	setNow(startupDay.Add(58 * time.Minute))
	if n := tickAt(); n != 0 {
		t.Fatalf("t0+58m: %d sync POSTs, want 0", n)
	}
	setNow(startupDay.Add(time.Hour))
	if n := tickAt(); n != 1 {
		t.Fatalf("t0+1h: %d sync POSTs, want 1 (the renewal watch)", n)
	}
	for at := startupDay.Add(time.Hour + 2*time.Minute); at.Before(startupDay.Add(2 * time.Hour)); at = at.Add(2 * time.Minute) {
		setNow(at)
		if n := tickAt(); n != 1 {
			t.Fatalf("%v: %d sync POSTs, want still 1 within the hour", at.Sub(startupDay), n)
		}
	}
	// A 402 did not end the watch: the next hour checks in again.
	setNow(startupDay.Add(2 * time.Hour))
	if n := tickAt(); n != 2 {
		t.Fatalf("t0+2h after a 402: %d sync POSTs, want 2", n)
	}
}

func TestRenewalWatch_NeverPaidStaysSilent(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, planRequiredBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "local", entitlement.KeySubscriptionStatus: "none",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
	})
	for at := startupDay; at.Before(startupDay.Add(24 * time.Hour)); at = at.Add(2 * time.Minute) {
		setNow(at)
		if n := tickAt(); n != 0 {
			t.Fatalf("%v: never-paid till made %d sync POSTs, want 0", at.Sub(startupDay), n)
		}
	}
	if got := getSetting(t, s, entitlement.KeyLapsedSeenAt); got != "" {
		t.Fatalf("never-paid till recorded lapsed_seen_at %q", got)
	}
}

func TestRenewalWatch_NoBlockStaysSilent(t *testing.T) {
	_, _, _, setNow, tickAt := watchFixture(t, planRequiredBlock)
	for at := startupDay; at.Before(startupDay.Add(6 * time.Hour)); at = at.Add(5 * time.Minute) {
		setNow(at)
		if n := tickAt(); n != 0 {
			t.Fatalf("%v: till with no block made %d sync POSTs, want 0", at.Sub(startupDay), n)
		}
	}
}

func TestRenewalWatch_EndsNinetyDaysAfterLastPaid(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, lapsedBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
		entitlement.KeyLastPaidAt:      startupDay.Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	for at := startupDay; at.Before(startupDay.Add(3 * time.Hour)); at = at.Add(2 * time.Minute) {
		setNow(at)
		if n := tickAt(); n != 0 {
			t.Fatalf("%v: a till lapsed 91 days ago made %d sync POSTs, want 0", at.Sub(startupDay), n)
		}
	}
}

func TestRenewalWatch_EndsNinetyDaysAfterLapseSeen(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, lapsedBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
		entitlement.KeyLapsedSeenAt:    startupDay.Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	setNow(startupDay.Add(time.Hour))
	if n := tickAt(); n != 0 {
		t.Fatalf("a lapse first seen 91 days ago: %d sync POSTs, want 0", n)
	}
}

// A paid till that the cloud now answers "none" (downgraded, no lapse)
// still watches for 90 days after its last paid confirmation.
func TestRenewalWatch_RecentlyPaidWatchesEvenWithoutLapsedStatus(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, planRequiredBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "local", entitlement.KeySubscriptionStatus: "none",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
		entitlement.KeyLastPaidAt:      startupDay.Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	setNow(startupDay.Add(time.Hour))
	if n := tickAt(); n != 1 {
		t.Fatalf("recently paid till: %d sync POSTs at t0+1h, want 1", n)
	}
}

// A renewal answered on a watch call turns the paid loop on.
func TestRenewalWatch_RenewalTurnsLoopOn(t *testing.T) {
	cloud, _, s, setNow, tickAt := watchFixture(t, lapsedBlock)
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
	})
	cloud.mu.Lock()
	cloud.active = true
	cloud.mu.Unlock()
	setNow(startupDay.Add(time.Hour))
	if n := tickAt(); n != 1 {
		t.Fatalf("t0+1h: %d sync POSTs, want 1", n)
	}
	if !entitlement.SyncAllowed(context.Background(), s) {
		t.Fatal("after an active answer the gate is still closed")
	}
	if getSetting(t, s, entitlement.KeyLastPaidAt) == "" || getSetting(t, s, entitlement.KeyLapsedSeenAt) != "" {
		t.Fatal("an active answer must record last_paid_at and clear lapsed_seen_at")
	}
}

// Without Start (no arm), the watch never fires.
func TestRenewalWatch_NotArmedMakesNoCall(t *testing.T) {
	_, _, s, setNow, tickAt := watchFixture(t, lapsedBlock)
	disarmRenewalWatch()
	seedWatchState(t, s, map[string]string{
		entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyLastConfirmedAt: startupDay.UTC().Format(time.RFC3339),
	})
	setNow(startupDay.Add(2 * time.Hour))
	if n := tickAt(); n != 0 {
		t.Fatalf("unarmed watch: %d sync POSTs, want 0", n)
	}
}

func TestRenewalWatch_JitterStaysWithinTenPercent(t *testing.T) {
	for i := 0; i < 1000; i++ {
		j := renewalWatchJitter()
		if j < 0.9 || j > 1.1 {
			t.Fatalf("jitter %v outside [0.9, 1.1]", j)
		}
	}
}

func TestCacheEntitlement_RecordsPaidAndLapseMarkers(t *testing.T) {
	ctx := context.Background()
	s := data.NewSettingsRepo(testDB(t))
	t1 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cacheEntitlement(ctx, s, json.RawMessage(activeShopBlock), t1)
	if got := getSetting(t, s, entitlement.KeyLastPaidAt); got != t1.Format(time.RFC3339) {
		t.Fatalf("last_paid_at after an active shop block = %q, want %q", got, t1.Format(time.RFC3339))
	}
	t2 := t1.Add(24 * time.Hour)
	cacheEntitlement(ctx, s, json.RawMessage(lapsedBlock), t2)
	if got := getSetting(t, s, entitlement.KeyLapsedSeenAt); got != t2.Format(time.RFC3339) {
		t.Fatalf("lapsed_seen_at = %q, want %q", got, t2.Format(time.RFC3339))
	}
	if got := getSetting(t, s, entitlement.KeyLastPaidAt); got != t1.Format(time.RFC3339) {
		t.Fatalf("a lapsed block moved last_paid_at to %q", got)
	}
	cacheEntitlement(ctx, s, json.RawMessage(lapsedBlock), t2.Add(time.Hour))
	if got := getSetting(t, s, entitlement.KeyLapsedSeenAt); got != t2.Format(time.RFC3339) {
		t.Fatalf("a second lapsed block rewrote lapsed_seen_at to %q, want it kept at %q", got, t2.Format(time.RFC3339))
	}
	t3 := t2.Add(48 * time.Hour)
	cacheEntitlement(ctx, s, json.RawMessage(activeShopBlock), t3)
	if got := getSetting(t, s, entitlement.KeyLapsedSeenAt); got != "" {
		t.Fatalf("an active block left lapsed_seen_at = %q, want cleared", got)
	}
	if got := getSetting(t, s, entitlement.KeyLastPaidAt); got != t3.Format(time.RFC3339) {
		t.Fatalf("last_paid_at = %q, want %q", got, t3.Format(time.RFC3339))
	}
	// An active free plan is not a paid confirmation.
	cacheEntitlement(ctx, s, json.RawMessage(`{"plan":"local","subscription_status":"active","expires_at":null}`), t3.Add(time.Hour))
	if got := getSetting(t, s, entitlement.KeyLastPaidAt); got != t3.Format(time.RFC3339) {
		t.Fatalf("an active local block moved last_paid_at to %q", got)
	}
}

// Start arms the watch an interval out (the start-up check-in covers the
// boot itself).
func TestStartArmsRenewalWatch(t *testing.T) {
	useRealSyncGate(t)
	pinStartupClock(t, startupDay)
	origJitter := renewalWatchJitter
	renewalWatchJitter = func() float64 { return 1.0 }
	t.Cleanup(func() { renewalWatchJitter = origJitter })
	origFirst := firstDelayNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst) })
	firstDelayNS.Store(int64(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg("http://127.0.0.1:1"), testDB(t), Hooks{})
	next := renewalWatchNextAt.Load()
	cancel()
	waitJoined(t, wg)
	if next == nil || !next.Equal(startupDay.Add(time.Hour)) {
		t.Fatalf("after Start the watch is due at %v, want %v", next, startupDay.Add(time.Hour))
	}
}

// disarmRenewalWatch stops the watch (tests only: production never
// disarms it; a restart re-arms it from Start).
func disarmRenewalWatch() { renewalWatchNextAt.Store(nil) }
