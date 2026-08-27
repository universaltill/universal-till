package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newAuthTestMux(t *testing.T) (*http.ServeMux, *auth.Service, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	// ut-docs#795: registerUsers' mutating handlers now render translated
	// inline messages (usersRespondError/usersRespondOK) instead of a bare
	// "/users?err=key" redirect, so this fixture needs a real translator
	// wired — same requirement newUsersTestDeps (users_page_test.go) already
	// has.
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
		 role TEXT NOT NULL DEFAULT 'cashier', pin_hash TEXT, is_active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
		 created_at TEXT NOT NULL DEFAULT (datetime('now')), expires_at TEXT NOT NULL, revoked_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL,
		 entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT)`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT,
		 is_active INTEGER NOT NULL DEFAULT 1)`,
		// ut-docs#672: registerSetup's language-detection branch writes
		// "setup.detected_lang_unavailable" via d.Settings.Set (setup_page.go)
		// when the OS-detected language isn't shipped — a nil Settings here
		// (this fixture's previous state) panics the whole test binary the
		// moment any /setup-touching test reaches that branch, mocked or via
		// a real unavailable CI locale. Same settings-table shape as
		// newFullAuthDeps (setup_page_test.go).
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`,
		// roles/permission_actions/role_permissions (039): registerUsers'
		// requireManager gates on canPerform(d, r, "user_management")
		// (ut-docs#556), not the old IsManager() bit, so this fixture needs
		// the real permission schema even though this file predates #554.
		`CREATE TABLE roles (role TEXT PRIMARY KEY)`,
		`CREATE TABLE permission_actions (action TEXT PRIMARY KEY)`,
		`CREATE TABLE role_permissions (role TEXT NOT NULL REFERENCES roles(role),
		 action TEXT NOT NULL REFERENCES permission_actions(action), granted INTEGER NOT NULL DEFAULT 0,
		 PRIMARY KEY (role, action))`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, role := range []string{"cashier", "manager", "admin", "super_admin"} {
		if _, err := db.Exec(`INSERT INTO roles (role) VALUES (?)`, role); err != nil {
			t.Fatalf("seed role %s: %v", role, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO permission_actions (action) VALUES ('user_management')`); err != nil {
		t.Fatalf("seed permission_action user_management: %v", err)
	}
	for _, role := range []string{"manager", "admin", "super_admin"} {
		if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, 'user_management', 1)`, role); err != nil {
			t.Fatalf("seed role_permission %s/user_management: %v", role, err)
		}
	}
	// country_settings (ut-docs#660): registerSetup below wires GET /setup,
	// and wizardCountries now queries this table on every render — see
	// seedCountrySettingsTable's own comment (setup_page_test.go) for why
	// this reads the real migration file rather than hand-rolling the DDL.
	seedCountrySettingsTable(t, db)
	svc := auth.NewService(db)
	store := settings.NewStore(db)
	d := &common.Deps{Db: db, Settings: store, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: svc}
	mux := http.NewServeMux()
	registerAuth(mux, d, svc)
	registerUsers(mux, d, svc)
	registerSetup(mux, d, svc)
	return mux, svc, d
}

func postForm(mux *http.ServeMux, path string, form url.Values, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#672: GET /setup's "language detected but unavailable" branch
// (setup_page.go's d.Settings.Set("setup.detected_lang_unavailable", ...))
// was only exercised, in this package, via newFullAuthDeps (a real
// Settings store). newAuthTestMux's minimal fixture leaves Settings nil,
// so a request that reaches this branch through THIS fixture panics the
// whole test binary — a real risk once ut-docs#662's OS-locale CI step
// (currently only en_GB.UTF-8, always "available") is broadened to include
// an unavailable-language locale, since any /setup-touching test here that
// doesn't explicitly mock detection via withOSLocale would then pick up
// the real CI env and hit this path for the first time.
func TestFirstBootSetupUnavailableLanguageDoesNotPanic(t *testing.T) {
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin") // "de" isn't shipped (web/locales has ar/en/fa/tr only)
	mux, _, d := newAuthTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup (de_DE, unavailable): code=%d, want 200 (no available-language redirect)", rec.Code)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), "setup.detected_lang_unavailable"); !ok || v != "de" {
		t.Errorf("setup.detected_lang_unavailable = %q ok=%v, want \"de\"", v, ok)
	}
}

func TestFirstBootSetupThenLogin(t *testing.T) {
	// ut-docs#662: hermetic against the developer machine's real OS locale —
	// otherwise ut-docs#590's /setup detection redirect fires on any machine
	// with a locale set and this test never even reaches the wizard.
	withOSLocale(t, "", "")
	mux, svc, d := newAuthTestMux(t)

	// Fresh DB: /login redirects to the guided setup wizard.
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("first-boot /login should redirect to /setup: code=%d loc=%s", rec.Code, rec.Header().Get("Location"))
	}

	// The wizard page renders and posts to /api/setup.
	req = httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/api/setup") {
		t.Fatalf("first-boot wizard: code=%d", rec.Code)
	}

	// Mismatched PINs are refused.
	rec = postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"8642"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/api/auth/setup") {
		t.Fatalf("mismatch should re-render setup: code=%d", rec.Code)
	}

	// Setup creates the admin, signs in, sets the session cookie.
	rec = postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("setup did not set a session cookie")
	}
	if u, ok := svc.Resolve(t.Context(), cookie); !ok || u.Role != "admin" {
		t.Fatalf("setup session resolves to %+v ok=%v", u, ok)
	}

	// ut-docs#429: the bare fallback must leave the till with a real usable
	// register too, not just an admin — a fresh till with no register can't
	// open a shift (FK constraint failure) even though setup "succeeded".
	if regs, err := data.NewPOSRepo(d.Db).ListRegisters(t.Context()); err != nil || len(regs) == 0 {
		t.Fatalf("ListRegisters after first-boot setup = %+v, err=%v; want at least one register", regs, err)
	}

	// Setup is one-time: a second attempt bounces to /login untouched.
	rec = postForm(mux, "/api/auth/setup", url.Values{"pin": {"1111"}, "pin_confirm": {"1111"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("second setup: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	if _, _, err := svc.Login(t.Context(), "1111"); err == nil {
		t.Fatal("second setup must not have set a PIN")
	}

	// The keypad login works with the admin PIN.
	rec = postForm(mux, "/api/auth/login", url.Values{"pin": {"2468"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Wrong PIN re-renders the keypad (with the localized error slot).
	rec = postForm(mux, "/api/auth/login", url.Values{"pin": {"0000"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("bad login: code=%d", rec.Code)
	}
}

func TestUsersPagePermissions(t *testing.T) {
	mux, svc, _ := newAuthTestMux(t)
	repo := svc.Repo()

	adminID, err := repo.CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	adminHash, _ := auth.HashPIN("9876")
	if err := repo.SetUserPIN(t.Context(), adminID, adminHash); err != nil {
		t.Fatal(err)
	}
	admin := auth.User{ID: adminID, Role: "admin", DisplayName: "Boss"}
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	// Cashiers cannot open the users page (GET stays a flat 403 — elevation
	// is for mutations only, ADR-0052 §2). The mutating APIs (ut-docs#795)
	// no longer flat-403 a denied cashier: with no override_pin supplied,
	// checkOrElevate renders the in-place elevation prompt instead (200,
	// htmx-swappable), same shape as every other checkOrElevate site.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier /users = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/users", url.Values{"username": {"x"}, "display_name": {"x"}, "role": {"cashier"}}, &cashier); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier create = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
	}

	// Admin creates a cashier and sets their PIN. ut-docs#795: success is
	// now an in-place htmx confirmation (200), not a 303 redirect.
	if rec := postForm(mux, "/api/users", url.Values{"username": {"jo"}, "display_name": {"Jo"}, "role": {"cashier"}}, &admin); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	users, _ := repo.ListUsers(t.Context())
	var joID string
	for _, u := range users {
		if u.Username == "jo" {
			joID = u.ID
		}
	}
	if joID == "" {
		t.Fatal("jo not created")
	}
	if rec := postForm(mux, "/api/users/"+joID+"/pin", url.Values{"pin": {"1234"}}, &admin); rec.Code != http.StatusOK {
		t.Fatalf("set pin = %d: %s", rec.Code, rec.Body.String())
	}
	if _, _, err := svc.Login(t.Context(), "1234"); err != nil {
		t.Fatalf("jo cannot log in after PIN set: %v", err)
	}

	// A PIN already owned by someone else is refused (PIN-only login).
	// ut-docs#795 review Blocker 2: the error renders inline (no
	// redirect/Location to inspect) with status ALWAYS 200 (a non-2xx here
	// would never be swapped by htmx) and X-UT-Response: refused as the
	// actual "this was a refusal, not a success" signal.
	if rec := postForm(mux, "/api/users/"+adminID+"/pin", url.Values{"pin": {"1234"}}, &admin); rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.pin_taken")) {
		t.Fatalf("duplicate pin: code=%d X-UT-Response=%q body=%q", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}

	// The last active admin with a PIN cannot be deactivated.
	if rec := postForm(mux, "/api/users/"+adminID+"/active", url.Values{"active": {"0"}}, &admin); rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), httpx.T("en", "users.error.last_admin")) {
		t.Fatalf("last admin: code=%d X-UT-Response=%q body=%q", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}

	// Deactivating jo revokes jo's sessions.
	_, tok, err := svc.Login(t.Context(), "1234")
	if err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/users/"+joID+"/active", url.Values{"active": {"0"}}, &admin); rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := svc.Resolve(t.Context(), tok); ok {
		t.Error("deactivated user's session still resolves")
	}
}

// ut-docs#1096: login.html and setup.html are standalone documents (their
// own <html>, bypassing web/ui/layouts/base.html) and were shipping without
// web/public/osk.js at all — a keyboard-less touchscreen till could
// neither complete setup (11 text inputs) nor sign in (2) afterwards.
// Static presence (scripts/ci/guard-osk-loaded.sh) catches "the tag is
// missing"; this proves the actual rendered response carries it, same
// split as guard-autofill-suppression.sh / autofill-suppression-400.spec.ts.
// Also asserts body[data-osk] now reaches these two pages — before this
// fix neither template set the attribute at all, so osk.js's own
// `document.body.dataset.osk || 'auto'` read silently fell back to "auto"
// regardless of the operator's forced Settings choice (osk_mode_test.go
// covers that mechanism on base-layout pages; these two pages bypass the
// layout the same way they bypass osk.js).
func TestLoginAndSetupLoadOnScreenKeyboard(t *testing.T) {
	withOSLocale(t, "", "")
	mux, _, _ := newAuthTestMux(t)

	assertOSK := func(label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d, want 200", label, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `src="/public/osk.js`) {
			t.Errorf("%s: response never loads osk.js — keyboard-less touchscreen till locked out", label)
		}
		if !strings.Contains(body, `data-osk="auto"`) {
			t.Errorf("%s: <body> missing data-osk=\"auto\" — the OSK mode setting can't reach this page", label)
		}
	}

	// Fresh DB: GET /setup renders login.html's sibling standalone document
	// directly (first-boot wizard).
	assertOSK("GET /setup (first boot)", get(t, mux, "/setup"))

	// Complete first-boot setup so /login renders its normal PIN keypad
	// (a fresh DB's GET /login redirects to /setup instead, per
	// TestFirstBootSetupThenLogin above) — same route both real forms of
	// login.html (first-boot admin-PIN-creation and the regular keypad)
	// take through the one template, so this also covers the firstBoot
	// branch implicitly.
	if rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertOSK("GET /login (keypad)", get(t, mux, "/login"))
}

// ut-docs#1126 review finding F2: the e2e coverage added alongside this fix
// (e2e/tests/login.spec.ts) drives a real browser at UT_UI_SCALE's default
// (1.0), where "--ui-scale: 1" and no --ui-scale attribute at all are
// indistinguishable once app.css's calc(var(--ui-scale, 1) * ...) fallback
// kicks in — so a broken "fix" that just deletes the inline style entirely
// would still pass every browser assertion. Only a non-default scale value
// makes the attribute's actual presence and content observable, and that's
// exactly what a Go-level render test (no browser, no CSS evaluation) can
// assert directly on the response body — this is the property the e2e
// suite structurally cannot see. Mirrors TestLoginAndSetupLoadOnScreenKeyboard
// just above (same two-page, first-boot-then-login shape).
func TestLoginAndSetupUseFluidUIScaleCSSVariable(t *testing.T) {
	httpx.InitUIScale(1.3)
	t.Cleanup(func() { httpx.InitUIScale(1.0) })
	withOSLocale(t, "", "")
	mux, _, _ := newAuthTestMux(t)

	assertUIScale := func(label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d, want 200", label, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `style="--ui-scale: 1.3"`) {
			t.Errorf("%s: <html> missing style=\"--ui-scale: 1.3\" — the operator's Settings > UI scale choice can't reach this page, or the fluid viewport mechanism was dropped instead of wired", label)
		}
		// Locks in the actual regression this card fixed: the OLD mechanism
		// rendered a fixed "font-size: <n>px" on <html> instead, independent
		// of viewport. If this ever reappears, --ui-scale was removed rather
		// than added alongside it.
		if strings.Contains(body, `style="font-size:`) {
			t.Errorf("%s: <html> still sets a fixed inline font-size — the pre-ut-docs#161 mechanism regressed back in", label)
		}
	}

	// Fresh DB: GET /setup renders login.html's sibling standalone document
	// directly (first-boot wizard).
	assertUIScale("GET /setup (first boot)", get(t, mux, "/setup"))

	// Complete first-boot setup so /login renders its normal PIN keypad.
	if rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertUIScale("GET /login (keypad)", get(t, mux, "/login"))
}
