package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0148 amendment 2026-10-14 (ut-docs#4285): "Check now" on the lapsed
// banner and Settings → Subscription opens the operator window, waits a few
// seconds for that check-in and shows the result in place.

func checkNowMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	recordWindows(t)
	mux, _, d := newFullAuthDeps(t)
	registerSubscriptionUI(mux, d)
	registerForCheckPlan(d)
	d.CloudSyncNow = make(chan struct{}, 1)
	if err := d.Settings.SetMany(t.Context(), subscriptionSeed("lapsed")); err != nil {
		t.Fatal(err)
	}
	prevWait, prevPoll := refreshEntitlementWait, refreshEntitlementPoll
	refreshEntitlementWait, refreshEntitlementPoll = 600*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { refreshEntitlementWait, refreshEntitlementPoll = prevWait, prevPoll })
	return mux, d
}

// stubCheckin answers the kick the way the loop would: it rewrites the
// cache with status (and a fresh last_confirmed_at a second later, so the
// handler sees a change).
func stubCheckin(t *testing.T, d *common.Deps, status string) {
	t.Helper()
	go func() {
		select {
		case <-d.CloudSyncNow:
		case <-time.After(3 * time.Second):
			return
		}
		_ = d.Settings.SetMany(context.Background(), map[string]string{
			entitlement.KeyPlan:               "shop",
			entitlement.KeySubscriptionStatus: status,
			entitlement.KeyExpiresAt:          "2026-11-14T00:00:00Z",
			entitlement.KeyLastConfirmedAt:    time.Now().Add(time.Second).UTC().Format(time.RFC3339),
		})
	}()
}

func TestSubscriptionCheckNow_ManagerRenewed(t *testing.T) {
	mux, d := checkNowMux(t)
	stubCheckin(t, d, "active")
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Your plan is active. Paid until") {
		t.Fatalf("check-now = %d %s, want 200 with the renewed message", rec.Code, body)
	}
	if !strings.Contains(body, `data-testid="subscription-block"`) || !strings.Contains(body, `data-sub-state="active"`) {
		t.Fatalf("check-now did not render the refreshed block: %s", body)
	}
	// The status-bar chip and the registration card re-read on this event.
	if got := rec.Header().Get("HX-Trigger"); got != "ut-subscription-changed" {
		t.Fatalf("renewed check-now HX-Trigger = %q, want ut-subscription-changed", got)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='subscription_check_now'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("subscription_check_now audit rows = %d (err %v), want 1", n, err)
	}
}

func TestSubscriptionCheckNow_StillLapsed(t *testing.T) {
	mux, d := checkNowMux(t)
	stubCheckin(t, d, "lapsed")
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Still not renewed.") {
		t.Fatalf("check-now = %s, want the still-lapsed message", body)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("still-lapsed check-now HX-Trigger = %q, want none", got)
	}
	// Still lapsed: the button stays so the owner can try again.
	if !strings.Contains(body, `hx-post="/api/subscription/check-now"`) {
		t.Fatalf("still-lapsed result lost its Check now button: %s", body)
	}
}

func TestSubscriptionCheckNow_Timeout(t *testing.T) {
	mux, _ := checkNowMux(t)
	start := time.Now()
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "Couldn&#39;t reach Universal Till.") {
		t.Fatalf("check-now with no answer = %s, want the offline message", rec.Body.String())
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("check-now took %v, want it bounded by the wait", took)
	}
}

func TestSubscriptionCheckNow_BannerScope(t *testing.T) {
	mux, d := checkNowMux(t)
	stubCheckin(t, d, "lapsed")
	rec := postForm(mux, "/api/subscription/check-now", url.Values{"scope": {"banner"}, "only": {"cloud_sync browser_catalog <x>"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Still not renewed.") || !strings.Contains(body, `data-testid="subscription-paused"`) {
		t.Fatalf("banner scope = %s, want the result and the banner", body)
	}
	if strings.Contains(body, `data-testid="subscription-block"`) {
		t.Fatalf("banner scope rendered the whole Subscription card: %s", body)
	}
	if strings.Contains(body, "<x>") {
		t.Fatalf("an unknown capability was echoed: %s", body)
	}
}

func TestSubscriptionCheckNow_CashierRefused(t *testing.T) {
	mux, d := checkNowMux(t)
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &cashUser)
	assertElevationPrompt(t, "/api/subscription/check-now", rec.Code, rec.Body.String())
	if kicked(d.CloudSyncNow) {
		t.Fatal("a cashier's check-now kicked a check-in")
	}
}

func TestSubscriptionCheckNow_NotRegistered(t *testing.T) {
	mux, d := checkNowMux(t)
	d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "", ""
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "This till is not registered yet.") {
		t.Fatalf("unregistered check-now = %s", rec.Body.String())
	}
	if kicked(d.CloudSyncNow) {
		t.Fatal("an unregistered till kicked a check-in")
	}
}

// The button renders for a manager on the lapsed Subscription card and
// on the registration card's banner (two buttons); a cashier never sees it.
func TestSubscriptionCheckNow_ButtonOnlyForManager(t *testing.T) {
	mux, d := checkNowMux(t)
	if err := d.Settings.SetMany(t.Context(), map[string]string{
		"marketplace.store_id": "store-4285", "marketplace.token": "tok-4285",
	}); err != nil {
		t.Fatal(err)
	}
	get := func(u auth.User) (int, string) {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get(mgrUser)
	if code != http.StatusOK {
		t.Fatalf("GET /settings as manager = %d", code)
	}
	if n := strings.Count(body, `hx-post="/api/subscription/check-now"`); n < 1 {
		t.Fatalf("manager: %d Check now buttons on a lapsed till, want at least 1", n)
	}
	if !strings.Contains(body, `id="subscription-check"`) {
		t.Fatal("manager: the Subscription card has no #subscription-check target")
	}
	// One renew action on a lapsed till: the banner's Check now replaces
	// "Check for a paid plan", and the card re-reads after a renewal.
	if strings.Contains(body, `hx-post="/api/enrol/check-plan"`) {
		t.Fatal("manager: a lapsed till shows both Check now and Check for a paid plan")
	}
	if !strings.Contains(body, `hx-trigger="ut-subscription-changed from:body delay:4s"`) {
		t.Fatal("manager: the registration card does not re-read after a renewal")
	}
	// The re-read lives on a sibling: hx-select/hx-target on the card
	// itself are inherited by every htmx control inside it and blank them.
	if strings.Contains(body, `<div class="card" id="registration" hx-`) {
		t.Fatal("manager: htmx attributes on #registration are inherited by its children")
	}
	// A cashier never reaches the page that carries the button (ut-docs#3079:
	// /settings refuses a cashier outright); the endpoint's own gate is
	// TestSubscriptionCheckNow_CashierRefused.
	code, body = get(cashUser)
	if code != http.StatusForbidden {
		t.Fatalf("GET /settings as cashier = %d, want 403", code)
	}
	if strings.Contains(body, `hx-post="/api/subscription/check-now"`) {
		t.Fatal("a cashier sees the Check now button")
	}
}

// last_confirmed_at has one-second resolution: a check-in that lands in
// the same wall second as the one before it (the banner's Check now, then
// the card's, after a renewal) writes the same string. Check now must still
// see it as answered, not report "Couldn't reach Universal Till" over a
// renewal that landed (found in the tester's driven run, ut-docs#4285).
func TestSubscriptionCheckNow_SameSecondCheckinStillCounts(t *testing.T) {
	mux, d := checkNowMux(t)
	refreshEntitlementWait = 3 * time.Second
	// Start just past a second boundary so the seed, the click and the
	// check-in below all fall in one wall second without the fix.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 20*time.Millisecond)))
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339) }
	if err := d.Settings.Set(t.Context(), entitlement.KeyLastConfirmedAt, stamp()); err != nil {
		t.Fatal(err)
	}
	go func() {
		select {
		case <-d.CloudSyncNow:
		case <-time.After(3 * time.Second):
			return
		}
		_ = d.Settings.SetMany(context.Background(), map[string]string{
			entitlement.KeyPlan:               "shop",
			entitlement.KeySubscriptionStatus: "active",
			entitlement.KeyExpiresAt:          "2027-12-31T00:00:00Z",
			entitlement.KeyLastConfirmedAt:    stamp(),
		})
	}()
	rec := postForm(mux, "/api/subscription/check-now", url.Values{}, &mgrUser)
	if body := rec.Body.String(); !strings.Contains(body, "Your plan is active. Paid until") {
		t.Fatalf("a renewal landing in the same second as the last check-in = %s, want the renewed message", body)
	}
}
