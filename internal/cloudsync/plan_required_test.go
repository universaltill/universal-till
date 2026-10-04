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
	return `{"data":{"entitlement":` + planRequiredBlock + `},"error":{"code":"plan_required","message":"cloud sync needs an active Shop, Pro or Chain plan"}}`
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
