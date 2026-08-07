package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// initAuthTestI18n loads the real locale bundle so templates that call {{ T }}
// (login/setup/pin/session-chip and the enrol error spans) render real text.
func initAuthTestI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

// newFullAuthDeps wires the auth/setup/settings handlers against a DB with the
// real users/sessions/audit_log schema (nullable pin_hash, so AuthRepo.CreateUser
// works) plus a settings table and a live Engine/Settings/AuthSvc — everything
// the setup wizard and the settings API endpoints touch. Distinct from
// newAuthTestMux, which has no settings table and no Engine.
func newFullAuthDeps(t *testing.T) (*http.ServeMux, *auth.Service, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initAuthTestI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
		 role TEXT NOT NULL DEFAULT 'cashier', pin_hash TEXT, is_active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
		 created_at TEXT NOT NULL DEFAULT (datetime('now')), expires_at TEXT NOT NULL, revoked_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL,
		 entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT,
		 is_active INTEGER NOT NULL DEFAULT 1)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup schema: %v", err)
		}
	}
	svc := auth.NewService(db)
	store := settings.NewStore(db)
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d := &common.Deps{
		Db:       db,
		Settings: store,
		Engine:   engine,
		AuthSvc:  svc,
		Cfg:      &config.Config{Theme: "default"},
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
	}
	mux := http.NewServeMux()
	registerAuth(mux, d, svc)
	registerSetup(mux, d, svc)
	registerSettings(mux, d)
	return mux, svc, d
}

// sessionCookie pulls the freshly-set session token out of a response.
func sessionCookie(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	return ""
}

// The guided wizard (POST /api/setup) is the primary first-boot path — distinct
// from the bare POST /api/auth/setup fallback that TestFirstBootSetupThenLogin
// already covers. It applies country/currency/tax + store name, then creates the
// admin and signs in.
func TestSetupWizardHappyPath(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":          {"2468"},
		"pin_confirm":  {"2468"},
		"country":      {"GB"},
		"currency":     {"GBP"},
		"tax_rate_pct": {"20"},
		"store_name":   {"Corner Shop"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	cookie := sessionCookie(rec)
	if cookie == "" {
		t.Fatal("wizard setup did not set a session cookie")
	}
	if u, ok := svc.Resolve(t.Context(), cookie); !ok || u.Role != "admin" {
		t.Fatalf("wizard session resolves to %+v ok=%v, want admin", u, ok)
	}
	// Country/currency/tax and the store name were persisted.
	if name, ok, _ := d.Settings.Get(t.Context(), "store.name"); !ok || name != "Corner Shop" {
		t.Fatalf("store.name = %q ok=%v", name, ok)
	}
	if done, ok, _ := d.Settings.Get(t.Context(), "setup.completed"); !ok || done != "true" {
		t.Fatalf("setup.completed = %q ok=%v", done, ok)
	}
	if d.CurrentState().Currency != "GBP" || d.CurrentState().TaxRatePct != 20 {
		t.Fatalf("state not applied: %+v", d.CurrentState())
	}
	// The admin PIN works at the keypad.
	if _, _, err := svc.Login(t.Context(), "2468"); err != nil {
		t.Fatalf("admin cannot log in after wizard: %v", err)
	}
}

// ut-docs#429: a genuinely fresh till, taken through the guided wizard with
// no other setup, must be able to open a shift immediately afterward. The
// wizard provisions an admin + PIN as real usable state; it must provision a
// register the same way. web/ui/pages/shifts.html's register <select> is
// driven entirely from POSRepo.ListRegisters — before this fix the wizard
// never inserted a row there, so the template fell back to a hardcoded
// <option value="reg-default"> that didn't exist in the DB, and submitting
// Open Shift 500'd on a FOREIGN KEY constraint failure.
func TestSetupWizardCreatesDefaultRegister(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":          {"2468"},
		"pin_confirm":  {"2468"},
		"country":      {"GB"},
		"currency":     {"GBP"},
		"tax_rate_pct": {"20"},
		"store_name":   {"Corner Shop"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	regs, err := data.NewPOSRepo(d.Db).ListRegisters(t.Context())
	if err != nil {
		t.Fatalf("ListRegisters: %v", err)
	}
	if len(regs) != 1 {
		t.Fatalf("ListRegisters after wizard = %+v, want exactly one register", regs)
	}
}

// The wizard's shop-name step also carries the primary till's own name
// (ut-docs#396, distinct from a replica's sync.till_name) — submitting it
// persists till.name, and omitting/blanking it leaves the name resolvable
// via tillNameOrDefault's default, exactly like store_name's own handling.
func TestSetupWizardTillName(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":          {"2468"},
		"pin_confirm":  {"2468"},
		"country":      {"GB"},
		"currency":     {"GBP"},
		"tax_rate_pct": {"20"},
		"store_name":   {"Corner Shop"},
		"till_name":    {"Front Register"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup with till_name: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if name, ok, _ := d.Settings.Get(t.Context(), "till.name"); !ok || name != "Front Register" {
		t.Fatalf("till.name = %q ok=%v, want %q", name, ok, "Front Register")
	}
}

// Omitting till_name (blank submit) must not error and must leave the name
// resolvable via the default — same skip-on-blank behaviour as store_name.
func TestSetupWizardBlankTillNameDoesNotErrorAndDefaultsOnRead(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":          {"2468"},
		"pin_confirm":  {"2468"},
		"country":      {"GB"},
		"currency":     {"GBP"},
		"tax_rate_pct": {"20"},
		"store_name":   {"Corner Shop"},
		// till_name intentionally omitted.
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup without till_name: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), "till.name"); ok {
		t.Fatalf("till.name should be unset when the wizard field was blank")
	}
	if got := tillNameOrDefault(t.Context(), d, "en"); got != "Till 1" {
		t.Fatalf("tillNameOrDefault after blank submit = %q, want default %q", got, "Till 1")
	}
}

// GET /setup and POST /api/setup both refuse to run once an operator with a PIN
// exists — the wizard window is exactly first boot.
func TestSetupWizardRefusesAfterFirstBoot(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)

	// Seed a real operator so NeedsFirstBoot is false.
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPIN("1234")
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("GET /setup after first boot: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	rec = postForm(mux, "/api/setup", url.Values{"pin": {"9999"}, "pin_confirm": {"9999"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup after first boot: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	// The seeded operator's PIN is unchanged; the ignored 9999 must not log in.
	if _, _, err := svc.Login(t.Context(), "9999"); err == nil {
		t.Fatal("refused wizard must not have set any PIN")
	}
}

// Bad PIN format and mismatched confirmation re-render the wizard (step 4, the
// error slot) rather than creating an operator.
func TestSetupWizardPINValidation(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)

	cases := []struct {
		name string
		form url.Values
	}{
		{"too short", url.Values{"pin": {"12"}, "pin_confirm": {"12"}}},
		{"non numeric", url.Values{"pin": {"abcd"}, "pin_confirm": {"abcd"}}},
		{"mismatch", url.Values{"pin": {"2468"}, "pin_confirm": {"8642"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postForm(mux, "/api/setup", tc.form, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("re-render code=%d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "login-error") {
				t.Fatalf("expected wizard error slot, got: %s", rec.Body.String())
			}
			if sessionCookie(rec) != "" {
				t.Fatal("a rejected wizard submit must not set a session cookie")
			}
		})
	}
	// Nothing was created.
	if fb, _ := svc.NeedsFirstBoot(t.Context()); !fb {
		t.Fatal("rejected wizard submits must leave the till in first-boot state")
	}
}

// The join-an-existing-shop step is driven entirely by htmx (hx-post, with no
// action/method fallback), so the setup page MUST load htmx.min.js. It didn't:
// the page loaded only alpine.min.js and cursor.js, which made the hx-*
// attributes inert markup and turned the Join button into a plain GET back to
// /setup. No request ever reached POST /api/setup/join, and #setup-join-msg
// never filled — from the shop owner's side the button simply did nothing, so
// a second till could not be enrolled at all (ADR-0011 D2).
//
// Found in the field on a real Pi-to-Pi setup (ut-docs#344, 2026-08-06), not
// by any test — hence this one. scripts/ci/guard-htmx-loaded.sh enforces the
// same invariant across every standalone template; this test covers the
// rendered output of the page the bug was actually reported against.
func TestSetupPageLoadsHTMXForTheJoinForm(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Guard the premise: if the join form ever stops being htmx-driven, this
	// test should be re-thought rather than silently passing on a page that no
	// longer needs htmx at all.
	if !strings.Contains(body, `hx-post="/api/setup/join"`) {
		t.Fatal("setup page no longer has the htmx-driven join form; " +
			"re-evaluate this test and scripts/ci/guard-htmx-loaded.sh")
	}
	// Assert a real <script src=...>, not a bare mention: this file's own
	// explanatory comment names the guard script, so a substring check on
	// "htmx.min.js" alone could go vacuous on an innocent comment edit.
	if !regexp.MustCompile(`<script[^>]*src="[^"]*htmx\.min\.js`).MatchString(body) {
		t.Error("setup page uses hx-post for the join form but never loads htmx.min.js — " +
			"the Join button will silently do nothing (ut-docs#344)")
	}
	// htmx 1.9 discards the response body on a non-2xx status, and every
	// failure of /api/setup/join answers 502, so without this listener the
	// whole error path renders nothing at all. e2e/tests/login.spec.ts proves
	// the behaviour in a real browser; this just pins the wiring in place.
	if !strings.Contains(body, "htmx:beforeSwap") {
		t.Error("setup page has no htmx:beforeSwap handler — a failed join (502) " +
			"would render nothing at all (ut-docs#344)")
	}
	// The live region the swap targets must exist, or a working htmx post
	// would still show the operator nothing.
	if !strings.Contains(body, `id="setup-join-msg"`) {
		t.Error("setup page is missing the #setup-join-msg target the join form swaps into")
	}
}

// POST /api/setup/join is middleware-exempt (internal/auth/middleware.go), so
// the ONLY thing stopping an unauthenticated stranger from re-enrolling a
// live till — wiping it and pulling another shop's whole database over the
// top — is its NeedsFirstBoot gate. That gate had no test at all; the sibling
// /api/sync/join is well covered, but it is a different handler with a
// different guard (manager-or-admin), so it proves nothing about this one.
func TestSetupJoinRefusedOnceAnOperatorExists(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	// The shared harness mounts auth/setup/settings only; the join endpoint
	// lives with the sync routes. Mount them here rather than widening the
	// harness, so no other test's route table changes.
	registerSyncAPI(mux, d)

	// While it is genuinely first boot, the endpoint is reachable: a garbage
	// code gets past the gate and fails on its own merits (502), rather than
	// being redirected away.
	rec := postForm(mux, "/api/setup/join", url.Values{"code": {"nonsense"}}, nil)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("join was redirected during first boot; the wizard's join step is unreachable")
	}

	// Complete first boot: an operator with a PIN is what closes the window.
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPIN("2468")
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	// Now it must refuse — no enrolment, just a redirect to the login screen.
	rec = postForm(mux, "/api/setup/join", url.Values{"code": {"nonsense"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("join after first boot: code=%d loc=%q, want 303 -> /login (an unauthenticated "+
			"caller must not be able to re-enrol a configured till)",
			rec.Code, rec.Header().Get("Location"))
	}
}
