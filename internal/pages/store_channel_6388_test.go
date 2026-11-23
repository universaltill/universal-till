package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/manual"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/web"
)

// ut-docs#6388 / ADR-0194 §2.3: the install channel decides whether any plan
// purchase surface exists. Hide AND refuse. These tests flip the process-wide
// channel, so none may run in parallel.

var allChannels = []buildinfo.InstallChannel{
	buildinfo.ChannelSideload, buildinfo.ChannelPlay, buildinfo.ChannelAppStore, buildinfo.ChannelDirect,
}

func withChannel(t *testing.T, c buildinfo.InstallChannel) {
	t.Helper()
	t.Cleanup(buildinfo.OverrideChannelForTest(c))
}

func TestPlanPurchaseAllowed_ByChannel(t *testing.T) {
	want := map[buildinfo.InstallChannel]bool{
		buildinfo.ChannelSideload: true, buildinfo.ChannelPlay: false,
		buildinfo.ChannelAppStore: false, buildinfo.ChannelDirect: true,
	}
	for _, c := range allChannels {
		withChannel(t, c)
		if got := planPurchaseAllowed(); got != want[c] {
			t.Errorf("%s: planPurchaseAllowed = %v, want %v", c, got, want[c])
		}
		if got := accountLinkOutAllowed(); got != want[c] {
			t.Errorf("%s: accountLinkOutAllowed = %v, want %v", c, got, want[c])
		}
	}
	// iOS stays closed on any channel.
	withChannel(t, buildinfo.ChannelDirect)
	withPlanPurchasePlatform(t, "ios")
	if planPurchaseAllowed() || accountLinkOutAllowed() {
		t.Error("ios must stay closed even on the direct channel")
	}
}

func TestStoreChannel_PurchaseRoutesAndSurfaces(t *testing.T) {
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			open := c.PlanPurchaseAllowed()
			mux, d := checkNowMux(t)
			if err := d.Settings.SetMany(t.Context(), subscriptionSeed("active-fresh")); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"/api/subscription/panel", "/api/subscription/panel/handoff", "/api/subscription/panel/poll"} {
				rec := postForm(mux, p, url.Values{"intent": {"renew"}}, &mgrUser)
				if got404 := rec.Code == http.StatusNotFound; got404 == open {
					t.Errorf("%s on %s: status %d, 404 expected=%v", p, c, rec.Code, !open)
				}
			}
			_, body := ppGet(mux, "/settings", mgrUser)
			for _, marker := range []string{`subscription-renew-open`, `hx-post="/api/subscription/panel"`, `id="subscription-panel"`, `/plans`} {
				if has := strings.Contains(body, marker); has && !open {
					t.Errorf("%s Settings still renders %q", c, marker)
				}
			}
			// Plan status (name, paid-until) stays on every channel.
			if !strings.Contains(body, `data-testid="subscription-renews"`) {
				t.Errorf("%s Settings lost the paid-until line", c)
			}
		})
	}
}

func TestStoreChannel_UpdateRoutesAndCard(t *testing.T) {
	// Hard-coded on purpose: never derive this from InAppUpdateAllowed().
	wantOpen := map[buildinfo.InstallChannel]bool{
		buildinfo.ChannelDirect: true, buildinfo.ChannelSideload: true,
		buildinfo.ChannelPlay: false, buildinfo.ChannelAppStore: false,
	}
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			open := wantOpen[c]
			if got := c.InAppUpdateAllowed(); got != open {
				t.Fatalf("%s: InAppUpdateAllowed = %v, want %v", c, got, open)
			}
			mux, d := checkNowMux(t)
			registerUpdateAPI(mux, d)
			for _, r := range []struct{ method, path string }{
				{http.MethodPost, "/api/update/check"}, {http.MethodPost, "/api/update/apply"},
				{http.MethodPost, "/api/update/android-install"}, {http.MethodGet, "/api/update/notes"},
				{http.MethodGet, "/api/update/status"},
			} {
				var code int
				if r.method == http.MethodGet {
					code, _ = ppGet(mux, r.path, mgrUser)
				} else {
					code = postForm(mux, r.path, url.Values{}, &mgrUser).Code
				}
				if got404 := code == http.StatusNotFound; got404 == open {
					t.Errorf("%s %s on %s: status %d, 404 expected=%v", r.method, r.path, c, code, !open)
				}
			}
			_, body := ppGet(mux, "/settings", mgrUser)
			for _, marker := range []string{`/api/update/check`, `/api/settings/update-schedule`, `data-testid="android-update-banner"`} {
				if has := strings.Contains(body, marker); has && !open {
					t.Errorf("%s Settings still renders %q", c, marker)
				}
			}
			if !strings.Contains(body, `id="settings-update"`) {
				t.Errorf("%s Settings lost the version card", c)
			}
		})
	}
}

// ut-docs#6936: store builds say who updates the app, in place of the controls.
func TestStoreChannel_UpdateManagedByStoreBanner(t *testing.T) {
	const play, appStore = "Updates for this app come from Google Play", "Updates for this app come from the App Store"
	want := map[buildinfo.InstallChannel]string{
		buildinfo.ChannelPlay: play, buildinfo.ChannelAppStore: appStore,
		buildinfo.ChannelDirect: "", buildinfo.ChannelSideload: "",
	}
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			mux, d := checkNowMux(t)
			registerUpdateAPI(mux, d)
			_, body := ppGet(mux, "/settings", mgrUser)
			const marker = `data-testid="update-managed-by-store"`
			if want[c] == "" {
				for _, s := range []string{marker, play, appStore} {
					if strings.Contains(body, s) {
						t.Errorf("%s Settings renders %q", c, s)
					}
				}
				if !strings.Contains(body, `/api/update/check`) {
					t.Errorf("%s Settings lost the update check", c)
				}
				return
			}
			other := play
			if want[c] == play {
				other = appStore
			}
			if !strings.Contains(body, marker) || !strings.Contains(body, want[c]) {
				t.Errorf("%s Settings missing managed-by-store line %q", c, want[c])
			}
			if strings.Contains(body, other) {
				t.Errorf("%s Settings shows the other store's text", c)
			}
			if strings.Contains(body, `/api/update/check`) {
				t.Errorf("%s Settings still renders the update check", c)
			}
			if code := postForm(mux, "/api/update/check", url.Values{}, &mgrUser).Code; code != http.StatusNotFound {
				t.Errorf("%s POST /api/update/check = %d, want 404", c, code)
			}
		})
	}
}

// ut-docs#6401: on Play / App Store the claim card shows the code as plain
// text with instructions for another device. No link, no QR, no claim URL in
// any response of any handler.
func TestStoreChannel_ClaimCodeTextOnly(t *testing.T) {
	const claimPath = "/claim-path-6401"
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			linkOut := c.AccountLinkOutAllowed()
			mux, _, d := newFullAuthDeps(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
					"code": "ABCD-EFGH", "expires_at": "2099-01-01T00:00:00Z", "claim_path": claimPath,
				}})
			}))
			t.Cleanup(srv.Close)
			d.Cfg.Marketplace.EndpointURL = srv.URL
			d.Cfg.Marketplace.StoreID = "store-claim-6401"
			d.Cfg.Marketplace.MerchantToken = "tok-claim"

			body := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser).Body.String()
			if !strings.Contains(body, "ABCD-EFGH") {
				t.Fatalf("claim response lost the code: %s", body)
			}
			hasLink := strings.Contains(body, "<a ") || strings.Contains(body, "<img") || strings.Contains(body, claimPath) || strings.Contains(body, "http")
			if linkOut && !hasLink {
				t.Errorf("%s must keep the claim link and QR: %s", c, body)
			}
			if !linkOut {
				if hasLink {
					t.Errorf("%s claim response has a link, QR or URL: %s", c, body)
				}
				for _, want := range []string{"Your claim code:", "sign in to your Universal Till account on another device and enter this code"} {
					if !strings.Contains(body, want) {
						t.Errorf("%s claim card missing %q: %s", c, want, body)
					}
				}
				assertNoSteeringWords(t, string(c)+" claim response", body)
				if strings.Contains(body, "universaltill.com") { // domain not legally cleared on store builds (card #6401)
					t.Errorf("%s claim response names the cloud domain: %s", c, body)
				}
				// The Settings card's own help line is management wording too.
				_, page := ppGet(mux, "/settings", mgrUser)
				assertNoSteeringWords(t, string(c)+" claim help line", cardSection(t, page, "registration"))
			}
		})
	}
}

// steeringWords are what a store build may never say about buying a plan.
var steeringWords = []string{"buy", "purchase", "subscribe", "upgrade", "checkout", "price", "pricing", "renew", "choose a plan", "paid feature", "paid plan"}

func assertNoSteeringWords(t *testing.T, what, text string) {
	t.Helper()
	low := strings.ToLower(text)
	for _, w := range steeringWords {
		if strings.Contains(low, w) {
			t.Errorf("%s contains steering word %q", what, w)
		}
	}
}

// The help topic: the plan-purchase block (and the claim QR/link steps) are
// removed where purchase is not allowed; the claim topic then names no QR,
// link, price or plan-buying words.
func TestStoreChannel_HelpClaimTopic(t *testing.T) {
	lib, err := manual.Load(web.HelpFS, "help")
	if err != nil {
		t.Fatal(err)
	}
	topic, ok := lib.Topic("en", "claim")
	if !ok {
		t.Fatal("no claim topic")
	}
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			tp := topic
			if !planPurchaseAllowed() {
				tp = tp.WithoutPlanPurchase()
			}
			html := string(tp.HTML)
			if planPurchaseAllowed() {
				if !strings.Contains(html, "Choose a plan") || !strings.Contains(html, "QR") {
					t.Errorf("%s lost the purchase text", c)
				}
				return
			}
			for _, bad := range []string{"QR", "Choose a plan", "Renew or change plan", "scan it with your phone", "open the claim page"} {
				if strings.Contains(html, bad) {
					t.Errorf("%s help claim topic still says %q", c, bad)
				}
			}
			if !strings.Contains(html, "another device") {
				t.Errorf("%s help claim topic lost the other-device instruction", c)
			}
			// Plan STATUS text stays on every channel (ADR-0194 §2.3); what must
			// not appear is a call to buy, subscribe, upgrade or renew.
			low := strings.ToLower(html)
			for _, w := range []string{"buy", "purchase", "subscribe", "upgrade", "checkout", "choose a plan", "renew or change", "renew current", "renew it"} {
				if strings.Contains(low, w) {
					t.Errorf("%s claim help contains steering word %q", c, w)
				}
			}
		})
	}
}

// Legal record section 6: no paid badge on App Store or Play builds.
func TestStoreChannel_PaidBadge(t *testing.T) {
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			t.Setenv("UT_AUTH", "off") // gate: cashier_sale_only_test.go
			mux := newStoreDetailMux(t, marketplace.PluginSummary{ID: "7c1e2a90-4d3b", Name: "Card reader", Version: "2.4.0",
				CanonicalType: "payment", Vendor: "Acme", TrustTier: "unverified", PaidListing: true, Description: "x"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/7c1e2a90-4d3b", nil))
			if has := strings.Contains(rec.Body.String(), "paid-badge"); has != c.PlanPurchaseAllowed() {
				t.Errorf("%s: paid badge present=%v, want %v", c, has, c.PlanPurchaseAllowed())
			}
		})
	}
}

// ut-docs#6401 (review M2): the Support partner topic's link and QR lead into
// the account area (sign-in, then checkout). Hidden on play/appstore; the
// prose stays.
func TestStoreChannel_SupportPartnerLinkGated(t *testing.T) {
	for _, c := range allChannels {
		want := c == buildinfo.ChannelSideload || c == buildinfo.ChannelDirect // hard-coded, not derived
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			mux := helpMuxCfg(t, func(cfg *config.Config) {
				cfg.Marketplace.EndpointURL = "https://cloud.example.test/api"
				cfg.Marketplace.StoreID = "store-1"
				cfg.Marketplace.MerchantToken = "tok"
			})
			body := get(t, mux, "/help/support-partner").Body.String()
			if !strings.Contains(body, `data-topic="support-partner"`) {
				t.Fatal("topic not rendered")
			}
			for _, m := range []string{"help-partner-link", "help-partner-qr", "help-partner-url", "cloud.example.test"} {
				if has := strings.Contains(body, m); has != want {
					t.Errorf("%s: %q present=%v, want %v", c, m, has, want)
				}
			}
		})
	}
}

// Review M6: the Software update topic tells nobody on a store build to
// sideload: no APK, "Allow from this source", Play Protect or Update-now
// steps; it says the store updates the app. The Settings update and About
// cards do not link to it there.
func TestStoreChannel_HelpUpdatesTopic(t *testing.T) {
	lib, err := manual.Load(web.HelpFS, "help")
	if err != nil {
		t.Fatal(err)
	}
	topic, ok := lib.Topic("en", "updates")
	if !ok {
		t.Fatal("no updates topic")
	}
	sideloadWords := []string{"Allow from this source", "Play Protect", "Install unknown apps", "Update now", "Check now", "Download"}
	for _, c := range allChannels {
		store := c == buildinfo.ChannelPlay || c == buildinfo.ChannelAppStore // hard-coded
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			tp := topic
			if !planPurchaseAllowed() {
				tp = tp.WithoutPlanPurchase()
			}
			html := string(tp.HTML)
			for _, w := range sideloadWords {
				if has := strings.Contains(html, w); has == store {
					t.Errorf("%s: help text %q present=%v, want %v", c, w, has, !store)
				}
			}
			if store && !strings.Contains(html, "updated by the store") {
				t.Errorf("%s: store paragraph missing", c)
			}
			mux, _ := checkNowMux(t)
			_, page := ppGet(mux, "/settings", mgrUser)
			if has := strings.Contains(page, `/help/updates`); has == store {
				t.Errorf("%s: Settings links to the updates topic present=%v, want %v", c, has, !store)
			}
		})
	}
}

// Early review F1/F2/F3: content from the cloud and the terms link must not
// re-open a purchase surface on a store build.
func TestStoreChannel_NoticeLinksAndTermsURL(t *testing.T) {
	n := data.CloudNotice{
		Title: "Upgrade", Body: "Upgrade to Pro for £9.99. [Choose a plan](https://my.universaltill.com/billing/checkout)",
		LinkJSON: `{"kind":"https","target":"https://universaltill.com/en-gb/pricing/","label":"Buy now"}`,
	}
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			row := newNoticeRow(n, true, false, "all")
			hasLink := row.Link != nil
			bodyLink := strings.Contains(string(row.Body), "<a ") || strings.Contains(string(row.Body), "billing/checkout")
			if open := c.AccountLinkOutAllowed(); hasLink != open || bodyLink != open {
				t.Errorf("%s: link=%v bodyLink=%v, want both %v", c, hasLink, bodyLink, open)
			}
		})
	}
	for _, c := range allChannels {
		withChannel(t, c)
		if got, chromeless := termsLinkURL("en"), strings.Contains(termsLinkURL("en"), "/app/legal/"); chromeless == c.AccountLinkOutAllowed() {
			t.Errorf("%s terms link %s: chrome-less=%v", c, got, chromeless)
		}
	}
	// route links stay on store builds (only https is dropped)
	withChannel(t, buildinfo.ChannelPlay)
	rn := data.CloudNotice{Title: "t", Body: "b", LinkJSON: `{"kind":"route","target":"/settings","label":"Open"}`}
	if row := newNoticeRow(rn, true, false, "all"); row.Link == nil || row.LinkExternal {
		t.Errorf("route link lost on play: %+v", row.Link)
	}
}

// ut-docs#6622: a notice is free text from the cloud and its price/purchase
// pitch cannot be filtered on the till, so on a store channel the inbox stays
// off whatever notices.enabled says (ADR-0194 §3). Hide AND refuse.
func TestStoreChannel_NoticesStayOff(t *testing.T) {
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			open := c.AccountLinkOutAllowed()
			r := newNoticesRig(t, true) // notices.enabled = true
			r.seed(t, r.n("c", "critical"), r.n("i", "important"))

			rec := r.do("GET", "/notices", nMgr, nil)
			if got404 := rec.Code == http.StatusNotFound; got404 == open {
				t.Errorf("GET /notices on %s = %d, 404 expected=%v", c, rec.Code, !open)
			}
			for _, p := range []string{"/notices/bell", "/notices/strip", "/notices/toast"} {
				rec := r.do("GET", p, nMgr, nil)
				if rec.Code != 200 {
					t.Fatalf("%s on %s = %d", p, c, rec.Code)
				}
				if empty := strings.TrimSpace(rec.Body.String()) == ""; empty == open {
					t.Errorf("%s on %s: empty=%v, want %v", p, c, empty, !open)
				}
			}
			// turning it on is refused on a store channel; off still works
			rec = r.do("POST", "/api/settings/notices", nMgr, url.Values{"enabled": {"on"}})
			if got404 := rec.Code == http.StatusNotFound; got404 == open {
				t.Errorf("POST enable on %s = %d, 404 expected=%v", c, rec.Code, !open)
			}
			if !open {
				if rec := r.do("POST", "/api/settings/notices", nMgr, url.Values{}); rec.Code != http.StatusNoContent {
					t.Errorf("POST disable on %s = %d, want 204", c, rec.Code)
				}
				// the turn-off path still erases the held rows and clears the flag
				if p, err := r.repo.Pairs(t.Context()); err != nil || len(p) != 0 {
					t.Errorf("disable on %s: held notices = %v (err %v), want none", c, p, err)
				}
				if v, _, _ := data.NewSettingsRepo(r.db).Get(t.Context(), "notices.enabled"); v != "false" {
					t.Errorf("disable on %s: notices.enabled = %q, want false", c, v)
				}
			}
		})
	}
}

func TestStoreChannel_NoticesSettingsCard(t *testing.T) {
	for _, c := range allChannels {
		t.Run(string(c), func(t *testing.T) {
			withChannel(t, c)
			open := c.AccountLinkOutAllowed()
			mux, d := checkNowMux(t)
			if err := d.Settings.SetMany(t.Context(), map[string]string{"notices.enabled": "true"}); err != nil {
				t.Fatal(err)
			}
			_, body := ppGet(mux, "/settings", mgrUser)
			for _, marker := range []string{`id="settings-notices"`, `/api/settings/notices`} {
				if has := strings.Contains(body, marker); has != open {
					t.Errorf("%s Settings has %q = %v, want %v", c, marker, has, open)
				}
			}
		})
	}
}

func TestStoreChannel_NoticesNavRowHidden(t *testing.T) {
	for _, c := range allChannels {
		withChannel(t, c)
		has := false
		for _, r := range filterSettingsNavForRender(settingsnav.Resolve("en", nil), true, true, true, true, true) {
			has = has || r.Key == "settings-notices"
		}
		if want := c.AccountLinkOutAllowed(); has != want {
			t.Errorf("%s: settings-notices nav row present=%v, want %v", c, has, want)
		}
	}
}
