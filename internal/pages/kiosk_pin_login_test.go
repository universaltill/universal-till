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
	// The live template flags are process-global; leave them as found.
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })

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

// ut-docs#2781: on a satellite till the kiosk's lock link still signs staff
// in, but never switches the till out of the self-order kiosk — the only
// profile a satellite may run. The person lands on Settings, signed in.
func TestKioskPINLogin_SatelliteStaysAKiosk(t *testing.T) {
	dp, _, h := kioskLoginHarness(t)
	if err := dp.Settings.Set(t.Context(), "sync.till_role", "satellite"); err != nil {
		t.Fatal(err)
	}
	if rec := kioskPostLogin(h, "9090", "", nil); rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("setup: kiosk login → %q", rec.Header().Get("Location"))
	}
	rec := kioskPostLogin(h, "4321", "kiosk", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("login = %d → %q, want 303 → /settings", rec.Code, rec.Header().Get("Location"))
	}
	if got := kioskDisplayMode(t, dp); got != "self_order" {
		t.Fatalf("display.mode = %q, want a satellite to stay self_order", got)
	}
	if c := kioskSessionCookie(rec); c == nil || c.Value == "" {
		t.Fatal("the manager must still be signed in")
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='kiosk_mode_exited'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("kiosk_mode_exited audit rows = %d (err %v), want 0", n, err)
	}
}
