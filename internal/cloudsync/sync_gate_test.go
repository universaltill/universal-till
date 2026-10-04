package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0148 (ut-docs#3615): a registered till checks in only when its last
// cloud-confirmed entitlement block is active + allows cloud_sync, or
// inside an operator check-in window. A gated tick makes NO network call
// and is not an error.

// useRealSyncGate swaps TestMain's always-open gate for the production one
// and closes the operator window, both restored/closed again afterwards so
// no other test sees this test's state.
func useRealSyncGate(t *testing.T) {
	t.Helper()
	prev := syncAllowedFn
	syncAllowedFn = entitlement.SyncAllowed
	resetOperatorWindow()
	t.Cleanup(func() {
		syncAllowedFn = prev
		resetOperatorWindow()
	})
}

// resetOperatorWindow closes the operator window.
func resetOperatorWindow() { operatorDeadlineNS.Store(0) }

// countingCloud wraps fakeCloud and counts every request of any path, so
// "zero requests" also covers the conditional GET /v1/stores/checkin.
type countingCloud struct {
	fakeCloud
	requests atomic.Int32
	syncs    atomic.Int32
}

func (c *countingCloud) handler() http.Handler {
	inner := c.fakeCloud.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		if r.URL.Path == "/v1/stores/sync" {
			c.syncs.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
}

func seedEntitlement(t *testing.T, db *sql.DB, plan, status string, confirmed time.Time) {
	t.Helper()
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), map[string]string{
		entitlement.KeyPlan:               plan,
		entitlement.KeySubscriptionStatus: status,
		entitlement.KeyLastConfirmedAt:    confirmed.UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
}

func TestTickGatedForUnpaidTillMakesNoRequest(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		seed func(t *testing.T, db *sql.DB)
	}{
		{"no entitlement cache", func(*testing.T, *sql.DB) {}},
		{"local plan", func(t *testing.T, db *sql.DB) { seedEntitlement(t, db, "local", "active", now) }},
		{"lapsed shop", func(t *testing.T, db *sql.DB) { seedEntitlement(t, db, "shop", "lapsed", now) }},
		{"status none", func(t *testing.T, db *sql.DB) { seedEntitlement(t, db, "pro", "none", now) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useRealSyncGate(t)
			cloud := &countingCloud{}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			db := openMigratedDB(t, "gate.db").DB
			tc.seed(t, db)
			contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{})
			if err != nil {
				t.Fatalf("a gated tick is not an error: %v", err)
			}
			if contacted {
				t.Fatal("a gated tick must report contacted=false")
			}
			if n := cloud.requests.Load(); n != 0 {
				t.Fatalf("unpaid till made %d cloud requests, want 0", n)
			}
		})
	}
}

func TestTickRunsForPaidTill(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		plan      string
		confirmed time.Time
	}{
		{"shop active, fresh", "shop", now.Add(-time.Minute)},
		{"pro active, stale beyond grace (must still re-confirm)", "pro", now.Add(-entitlement.Grace - 24*time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useRealSyncGate(t)
			cloud := &countingCloud{}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			db := openMigratedDB(t, "paid.db").DB
			seedEntitlement(t, db, tc.plan, "active", tc.confirmed)
			contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{})
			if err != nil {
				t.Fatalf("tick: %v", err)
			}
			if !contacted || cloud.syncs.Load() != 1 {
				t.Fatalf("paid till: contacted=%v syncs=%d, want true/1", contacted, cloud.syncs.Load())
			}
		})
	}
}

// An operator window lets an unpaid till check in; a paid entitlement
// block in that answer turns the periodic loop on with no window.
func TestOperatorWindowChecksInAndPaidAnswerKeepsLoopOn(t *testing.T) {
	useRealSyncGate(t)
	cloud := &countingCloud{}
	cloud.entitlement = json.RawMessage(`{"plan":"shop","subscription_status":"active"}`)
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "window.db").DB
	cfg := testCfg(srv.URL)

	OpenOperatorWindow(2 * time.Minute)
	contacted, err := tick(context.Background(), cfg, db, Hooks{})
	if err != nil || !contacted || cloud.syncs.Load() != 1 {
		t.Fatalf("in-window tick: contacted=%v err=%v syncs=%d, want true/nil/1", contacted, err, cloud.syncs.Load())
	}

	resetOperatorWindow() // the window has closed
	cloud.mu.Lock()
	cloud.entitlement = json.RawMessage(`{"plan":"local","subscription_status":"none"}`)
	cloud.mu.Unlock()
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if n := cloud.syncs.Load(); n != 2 {
		t.Fatalf("paid answer should keep the loop on: syncs = %d, want 2", n)
	}

	// That second answer said local/none: the loop stops after it (ADR-0148 §4).
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if n := cloud.syncs.Load(); n != 2 {
		t.Fatalf("a local/none answer must stop the loop: syncs = %d, want 2", n)
	}
}

func TestOperatorWindowOnlyExtends(t *testing.T) {
	useRealSyncGate(t)
	now := time.Now()
	OpenOperatorWindow(15 * time.Minute)
	OpenOperatorWindow(2 * time.Minute) // must not shorten the claim-code window
	if !operatorWindowOpen(now.Add(10 * time.Minute)) {
		t.Fatal("a shorter window shortened a longer open one")
	}
	if operatorWindowOpen(now.Add(16 * time.Minute)) {
		t.Fatal("window still open after its deadline")
	}
}

func TestOperatorWindowClosedByDefault(t *testing.T) {
	useRealSyncGate(t)
	if operatorWindowOpen(time.Now()) {
		t.Fatal("operator window open without an operator action")
	}
}

// Review finding (ut-docs#3615): an operator action kicks the loop, but a
// kick used to be dropped while the loop backed off after a failed
// check-in — so an unpaid till whose window check-in failed once waited
// out a backoff of up to an hour, well past its 2-minute window. While an
// operator window is open, a kick during backoff must cause a check-in.
func TestStartKickDuringBackoffChecksInWhileOperatorWindowOpen(t *testing.T) {
	useRealSyncGate(t)
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	origNew := newSchedulerFn
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick); newSchedulerFn = origNew })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(time.Hour)) // the backoff after a failure is ~an hour
	newSchedulerFn = func() *scheduler { return &scheduler{rng: func() float64 { return 0.99 }} }

	var syncs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stores/sync" {
			return
		}
		if syncs.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	}))
	defer srv.Close()

	OpenOperatorWindow(2 * time.Minute) // an operator action on this unpaid till
	kick := make(chan struct{}, 1)
	failed := make(chan struct{})
	var once sync.Once
	hooks := Hooks{
		Kick: kick,
		AfterTick: func(_ context.Context, _ bool, err error) {
			if err != nil {
				once.Do(func() { close(failed) })
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), testDB(t), hooks)
	select {
	case <-failed:
	case <-time.After(3 * time.Second):
		t.Fatal("the in-window check-in never ran")
	}
	time.Sleep(20 * time.Millisecond) // the loop is now in its backoff wait
	kick <- struct{}{}                // the operator acts again (e.g. "Check for a paid plan")

	deadline := time.Now().Add(2 * time.Second)
	for syncs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	waitJoined(t, wg)
	if n := syncs.Load(); n < 2 {
		t.Fatalf("a kick during backoff with the operator window open caused no check-in (syncs = %d, want 2)", n)
	}
}
