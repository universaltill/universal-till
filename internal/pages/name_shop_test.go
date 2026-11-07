package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4391: the "Name your shop" gate for an unnamed main till.

func TestSafeNameShopNext(t *testing.T) {
	cases := map[string]string{
		"/reports?x=1":          "/reports?x=1",
		"/":                     "/",
		"":                      "/settings",
		"//evil.example":        "/settings",
		"https://evil.example":  "/settings",
		"/\\evil":               "/settings",
		"\\\\evil":              "/settings",
		"reports":               "/settings",
		"/name-shop?next=/x":    "/settings",
		"/a\nSet-Cookie: x=y":   "/settings",
		"/a\x00b":               "/settings",
		"/a\\b":                 "/settings",
		"/name-shop":            "/settings",
		"http://evil.example/x": "/settings",
	}
	for in, want := range cases {
		if got := safeNameShopNext(in); got != want {
			t.Errorf("safeNameShopNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func gateReq(method, path string, hdr map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return req
}

func TestNameShopGateCandidate_Shapes(t *testing.T) {
	boosted := map[string]string{"HX-Request": "true", "HX-Boosted": "true", httpx.ShellNavHeader: "1"}
	type tc struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   bool
	}
	cases := []tc{
		{"document GET", "GET", "/reports", nil, true},
		{"query kept", "GET", "/reports?x=1", nil, true},
		{"POST", "POST", "/reports", nil, false},
		{"plain htmx fragment", "GET", "/reports", map[string]string{"HX-Request": "true"}, false},
		{"boosted shell nav", "GET", "/reports", boosted, true},
		{"image dest", "GET", "/reports", map[string]string{"Sec-Fetch-Dest": "image"}, false},
		{"Accept */*", "GET", "/reports", map[string]string{"Accept": "*/*"}, false},
		{"no Accept", "GET", "/reports", map[string]string{"Accept": ""}, false},
	}
	for _, p := range []string{"/", "/login", "/pin", "/help/display", "/self-order", "/api/pos/x",
		"/ui/shop-name-notice", "/public/app.js", "/kitchen-display/1", "/orders", "/tables",
		"/open-orders", "/refund/R1", "/plugin/x/y", "/setup", "/name-shop", "/settings/cloud/move/m1",
		"/o/abc", "/lock", "/logout"} {
		cases = append(cases, tc{"exempt " + p, "GET", p, nil, false})
		cases = append(cases, tc{"exempt boosted " + p, "GET", p, boosted, false})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nameShopGateCandidate(gateReq(c.method, c.path, c.hdr)); got != c.want {
				t.Fatalf("candidate = %v, want %v", got, c.want)
			}
		})
	}
}

// gateFixture builds the gate over a stub "PAGE" handler with real settings.
func gateFixture(t *testing.T, store, till string) (http.Handler, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if err := d.Settings.Set(ctx, common.KeyStoreName, store); err != nil {
		t.Fatal(err)
	}
	if till != "" {
		if err := d.Settings.Set(ctx, "till.name", till); err != nil {
			t.Fatal(err)
		}
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PAGE"))
	})
	return nameShopGate(stub, d), d
}

func serveGate(h http.Handler, req *http.Request, u *auth.User) *httptest.ResponseRecorder {
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNameShopGate_ManagerPlaceholderShopRedirects(t *testing.T) {
	for _, name := range []string{"", "My Store", "  my store ", "Universal Till store"} {
		t.Run(name, func(t *testing.T) {
			h, _ := gateFixture(t, name, "Front")
			rec := serveGate(h, gateReq("GET", "/reports", nil), &mgrUser)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/name-shop?next=%2Freports" {
				t.Fatalf("code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
			}
			rec = serveGate(h, gateReq("GET", "/reports?x=1", nil), &mgrUser)
			if rec.Header().Get("Location") != "/name-shop?next=%2Freports%3Fx%3D1" {
				t.Fatalf("query not kept: %q", rec.Header().Get("Location"))
			}
		})
	}
}

func TestNameShopGate_ManagerUnnamedTillRedirects(t *testing.T) {
	h, _ := gateFixture(t, "Corner Bakery", "")
	rec := serveGate(h, gateReq("GET", "/settings", nil), &mgrUser)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/name-shop?next=") {
		t.Fatalf("code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestNameShopGate_BothNamedPassesThrough(t *testing.T) {
	h, _ := gateFixture(t, "Corner Bakery", "Front")
	rec := serveGate(h, gateReq("GET", "/reports", nil), &mgrUser)
	if rec.Code != 200 || rec.Body.String() != "PAGE" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestNameShopGate_CashierPassesThrough(t *testing.T) {
	h, _ := gateFixture(t, "", "")
	rec := serveGate(h, gateReq("GET", "/reports", nil), &cashUser)
	if rec.Code != 200 || rec.Body.String() != "PAGE" {
		t.Fatalf("cashier must never be redirected: code=%d", rec.Code)
	}
}

func TestNameShopGate_NoSessionPassesThrough(t *testing.T) {
	h, _ := gateFixture(t, "", "")
	rec := serveGate(h, gateReq("GET", "/reports", nil), nil)
	if rec.Code != 200 || rec.Body.String() != "PAGE" {
		t.Fatalf("no session must pass through: code=%d", rec.Code)
	}
}

func TestNameShopGate_ReplicaNeverRedirects(t *testing.T) {
	h, d := gateFixture(t, "", "")
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main.local:8080"); err != nil {
		t.Fatal(err)
	}
	if !tillFollowsMain(t.Context(), d) {
		t.Skip("fixture does not make this till a replica")
	}
	rec := serveGate(h, gateReq("GET", "/reports", nil), &mgrUser)
	if rec.Code != 200 || rec.Body.String() != "PAGE" {
		t.Fatalf("a till that follows a main till never asks: code=%d", rec.Code)
	}
}

func TestNameShopGate_BoostedNavGetsHXRedirect(t *testing.T) {
	h, _ := gateFixture(t, "", "")
	req := gateReq("GET", "/reports", map[string]string{"HX-Request": "true", "HX-Boosted": "true", httpx.ShellNavHeader: "1"})
	rec := serveGate(h, req, &mgrUser)
	if rec.Code != 200 || rec.Header().Get("HX-Redirect") != "/name-shop?next=%2Freports" || rec.Header().Get("Location") != "" {
		t.Fatalf("code=%d HX-Redirect=%q Location=%q", rec.Code, rec.Header().Get("HX-Redirect"), rec.Header().Get("Location"))
	}
}

func TestNameShopGate_SaleScreenNeverRedirects(t *testing.T) {
	h, _ := gateFixture(t, "", "")
	for _, p := range []string{"/", "/tables", "/open-orders"} {
		if rec := serveGate(h, gateReq("GET", p, nil), &mgrUser); rec.Code != 200 {
			t.Fatalf("%s: code=%d, the sale flow must never be gated", p, rec.Code)
		}
	}
}

// --- the page ---

func nameShopMux(t *testing.T, store, till string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	_, _, d := newFullAuthDeps(t)
	t.Setenv("UT_AUTH", "on")
	ctx := t.Context()
	if err := d.Settings.Set(ctx, common.KeyStoreName, store); err != nil {
		t.Fatal(err)
	}
	if till != "" {
		if err := d.Settings.Set(ctx, "till.name", till); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	registerNameShop(mux, d)
	registerShopNameNoticeUI(mux, d)
	return mux, d
}

func TestNameShopPage_ShowsOnlyMissingFieldsEmpty(t *testing.T) {
	cases := []struct {
		name, store, till string
		shop, till_       bool
		heading           string
	}{
		{"both", "My Store", "", true, true, "Name your shop and this till"},
		{"shop only", "", "Front", true, false, "Name your shop"},
		{"till only", "Corner Bakery", "", false, true, "Name this till"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, _ := nameShopMux(t, c.store, c.till)
			rec := getWithUser(mux, "/name-shop?next=/reports", &mgrUser)
			if rec.Code != 200 {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if has := strings.Contains(body, `name="store_name"`); has != c.shop {
				t.Errorf("store field present=%v want %v", has, c.shop)
			}
			if has := strings.Contains(body, `name="till_name"`); has != c.till_ {
				t.Errorf("till field present=%v want %v", has, c.till_)
			}
			if !strings.Contains(body, "<h1>"+c.heading) {
				t.Errorf("heading %q missing", c.heading)
			}
			// The page's own card only: the base layout carries dialogs.
			card := body[strings.Index(body, `data-testid="name-shop"`):]
			card = card[:strings.Index(card, "</form>")]
			for _, bad := range []string{`value="My Store"`, `value="Till 1"`, "placeholder=", "<dialog", "showModal", " required"} {
				if strings.Contains(card, bad) {
					t.Errorf("page must not contain %q", bad)
				}
			}
			if !strings.Contains(body, `name="next" value="/reports"`) {
				t.Errorf("hidden next missing")
			}
			if !strings.Contains(body, `data-testid="name-shop-back"`) || !strings.Contains(body, `href="/"`) {
				t.Errorf("back to sale link missing")
			}
			if strings.Contains(body, "name_shop.") {
				t.Errorf("raw i18n key rendered")
			}
		})
	}
}

func TestNameShopPage_NothingMissingRedirectsToNext(t *testing.T) {
	mux, _ := nameShopMux(t, "Corner Bakery", "Front")
	rec := getWithUser(mux, "/name-shop?next=/reports", &mgrUser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reports" {
		t.Fatalf("code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestNameShopPage_CashierForbidden(t *testing.T) {
	mux, _ := nameShopMux(t, "", "")
	if rec := getWithUser(mux, "/name-shop", &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("GET code=%d want 403", rec.Code)
	}
	if rec := postForm(mux, "/name-shop", url.Values{"store_name": {"X Shop"}, "till_name": {"Front"}}, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("POST code=%d want 403", rec.Code)
	}
}

func countAudit(t *testing.T, d *common.Deps) int {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('store_name_changed','till_name_changed')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNameShopPost_RefusesLikeSettings(t *testing.T) {
	longShop := strings.Repeat("é", 81)
	longTill := strings.Repeat("é", 61)
	type tc struct {
		name, shop, till, wantShop, wantTill string
	}
	cases := []tc{
		{"blank shop", "", "Front", "settings.store_name.error_required", ""},
		{"placeholder shop", "My Store", "Front", "settings.store_name.error_required", ""},
		{"long shop", longShop, "Front", "settings.store_name.error_too_long", ""},
		{"bidi shop", "Shop‮", "Front", "settings.store_name.error_invalid_chars", ""},
		{"blank till", "Corner Bakery", "", "", "name_shop.error.till_name_invalid"},
		{"long till", "Corner Bakery", longTill, "", "name_shop.error.till_name_invalid"},
		{"tab till", "Corner Bakery", "a\tb", "", "name_shop.error.till_name_invalid"},
		{"taken till", "Corner Bakery", "other", "", "sync.error.name_taken"},
		{"both wrong", "", "", "settings.store_name.error_required", "name_shop.error.till_name_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, d := nameShopMux(t, "", "")
			if _, err := d.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('t9','Other','h9')`); err != nil {
				t.Fatal(err)
			}
			rec := postForm(mux, "/name-shop", url.Values{"next": {"/reports"}, "store_name": {c.shop}, "till_name": {c.till}}, &mgrUser)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code=%d want 422", rec.Code)
			}
			body := rec.Body.String()
			for _, k := range []string{c.wantShop, c.wantTill} {
				if k == "" {
					continue
				}
				want := template.HTMLEscapeString(httpx.T("en", k))
				if !strings.Contains(body, want) {
					t.Errorf("body lacks %q (%s)", want, k)
				}
			}
			if c.wantShop == "" && strings.Contains(body, `data-testid="name-shop-store-error"`) {
				t.Errorf("unexpected shop error")
			}
			if c.wantTill == "" && strings.Contains(body, `data-testid="name-shop-till-error"`) {
				t.Errorf("unexpected till error")
			}
			if v, _, _ := d.Settings.Get(t.Context(), common.KeyStoreName); v != "" {
				t.Errorf("store.name written on refusal: %q", v)
			}
			if v, _, _ := d.Settings.Get(t.Context(), "till.name"); v != "" {
				t.Errorf("till.name written on refusal: %q", v)
			}
			if n := countAudit(t, d); n != 0 {
				t.Errorf("%d audit rows on refusal", n)
			}
		})
	}
}

func TestNameShopPost_SavesAuditsAndRedirects(t *testing.T) {
	mux, d := nameShopMux(t, "", "")
	ctx := t.Context()
	rec := postForm(mux, "/name-shop", url.Values{"next": {"/reports?x=1"}, "store_name": {"  Corner Bakery "}, "till_name": {"Front counter"}}, &mgrUser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/reports?x=1" {
		t.Fatalf("code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(ctx, common.KeyStoreName); v != "Corner Bakery" {
		t.Errorf("store.name=%q", v)
	}
	if v, _, _ := d.Settings.Get(ctx, "till.name"); v != "Front counter" {
		t.Errorf("till.name=%q", v)
	}
	for action, frag := range map[string]string{
		"store_name_changed": `"new":"Corner Bakery"`,
		"till_name_changed":  `"key":"till.name"`,
	} {
		var actor, payload string
		if err := d.Db.QueryRow(`SELECT actor_id, data_json FROM audit_log WHERE action = ?`, action).Scan(&actor, &payload); err != nil {
			t.Fatalf("%s audit: %v", action, err)
		}
		if actor != "m1" || !strings.Contains(payload, frag) {
			t.Errorf("%s audit actor=%q payload=%s", action, actor, payload)
		}
	}
	// Banner gone, gate now passes.
	if body := getWithUser(mux, "/ui/shop-name-notice", &mgrUser).Body.String(); strings.TrimSpace(body) != "" {
		t.Errorf("banner still shown: %s", body)
	}
	if shop, till, err := missingShopNames(ctx, d); err != nil || shop || till {
		t.Errorf("still missing: shop=%v till=%v err=%v", shop, till, err)
	}
}

func TestNameShopPost_UnsafeNextFallsBackToSettings(t *testing.T) {
	mux, _ := nameShopMux(t, "", "")
	rec := postForm(mux, "/name-shop", url.Values{"next": {"//evil.example"}, "store_name": {"Corner Bakery"}, "till_name": {"Front"}}, &mgrUser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestShopNameNoticeUI_HiddenOnNameShopPage(t *testing.T) {
	mux := shopNameNoticeMux(t, "My Store")
	rec := getWithUserFromPage(mux, "/ui/shop-name-notice", &mgrUser, "/name-shop?next=%2Freports")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("banner must not show on /name-shop: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
