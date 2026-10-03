package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0148 §2 (ut-docs#3615): an operator action on an unpaid till opens a
// short in-memory check-in window and kicks the loop, so the till learns a
// paid plan without a background poll.

// recordWindows swaps the window seam for a recorder; restored on cleanup.
func recordWindows(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var got []time.Duration
	prev := openOperatorWindow
	openOperatorWindow = func(d time.Duration) {
		mu.Lock()
		got = append(got, d)
		mu.Unlock()
	}
	t.Cleanup(func() { openOperatorWindow = prev })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), got...)
	}
}

func kicked(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestRequestOperatorCheckinOpensWindowAndKicks(t *testing.T) {
	windows := recordWindows(t)
	d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
	requestOperatorCheckin(d, 2*time.Minute)
	if w := windows(); len(w) != 1 || w[0] != 2*time.Minute {
		t.Fatalf("windows = %v, want [2m]", w)
	}
	if !kicked(d.CloudSyncNow) {
		t.Fatal("no check-in kick sent")
	}
	// A pending kick already queued: a second request must not block.
	d.CloudSyncNow <- struct{}{}
	done := make(chan struct{})
	go func() { requestOperatorCheckin(d, time.Minute); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("requestOperatorCheckin blocked on a full kick channel")
	}
	// nil-safe: no loop (bare deps) and nil deps.
	requestOperatorCheckin(&common.Deps{}, time.Minute)
	requestOperatorCheckin(nil, time.Minute)
}

func TestCheckinAfterRegistrationOnlyWhenRegistered(t *testing.T) {
	windows := recordWindows(t)
	d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
	checkinAfterRegistration(d, config.Config{})
	if len(windows()) != 0 || kicked(d.CloudSyncNow) {
		t.Fatal("an unregistered effective config opened a window")
	}
	var reg config.Config
	reg.Marketplace.EndpointURL, reg.Marketplace.StoreID, reg.Marketplace.MerchantToken = "http://cloud", "s1", "tok"
	checkinAfterRegistration(d, reg)
	if w := windows(); len(w) != 1 || w[0] != operatorCheckinWindow {
		t.Fatalf("windows = %v, want [%v]", w, operatorCheckinWindow)
	}
	if !kicked(d.CloudSyncNow) {
		t.Fatal("no kick after a successful registration")
	}
}

func TestEnrolCheckPlan_CashierGetsElevationPrompt(t *testing.T) {
	windows := recordWindows(t)
	mux, _, _ := newFullAuthDeps(t)
	rec := postForm(mux, "/api/enrol/check-plan", url.Values{}, &cashUser)
	assertElevationPrompt(t, "/api/enrol/check-plan", rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "#check-plan-msg") {
		t.Fatalf("prompt does not retry into #check-plan-msg: %s", rec.Body.String())
	}
	if len(windows()) != 0 {
		t.Fatal("a denied session opened a check-in window")
	}
}

// registerForCheckPlan gives d an explicitly configured (registered) cloud
// identity: the check-in needs endpoint, store and token.
func registerForCheckPlan(d *common.Deps) {
	d.Cfg.Marketplace.EndpointURL, d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "http://127.0.0.1:1/api", "s1", "tok"
}

func TestEnrolCheckPlan_ManagerOpensWindowAndKicks(t *testing.T) {
	windows := recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	registerForCheckPlan(d)
	d.CloudSyncNow = make(chan struct{}, 1)
	rec := postForm(mux, "/api/enrol/check-plan", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "✅") || !strings.Contains(body, "Checking with the cloud.") {
		t.Fatalf("check-plan = %d %s, want 200 with the sent message", rec.Code, body)
	}
	if w := windows(); len(w) != 1 || w[0] != 2*time.Minute {
		t.Fatalf("windows = %v, want [2m]", w)
	}
	if !kicked(d.CloudSyncNow) {
		t.Fatal("no check-in kick")
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='check_plan_requested'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("check_plan_requested audit rows = %d (err %v), want 1", n, err)
	}
}

func TestEnrolCheckPlan_ElevatedCashierAuditsDualAttribution(t *testing.T) {
	recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	registerForCheckPlan(d)
	mgrID, cashierID := seedElevationUsers(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}
	rec := postForm(mux, "/api/enrol/check-plan", url.Values{"override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("elevated check-plan = %d %s", rec.Code, rec.Body.String())
	}
	assertElevatedAudit(t, d, "check_plan_requested", mgrID, cashierID)
}

func TestEnrolPair_SuccessOpensWindow_FailureDoesNot(t *testing.T) {
	windows := recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	pairDeps(t, d)

	postForm(mux, "/api/enrol/pair", url.Values{"code": {"WRONG-123"}}, &mgrUser)
	if len(windows()) != 0 {
		t.Fatal("a refused pairing opened a check-in window")
	}
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✅") {
		t.Fatalf("pair failed: %s", rec.Body.String())
	}
	if w := windows(); len(w) != 1 || w[0] != 2*time.Minute {
		t.Fatalf("windows = %v, want [2m] after a successful pairing", w)
	}
}

func TestEnrolClaimCode_SuccessOpensFifteenMinuteWindow(t *testing.T) {
	windows := recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stores/claim-code" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"code": "ABCD-1234", "expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339), "claim_path": "/claim",
		}})
	}))
	t.Cleanup(srv.Close)
	d.Cfg.Marketplace.EndpointURL = srv.URL
	d.Cfg.Marketplace.StoreID = "store-claim-3615"
	d.Cfg.Marketplace.MerchantToken = "tok-claim"

	rec := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "ABCD-1234") {
		t.Fatalf("claim-code = %s, want the code", rec.Body.String())
	}
	if w := windows(); len(w) != 1 || w[0] != 15*time.Minute {
		t.Fatalf("windows = %v, want [15m] after a claim code", w)
	}
}

func TestEnrolClaimCode_FailureOpensNoWindow(t *testing.T) {
	windows := recordWindows(t)
	mux, _, _ := newFullAuthDeps(t)
	rec := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("unregistered claim-code = %s, want the error span", rec.Body.String())
	}
	if len(windows()) != 0 {
		t.Fatal("a failed claim code opened a check-in window")
	}
}

// Settings → Till registration says plainly that cloud sync is off, with a
// "Check for a paid plan" button, while the till is registered but not on a
// plan with cloud_sync; neither shows on a paid till.
func TestSettingsRegistrationCard_CloudSyncOffNotice(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	// A registered main till: store id + store token in its own settings.
	if err := d.Settings.SetMany(ctx, map[string]string{
		"marketplace.store_id": "store-3615", "marketplace.token": "tok-3615",
	}); err != nil {
		t.Fatal(err)
	}
	enroll.Init(ctx, &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	if !strings.Contains(body, "Cloud sync is off.") || !strings.Contains(body, `hx-post="/api/enrol/check-plan"`) ||
		!strings.Contains(body, `id="check-plan-msg"`) {
		t.Fatal("unpaid registered till: want the cloud-sync-off notice and the Check for a paid plan button")
	}
	// ADR-0148: an unpaid till makes no cloud calls on its own; the fleet
	// list is one (enroll.Fleet), so the card must not load it.
	if strings.Contains(body, `hx-get="/api/enrol/devices"`) {
		t.Fatal("unpaid registered till: the card still loads the fleet list from the cloud")
	}

	if err := d.Settings.SetMany(ctx, map[string]string{
		entitlement.KeyPlan:               "shop",
		entitlement.KeySubscriptionStatus: "active",
		entitlement.KeyLastConfirmedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	body = get()
	if strings.Contains(body, "Cloud sync is off.") || strings.Contains(body, `hx-post="/api/enrol/check-plan"`) {
		t.Fatal("paid till still shows the cloud-sync-off notice")
	}
	if !strings.Contains(body, `hx-get="/api/enrol/devices"`) {
		t.Fatal("paid till: the card no longer loads the fleet list")
	}
}

// A till registered through its main till has no cloud credential of its
// own, so it never checks in and its entitlement cache stays empty whatever
// the shop pays: the cloud-sync-off notice and its button must not appear in
// that branch of the card (ADR-0148, ut-docs#3615).
func TestSettingsRegistrationCard_NoCloudSyncNoticeOnReplicaViaMain(t *testing.T) {
	raw, err := os.ReadFile("web/ui/pages/settings.html") // TestMain chdirs to the repo root
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	start := strings.Index(s, "{{ else if enrolledviamain }}")
	if start < 0 {
		t.Fatal("settings.html: enrolledviamain branch not found")
	}
	end := strings.Index(s[start:], "{{ else }}")
	if end < 0 {
		t.Fatal("settings.html: end of enrolledviamain branch not found")
	}
	branch := s[start : start+end]
	if strings.Contains(branch, "check-plan") || strings.Contains(branch, "cloud_sync_off") {
		t.Fatal("enrolledviamain branch shows the cloud-sync-off notice; a via-main replica never checks in")
	}
}

// Review finding (ut-docs#3615): on a till that cannot check in itself —
// not registered, or a replica registered through its main till (no store
// token of its own) — "Check for a paid plan" must say so, not report
// "Checking with the cloud" for a check-in that never happens. No window,
// no kick, no audit row.
func TestEnrolCheckPlan_UnregisteredOrViaMainSaysNotRegistered(t *testing.T) {
	assertRefused := func(t *testing.T, d *common.Deps, body, want string) {
		t.Helper()
		if !strings.Contains(body, `class="error"`) || !strings.Contains(body, want) || strings.Contains(body, "Checking with the cloud.") {
			t.Fatalf("check-plan body = %s, want an error span with %q", body, want)
		}
		if kicked(d.CloudSyncNow) {
			t.Fatal("a till that cannot check in was kicked")
		}
		var n int
		if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='check_plan_requested'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("check_plan_requested audit rows = %d (err %v), want 0", n, err)
		}
	}
	t.Run("not registered", func(t *testing.T) {
		windows := recordWindows(t)
		mux, _, d := newFullAuthDeps(t)
		d.CloudSyncNow = make(chan struct{}, 1)
		rec := postForm(mux, "/api/enrol/check-plan", url.Values{}, &mgrUser)
		assertRefused(t, d, rec.Body.String(), "This till is not registered yet.")
		if len(windows()) != 0 {
			t.Fatal("an unregistered till opened a check-in window")
		}
	})
	t.Run("via main till", func(t *testing.T) {
		windows := recordWindows(t)
		mux, _, d := newFullAuthDeps(t)
		d.CloudSyncNow = make(chan struct{}, 1)
		ctx := t.Context()
		t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, emptyKV{}, &sync.WaitGroup{}) })
		for k, v := range map[string]string{
			"sync.primary_url":              "http://127.0.0.1:1",
			"marketplace.device_id":         "till-replica-own",
			"marketplace.device_registered": "till-replica-own",
		} {
			if err := d.Settings.Set(ctx, k, v); err != nil {
				t.Fatal(err)
			}
		}
		enroll.Init(ctx, &config.Config{}, d.Settings, &sync.WaitGroup{})
		if !enroll.CurrentStatus().ViaMainTill {
			t.Fatal("precondition: replica is not registered via its main till")
		}
		rec := postForm(mux, "/api/enrol/check-plan", url.Values{}, &mgrUser)
		assertRefused(t, d, rec.Body.String(), "This till is registered through the main till.")
		if len(windows()) != 0 {
			t.Fatal("a via-main replica opened a check-in window")
		}
	})
}
