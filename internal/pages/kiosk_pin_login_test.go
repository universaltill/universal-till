package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3136: the device mode follows who signs in. The seeded "kiosk"
// user's PIN puts the till into self-order kiosk mode (persisted, like the
// Settings switch); any other user's PIN on a self-order till puts it back
// into normal till mode and lands on the sale screen.

func kioskSetPIN(t *testing.T, svc *auth.Service, userID, pin string) {
	t.Helper()
	hash, err := auth.HashPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), userID, hash); err != nil {
		t.Fatal(err)
	}
}

func kioskLoginHarness(t *testing.T) (*common.Deps, *auth.Service, http.Handler) {
	t.Helper()
	dp, d := setupSelfOrderShopDeps(t)
	svc := auth.NewService(d.DB)
	dp.AuthSvc = svc
	kioskSetPIN(t, svc, "kiosk", "9090")
	mgrID, err := svc.Repo().CreateUser(t.Context(), "mgr", "Manager", "manager")
	if err != nil {
		t.Fatal(err)
	}
	kioskSetPIN(t, svc, mgrID, "4321")
	cashID, err := svc.Repo().CreateUser(t.Context(), "cash", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	kioskSetPIN(t, svc, cashID, "1234")
	// The anonymous-"/" seam pages.Init installs (ut-docs#1259).
	svc.SetAnonymousRootRedirect(func(ctx context.Context) string {
		if mode, _, _ := dp.Settings.Get(ctx, "display.mode"); mode == "self_order" {
			return "/self-order"
		}
		return ""
	})
	// The live display mode is process-global; leave it as found.
	t.Cleanup(func() { httpx.InitDisplayMode("") })

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	registerAuth(mux, dp, svc)
	return dp, svc, auth.Middleware(mux, svc)
}

func kioskPostLogin(h http.Handler, pin, next string, cookie *http.Cookie) *httptest.ResponseRecorder {
	form := url.Values{"pin": {pin}}
	if next != "" {
		form.Set("next", next)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func kioskSessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

func kioskDisplayMode(t *testing.T, dp *common.Deps) string {
	t.Helper()
	mode, _, err := dp.Settings.Get(t.Context(), "display.mode")
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func kioskAuditCount(t *testing.T, dp *common.Deps, action, actor string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action=? AND actor_id=?`, action, actor).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKioskPINLogin_EntersSelfOrderModeWithoutASession(t *testing.T) {
	dp, svc, h := kioskLoginHarness(t)

	// The browser still carries a staff session (whoever set the PIN up):
	// signing in as the kiosk must revoke it, exactly like the Settings
	// switch does (ut-docs#1259) — the screen is about to face customers.
	staff := kioskSessionCookie(kioskPostLogin(h, "4321", "", nil))
	if staff == nil || staff.Value == "" {
		t.Fatal("setup: manager login set no session cookie")
	}

	rec := kioskPostLogin(h, "9090", "", staff)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("kiosk PIN login = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}
	if got := kioskDisplayMode(t, dp); got != "self_order" {
		t.Fatalf("display.mode after kiosk login = %q, want self_order (persisted like the Settings switch)", got)
	}
	if c := kioskSessionCookie(rec); c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("kiosk login must clear the session cookie, got %+v", c)
	}
	if _, ok := svc.Resolve(t.Context(), staff.Value); ok {
		t.Fatal("the browser's previous staff session is still live after a kiosk login")
	}
	var live int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id='kiosk' AND revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("kiosk user has %d live sessions (err %v), want 0 — it must never hold one", live, err)
	}
	if kioskAuditCount(t, dp, "kiosk_mode_entered", "kiosk") != 1 {
		t.Fatal("kiosk login did not audit kiosk_mode_entered for the kiosk user")
	}

	// An anonymous "/" (every kiosk launcher opens "/") now lands on the kiosk.
	root := httptest.NewRecorder()
	h.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Header().Get("Location") != "/self-order" {
		t.Fatalf("anonymous / after kiosk login → %q, want /self-order", root.Header().Get("Location"))
	}
}

func TestKioskPINLogin_OtherUserRestoresTillMode(t *testing.T) {
	for _, tc := range []struct {
		name, pin, next string
	}{
		{"manager via kiosk exit", "4321", "kiosk"},
		{"cashier via kiosk exit", "1234", "kiosk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp, _, h := kioskLoginHarness(t)
			if rec := kioskPostLogin(h, "9090", "", nil); rec.Header().Get("Location") != "/self-order" {
				t.Fatalf("setup: kiosk login → %q", rec.Header().Get("Location"))
			}

			rec := kioskPostLogin(h, tc.pin, tc.next, nil)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
				t.Fatalf("login = %d → %q, want 303 → / (the sale screen)", rec.Code, rec.Header().Get("Location"))
			}
			if got := kioskDisplayMode(t, dp); got != "" {
				t.Fatalf("display.mode after %s = %q, want \"\" (normal till mode)", tc.name, got)
			}
			cookie := kioskSessionCookie(rec)
			if cookie == nil || cookie.Value == "" {
				t.Fatal("a non-kiosk login must still sign the operator in")
			}
			// Following the redirect shows the sale screen, not the kiosk.
			root := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(cookie)
			h.ServeHTTP(root, req)
			if root.Code != http.StatusOK {
				t.Fatalf("GET / after leaving kiosk mode = %d → %q, want the sale screen (200)", root.Code, root.Header().Get("Location"))
			}
			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='kiosk_mode_exited'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("kiosk_mode_exited audit rows = %d (err %v), want 1", n, err)
			}
		})
	}
}

// Only the kiosk's own lock link switches a self-order till back: a
// manager signing in from another browser (plain /login) leaves the kiosk
// running, and back-office stations and registers keep their mode.
func TestKioskPINLogin_OtherModesUntouchedByOrdinaryLogin(t *testing.T) {
	for _, mode := range []string{"", "backoffice", "self_order"} {
		t.Run("mode="+mode, func(t *testing.T) {
			dp, _, h := kioskLoginHarness(t)
			if err := dp.Settings.Set(t.Context(), "display.mode", mode); err != nil {
				t.Fatal(err)
			}
			rec := kioskPostLogin(h, "4321", "", nil)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
				t.Fatalf("login = %d → %q, want 303 → /", rec.Code, rec.Header().Get("Location"))
			}
			if got := kioskDisplayMode(t, dp); got != mode {
				t.Fatalf("display.mode = %q after an ordinary login, want it unchanged (%q)", got, mode)
			}
			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('kiosk_mode_exited','kiosk_mode_entered')`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("kiosk audit rows on an ordinary login = %d (err %v), want 0", n, err)
			}
		})
	}
}

// kioskJoinAs makes the harness's till a joined till with the given role,
// as the main till last reported it (till_role.go's ownTillRole). The main
// till's address is never dialled by the paths these tests drive.
func kioskJoinAs(t *testing.T, dp *common.Deps, role string) {
	t.Helper()
	for k, v := range map[string]string{
		"sync.primary_url": "https://192.0.2.1:8443",
		"sync.till_role":   role,
	} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// ut-docs#4295: on an additional joined till the kiosk user's PIN is not a
// way around Settings → Device profile, which asks first and tells the main
// till that this till becomes a satellite. Refused with that pointer:
// nothing changes, the kiosk user still holds no session, and the browser's
// staff session survives (the screen is not turning customer-facing).
func TestKioskPINLogin_AdditionalTillRefusedWithDeviceProfilePointer(t *testing.T) {
	dp, svc, h := kioskLoginHarness(t)
	initTillRoleI18n(t)
	kioskJoinAs(t, dp, "additional")
	staff := kioskSessionCookie(kioskPostLogin(h, "4321", "", nil))
	if staff == nil || staff.Value == "" {
		t.Fatal("setup: manager login set no session cookie")
	}

	rec := kioskPostLogin(h, "9090", "", staff)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("kiosk PIN on an additional till redirected to %q, want the login page with the refusal", rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "This till is an additional till") {
		t.Fatalf("kiosk PIN on an additional till: %d, body lacks the Device profile pointer:\n%s", rec.Code, rec.Body)
	}
	if got := kioskDisplayMode(t, dp); got != "" {
		t.Fatalf("display.mode = %q after a refused kiosk sign-in, want unchanged", got)
	}
	if got := ownTillRole(t.Context(), dp); got != "additional" {
		t.Fatalf("own role = %q, want additional", got)
	}
	if _, ok := svc.Resolve(t.Context(), staff.Value); !ok {
		t.Fatal("a refused kiosk sign-in revoked the browser's staff session")
	}
	var live int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id='kiosk' AND revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("kiosk user has %d live sessions (err %v), want 0", live, err)
	}
	if kioskAuditCount(t, dp, "kiosk_mode_entered", "kiosk") != 0 {
		t.Fatal("a refused kiosk sign-in audited kiosk_mode_entered")
	}
	if kioskAuditCount(t, dp, "kiosk_mode_refused", "kiosk") != 1 {
		t.Fatal("a refused kiosk sign-in left no kiosk_mode_refused audit row")
	}
}

// A satellite is a kiosk by role: the kiosk user's PIN still turns it into
// one (ut-docs#3136 unchanged there).
func TestKioskPINLogin_SatelliteStillEntersSelfOrder(t *testing.T) {
	dp, _, h := kioskLoginHarness(t)
	kioskJoinAs(t, dp, "satellite")
	rec := kioskPostLogin(h, "9090", "", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("kiosk PIN on a satellite = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}
	if got := kioskDisplayMode(t, dp); got != "self_order" {
		t.Fatalf("display.mode = %q, want self_order", got)
	}
}

// ut-docs#4307: a satellite can only be a kiosk, so staff signing in through
// the kiosk's own lock must not switch it to Register (its role would stay
// satellite on both sides). A manager still gets in — a maintenance sign-in
// that keeps the kiosk profile and lands on Settings, where Status and the
// lock are reachable as on every operator page.
func TestKioskPINLogin_SatelliteManagerSignInKeepsKioskAndReachesSettings(t *testing.T) {
	dp, svc, h := kioskLoginHarness(t)
	kioskJoinAs(t, dp, "satellite")
	if rec := kioskPostLogin(h, "9090", "", nil); rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("setup: kiosk login → %q", rec.Header().Get("Location"))
	}

	rec := kioskPostLogin(h, "4321", "kiosk", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("manager PIN via the kiosk lock on a satellite = %d → %q, want 303 → /settings", rec.Code, rec.Header().Get("Location"))
	}
	if got := kioskDisplayMode(t, dp); got != "self_order" {
		t.Fatalf("display.mode = %q after a manager sign-in on a satellite, want self_order (a satellite is only ever a kiosk)", got)
	}
	if got := ownTillRole(t.Context(), dp); got != "satellite" {
		t.Fatalf("own role = %q, want satellite", got)
	}
	if refusal := displayModeRoleRefusal(ownTillRole(t.Context(), dp), kioskDisplayMode(t, dp)); refusal != "" {
		t.Fatalf("role and mode disagree after the sign-in: %s", refusal)
	}
	cookie := kioskSessionCookie(rec)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("a manager must still be signed in on a satellite")
	}
	if _, ok := svc.Resolve(t.Context(), cookie.Value); !ok {
		t.Fatal("the manager's session is not live")
	}
	// Settings is reachable with that session (not bounced back to the
	// kiosk the way "/" is while the till is in self-order mode).
	settings := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(settings, req)
	if settings.Code != http.StatusOK {
		t.Fatalf("GET /settings after the maintenance sign-in = %d → %q, want 200", settings.Code, settings.Header().Get("Location"))
	}
	var exited, maint int
	if err := dp.Db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM audit_log WHERE action='kiosk_mode_exited'),
		(SELECT COUNT(*) FROM audit_log WHERE action='kiosk_maintenance_sign_in')`).Scan(&exited, &maint); err != nil {
		t.Fatal(err)
	}
	if exited != 0 || maint != 1 {
		t.Fatalf("audit rows: kiosk_mode_exited=%d kiosk_maintenance_sign_in=%d, want 0 and 1", exited, maint)
	}
}

// A cashier can't open Settings at all (ut-docs#3079) and can't take a
// satellite out of kiosk mode, so their PIN through the kiosk lock is
// refused with the satellite pointer: no session, mode and role unchanged,
// and the kiosk's own back-out on the sign-in screen still leads back.
func TestKioskPINLogin_SatelliteCashierRefusedWithSatellitePointer(t *testing.T) {
	dp, _, h := kioskLoginHarness(t)
	initTillRoleI18n(t)
	kioskJoinAs(t, dp, "satellite")
	if rec := kioskPostLogin(h, "9090", "", nil); rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("setup: kiosk login → %q", rec.Header().Get("Location"))
	}

	rec := kioskPostLogin(h, "1234", "kiosk", nil)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("cashier PIN via the kiosk lock on a satellite redirected to %q, want the sign-in page with the refusal", rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "This till is a satellite") {
		t.Fatalf("cashier PIN on a satellite: %d, body lacks the satellite pointer:\n%s", rec.Code, rec.Body)
	}
	if c := kioskSessionCookie(rec); c != nil && c.Value != "" {
		t.Fatal("a refused cashier sign-in set a session cookie")
	}
	var live int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id IN (SELECT id FROM users WHERE username='cash') AND revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("cashier has %d live sessions (err %v) after a refused sign-in, want 0", live, err)
	}
	if got := kioskDisplayMode(t, dp); got != "self_order" {
		t.Fatalf("display.mode = %q after a refused sign-in, want self_order", got)
	}
	var exited, refused int
	if err := dp.Db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM audit_log WHERE action='kiosk_mode_exited'),
		(SELECT COUNT(*) FROM audit_log WHERE action='kiosk_mode_refused')`).Scan(&exited, &refused); err != nil {
		t.Fatal(err)
	}
	if exited != 0 || refused != 1 {
		t.Fatalf("audit rows: kiosk_mode_exited=%d kiosk_mode_refused=%d, want 0 and 1", exited, refused)
	}
}

// ut-docs#4307 review finding: after a maintenance sign-in on a satellite,
// locking must hand the screen back to the kiosk, not strand it on the staff
// keypad — both the Lock button and the idle auto-lock (whose timer follows
// the X-UT-Lock-To hint, app.js). Elsewhere locking still shows the keypad.
func TestKioskPINLogin_SatelliteLockReturnsToKiosk(t *testing.T) {
	signIn := func(t *testing.T, role string) (*common.Deps, http.Handler, *http.Cookie) {
		t.Helper()
		dp, _, h := kioskLoginHarness(t)
		if role != "" {
			kioskJoinAs(t, dp, role)
		}
		if role == "satellite" {
			if rec := kioskPostLogin(h, "9090", "", nil); rec.Header().Get("Location") != "/self-order" {
				t.Fatalf("setup: kiosk login → %q", rec.Header().Get("Location"))
			}
			return dp, h, kioskSessionCookie(kioskPostLogin(h, "4321", "kiosk", nil))
		}
		return dp, h, kioskSessionCookie(kioskPostLogin(h, "4321", "", nil))
	}
	post := func(h http.Handler, path string, cookie *http.Cookie, htmx bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(cookie)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("satellite", func(t *testing.T) {
		_, h, cookie := signIn(t, "satellite")
		if rec := post(h, "/api/auth/logout", cookie, false); rec.Header().Get("Location") != "/" {
			t.Fatalf("Lock on a satellite kiosk → %q, want / (back to the kiosk)", rec.Header().Get("Location"))
		}
		root := httptest.NewRecorder()
		h.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
		if root.Header().Get("Location") != "/self-order" {
			t.Fatalf("anonymous / after the lock → %q, want /self-order", root.Header().Get("Location"))
		}
		_, h, cookie = signIn(t, "satellite")
		if rec := post(h, "/api/auth/logout", cookie, true); rec.Header().Get("HX-Redirect") != "/" {
			t.Fatalf("htmx Lock on a satellite kiosk → HX-Redirect %q, want /", rec.Header().Get("HX-Redirect"))
		}
		_, h2, cookie2 := signIn(t, "satellite")
		if rec := post(h2, "/api/auth/idle-lock", cookie2, false); rec.Code != http.StatusNoContent || rec.Header().Get("X-UT-Lock-To") != "/" {
			t.Fatalf("idle-lock on a satellite kiosk = %d, X-UT-Lock-To %q, want 204 and /", rec.Code, rec.Header().Get("X-UT-Lock-To"))
		}
	})
	for _, role := range []string{"", "additional"} {
		t.Run("role="+role, func(t *testing.T) {
			_, h, cookie := signIn(t, role)
			if rec := post(h, "/api/auth/logout", cookie, false); rec.Header().Get("Location") != "/login" {
				t.Fatalf("Lock → %q, want /login", rec.Header().Get("Location"))
			}
			_, h2, cookie2 := signIn(t, role)
			if rec := post(h2, "/api/auth/idle-lock", cookie2, false); rec.Header().Get("X-UT-Lock-To") != "" {
				t.Fatalf("idle-lock sent X-UT-Lock-To %q off a satellite kiosk, want none", rec.Header().Get("X-UT-Lock-To"))
			}
		})
	}
}
