package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ut-docs#4286: the Renew / Choose-a-plan panel. Step 1 refreshes from the
// cloud first (owner decision 2026-10-14: a plan the admin already renewed
// shows at once, with no QR); step 2 offers Renew / Choose a plan; step 3
// is a QR of the cloud's /plans redirect, re-checked every 20 s for up to
// 10 minutes.

func TestSubscriptionPanel_RefreshesFirstThenOffersChoices(t *testing.T) {
	mux, d := checkNowMux(t)
	before, _, _ := d.Settings.Get(t.Context(), entitlement.KeyLastConfirmedAt)
	stubCheckin(t, d, "lapsed")
	rec := postForm(mux, "/api/subscription/panel", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `data-testid="subscription-panel-choices"`) {
		t.Fatalf("panel on a lapsed till = %d %s, want the two choices", rec.Code, body)
	}
	// The stub only answers a kick: a new last_confirmed_at proves the
	// panel asked the cloud (and waited for it) before it answered.
	if after, _, _ := d.Settings.Get(t.Context(), entitlement.KeyLastConfirmedAt); after == before {
		t.Fatal("the panel answered without refreshing from the cloud first")
	}
	if strings.Contains(body, `data-testid="subscription-panel-offline"`) {
		t.Fatalf("an answered refresh reads as offline: %s", body)
	}
	for _, want := range []string{`data-testid="subscription-panel-renew"`, `data-testid="subscription-panel-choose"`, `{&#34;intent&#34;:&#34;renew&#34;}`, `{&#34;intent&#34;:&#34;change&#34;}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("choices missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "data:image/png") {
		t.Fatalf("step 2 already shows a QR: %s", body)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("still-lapsed panel HX-Trigger = %q, want none", got)
	}
}

func TestSubscriptionPanel_AlreadyRenewedShowsActiveWithoutQR(t *testing.T) {
	mux, d := checkNowMux(t)
	stubCheckin(t, d, "active")
	rec := postForm(mux, "/api/subscription/panel", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Your plan is active. Paid until") || !strings.Contains(body, `data-testid="subscription-panel-active"`) {
		t.Fatalf("panel after an admin renewal = %s, want the active result", body)
	}
	if strings.Contains(body, "data:image/png") || strings.Contains(body, `data-testid="subscription-panel-renew"`) {
		t.Fatalf("an active plan got a QR or the renew choice: %s", body)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "ut-subscription-changed" {
		t.Fatalf("renewed panel HX-Trigger = %q, want ut-subscription-changed", got)
	}
	// An active plan can still be changed (upgrade).
	if !strings.Contains(body, `data-testid="subscription-panel-change"`) {
		t.Fatalf("active result has no Change plan: %s", body)
	}
}

func TestSubscriptionPanel_OfflineStillOffersChoices(t *testing.T) {
	mux, _ := checkNowMux(t)
	rec := postForm(mux, "/api/subscription/panel", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="subscription-panel-offline"`) || !strings.Contains(body, `data-testid="subscription-panel-choices"`) {
		t.Fatalf("panel with no answer = %s, want the offline line and the choices", body)
	}
}

func TestSubscriptionPanel_NoPaidPlanOnlyChoose(t *testing.T) {
	mux, d := checkNowMux(t)
	if err := d.Settings.SetMany(t.Context(), map[string]string{entitlement.KeyPlan: "local", entitlement.KeySubscriptionStatus: "none"}); err != nil {
		t.Fatal(err)
	}
	// "back" skips the refresh: the operator just came from step 3.
	rec := postForm(mux, "/api/subscription/panel", url.Values{"back": {"1"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="subscription-panel-choose"`) || strings.Contains(body, `data-testid="subscription-panel-renew"`) {
		t.Fatalf("no paid plan: %s, want only Choose a plan", body)
	}
	if kicked(d.CloudSyncNow) {
		t.Fatal("Back re-ran the refresh")
	}
}

func TestSubscriptionPanel_HandoffQRAndExactURL(t *testing.T) {
	mux, d := checkNowMux(t)
	d.Cfg.Marketplace.StoreID = "s 1&<x>"
	rec := postForm(mux, "/api/subscription/panel/handoff", url.Values{"intent": {"renew"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `src="data:image/png;base64,`) {
		t.Fatalf("handoff has no QR image: %s", body)
	}
	wantURL := "http://127.0.0.1:1/plans?intent=renew&amp;lang=en-gb&amp;store=s&#43;1%26%3Cx%3E"
	if !strings.Contains(body, wantURL) {
		t.Fatalf("handoff URL text missing %s: %s", wantURL, body)
	}
	if strings.Contains(body, "<x>") {
		t.Fatalf("store id reached the page unescaped: %s", body)
	}
	// a11y (review of ut-docs#4286): the body is not a live region (a
	// whole QR step re-announced); each step's lead line is role="status".
	if !strings.Contains(body, `class="subscription-panel-lead" role="status"`) {
		t.Fatalf("handoff lead is not role=status: %s", body)
	}
	if base, err := os.ReadFile("web/ui/layouts/base.html"); err != nil || strings.Contains(string(base), `id="subscription-panel-body" class="subscription-panel-body" aria-live`) {
		t.Fatalf("#subscription-panel-body is still a live region (err %v)", err)
	}
	if !strings.Contains(body, `hx-trigger="every 20s"`) || !strings.Contains(body, `data-poll-count="0"`) {
		t.Fatalf("handoff does not start the 20 s re-check: %s", body)
	}
	// Unknown intent → change.
	rec = postForm(mux, "/api/subscription/panel/handoff", url.Values{"intent": {"x"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "intent=change") {
		t.Fatalf("unknown intent handoff = %s", rec.Body.String())
	}
}

func TestSubscriptionPanel_CashierRefused(t *testing.T) {
	mux, d := checkNowMux(t)
	for _, path := range []string{"/api/subscription/panel", "/api/subscription/panel/handoff"} {
		rec := postForm(mux, path, url.Values{"intent": {"renew"}}, &cashUser)
		assertElevationPrompt(t, path, rec.Code, rec.Body.String())
		if strings.Contains(rec.Body.String(), "data:image/png") {
			t.Fatalf("%s: a cashier got the QR", path)
		}
	}
	rec := postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"1"}}, &cashUser)
	if strings.Contains(rec.Body.String(), "every 20s") {
		t.Fatalf("a cashier's poll keeps polling: %s", rec.Body.String())
	}
	if kicked(d.CloudSyncNow) {
		t.Fatal("a cashier kicked a check-in")
	}
}

func TestSubscriptionPanel_UnregisteredExplains(t *testing.T) {
	mux, d := checkNowMux(t)
	d.Cfg.Marketplace.StoreID, d.Cfg.Marketplace.MerchantToken = "", ""
	for _, path := range []string{"/api/subscription/panel", "/api/subscription/panel/handoff"} {
		rec := postForm(mux, path, url.Values{"intent": {"renew"}}, &mgrUser)
		body := rec.Body.String()
		if !strings.Contains(body, "This till is not registered yet.") || strings.Contains(body, "data:image/png") {
			t.Fatalf("%s unregistered = %s, want the message and no QR", path, body)
		}
	}
	if kicked(d.CloudSyncNow) {
		t.Fatal("an unregistered till kicked a check-in")
	}
}

func TestSubscriptionPanel_PollKeepsCountingThenStops(t *testing.T) {
	mux, d := checkNowMux(t)
	rec := postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"3"}, "was": {"x"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `data-poll-count="4"`) || !strings.Contains(body, `hx-trigger="every 20s"`) {
		t.Fatalf("poll 4 = %s, want the next poller", body)
	}
	if !kicked(d.CloudSyncNow) {
		t.Fatal("a poll did not ask the cloud")
	}
	last := strconv.Itoa(subscriptionPanelPollMax - 1)
	rec = postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {last}}, &mgrUser)
	body = rec.Body.String()
	if strings.Contains(body, "every 20s") || !strings.Contains(body, `data-testid="subscription-panel-poll-stopped"`) {
		t.Fatalf("poll %d = %s, want polling stopped", subscriptionPanelPollMax, body)
	}
	// A forged count past the limit stops too, and never kicks.
	_ = kicked(d.CloudSyncNow)
	rec = postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"999"}}, &mgrUser)
	if strings.Contains(rec.Body.String(), "every 20s") || kicked(d.CloudSyncNow) {
		t.Fatalf("poll past the limit = %s", rec.Body.String())
	}
	// A missing, malformed or negative count is treated as spent — it must
	// never restart the 10-minute budget from zero.
	for _, n := range []string{"", "abc", "-1"} {
		rec = postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {n}}, &mgrUser)
		if strings.Contains(rec.Body.String(), "every 20s") || kicked(d.CloudSyncNow) {
			t.Fatalf("poll with n=%q = %s, want stopped", n, rec.Body.String())
		}
	}
	if subscriptionPanelPollMax*20 != 600 {
		t.Fatalf("poll limit %d × 20 s is not 10 minutes", subscriptionPanelPollMax)
	}
}

func TestSubscriptionPanel_PollFlipsToActive(t *testing.T) {
	mux, d := checkNowMux(t)
	was := subscriptionSignature(t.Context(), d.Settings)
	// Renewed while the QR was up.
	if err := d.Settings.SetMany(t.Context(), subscriptionSeed("active-fresh")); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"2"}, "was": {was}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="subscription-panel-active"`) {
		t.Fatalf("poll after renewal = %s, want the active result", body)
	}
	if rec.Header().Get("HX-Retarget") != "#subscription-panel-body" || rec.Header().Get("HX-Trigger") != "ut-subscription-changed" {
		t.Fatalf("poll after renewal headers = %v", rec.Header())
	}
	// An active plan that has not changed (an upgrade QR still open) keeps
	// polling instead of flipping straight back.
	now := subscriptionSignature(t.Context(), d.Settings)
	rec = postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"2"}, "was": {now}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), `data-poll-count="3"`) {
		t.Fatalf("unchanged active plan poll = %s, want to keep polling", rec.Body.String())
	}
}

// Tester-found (ut-docs#4286): the poll compared the cache before its own
// check-in landed, so a renewal showed only on the NEXT poll (~40 s driven,
// AC says ~20 s). The poll that asks the cloud must answer with what the
// cloud said.
func TestSubscriptionPanel_PollFlipsOnItsOwnCheckin(t *testing.T) {
	mux, d := checkNowMux(t)
	was := subscriptionSignature(t.Context(), d.Settings)
	stubCheckin(t, d, "active") // the cloud renewed; only a kick reveals it
	rec := postForm(mux, "/api/subscription/panel/poll", url.Values{"n": {"1"}, "was": {was}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), `data-testid="subscription-panel-active"`) {
		t.Fatalf("poll whose check-in found a renewal = %s, want the active result now, not on the next poll", rec.Body.String())
	}
}

// The panel's openers: a manager gets "Renew or change plan" on the lapsed
// Subscription card, the registration card's banner and the status-bar
// chip; the active card offers it too (upgrade). The panel itself is
// opened with .show(), never showModal().
func TestSubscriptionPanel_OpenersRenderGated(t *testing.T) {
	mux, d := checkNowMux(t)
	get := func(path string, u auth.User) (int, string) {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	_, body := get("/settings", mgrUser)
	if n := strings.Count(body, `data-testid="subscription-renew-open"`); n < 2 {
		t.Fatalf("manager on a lapsed till: %d Renew openers, want the card and the banner", n)
	}
	if !strings.Contains(body, `id="subscription-panel"`) || !strings.Contains(body, `id="subscription-panel-body"`) {
		t.Fatal("the page has no #subscription-panel")
	}
	_, chip := get("/ui/subscription-chip", mgrUser)
	if !strings.Contains(chip, `data-sub-panel-open`) || !strings.Contains(chip, `hx-post="/api/subscription/panel"`) {
		t.Fatalf("lapsed chip does not open the panel: %s", chip)
	}
	_, chip = get("/ui/subscription-chip", cashUser)
	if strings.TrimSpace(chip) != "" {
		t.Fatalf("a cashier got the chip: %s", chip)
	}
	if err := d.Settings.SetMany(t.Context(), subscriptionSeed("active-fresh")); err != nil {
		t.Fatal(err)
	}
	_, body = get("/settings", mgrUser)
	if !strings.Contains(body, `data-testid="subscription-renew-open"`) {
		t.Fatal("an active plan's card has no Renew or change plan (upgrade)")
	}

	// Opened with .show() in a fixed frame over #ut-scrim, never
	// showModal() (the status bar must stay reachable).
	base, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(base), `<dialog id="subscription-panel"`)
	if i < 0 {
		t.Fatal("base.html has no #subscription-panel dialog")
	}
	panel := string(base[i:])
	panel = panel[:strings.Index(panel, "</script>")]
	if strings.Contains(panel, ".showModal(") || !strings.Contains(panel, ".show()") {
		t.Fatalf("the panel is not opened with .show(): %s", panel)
	}
	partial, err := os.ReadFile("web/ui/partials/subscription_panel.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(partial), ".showModal(") {
		t.Fatal("subscription_panel.html calls showModal")
	}
}

// Review of ut-docs#4286: a cashier session elevated by a manager PIN gets
// the QR, but the 20 s poll has no PIN prompt and would stop 20 s later
// with "stopped checking". The handoff must not start a poller it cannot
// keep: it shows the stopped guidance instead of "checks every 20 seconds".
func TestSubscriptionPanel_ElevatedHandoffDoesNotPoll(t *testing.T) {
	mux, d := checkNowMux(t)
	seedDataAPIManager(t, d.Db)
	rec := postForm(mux, "/api/subscription/panel/handoff",
		url.Values{"intent": {"renew"}, "override_pin": {dataAPITestManagerPIN}}, &cashUser)
	body := rec.Body.String()
	if !strings.Contains(body, `src="data:image/png;base64,`) {
		t.Fatalf("elevated handoff has no QR: %s", body)
	}
	if strings.Contains(body, "every 20s") || strings.Contains(body, `data-testid="subscription-panel-poll"`) {
		t.Fatalf("elevated handoff starts a poller the cashier session cannot keep: %s", body)
	}
	if !strings.Contains(body, `data-testid="subscription-panel-poll-stopped"`) {
		t.Fatalf("elevated handoff lacks the stopped guidance: %s", body)
	}
	// The manager's own session still polls.
	rec = postForm(mux, "/api/subscription/panel/handoff", url.Values{"intent": {"renew"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "every 20s") {
		t.Fatalf("manager handoff stopped polling: %s", rec.Body.String())
	}
}

// Review of ut-docs#4286: every panel open, poll and Check now asks for an
// operator check-in. A server-side floor keeps a burst (several tabs, a
// double click, a forged poll loop) to one kick per refreshKickFloor.
func TestRefreshEntitlementNow_ThrottlesKicks(t *testing.T) {
	_, d := checkNowMux(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	prevClock := refreshClock
	refreshClock = func() time.Time { return now }
	t.Cleanup(func() { refreshClock = prevClock })

	_, _ = refreshEntitlementNow(t.Context(), d)
	if !kicked(d.CloudSyncNow) {
		t.Fatal("the first refresh did not kick a check-in")
	}
	now = now.Add(4 * time.Second)
	start := time.Now()
	out, err := refreshEntitlementNow(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if kicked(d.CloudSyncNow) {
		t.Fatal("a second refresh within 5 s kicked another check-in")
	}
	if out != refreshNotActive {
		t.Fatalf("throttled refresh on a lapsed cache = %v, want refreshNotActive", out)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("a throttled refresh waited for a check-in it never asked for")
	}
	if err := d.Settings.SetMany(t.Context(), subscriptionSeed("active-fresh")); err != nil {
		t.Fatal(err)
	}
	if out, _ := refreshEntitlementNow(t.Context(), d); out != refreshActive {
		t.Fatalf("throttled refresh on an active cache = %v, want refreshActive", out)
	}
	now = now.Add(time.Second) // 5 s after the first kick
	_, _ = refreshEntitlementNow(t.Context(), d)
	if !kicked(d.CloudSyncNow) {
		t.Fatal("a refresh 5 s after the last kick did not kick")
	}
}
