package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
)

// ut-docs#2569 (ADR-0060 §6): the subscription chip, the Settings →
// Subscription card and the paused banners, driven through the real
// handlers over a seeded settings KV.

// subscriptionSeed returns the four cached entitlement keys for each of the
// card's states (fresh / within grace / past grace / confirmed lapse), plus
// the free and unknown-plan cases.
func subscriptionSeed(state string) map[string]string {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	switch state {
	case "active-fresh":
		return map[string]string{
			entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "active",
			entitlement.KeyExpiresAt: "2026-10-27T00:00:00Z", entitlement.KeyLastConfirmedAt: at(-time.Minute),
		}
	case "active-grace":
		return map[string]string{
			entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "active",
			entitlement.KeyLastConfirmedAt: at(-entitlement.Grace + time.Hour),
		}
	case "stale":
		return map[string]string{
			entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "active",
			entitlement.KeyExpiresAt: "2026-10-27T00:00:00Z", entitlement.KeyLastConfirmedAt: at(-entitlement.Grace - time.Hour),
		}
	case "lapsed":
		return map[string]string{
			entitlement.KeyPlan: "shop", entitlement.KeySubscriptionStatus: "lapsed",
			entitlement.KeyLastConfirmedAt: at(-time.Minute),
		}
	case "unknown-plan":
		return map[string]string{
			entitlement.KeyPlan: "platinum", entitlement.KeySubscriptionStatus: "lapsed",
			entitlement.KeyLastConfirmedAt: at(-30 * 24 * time.Hour),
		}
	}
	return nil // free: nothing cached
}

func newSubscriptionChipMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	dp := newSyncPairingGateTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerSubscriptionUI(mux, dp)
	return mux, dp
}

func getSubscriptionChip(t *testing.T, mux *http.ServeMux, role string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ui/subscription-chip", nil)
	if role != "" {
		req = auth.WithUser(req, auth.User{ID: "u1", Role: role})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/subscription-chip as %q = %d, want 200 (%s)", role, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestSubscriptionChip_States(t *testing.T) {
	cases := []struct {
		seed     string
		wantChip string // "" = empty body
		wantText string
	}{
		{"free", "", ""},
		{"active-fresh", "", ""},
		{"active-grace", "", ""},
		{"stale", "stale", "Subscription not confirmed"},
		{"lapsed", "lapsed", "Subscription ended"},
		{"unknown-plan", "", ""}, // fails closed to free: no chip
	}
	for _, tc := range cases {
		t.Run(tc.seed, func(t *testing.T) {
			mux, dp := newSubscriptionChipMux(t)
			seedEntitlement(t, dp, subscriptionSeed(tc.seed))
			body := getSubscriptionChip(t, mux, "manager")
			if tc.wantChip == "" {
				if strings.TrimSpace(body) != "" {
					t.Fatalf("%s: want an empty body, got %q", tc.seed, body)
				}
				return
			}
			for _, want := range []string{
				`data-testid="sb-subscription"`,
				`data-sub-state="` + tc.wantChip + `"`,
				`href="/settings#subscription"`,
				tc.wantText,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("%s: chip missing %q:\n%s", tc.seed, want, body)
				}
			}
			if strings.Contains(body, `role="alert"`) {
				t.Fatalf("%s: a polled chip must not be role=alert:\n%s", tc.seed, body)
			}
		})
	}
}

// A cashier can't act on the chip (ut-docs#3079), and neither can a request
// with no session: both get the empty body, whatever the state.
func TestSubscriptionChip_CashierSeesNoChip(t *testing.T) {
	for _, seed := range []string{"stale", "lapsed"} {
		mux, dp := newSubscriptionChipMux(t)
		seedEntitlement(t, dp, subscriptionSeed(seed))
		for _, role := range []string{"cashier", ""} {
			if body := getSubscriptionChip(t, mux, role); strings.TrimSpace(body) != "" {
				t.Fatalf("%s as %q: want an empty body, got %q", seed, role, body)
			}
		}
		if body := getSubscriptionChip(t, mux, "admin"); !strings.Contains(body, `data-sub-state="`+seed+`"`) {
			t.Fatalf("%s as admin: want the chip, got %q", seed, body)
		}
	}
}

// cardSection returns the markup of the settings.html card with this id, up
// to the next card.
func cardSection(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `<div class="card" id="`+id+`"`)
	if start == -1 {
		return ""
	}
	rest := body[start+1:]
	if end := strings.Index(rest, `<div class="card" id="`); end != -1 {
		return body[start : start+1+end]
	}
	return body[start:]
}

func TestSettingsSubscriptionCard_States(t *testing.T) {
	cases := []struct {
		seed    string
		state   string
		want    []string
		notWant []string
	}{
		{"free", "free", []string{"Local (free)", `data-testid="subscription-free-note"`, "free on every plan"},
			[]string{`data-testid="subscription-paused"`, `data-testid="subscription-renews"`}},
		{"unknown-plan", "free", []string{"Local (free)", `data-testid="subscription-free-note"`},
			[]string{`data-testid="subscription-paused"`}},
		{"active-fresh", "active", []string{"Shop", "Active", `data-testid="subscription-renews"`, `data-testid="subscription-last-confirmed"`},
			[]string{`data-testid="subscription-paused"`, `data-testid="subscription-free-note"`}},
		{"active-grace", "active", []string{"Shop", "Active", `data-testid="subscription-last-confirmed"`},
			[]string{`data-testid="subscription-paused"`, `data-testid="subscription-renews"`}},
		{"stale", "stale", []string{"Subscription not confirmed", `data-testid="subscription-paused"`, "Some paid features are paused",
			`data-cap="cloud_sync"`, `data-cap="managed_tse"`, `data-cap="cloud_backup"`, "more than 7 days", "not affected"},
			[]string{`data-testid="subscription-renews"`, `data-cap="central_catalog"`}},
		{"lapsed", "lapsed", []string{"Ended", `data-testid="subscription-paused"`, "Paid features are paused", "no longer active", "Universal Till account", "not affected"},
			[]string{`data-testid="subscription-renews"`, `data-testid="subscription-free-note"`}},
	}
	for _, tc := range cases {
		t.Run(tc.seed, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			seedEntitlement(t, d, subscriptionSeed(tc.seed))
			card := cardSection(t, getSettingsAs(t, mux, mgrUser), "subscription")
			if card == "" {
				t.Fatal("no #subscription card for a manager")
			}
			if !strings.Contains(card, `data-sub-state="`+tc.state+`"`) {
				t.Fatalf("card state is not %q:\n%s", tc.state, card)
			}
			for _, w := range tc.want {
				if !strings.Contains(card, w) {
					t.Errorf("card missing %q:\n%s", w, card)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(card, w) {
					t.Errorf("card must not contain %q:\n%s", w, card)
				}
			}
		})
	}
}

// The renewal date goes through httpx.FormatDate: in fa it carries Persian
// digits, never the raw RFC3339 value.
func TestSettingsSubscriptionCard_RenewsDateIsLocalized(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedEntitlement(t, d, subscriptionSeed("active-fresh"))
	card := cardSection(t, getSettingsAs(t, mux, mgrUser), "subscription")
	if strings.Contains(card, "2026-10-27T") {
		t.Fatalf("raw expires_at leaked into the card:\n%s", card)
	}
	if !strings.Contains(card, "2026") {
		t.Fatalf("no formatted renewal date in the card:\n%s", card)
	}
}

// The banner names only the surface's own capabilities: #registration
// shows cloud sync and browser management (never managed TSE); with
// nothing of its own paused (a free shop) it shows nothing.
func TestSettingsSubscriptionBanner_RegistrationFiltered(t *testing.T) {
	for _, seed := range []string{"stale", "lapsed"} {
		mux, _, d := newFullAuthDeps(t)
		seedEntitlement(t, d, subscriptionSeed(seed))
		reg := cardSection(t, getSettingsAs(t, mux, mgrUser), "registration")
		if !strings.Contains(reg, `data-testid="subscription-paused"`) || !strings.Contains(reg, `data-sub-state="`+seed+`"`) {
			t.Fatalf("%s: #registration has no paused banner:\n%s", seed, reg)
		}
		for _, w := range []string{`data-cap="cloud_sync"`, `data-cap="browser_catalog"`} {
			if !strings.Contains(reg, w) {
				t.Errorf("%s: #registration banner missing %s", seed, w)
			}
		}
		for _, w := range []string{`data-cap="managed_tse"`, `data-cap="cloud_backup"`, `data-testid="subscription-tse-keeps-signing"`} {
			if strings.Contains(reg, w) {
				t.Errorf("%s: #registration banner must not show %s", seed, w)
			}
		}
		// Degrades in place: the card's own controls are still there.
		if !strings.Contains(reg, "/api/settings/auto-register") {
			t.Errorf("%s: #registration lost its own controls", seed)
		}
	}
	for _, seed := range []string{"free", "active-fresh"} {
		mux, _, d := newFullAuthDeps(t)
		seedEntitlement(t, d, subscriptionSeed(seed))
		if body := getSettingsAs(t, mux, mgrUser); strings.Contains(body, `data-testid="subscription-paused"`) {
			t.Fatalf("%s: no banner expected anywhere on /settings", seed)
		}
	}
}

// Beside the TSE provisioning block: managed TSE only, plus the ADR-0060 §7
// line that an already set-up TSE keeps signing.
func TestSettingsSubscriptionBanner_TSE(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedEntitlement(t, d, subscriptionSeed("lapsed"))
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "DE", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	body := getSettingsAs(t, mux, mgrUser)
	i := strings.Index(body, `data-testid="subscription-tse-keeps-signing"`)
	if i == -1 {
		t.Fatalf("no TSE keeps-signing line beside the TSE block:\n%s", body)
	}
	// The banner it sits in lists managed TSE only.
	start := strings.LastIndex(body[:i], `data-testid="subscription-paused"`)
	banner := body[start:i]
	if !strings.Contains(banner, `data-cap="managed_tse"`) || strings.Contains(banner, `data-cap="cloud_sync"`) {
		t.Fatalf("TSE banner must list managed_tse only:\n%s", banner)
	}
	if !strings.Contains(body[i:], `data-testid="tse-provisioning"`) {
		t.Fatal("the TSE provisioning block itself must still render after the banner")
	}
}

func TestSubscriptionViewPausedOnly(t *testing.T) {
	v := subscriptionView{Paused: []string{"browser_catalog", "cloud_backup", "cloud_sync", "managed_tse"}}
	if got := v.PausedOnly(""); len(got) != 4 {
		t.Fatalf("PausedOnly(\"\") = %v, want all", got)
	}
	if got := v.PausedOnly("cloud_sync browser_catalog"); len(got) != 2 || got[0] != "browser_catalog" || got[1] != "cloud_sync" {
		t.Fatalf("PausedOnly(registration) = %v", got)
	}
	if got := v.PausedOnly("central_catalog"); len(got) != 0 {
		t.Fatalf("PausedOnly(not paused) = %v, want none", got)
	}
}

// The Subscription card is manager-only, and so is its sidebar row.
func TestSettingsSubscriptionCard_NavRowFilteredForNonManager(t *testing.T) {
	for _, isManager := range []bool{true, false} {
		found := false
		for _, r := range filterSettingsNavForRender(settingsnav.Resolve("en", nil), isManager, true, true, true) {
			if r.Key == "subscription" {
				found = true
			}
		}
		if found != isManager {
			t.Fatalf("isManager=%v: subscription row present=%v", isManager, found)
		}
	}
}
