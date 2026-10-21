package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
)

// ADR-0148 follow-up (ut-docs#3624): the cloud refuses /v1/stores/sync and
// /v1/stores/checkin for a store without an active paid plan with 402
// plan_required, the ADR-0060 entitlement block in data.entitlement and
// Retry-After: 3600. The till caches that block (so its own ADR-0148 gate
// takes over from the next tick), backs off for the hour, and treats the
// refusal as quiet: no warning, no problem line, no credential chip.

const planRequiredBlock = `{"plan":"local","subscription_status":"none","expires_at":null,"refreshed_at":"2026-10-04T10:00:00Z","cloud_link":"periodic"}`

func planRequiredBody() string {
	return `{"data":{"entitlement":` + planRequiredBlock + `},"error":{"code":"plan_required","message":"cloud sync needs an active Shop, Pro or Multi-location plan"}}`
}

func writePlanRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "3600")
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write([]byte(planRequiredBody()))
}

func err402() error {
	return &statusError{Path: "/v1/stores/sync", StatusCode: http.StatusPaymentRequired, RetryAfter: time.Hour,
		Entitlement: json.RawMessage(planRequiredBlock)}
}

func assertBlock(t *testing.T, got json.RawMessage) {
	t.Helper()
	var g, w map[string]any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("Entitlement = %q: %v", got, err)
	}
	_ = json.Unmarshal([]byte(planRequiredBlock), &w)
	if g["plan"] != w["plan"] || g["subscription_status"] != w["subscription_status"] || g["refreshed_at"] != w["refreshed_at"] {
		t.Fatalf("Entitlement = %s, want %s", got, planRequiredBlock)
	}
}

// --- the 402 on the wire ---

func TestPost402CapturesEntitlementAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writePlanRequired(w) }))
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("err = %v, want a 402 statusError", err)
	}
	assertBlock(t, se.Entitlement)
	if se.RetryAfter != time.Hour {
		t.Fatalf("RetryAfter = %v, want 1h", se.RetryAfter)
	}
	if want := "cloudsync: /v1/stores/sync returned 402"; se.Error() != want {
		t.Fatalf("Error() = %q, want the unchanged %q", se.Error(), want)
	}
	if se.Code != "" {
		t.Fatalf("Code = %q, want empty (decoded for 401 only)", se.Code)
	}
}

// Only a 402 carries the block: any other status leaves Entitlement empty,
// so no existing caller's view of a 503/429/401 changes.
func TestPostNon402LeavesEntitlementEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(planRequiredBody()))
	}))
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusServiceUnavailable || len(se.Entitlement) != 0 {
		t.Fatalf("err = %#v, want a 503 statusError with no Entitlement", err)
	}
}

func TestCheckin402CapturesEntitlementAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writePlanRequired(w) }))
	defer srv.Close()
	status, _, err := getCheckin(context.Background(), srv.URL, "store-1", "tok-1", false, 0)
	var se *statusError
	if status != http.StatusPaymentRequired || !errors.As(err, &se) {
		t.Fatalf("status=%d err=%#v, want 402 with a statusError", status, err)
	}
	assertBlock(t, se.Entitlement)
	if se.RetryAfter != time.Hour {
		t.Fatalf("RetryAfter = %v, want 1h", se.RetryAfter)
	}
}

// --- the scheduler and the log ---

func TestRetryAfterHintHonours402(t *testing.T) {
	if got := retryAfterHint(err402()); got != time.Hour {
		t.Fatalf("retryAfterHint(402, 1h) = %v, want 1h", got)
	}
	long := &statusError{StatusCode: http.StatusPaymentRequired, RetryAfter: 5 * time.Hour}
	if got := retryAfterHint(long); got != retryAfterClamp {
		t.Fatalf("retryAfterHint(402, 5h) = %v, want the %v clamp", got, retryAfterClamp)
	}
}

// One 402 parks the loop for the hour (not the 10-minute generic cap) and
// never touches the ADR-0116 D6 credential streak or its chip.
func TestSchedulerBacksOffHourlyAfterOne402WithoutAuthChip(t *testing.T) {
	s := testScheduler(t)
	if d := s.next(err402()); d != time.Hour {
		t.Fatalf("after one 402: wait %v, want 1h", d)
	}
	for i := 0; i < 3; i++ {
		s.next(err402())
	}
	if s.auth.status().Show {
		t.Fatal("402s raised the credential chip")
	}
}

// A 402 plan_required is not a problem (ADR-0148 §5): logTickError keeps it
// out of the warn/error ring the back-office panel and the heartbeat's
// problems feed read. Any other tick failure still lands there.
func TestLogTickError402IsNotAProblem(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	logTickError(err402())
	if got := logging.Recent(); len(got) != 0 {
		t.Fatalf("a 402 logged problems: %+v", got)
	}
	logTickError(err503())
	if got := logging.Recent(); len(got) != 1 {
		t.Fatalf("a 503 logged %d problems, want 1 (control)", len(got))
	}
}

// --- driven: a till whose cache still says paid meets a cloud that says no ---

// The full path, both ways the refusal can arrive (the check-in GET, or the
// POST when the GET isn't usable): the tick fails with the 402, the block
// is cached, the scheduler waits the hour, nothing is logged as a problem,
// no chip — and with the real ADR-0148 gate, the next tick makes no call.
func TestTickOn402CachesBlockBacksOffHourlyThenStaysQuiet(t *testing.T) {
	for _, tc := range []struct {
		name       string
		checkinRaw int // what GET /v1/stores/checkin answers; 0 = 402
	}{
		{name: "checkin_402"},
		{name: "sync_402_after_unusable_checkin", checkinRaw: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeClock(t)
			resetTillAuth(t)
			logging.ResetRecent()
			t.Cleanup(logging.ResetRecent)

			var calls, posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method == http.MethodGet && tc.checkinRaw != 0 {
					w.WriteHeader(tc.checkinRaw)
					return
				}
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				writePlanRequired(w)
			}))
			defer srv.Close()

			origTick := tickIntervalNS.Load()
			t.Cleanup(func() { tickIntervalNS.Store(origTick) })
			tickIntervalNS.Store(int64(2 * time.Minute))
			sched := newScheduler()
			sched.rng = func() float64 { return 0.5 }

			cfg, settingsDB := testCfg(srv.URL), testDB(t)
			contacted, err := tick(context.Background(), cfg, settingsDB, Hooks{})
			var se *statusError
			if contacted || !errors.As(err, &se) || se.StatusCode != http.StatusPaymentRequired {
				t.Fatalf("tick = (%v, %v), want (false, a 402 statusError)", contacted, err)
			}
			if tc.checkinRaw == 0 && posts.Load() != 0 {
				t.Fatalf("POSTed %d times after a 402 check-in, want 0", posts.Load())
			}
			logTickError(err)
			if d := sched.next(err); d != time.Hour {
				t.Fatalf("wait after the 402 = %v, want 1h", d)
			}
			if AuthChipStatus().Show {
				t.Fatal("a 402 raised the credential chip")
			}
			if got := logging.Recent(); len(got) != 0 {
				t.Fatalf("a 402 tick logged problems: %+v", got)
			}

			repo := data.NewSettingsRepo(settingsDB)
			for k, want := range map[string]string{
				entitlement.KeyPlan:               "local",
				entitlement.KeySubscriptionStatus: "none",
				entitlement.KeyCloudLinkTier:      "periodic",
			} {
				if v, ok, _ := repo.Get(context.Background(), k); !ok || v != want {
					t.Fatalf("%s = %q (set=%v), want %q from the 402's block", k, v, ok, want)
				}
			}
			if v, ok, _ := repo.Get(context.Background(), entitlement.KeyLastConfirmedAt); !ok || v == "" {
				t.Fatal("last_confirmed_at not recorded: the 402's block was not cached")
			}

			// The real gate now reads the cached "not paid": the next tick
			// is quiet (no call, no error) — ADR-0148's steady state.
			useRealSyncGate(t)
			before := calls.Load()
			contacted, err = tick(context.Background(), cfg, settingsDB, Hooks{})
			if contacted || err != nil || calls.Load() != before {
				t.Fatalf("tick after caching = (%v, %v), %d new calls; want a quiet gated tick", contacted, err, calls.Load()-before)
			}
		})
	}
}

// --- ut-docs#4304: a 402 that closes the gate keeps the normal cadence ---

// The scheduler: a refusal tick marked gatedRefusal waits one normal
// jittered tick and resets the failure streak; the bare 402 (no gate
// closed: an older cloud, a proxy) keeps its hour (test above).
func TestSchedulerKeepsNormalCadenceAfterAGated402(t *testing.T) {
	s := testScheduler(t)
	s.next(err503()) // a failure streak the gated 402 must reset
	if d := s.next(&gatedRefusal{err: err402()}); d > jitteredWait(2*time.Minute, 1) || d < jitteredWait(2*time.Minute, 0) {
		t.Fatalf("after a gated 402: wait %v, want one normal tick (≤ %v)", d, jitteredWait(2*time.Minute, 1))
	}
	if s.fails != 0 {
		t.Fatalf("fails = %d after a gated 402, want 0", s.fails)
	}
	if s.auth.status().Show {
		t.Fatal("a gated 402 raised the credential chip")
	}
	var se *statusError
	if err := error(&gatedRefusal{err: err402()}); !errors.As(err, &se) || se.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("errors.As through gatedRefusal = %v, want the 402 statusError", err)
	}
}

// Driven, with the real ADR-0148 gate: a till whose cache says paid meets a
// cloud that says 402 with a not-paid block. The tick marks the refusal
// gated, the scheduler waits one normal tick (not the 402's hour), and the
// next tick uploads a newly sent issue report with no sync call.
func TestTickGated402UploadsAnIssueReportWithinOneNormalTick(t *testing.T) {
	fakeClock(t)
	resetTillAuth(t)
	withTempPendingDir(t)
	useRealSyncGate(t)
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	var syncCalls, reports atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/stores/issue-reports":
			if r.Method == http.MethodPost {
				reports.Add(1)
			}
			w.WriteHeader(http.StatusOK)
		default:
			syncCalls.Add(1)
			writePlanRequired(w)
		}
	}))
	defer srv.Close()

	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))
	sched := newScheduler()
	sched.rng = func() float64 { return 0.5 }

	cfg, d := testCfg(srv.URL), openMigratedDB(t, "gated_402_4304.db")
	seedEntitlement(t, d.DB, "shop", "active", time.Now())

	_, err := tick(context.Background(), cfg, d.DB, Hooks{})
	if _, ok := planRequired(err); !ok || !gatedByRefusal(err) {
		t.Fatalf("tick err = %v, want a 402 marked gatedRefusal", err)
	}
	logTickError(err)
	if got := logging.Recent(); len(got) != 0 {
		t.Fatalf("a gated 402 logged problems: %+v", got)
	}
	if w := sched.next(err); w != 2*time.Minute {
		t.Fatalf("wait after the gated 402 = %v, want the normal 2m tick (rng 0.5)", w)
	}

	if _, err := issuereport.Save("sent after the 402", "", []byte("audio"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := syncCalls.Load()
	contacted, err := tick(context.Background(), cfg, d.DB, Hooks{})
	if contacted || err != nil || syncCalls.Load() != before {
		t.Fatalf("next tick = (%v, %v), %d sync calls; want a quiet gated tick", contacted, err, syncCalls.Load()-before)
	}
	if reports.Load() != 1 {
		t.Fatalf("issue-report uploads on the next tick = %d, want 1", reports.Load())
	}
}

// The other way round: when the gate stays open after the 402 (its block
// still says paid — the cloud and cache disagree), the refusal is not
// marked, so the scheduler keeps the hour instead of retrying every tick.
func TestMarkGatedRefusalOnlyWhenTheGateIsClosed(t *testing.T) {
	useRealSyncGate(t)
	d := openMigratedDB(t, "gated_402_open_4304.db")
	settings := data.NewSettingsRepo(d.DB)
	seedEntitlement(t, d.DB, "shop", "active", time.Now())
	if err := markGatedRefusal(context.Background(), settings, err402()); gatedByRefusal(err) {
		t.Fatal("402 marked gated while the cache still says paid")
	}
	if err := markGatedRefusal(context.Background(), settings, err503()); gatedByRefusal(err) {
		t.Fatal("a 503 was marked gated")
	}
	seedEntitlement(t, d.DB, "shop", "lapsed", time.Now())
	if err := markGatedRefusal(context.Background(), settings, err402()); !gatedByRefusal(err) {
		t.Fatal("402 not marked gated although the cache says lapsed")
	}
}

// The unpaid check-in's own 402 (an operator window on a lapsed till: the
// POST path, planUnpaidCheckin) is marked gated too, and the tick after it
// makes no call.
func TestTickUnpaidCheckin402IsMarkedGated(t *testing.T) {
	fakeClock(t)
	resetTillAuth(t)
	useRealSyncGate(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writePlanRequired(w)
	}))
	defer srv.Close()
	cfg, d := testCfg(srv.URL), openMigratedDB(t, "unpaid_402_4304.db")
	seedEntitlement(t, d.DB, "shop", "lapsed", time.Now())
	OpenOperatorWindow(time.Minute)

	_, err := tick(context.Background(), cfg, d.DB, Hooks{})
	if _, ok := planRequired(err); !ok || !gatedByRefusal(err) || calls.Load() == 0 {
		t.Fatalf("tick err = %v after %d calls, want a 402 marked gatedRefusal", err, calls.Load())
	}
	before := calls.Load()
	if contacted, err := tick(context.Background(), cfg, d.DB, Hooks{}); contacted || err != nil || calls.Load() != before {
		t.Fatalf("next tick = (%v, %v), %d calls; want a quiet gated tick", contacted, err, calls.Load()-before)
	}
}

// --- ut-docs#4313: a 402 on the snapshot pushes is quiet too ---

// The store lapses between the tick's 200 sync and the catalog / order
// tracking pushes: those 402s must not reach the problems ring as warnings
// (ADR-0148 §5), and no hash may be stored, so the pushes retry once the
// store is paid.
func TestTickPushes402AreQuietAndStoreNoHash(t *testing.T) {
	fakeClock(t)
	resetTillAuth(t)
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	var snapPosts, trackPosts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	})
	// Answer the issue-report status pull so it doesn't warn about a 404.
	mux.HandleFunc("/v1/stores/issue-reports", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"reports": []any{}, "total": 0}})
	})
	mux.HandleFunc("/v1/stores/catalog-snapshot", func(w http.ResponseWriter, r *http.Request) {
		snapPosts.Add(1)
		writePlanRequired(w)
	})
	mux.HandleFunc("/v1/stores/order-tracking-snapshot", func(w http.ResponseWriter, r *http.Request) {
		trackPosts.Add(1)
		writePlanRequired(w)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A real migrated schema, so the other tick steps don't warn on
	// missing tables and the problems ring only sees what the pushes log.
	db := openMigratedDB(t, "plan-required-push.db").DB
	if _, err := db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('it-1','SKU1','Cola',100,1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v (a refused snapshot push must not fail the tick)", err)
	}
	if snapPosts.Load() == 0 {
		t.Fatal("snapshot push never attempted: the test would pass vacuously")
	}
	// ut-docs#4385: the snapshot 402 cached the block, so the order
	// tracking push is skipped for the rest of this tick.
	if n := trackPosts.Load(); n != 0 {
		t.Fatalf("%d order-tracking POSTs after the snapshot push's 402, want 0", n)
	}
	if plan, _, _ := data.NewSettingsRepo(db).Get(context.Background(), entitlement.KeyPlan); plan != "local" {
		t.Fatalf("%s = %q after the snapshot push's 402, want the block's %q", entitlement.KeyPlan, plan, "local")
	}
	if got := logging.Recent(); len(got) != 0 {
		t.Fatalf("a 402 on a snapshot push logged problems: %+v", got)
	}
	repo := data.NewSettingsRepo(db)
	for _, k := range []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"} {
		if v, ok, _ := repo.Get(context.Background(), k); ok {
			t.Fatalf("%s = %q stored after a refused push; it must retry once paid", k, v)
		}
	}
}

// ut-docs#4385: the catalog snapshot is accepted, then the order-tracking
// push is refused with 402: its block is cached too (the ADR-0148 gate
// closes in this tick, not the next), quietly, and no hash is stored.
func TestTickOrderTrackingPush402CachesBlock(t *testing.T) {
	fakeClock(t)
	resetTillAuth(t)
	useRealSyncGate(t)
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	var trackPosts, requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	})
	mux.HandleFunc("/v1/stores/issue-reports", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"reports": []any{}, "total": 0}})
	})
	mux.HandleFunc("/v1/stores/catalog-snapshot", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	})
	mux.HandleFunc("/v1/stores/order-tracking-snapshot", func(w http.ResponseWriter, r *http.Request) {
		trackPosts.Add(1)
		writePlanRequired(w)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	db := openMigratedDB(t, "plan-required-tracking.db").DB
	// A paid store, so the real ADR-0148 gate lets this tick sync.
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), map[string]string{
		entitlement.KeyPlan:               "pro",
		entitlement.KeySubscriptionStatus: entitlement.StatusActive,
		entitlement.KeyLastConfirmedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if !syncAllowedFn(context.Background(), data.NewSettingsRepo(db)) {
		t.Fatal("seeded paid entitlement does not open the gate: the test would pass vacuously")
	}
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v (a refused order-tracking push must not fail the tick)", err)
	}
	if trackPosts.Load() != 1 {
		t.Fatalf("order-tracking POSTs = %d, want 1", trackPosts.Load())
	}
	if got := logging.Recent(); len(got) != 0 {
		t.Fatalf("a 402 on the order-tracking push logged problems: %+v", got)
	}
	repo := data.NewSettingsRepo(db)
	if plan, _, _ := repo.Get(context.Background(), entitlement.KeyPlan); plan != "local" {
		t.Fatalf("%s = %q after the order-tracking push's 402, want the block's %q", entitlement.KeyPlan, plan, "local")
	}
	if v, ok, _ := repo.Get(context.Background(), "cloudsync.order_tracking_hash"); ok {
		t.Fatalf("cloudsync.order_tracking_hash = %q stored after a refused push", v)
	}
	// The gate is really closed now: the next tick is quiet, no request.
	before := requests.Load()
	if contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); contacted || err != nil || requests.Load() != before {
		t.Fatalf("next tick = (%v, %v), %d requests; want a quiet gated tick", contacted, err, requests.Load()-before)
	}
}

func TestLogPushError402IsNotAProblem(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	logPushError("snapshot push", err402())
	if got := logging.Recent(); len(got) != 0 {
		t.Fatalf("a 402 logged problems: %+v", got)
	}
	logPushError("snapshot push", err503())
	if got := logging.Recent(); len(got) != 1 {
		t.Fatalf("a 503 logged %d problems, want 1 (control)", len(got))
	}
}
