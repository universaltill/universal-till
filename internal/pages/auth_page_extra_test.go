package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// auditCount returns how many audit_log rows carry the given action.
func auditCount(t *testing.T, db *sql.DB, action string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("count audit %q: %v", action, err)
	}
	return n
}

// seedOperator creates an active operator with a PIN and returns their id.
func seedOperator(t *testing.T, svc *auth.Service, username, role, pin string) string {
	t.Helper()
	id, err := svc.Repo().CreateUser(t.Context(), username, username, role)
	if err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	hash, err := auth.HashPIN(pin)
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	return id
}

// The device-wide lockout: five wrong keypad PINs lock further attempts, and the
// sixth is refused as locked-out (audited distinctly) even though the counter has
// rolled — no endpoint is a brute-force oracle.
func TestLoginLockoutAfterFailedAttempts(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	seedOperator(t, svc, "cash", "cashier", "4321")

	for i := 0; i < 5; i++ {
		rec := postForm(mux, "/api/auth/login", url.Values{"pin": {"0000"}}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: code=%d", i, rec.Code)
		}
	}
	// The sixth attempt is locked out — even the correct PIN is refused.
	rec := postForm(mux, "/api/auth/login", url.Values{"pin": {"4321"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("locked attempt: code=%d", rec.Code)
	}
	if got := auditCount(t, d.Db, "login_locked_out"); got != 1 {
		t.Fatalf("login_locked_out audit rows = %d, want 1", got)
	}
	if got := auditCount(t, d.Db, "login_failed"); got != 5 {
		t.Fatalf("login_failed audit rows = %d, want 5", got)
	}
	// No session was created for the locked-out attempt.
	if sessionCookie(rec) != "" {
		t.Fatal("a locked-out login must not set a session cookie")
	}
}

// GET /login short-circuits to "/" when a live session cookie is presented, so a
// signed-in operator never sees the keypad again.
func TestLoginRedirectsWhenAuthenticated(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)
	seedOperator(t, svc, "cash", "cashier", "4321")
	_, token, err := svc.Login(t.Context(), "4321")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("authenticated GET /login: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// Logout revokes the session, clears the cookie, and (for an HTMX lock button)
// answers 200 with an HX-Redirect rather than a 303.
func TestLogoutRevokesSessionAndClearsCookie(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	uid := seedOperator(t, svc, "cash", "cashier", "4321")
	_, token, err := svc.Login(t.Context(), "4321")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("htmx logout: code=%d hx=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	// Cookie cleared (MaxAge<0) and the session no longer resolves.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not expire the session cookie")
	}
	if _, ok := svc.Resolve(t.Context(), token); ok {
		t.Fatal("session still resolves after logout")
	}
	if got := auditCount(t, d.Db, "logout"); got != 1 {
		t.Fatalf("logout audit rows = %d, want 1 (actor %s)", got, uid)
	}

	// A plain (non-HTMX) logout lands on the keypad via a 303.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("plain logout: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// GET /pin gates on an authenticated operator; the page renders the change form.
func TestChangePINPageRequiresSession(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)
	uid := seedOperator(t, svc, "cash", "cashier", "4321")

	// No session → bounced to the keypad.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pin", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth /pin: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Signed in → the change-PIN form renders.
	rec = httptest.NewRecorder()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/pin", nil), auth.User{ID: uid, Role: "cashier", DisplayName: "cash"})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/api/pin/change") {
		t.Fatalf("auth /pin: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Self-service PIN change: mismatch and wrong current PIN are refused (with the
// localized error in the redirect); a valid change revokes the session and forces
// a re-login with the new PIN.
func TestChangeOwnPINFlow(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	uid := seedOperator(t, svc, "cash", "cashier", "4321")
	other := seedOperator(t, svc, "boss", "admin", "1234")
	u := auth.User{ID: uid, Role: "cashier", DisplayName: "cash"}

	post := func(form url.Values) *httptest.ResponseRecorder {
		return postForm(mux, "/api/pin/change", form, &u)
	}

	// Unauthenticated → login.
	if rec := postForm(mux, "/api/pin/change", url.Values{"new_pin": {"5678"}, "new_pin2": {"5678"}}, nil); rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauth pin change: loc=%q", rec.Header().Get("Location"))
	}

	// New PIN confirmation mismatch.
	if rec := post(url.Values{"current_pin": {"4321"}, "new_pin": {"5678"}, "new_pin2": {"9999"}}); rec.Header().Get("Location") != "/pin?err=auth.error.pin_mismatch" {
		t.Fatalf("mismatch: loc=%q", rec.Header().Get("Location"))
	}

	// Wrong current PIN.
	if rec := post(url.Values{"current_pin": {"0000"}, "new_pin": {"5678"}, "new_pin2": {"5678"}}); rec.Header().Get("Location") != "/pin?err=auth.error.invalid" {
		t.Fatalf("wrong current: loc=%q", rec.Header().Get("Location"))
	}

	// New PIN already owned by another operator.
	if rec := post(url.Values{"current_pin": {"4321"}, "new_pin": {"1234"}, "new_pin2": {"1234"}}); rec.Header().Get("Location") != "/pin?err=users.error.pin_taken" {
		t.Fatalf("pin taken: loc=%q", rec.Header().Get("Location"))
	}
	_ = other

	// Give a live session, then change the PIN successfully.
	_, token, err := svc.Login(t.Context(), "4321")
	if err != nil {
		t.Fatal(err)
	}
	rec := post(url.Values{"current_pin": {"4321"}, "new_pin": {"5678"}, "new_pin2": {"5678"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("pin change: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	// The changed credential revokes existing sessions.
	if _, ok := svc.Resolve(t.Context(), token); ok {
		t.Fatal("session survived own-PIN change")
	}
	// Old PIN no longer works; the new one does.
	if _, _, err := svc.Login(t.Context(), "4321"); err == nil {
		t.Fatal("old PIN still logs in after change")
	}
	if _, _, err := svc.Login(t.Context(), "5678"); err != nil {
		t.Fatalf("new PIN cannot log in: %v", err)
	}
	if got := auditCount(t, d.Db, "pin_changed"); got != 1 {
		t.Fatalf("pin_changed audit rows = %d, want 1", got)
	}
}

// The nav operator chip renders nothing without a session and the operator's
// name + manager admin links when signed in as a manager.
func TestSessionChip(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/session-chip", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("no-session chip: code=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/session-chip", nil), auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"})
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Mgr") || !strings.Contains(body, `href="/users"`) {
		t.Fatalf("manager chip: code=%d body=%s", rec.Code, body)
	}
}

// ensureFirstBootAdmin attaches the first-boot PIN to an existing active,
// non-system admin instead of creating a duplicate.
func TestFirstBootReusesExistingActiveAdmin(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)
	// A dormant-but-active admin with no PIN keeps the till in first-boot.
	existing, err := svc.Repo().CreateUser(t.Context(), "owner", "Owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := svc.Repo().ListUsers(t.Context())

	rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: code=%d", rec.Code)
	}
	after, _ := svc.Repo().ListUsers(t.Context())
	if len(after) != len(before) {
		t.Fatalf("first boot created a new user (%d→%d) instead of reusing the existing admin", len(before), len(after))
	}
	// The PIN attached to the pre-existing admin.
	u, _, _ := svc.FindUserByPIN(t.Context(), "2468")
	if u.ID != existing {
		t.Fatalf("PIN attached to %q, want existing admin %q", u.ID, existing)
	}
}

// ensureFirstBootAdmin reactivates a dormant user named "admin" (whose username
// would otherwise collide on create) rather than failing or duplicating.
func TestFirstBootReactivatesDormantAdmin(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	dormant, err := svc.Repo().CreateUser(t.Context(), "admin", "Administrator", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserActive(t.Context(), dormant, false); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.Repo().ListUsers(t.Context())

	rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	after, _ := svc.Repo().ListUsers(t.Context())
	if len(after) != len(before) {
		t.Fatalf("reactivation should not create a new user (%d→%d)", len(before), len(after))
	}
	row, _, _ := svc.Repo().GetUser(t.Context(), dormant)
	if !row.IsActive {
		t.Fatal("dormant admin was not reactivated")
	}
	if u, _, _ := svc.FindUserByPIN(t.Context(), "2468"); u.ID != dormant {
		t.Fatalf("PIN attached to %q, want reactivated %q", u.ID, dormant)
	}
	_ = d
}

// ut-docs#1718: /login must never become a dead end when the database is
// unreachable.
//
// The product owner hit this twice in one day on a real tablet: a poisoned
// SQLite connection made NeedsFirstBoot fail, renderLogin answered with a bare
// http.Error, and the WebView's whole document was replaced by the plain,
// untranslated text "auth unavailable" on a white screen — no rail, no Back,
// and on a pinned kiosk no Android navigation bar either. The keypad is the
// one screen that must never do this: it is the only way back into the till.
//
// The driver-level cause is fixed separately (internal/db's
// TestCancelledQueryNeverInterruptsAnotherGoroutine). This is the second half:
// however the read fails, the operator still gets a real page telling them
// what to do.
func TestLoginPageStillRendersWhenTheDatabaseIsUnreachable(t *testing.T) {
	mux, _, d := newAuthTestMux(t)

	// Kill the database under the handler. Any query now fails — which is
	// what a poisoned connection looked like in production, without needing
	// to reproduce the driver race here.
	if err := d.Db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the login page to still render (200), got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "auth unavailable") {
		t.Errorf("the bare untranslated http.Error text must be gone, got: %s", body)
	}
	// A real page, not a fragment: the operator needs the keypad in front of
	// them, with the explanation above it.
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("expected a full login document, got: %s", body)
	}
	// The message is rendered through the template's existing .errKey slot,
	// so it is translated like every other login error.
	if !strings.Contains(body, "login-error") {
		t.Errorf("expected the translated error to be rendered in the page, got: %s", body)
	}
	// It must NOT offer the one-time first-boot admin setup: we could not
	// prove this is a fresh till, and inviting someone to create an admin on
	// a configured one would be worse than the error itself.
	if strings.Contains(body, `name="confirm_pin"`) {
		t.Errorf("must not offer first-boot admin setup when the check itself failed, got: %s", body)
	}
}

// ut-docs#1718, second pass (independent review finding): GET /login was not
// the only dead end, and not the one an operator is most likely to meet.
//
// login.html submits the keypad as a PLAIN <form method="post"
// action="/api/auth/login"> — no htmx, no fetch — so a bare http.Error on
// that route replaces the entire WebView document exactly the way the GET
// path did. This is the literal reported sequence: the operator taps their
// PIN, submits, and the till goes white. guard-page-http-error.sh cannot see
// it, because it excludes /api/ routes as htmx fragments — correct for every
// other /api/ route here, wrong for this one.
func TestLoginPostStillRendersTheKeypadWhenTheDatabaseIsUnreachable(t *testing.T) {
	mux, _, d := newAuthTestMux(t)
	if err := d.Db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	form := url.Values{"pin": {"1234"}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the keypad rendered back (200), got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "login failed") {
		t.Errorf("the bare untranslated http.Error text must be gone, got: %s", body)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "login-error") {
		t.Errorf("expected a full login document carrying the translated error, got: %s", body)
	}
}

// The first-boot fallback form posts to /api/auth/setup from the same page.
// Its own opening guard already redirects to /login when NeedsFirstBoot
// errors, so a dead database never reaches a bare http.Error there — and now
// that /login itself renders properly (the test above), that redirect lands
// the operator somewhere useful rather than on a white page. Pinned here
// because the guarantee is a two-step one: the redirect is only safe while
// its destination is.
//
// The bare http.Error calls deeper in that handler (EnsureRegister,
// ensureFirstBootAdmin, SetUserPIN, CreateSession) were replaced too. They
// sit past this guard, so they need a genuinely fresh till whose database
// fails mid-setup — rare, but it is a till failing on its very first run,
// with nobody yet able to sign in and fix it.
func TestSetupPostRedirectsToAWorkingLoginWhenTheDatabaseIsUnreachable(t *testing.T) {
	mux, _, d := newAuthTestMux(t)
	if err := d.Db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	form := url.Values{"pin": {"4321"}, "pin_confirm": {"4321"}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect away from the setup POST, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("expected the redirect to land on /login, got %q", loc)
	}
	if strings.Contains(rec.Body.String(), "setup failed") {
		t.Errorf("the bare untranslated http.Error text must never appear, got: %s", rec.Body.String())
	}

	// And that destination must itself be a real page, not the old dead end
	// — otherwise this redirect just moves the white screen one hop.
	follow := httptest.NewRequest(http.MethodGet, "/login", nil)
	frec := httptest.NewRecorder()
	mux.ServeHTTP(frec, follow)
	if frec.Code != http.StatusOK || !strings.Contains(frec.Body.String(), "login-error") {
		t.Fatalf("the redirect target must render the explained login page, got %d: %s", frec.Code, frec.Body.String())
	}
}
