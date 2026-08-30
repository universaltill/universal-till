package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// Self-order kiosk mode (ADR-0020): setting display.mode=self_order sends
// "/" to the kiosk landing page for any authenticated visitor. Mirrors
// TestBackofficeModeRedirectsHome.
func TestSelfOrderModeRedirectsHome(t *testing.T) {
	chdirRoot(t)
	t.Setenv("UT_AUTH", "off")
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)

	home := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec
	}
	if rec := home(); rec.Code != http.StatusOK {
		t.Fatalf("register profile home = %d, want 200 sale screen", rec.Code)
	}

	form := strings.NewReader("mode=self_order")
	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set self_order: %d %s", rec.Code, rec.Body.String())
	}

	if rec := home(); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("self_order home = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}

	// The kiosk landing page itself renders.
	kioskRec := httptest.NewRecorder()
	mux.ServeHTTP(kioskRec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if kioskRec.Code != http.StatusOK {
		t.Fatalf("kiosk landing = %d: %s", kioskRec.Code, kioskRec.Body.String())
	}
	body := kioskRec.Body.String()
	for _, want := range []string{"Welcome!", "Tap to start"} {
		if !strings.Contains(body, want) {
			t.Fatalf("kiosk landing missing %q", want)
		}
	}

	// Back to register: the sale screen returns.
	req = httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=register"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set register: %d", rec.Code)
	}
	if rec := home(); rec.Code != http.StatusOK {
		t.Fatalf("register-again home = %d, want 200", rec.Code)
	}
}

// Unlike backoffice mode (which only redirects an already-manager session),
// self-order mode redirects EVERY authenticated session, including a plain
// cashier — a kiosk isn't meant to show the cashier screen to anyone by
// default once it's in self-order mode.
func TestSelfOrderModeRedirectsEverySession(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	// AuthSvc (ut-docs#710): display-mode is now canPerform()-gated, which
	// queries role_permissions for real via AuthSvc.Can() — seedForPages
	// already seeds it (manager/admin/super_admin granted "settings").
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)

	mgr := &auth.User{ID: "mgr-1", Role: "manager"}
	setRec := httptest.NewRecorder()
	setReq := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=self_order")), *mgr)
	setReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusNoContent {
		t.Fatalf("manager set self_order mode: %d %s", setRec.Code, setRec.Body.String())
	}

	home := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := home(mgr); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("manager home = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}
	if rec := home(cashier); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("cashier home on a self_order-mode till = %d → %q, want 303 → /self-order (unlike backoffice mode)", rec.Code, rec.Header().Get("Location"))
	}
}

// Setting display.mode=self_order is manager/admin gated, same as backoffice.
func TestDisplayModeSelfOrderRequiresManagerRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	// AuthSvc (ut-docs#710): see the comment in
	// TestSelfOrderModeRedirectsEverySession above.
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerSettings(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=self_order"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "cashier-1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// ut-docs#865: elevation prompt, not a flat 403.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier setting self_order mode = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
}

// The kiosk landing page itself is auth-exempt (internal/auth/middleware.go)
// and must render for a request with NO session at all — a real anonymous
// customer, not just an authenticated one who happened to get redirected.
func TestSelfOrderPage_ServesAnonymousRequest(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default", StoreName: "Task Runner Cafe"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	authSvc := auth.NewService(db)
	h := auth.Middleware(mux, authSvc)

	// No cookie, no auth.WithUser — a genuinely anonymous request through
	// the real middleware, not a bare mux.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous /self-order = %d, want 200 (must be auth-exempt)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Task Runner Cafe") {
		t.Errorf("kiosk landing missing shop name: %s", rec.Body.String())
	}
}

// ut-docs#208: the kiosk start screen has no way back to till settings
// today — a direct gap against universal-till/CLAUDE.md's offline-first
// "Status/lock/exit must always be reachable" rule. A discreet exit
// control must be present, linking to the existing /login flow (reusing
// the existing PIN-auth mechanism, not a new one).
func TestSelfOrderPage_HasPinGatedExitLink(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/login?next=kiosk"`) {
		t.Fatalf("self-order landing missing an exit link to /login?next=kiosk: %s", body)
	}
	if !strings.Contains(body, "selforder-exit") {
		t.Fatalf("self-order landing missing the discreet exit affordance styling: %s", body)
	}
}

// ut-docs#208 review finding: the markup-presence tests above prove the
// link exists, not that using it actually works — and on a self_order-mode
// till it didn't: "/" (what a bare /login redirects to on success)
// unconditionally bounces every session back to /self-order
// (registerIndex, and TestSelfOrderModeRedirectsEverySession above), so a
// manager who followed the exit link and entered a valid PIN landed right
// back on the anonymous kiosk screen, never reaching till settings. This
// drives the real round trip against a fully-migrated DB (real sessions
// table, real auth.Service) and asserts the actual acceptance criteria:
// valid PIN reaches the gated till surface, invalid PIN grants nothing.
func TestSelfOrderExit_PinLoginReachesTillSettingsNotKioskLoop(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO settings(key, value) VALUES ('display.mode', 'self_order')`); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(d.DB)
	mgrID, err := svc.Repo().CreateUser(t.Context(), "mgr", "Manager", "manager")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("4321")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), mgrID, hash); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerAuth(mux, dp, svc)
	h := auth.Middleware(mux, svc)

	// The exit link itself: GET /self-order/shop (covers browse/cart/
	// checkout) must point at /login?next=kiosk.
	kioskRec := httptest.NewRecorder()
	h.ServeHTTP(kioskRec, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	if !strings.Contains(kioskRec.Body.String(), `href="/login?next=kiosk"`) {
		t.Fatalf("kiosk shop screen exit link: %s", kioskRec.Body.String())
	}

	// An invalid PIN is rejected — no session cookie, no access granted.
	badRec := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"0000"}, "next": {"kiosk"}}.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusOK {
		t.Fatalf("invalid PIN: code=%d", badRec.Code)
	}
	for _, c := range badRec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			t.Fatalf("invalid PIN must not set a session cookie, got %q", c.Value)
		}
	}

	// A valid manager PIN via the kiosk exit must land on the real gated
	// till surface (/settings) — NOT loop back to /self-order.
	okRec := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"4321"}, "next": {"kiosk"}}.Encode()))
	okReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusSeeOther {
		t.Fatalf("valid PIN: code=%d body=%s", okRec.Code, okRec.Body.String())
	}
	if loc := okRec.Header().Get("Location"); loc != "/settings" {
		t.Fatalf("valid PIN via kiosk exit redirected to %q, want /settings (not looped back into the kiosk)", loc)
	}
	var cookie *http.Cookie
	for _, c := range okRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("valid PIN did not set a session cookie")
	}
	if u, ok := svc.Resolve(t.Context(), cookie.Value); !ok || u.ID != mgrID {
		t.Fatalf("session cookie does not resolve to the manager: %+v ok=%v", u, ok)
	}

	// Following the redirect: /settings itself must actually render for
	// that session (not itself bounce back into /self-order).
	settingsRec := httptest.NewRecorder()
	settingsReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	settingsReq.AddCookie(cookie)
	h.ServeHTTP(settingsRec, settingsReq)
	if settingsRec.Code != http.StatusOK {
		t.Fatalf("GET /settings with the new session = %d: %s", settingsRec.Code, settingsRec.Body.String())
	}
}

// ut-docs#1253: a customer at the self-order kiosk who taps the lock icon
// must always be met with a fresh PIN prompt, never walked straight into
// Settings — even when the browser this kiosk is running in still carries a
// perfectly valid session cookie. That's the real-world case this guards:
// display.mode=self_order here is set directly (a DB write, not through the
// POST /api/settings/display-mode handler ut-docs#1259 later taught to
// revoke its OWN caller's session — see TestSelfOrderMode_RevokesActingSessionOnEntry
// for that), and the session is minted AFTER that write — modelling any
// route to a live session on an already-self_order till that #1259's fix
// doesn't touch: a session minted via the kiosk's own PIN-gated exit link
// (the only sign-in route left reachable once #1259 also redirects a
// bare, session-less /login straight back to the kiosk), a sync-pulled
// setting change, or any other session that predates or never went
// through that one handler. Before the #1253
// fix, GET /login's own "already authenticated? skip the form" shortcut
// (meant for a plain register re-visiting /login) applied identically to
// next=kiosk, so the exit link's PIN gate was a no-op for as long as that
// staff session stayed alive — reported live on a real kiosk (ut-docs#1253).
func TestSelfOrderExit_ExistingSessionCookieStillRequiresPIN(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO settings(key, value) VALUES ('display.mode', 'self_order')`); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(d.DB)
	cashierID, err := svc.Repo().CreateUser(t.Context(), "cash", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), cashierID, hash); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerAuth(mux, dp, svc)
	h := auth.Middleware(mux, svc)

	// Mint a real, still-valid session the till's own browser is carrying —
	// e.g. left over from whoever put the till into self-order mode, never
	// having logged out. Not via the kiosk exit itself: this stands in for
	// any pre-existing session, exactly as a customer would find it.
	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"1234"}}.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(loginRec, loginReq)
	var cookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("setup: cashier login did not set a session cookie")
	}
	if _, ok := svc.Resolve(t.Context(), cookie.Value); !ok {
		t.Fatal("setup: session cookie does not resolve")
	}

	// The exploit path: same browser (same cookie) follows the kiosk's own
	// exit link.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?next=kiosk", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusSeeOther {
		t.Fatalf("existing session cookie let /login?next=kiosk skip straight to %q with no PIN prompt — must render the PIN form instead", rec.Header().Get("Location"))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login?next=kiosk with an existing session = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `action="/api/auth/login"`) {
		t.Fatalf("expected the PIN entry form, got: %s", rec.Body.String())
	}

	// And the PIN form that IS rendered must still actually require a PIN —
	// posting it (with next=kiosk, as the template does) reaches /settings
	// only via a fresh, correctly-authorized login, same as the no-existing-
	// cookie path already covered above.
	postRec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"1234"}, "next": {"kiosk"}}.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookie)
	h.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusSeeOther || postRec.Header().Get("Location") != "/settings" {
		t.Fatalf("PIN re-entry via the kiosk exit = %d → %q, want 303 → /settings", postRec.Code, postRec.Header().Get("Location"))
	}

	// And, once through, the session genuinely reaches the real till surface
	// for a plain operator too — not role-gated at the route (individual
	// mutating actions are, via canPerform/elevation; that's out of scope
	// for this ticket, which is about reaching the page at all, not what a
	// cashier can do once there).
	settingsRec := httptest.NewRecorder()
	settingsReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	var freshCookie *http.Cookie
	for _, c := range postRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			freshCookie = c
		}
	}
	if freshCookie == nil {
		t.Fatal("PIN re-entry via the kiosk exit did not set a fresh session cookie")
	}
	settingsReq.AddCookie(freshCookie)
	h.ServeHTTP(settingsRec, settingsReq)
	if settingsRec.Code != http.StatusOK {
		t.Fatalf("GET /settings with the re-entered session = %d: %s", settingsRec.Code, settingsRec.Body.String())
	}
}

// ut-docs#1259 (follow-up review finding on #1253's fix): #1253 closed every
// path reachable by TAPPING through the kiosk UI, but entering self-order
// mode never touched the acting browser's own session — so on a till where
// the OS chrome/URL bar is reachable (unlike a locked-down Pi kiosk), the
// manager/admin session that made the switch could still type /settings
// directly and land on it with zero PIN, because checkOrElevate returns
// allowed outright once canPerform passes for a live session. Setting
// display.mode=self_order must now revoke that acting session outright, not
// just re-route "/".
func TestSelfOrderMode_RevokesActingSessionOnEntry(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	svc := auth.NewService(d.DB)
	mgrID, err := svc.Repo().CreateUser(t.Context(), "mgr", "Manager", "manager")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("4321")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), mgrID, hash); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerAuth(mux, dp, svc)
	h := auth.Middleware(mux, svc)

	login := func(pin string) *http.Cookie {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(url.Values{"pin": {pin}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.CookieName {
				return c
			}
		}
		t.Fatalf("login with PIN %q did not set a session cookie (code=%d)", pin, rec.Code)
		return nil
	}

	// The manager logs in and switches the till into self-order mode —
	// this is the request whose OWN session must not survive it.
	cookie := login("4321")
	if _, ok := svc.Resolve(t.Context(), cookie.Value); !ok {
		t.Fatal("setup: manager session does not resolve before the switch")
	}

	setRec := httptest.NewRecorder()
	setReq := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=self_order"))
	setReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setReq.AddCookie(cookie)
	h.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusNoContent {
		t.Fatalf("manager set self_order mode: %d %s", setRec.Code, setRec.Body.String())
	}

	// The response must clear the browser's cookie, not just leave it be.
	var cleared bool
	for _, c := range setRec.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("entering self-order mode did not expire the acting browser's session cookie")
	}

	// htmx must be told to navigate this browser to the kiosk screen — the
	// 204 fast path already gets there via settings.html's own JS, but the
	// "elevated" (override-PIN) success path returns text/html and
	// deliberately does NOT navigate on its own (that guard is for a
	// genuine elevation PROMPT, a different, earlier return); without this
	// header an operator using an override PIN would be left staring at a
	// "✓ approved" page with an already-dead session underneath it.
	if got := setRec.Header().Get("HX-Redirect"); got != "/self-order" {
		t.Fatalf("HX-Redirect after entering self-order mode = %q, want /self-order", got)
	}

	// The revoke itself must be audited, same as a plain logout — silently
	// killing a session shouldn't be invisible in the audit trail.
	if got := auditCount(t, d.DB, "session_revoked_self_order"); got != 1 {
		t.Fatalf("session_revoked_self_order audit rows = %d, want 1", got)
	}

	// The token itself must be dead server-side too — not just the cookie
	// header on this one response — so presenting the same cookie value
	// again (e.g. a cached/replayed request) doesn't still work.
	if _, ok := svc.Resolve(t.Context(), cookie.Value); ok {
		t.Fatal("session still resolves after entering self-order mode")
	}

	// The till must land back on the kiosk, not a stranded PIN pad: this is
	// the till's own launch URL "/" (not auth-exempt, unlike /self-order
	// itself), visited by a browser that no longer carries ANY cookie —
	// modelling both the acting browser right after the switch (its cookie
	// was just cleared) and the kiosk shell reopening "/" after a restart,
	// since the revoke persists. Before this fix this stranded the till on
	// the bare PIN keypad with no way back to the kiosk short of logging in
	// — which would just recreate the very session this card revokes.
	homeRec := httptest.NewRecorder()
	h.ServeHTTP(homeRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if homeRec.Code != http.StatusSeeOther || homeRec.Header().Get("Location") != "/login" {
		t.Fatalf("GET / with no session on a self_order till = %d → %q, want 303 → /login (middleware's own gate)",
			homeRec.Code, homeRec.Header().Get("Location"))
	}
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if loginRec.Code != http.StatusSeeOther || loginRec.Header().Get("Location") != "/self-order" {
		t.Fatalf("GET /login with no session on a self_order till = %d → %q, want 303 → /self-order (not stranded on the PIN pad)",
			loginRec.Code, loginRec.Header().Get("Location"))
	}
	// The PIN-gated kiosk exit (ut-docs#1253) must still demand a fresh PIN
	// — this fix must not accidentally widen that exemption to next=kiosk.
	kioskExitRec := httptest.NewRecorder()
	h.ServeHTTP(kioskExitRec, httptest.NewRequest(http.MethodGet, "/login?next=kiosk", nil))
	if kioskExitRec.Code != http.StatusOK || !strings.Contains(kioskExitRec.Body.String(), `action="/api/auth/login"`) {
		t.Fatalf("GET /login?next=kiosk on a self_order till with no session = %d, want 200 with the PIN form: %s",
			kioskExitRec.Code, kioskExitRec.Body.String())
	}

	// And the acting browser's stale cookie genuinely can't reach an
	// authenticated page directly anymore — the actual customer-facing
	// threat this card describes, not just an internal bookkeeping check.
	settingsRec := httptest.NewRecorder()
	settingsReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	settingsReq.AddCookie(cookie)
	h.ServeHTTP(settingsRec, settingsReq)
	if settingsRec.Code == http.StatusOK {
		t.Fatalf("GET /settings with the revoked cookie = 200, want bounced to /login")
	}
	if settingsRec.Code != http.StatusSeeOther || settingsRec.Header().Get("Location") != "/login" {
		t.Fatalf("GET /settings with the revoked cookie = %d → %q, want 303 → /login",
			settingsRec.Code, settingsRec.Header().Get("Location"))
	}

	// The "back to register" flow must still work normally afterward: a
	// fresh login reaches the till, and switching back to register mode
	// neither errors nor revokes that new, unrelated session.
	fresh := login("4321")
	regRec := httptest.NewRecorder()
	regReq := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=register"))
	regReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	regReq.AddCookie(fresh)
	h.ServeHTTP(regRec, regReq)
	if regRec.Code != http.StatusNoContent {
		t.Fatalf("manager set register mode: %d %s", regRec.Code, regRec.Body.String())
	}
	for _, c := range regRec.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge < 0 {
			t.Fatal("switching back to register mode must not revoke the acting session")
		}
	}
	if _, ok := svc.Resolve(t.Context(), fresh.Value); !ok {
		t.Fatal("session was revoked by switching to register mode, want it to survive")
	}
}

// Regression guard for the convenience path this fix deliberately preserves
// (review finding on ut-docs#1253): a plain register — NOT the self-order
// kiosk — revisiting /login with a still-valid session must keep skipping
// straight past the PIN form. Only next=="kiosk" gets the always-prompt
// treatment; nothing else should.
func TestLogin_ExistingSessionStillSkipsFormWhenNotKioskExit(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	svc := auth.NewService(d.DB)
	cashierID, err := svc.Repo().CreateUser(t.Context(), "cash2", "Cashier Two", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("5678")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), cashierID, hash); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerAuth(mux, dp, svc)
	h := auth.Middleware(mux, svc)

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(url.Values{"pin": {"5678"}}.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(loginRec, loginReq)
	var cookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("setup: login did not set a session cookie")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("bare /login with an existing session = %d → %q, want 303 → / (unchanged convenience path)", rec.Code, rec.Header().Get("Location"))
	}
}
