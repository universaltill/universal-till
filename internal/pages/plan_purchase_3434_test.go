package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ut-docs#3434: no plan purchase / call to action in the iOS app (App Store
// 3.1). These tests flip planPurchasePlatform, so none may run in parallel.

func withPlanPurchasePlatform(t *testing.T, goos string) {
	t.Helper()
	orig := planPurchasePlatform
	planPurchasePlatform = goos
	t.Cleanup(func() { planPurchasePlatform = orig })
}

func ppGet(mux *http.ServeMux, path string, u auth.User) (int, string) {
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestPlanPurchaseAllowedOn(t *testing.T) {
	if planPurchaseAllowedOn("ios") {
		t.Error("ios must not allow plan purchase")
	}
	for _, g := range []string{"linux", "windows", "darwin", "android"} {
		if !planPurchaseAllowedOn(g) {
			t.Errorf("%s must allow plan purchase", g)
		}
	}
}

func TestPlanPurchaseIOS_SettingsHasNoOpener(t *testing.T) {
	withPlanPurchasePlatform(t, "ios")
	mux, d := checkNowMux(t)
	_, body := ppGet(mux, "/settings", mgrUser)
	for _, bad := range []string{`subscription-renew-open`, `hx-post="/api/subscription/panel"`} {
		if strings.Contains(body, bad) {
			t.Fatalf("iOS lapsed Settings still has %s", bad)
		}
	}
	for _, want := range []string{`subscription-last-confirmed`, `data-testid="subscription-check-now"`, `data-testid="subscription-check-now-banner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("iOS Settings lost %s", want)
		}
	}
	reg := cardSection(t, body, "registration")
	for _, want := range []string{"notices by itself, usually within an hour", "Selling on this till is not affected"} {
		if !strings.Contains(reg, want) {
			t.Errorf("banner missing iOS wording %q:\n%s", want, reg)
		}
	}
	if strings.Contains(reg, "Renew it") || strings.Contains(strings.ToLower(reg), "renew it in") {
		t.Errorf("iOS banner still tells the reader to renew:\n%s", reg)
	}

	if err := d.Settings.SetMany(t.Context(), subscriptionSeed("active-fresh")); err != nil {
		t.Fatal(err)
	}
	_, body = ppGet(mux, "/settings", mgrUser)
	if strings.Contains(body, "subscription-renew-open") || strings.Contains(body, `hx-post="/api/subscription/panel"`) {
		t.Fatal("iOS active card still has an upgrade opener")
	}
	if !strings.Contains(body, `data-testid="subscription-renews"`) {
		t.Error("iOS active card lost the paid-until line")
	}

	if err := d.Settings.SetMany(t.Context(), map[string]string{
		entitlement.KeyPlan: "", entitlement.KeySubscriptionStatus: "", entitlement.KeyExpiresAt: "", entitlement.KeyLastConfirmedAt: "",
	}); err != nil {
		t.Fatal(err)
	}
	_, body = ppGet(mux, "/settings", mgrUser)
	if !strings.Contains(body, `data-testid="subscription-free-note"`) {
		t.Error("iOS free card lost the free-plan note")
	}
}

func TestPlanPurchaseIOS_ViaMainBanner(t *testing.T) {
	withPlanPurchasePlatform(t, "ios")
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if err := d.Settings.SetMany(ctx, map[string]string{
		"sync.primary_url":              "http://127.0.0.1:1",
		"marketplace.device_id":         "till-replica-3434",
		"marketplace.device_registered": "till-replica-3434",
	}); err != nil {
		t.Fatal(err)
	}
	enroll.Init(ctx, &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(t.Context(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	seedEntitlement(t, d, subscriptionSeed("lapsed"))
	body := getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(body, "press Check now on the main till") || !strings.Contains(body, "Selling on this till is not affected") {
		t.Fatalf("via-main iOS banner wrong:\n%s", cardSection(t, body, "registration"))
	}
	if strings.Contains(body, "renew") && strings.Contains(cardSection(t, body, "registration"), "renewal from the main till") {
		t.Fatal("via-main iOS banner still uses the renewal wording")
	}
}

func TestPlanPurchaseIOS_CheckNowNoOpenerNeutralResult(t *testing.T) {
	withPlanPurchasePlatform(t, "ios")
	for _, scope := range []string{"card", "banner"} {
		t.Run(scope, func(t *testing.T) {
			mux, d := checkNowMux(t)
			stubCheckin(t, d, "lapsed")
			rec := postForm(mux, "/api/subscription/check-now", url.Values{"scope": {scope}}, &mgrUser)
			body := rec.Body.String()
			if strings.Contains(body, "subscription-renew-open") || strings.Contains(body, "data-sub-panel-open") {
				t.Fatalf("iOS check-now (%s) re-rendered an opener: %s", scope, body)
			}
			if !strings.Contains(body, "The subscription is still not active.") {
				t.Fatalf("iOS check-now result not the neutral variant: %s", body)
			}
			if strings.Contains(body, "If you've just paid") || strings.Contains(body, "If you&#39;ve just paid") {
				t.Fatalf("iOS check-now still says to pay: %s", body)
			}
		})
	}
}

func TestPlanPurchaseIOS_ChipIsSettingsLink(t *testing.T) {
	withPlanPurchasePlatform(t, "ios")
	for _, seed := range []string{"lapsed", "stale"} {
		t.Run(seed, func(t *testing.T) {
			mux, d := newSubscriptionChipMux(t)
			seedEntitlement(t, d, subscriptionSeed(seed))
			if err := d.Settings.SetMany(t.Context(), map[string]string{"marketplace.store_id": "s", "marketplace.token": "t"}); err != nil {
				t.Fatal(err)
			}
			enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
			t.Cleanup(func() { enroll.Init(t.Context(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
			body := getSubscriptionChip(t, mux, "manager")
			for _, want := range []string{`href="/settings#subscription"`, `data-testid="sb-subscription"`} {
				if !strings.Contains(body, want) {
					t.Errorf("iOS chip missing %s: %s", want, body)
				}
			}
			if strings.Contains(body, "<button") || strings.Contains(body, "data-sub-panel-open") {
				t.Errorf("iOS chip is a panel button: %s", body)
			}
		})
	}
}

func TestPlanPurchaseIOS_PanelEndpointsRefuse(t *testing.T) {
	withPlanPurchasePlatform(t, "ios")
	mux, d := checkNowMux(t)
	stubCheckin(t, d, "lapsed")
	before, _, _ := d.Settings.Get(t.Context(), entitlement.KeyLastConfirmedAt)
	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/api/subscription/panel", url.Values{}},
		{"/api/subscription/panel/handoff", url.Values{"intent": {"renew"}}},
		{"/api/subscription/panel/handoff", url.Values{"intent": {"change"}}},
		{"/api/subscription/panel/poll", url.Values{"n": {"0"}}},
	} {
		rec := postForm(mux, tc.path, tc.form, &mgrUser)
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %v = %d, want 404", tc.path, tc.form, rec.Code)
		}
		for _, bad := range []string{"subscription-panel-choices", "subscription-panel-renew", "subscription-panel-choose",
			"subscription-panel-change", "subscription-panel-qr", "subscription-panel-url", "data:image/png", "/plans"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s leaked %s: %s", tc.path, bad, body)
			}
		}
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('subscription_panel','subscription_handoff')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("panel audit rows = %d (err %v), want 0", n, err)
	}
	if after, _, _ := d.Settings.Get(t.Context(), entitlement.KeyLastConfirmedAt); after != before {
		t.Fatal("a refused panel call still kicked a cloud check-in")
	}
}

func TestPlanPurchaseIOS_JoinErrorNoRenew(t *testing.T) {
	err := &joinError{kind: joinErrUnsentDayClosesRefused, detail: "2"}
	initAuthTestI18n(t)
	linux := friendlyJoinError("en", err)
	withPlanPurchasePlatform(t, "ios")
	ios := friendlyJoinError("en", err)
	if !strings.Contains(ios, "report retention") || strings.Contains(ios, "Renew") {
		t.Errorf("iOS join error = %q", ios)
	}
	if !strings.Contains(ios, "2") {
		t.Errorf("iOS join error lost its detail: %q", ios)
	}
	if !strings.Contains(linux, "Renew it") {
		t.Errorf("non-iOS join error changed: %q", linux)
	}
}

func TestPlanPurchaseNonIOS_Unchanged(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "darwin", "android"} {
		t.Run(goos, func(t *testing.T) {
			withPlanPurchasePlatform(t, goos)
			mux, d := checkNowMux(t)
			_, body := ppGet(mux, "/settings", mgrUser)
			if n := strings.Count(body, `data-testid="subscription-renew-open"`); n < 2 {
				t.Errorf("%d Renew openers, want >= 2", n)
			}
			if !strings.Contains(body, "Renew it in your Universal Till account") {
				t.Error("original banner fix text missing")
			}
			_, chip := ppGet(mux, "/ui/subscription-chip", mgrUser)
			if !strings.Contains(chip, "data-sub-panel-open") {
				t.Errorf("chip lost its panel opener: %s", chip)
			}
			stubCheckin(t, d, "lapsed")
			rec := postForm(mux, "/api/subscription/panel", url.Values{}, &mgrUser)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "subscription-panel-choices") {
				t.Errorf("panel = %d %s", rec.Code, rec.Body.String())
			}
			rec = postForm(mux, "/api/subscription/panel/handoff", url.Values{"intent": {"renew"}}, &mgrUser)
			if !strings.Contains(rec.Body.String(), "subscription-panel-qr") {
				t.Errorf("handoff has no QR: %s", rec.Body.String())
			}
		})
	}
}

// T8: a new plan-purchase opener must not appear outside the gate unnoticed.
func TestPlanPurchaseOpenersStayGated(t *testing.T) {
	chdirRoot(t)
	pin := map[string][2]int{ // file -> {data-sub-panel-open, /api/subscription/panel}
		"web/ui/partials/subscription_block.html": {1, 1},
		"web/ui/partials/subscription_chip.html":  {1, 1},
		"web/ui/layouts/base.html":                {2, 0}, // comment + delegated click selector
	}
	got := map[string][2]int{}
	scan := func(root string) {
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				if fi.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if ext := filepath.Ext(p); ext != ".html" && ext != ".js" {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			s := string(b)
			c := [2]int{strings.Count(s, "data-sub-panel-open"), strings.Count(s, "/api/subscription/panel")}
			p = filepath.ToSlash(p)
			if p == "web/ui/partials/subscription_panel.html" {
				return nil // reachable only through the three refused endpoints
			}
			if c != [2]int{} {
				got[p] = c
			}
			return nil
		})
	}
	scan("web/ui")
	scan("web/public")
	for p, c := range got {
		if pin[p] != c {
			t.Errorf("%s has %v opener counts, pinned %v: a new plan-purchase opener: gate it on planPurchaseAllowed (plan_purchase.go, ut-docs#3434) and update this pin", p, c, pin[p])
		}
	}
	for p := range pin {
		if _, ok := got[p]; !ok {
			t.Errorf("pinned opener file %s no longer has openers; update the pin", p)
		}
	}
	// The renew/change-plan partials are only entered through gated flags.
	blk, _ := os.ReadFile("web/ui/partials/subscription_block.html")
	if !regexp.MustCompile(`(?s)\{\{\s*if \.CanRenew\s*\}\}.*data-sub-panel-open`).Match(blk) {
		t.Error("subscription_block.html opener is not under .CanRenew")
	}
	chip, _ := os.ReadFile("web/ui/partials/subscription_chip.html")
	if !regexp.MustCompile(`(?s)\{\{\s*if \.openPanel\s*\}\}.*data-sub-panel-open`).Match(chip) {
		t.Error("subscription_chip.html opener is not under .openPanel")
	}

	// enroll.PlansURL is evaluated only by the (gated) panel handler.
	var users []string
	_ = filepath.Walk("internal", func(p string, fi os.FileInfo, err error) error {
		p = filepath.ToSlash(p)
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasPrefix(p, "internal/enroll/") {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), "enroll.PlansURL(") {
			users = append(users, p)
		}
		return nil
	})
	if len(users) != 1 || users[0] != "internal/pages/subscription_panel.go" {
		t.Errorf("enroll.PlansURL used from %v, want only internal/pages/subscription_panel.go", users)
	}
}

// topicArticle is the manual topic itself: the page around it carries the
// (closed, inert) #subscription-panel dialog from base.html on every page.
func topicArticle(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `id="manual-topic"`)
	if i < 0 {
		t.Fatal("no manual topic in the help page")
	}
	rest := page[i:]
	if j := strings.Index(rest, "</article>"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestPlanPurchaseIOS_HelpClaimHasNoPurchase(t *testing.T) {
	mux := helpMux(t)
	gone := []string{"Renew or change plan", "Choose a plan", "pay on your phone", "renew it in your Universal Till account"}

	withPlanPurchasePlatform(t, "linux")
	if body := topicArticle(t, get(t, mux, "/help/claim").Body.String()); !strings.Contains(body, "Renew or change plan") {
		t.Fatal("non-iOS help lost the Renew or change plan paragraph")
	}

	withPlanPurchasePlatform(t, "ios")
	body := topicArticle(t, get(t, mux, "/help/claim").Body.String())
	for _, g := range gone {
		if strings.Contains(body, g) {
			t.Errorf("iOS help claim still contains %q", g)
		}
	}
	for _, want := range []string{"Check now", "Cloud sync is part of the paid plans"} {
		if !strings.Contains(body, want) {
			t.Errorf("iOS help claim lost %q", want)
		}
	}
	if de := topicArticle(t, get(t, mux, "/help/claim?lang=de").Body.String()); strings.Contains(de, "Tarif verlängern oder ändern") {
		t.Error("iOS German help claim still has the purchase paragraph")
	}
	// Outside the marker block, no translation may still tell the user to
	// renew in their account or point at the (now absent) panel (UX gate).
	staleCTA := map[string][]string{
		"de": {"Universal-Till-Konto verlängern", "unten beschriebene Fenster", "Tarif verlängern oder ändern"},
		"tr": {"hesabınızdan yenileyene", "aşağıda anlatılan paneli"},
		"ar": {"تجدده من حسابك", "تفتح اللوحة الموضّحة أدناه"},
		"fa": {"در حساب Universal Till خود تمدید", "پنلی را که در ادامه"},
	}
	for lang, phrases := range staleCTA {
		art := topicArticle(t, get(t, mux, "/help/claim?lang="+lang).Body.String())
		if !strings.Contains(art, "Universal Till") {
			t.Fatalf("iOS %s help claim did not render the %s topic", lang, lang)
		}
		for _, p := range phrases {
			if strings.Contains(art, p) {
				t.Errorf("iOS %s help claim still contains %q", lang, p)
			}
		}
	}
	// "Escape" appears in claim only inside the purchase paragraph, so search
	// must stop finding the topic by it on iOS (and still find it elsewhere).
	if rs := get(t, mux, "/help/search?q=Escape").Body.String(); strings.Contains(rs, "/help/claim") {
		t.Errorf("iOS help search still finds claim by its purchase paragraph: %s", rs)
	}
	withPlanPurchasePlatform(t, "linux")
	if rs := get(t, mux, "/help/search?q=Escape").Body.String(); !strings.Contains(rs, "/help/claim") {
		t.Errorf("non-iOS help search no longer finds claim by its purchase paragraph: %s", rs)
	}
}
