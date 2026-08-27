package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

// seedCountrySettingsTable applies the REAL country_settings migration
// (ut-docs#660) to a hand-rolled test schema, rather than retyping its
// DDL/seed here — a hand-rolled copy is exactly the schema drift the tester
// skill warns against; reading the actual migration file can't drift
// because it IS the migration. Self-contained (no FK to any other table),
// so it's safe to layer onto any hand-rolled schema. Shared by every fixture
// in this package that registers the setup wizard (registerSetup), since
// wizardCountries now queries this table on every render.
//
// Also applies 073 (ut-docs#1027, default_locale column) for the same
// reason — 041 alone no longer matches the real, current table shape, and
// any later migration that further alters country_settings needs adding
// here too, or this fixture silently drifts from what production actually
// runs (exactly the failure mode this helper's own doc comment guards
// against for 041).
func seedCountrySettingsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{"041_country_settings.sql", "073_country_default_locale.sql"} {
		migrationSQL, err := os.ReadFile(filepath.Join("internal", "db", "migrations", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(string(migrationSQL)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
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
		 entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT,
		 is_active INTEGER NOT NULL DEFAULT 1)`,
		// roles/permission_actions/role_permissions (ut-docs#710): settings_page.go's
		// endpoints now gate via canPerform()/AuthSvc.Can(), which queries
		// role_permissions for real — column-identical to
		// internal/db/migrations/039_role_permissions.sql, same drift rule as
		// ui_smoke_test.go's seedForPages (a drifted fixture here would let a
		// canPerform()-gated test pass against a permission schema production
		// doesn't have).
		`CREATE TABLE roles (role TEXT PRIMARY KEY)`,
		`CREATE TABLE permission_actions (action TEXT PRIMARY KEY)`,
		`CREATE TABLE role_permissions (role TEXT NOT NULL REFERENCES roles(role), action TEXT NOT NULL REFERENCES permission_actions(action),
		 granted INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (role, action))`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup schema: %v", err)
		}
	}
	seedCountrySettingsTable(t, db)
	// Seed grants identical to 039's own seed data: manager/admin/super_admin
	// get every catalog action, cashier gets none (no rows inserted for
	// cashier — Can() treats "no row" as denied).
	for _, role := range []string{"cashier", "manager", "admin", "super_admin"} {
		if _, err := db.Exec(`INSERT INTO roles (role) VALUES (?)`, role); err != nil {
			t.Fatalf("seed role %s: %v", role, err)
		}
	}
	permissionCatalog := []string{"refund", "eod_report", "cash_adjustment", "price_override", "void", "user_management", "settings"}
	for _, action := range permissionCatalog {
		if _, err := db.Exec(`INSERT INTO permission_actions (action) VALUES (?)`, action); err != nil {
			t.Fatalf("seed permission_action %s: %v", action, err)
		}
	}
	for _, role := range []string{"manager", "admin", "super_admin"} {
		for _, action := range permissionCatalog {
			if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, ?, 1)`, role, action); err != nil {
				t.Fatalf("seed role_permission %s/%s: %v", role, action, err)
			}
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
		// Shell mirrors pages.Init's production wiring (ADR-0064): the
		// window-state endpoint and the Display card read it. Handlers stay
		// nil-safe for the bare-Deps helpers that predate this field.
		Shell: common.NewShellChannel(common.DefaultWindowMode),
	}
	// Mirror the desktop-shell topology: in production
	// NewShellPollWindowController marks the channel as the exit path
	// (review of ut-docs#1039, blocker 2). WindowCtl itself stays nil here
	// — several tests assert the handlers' nil fallback — so mark the
	// channel directly; tests for the Pi kiosk topology build their own
	// unmarked channel.
	d.Shell.MarkExitPath()
	mux := http.NewServeMux()
	registerAuth(mux, d, svc)
	registerSetup(mux, d, svc)
	registerSettings(mux, d)
	registerWindowState(mux, d) // ut-docs#611: desktop shell's pre-login window-mode read
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

// TestSetupWizardDrivesDisplayedSymbolFromCurrencyCodeAlone is the regression
// test for ut-docs#1172: a live German till's DB showed a wizard-committed
// "store.currency"="EUR" alongside a stale "store.currency_symbol"="£" left
// over from the GB boot default, because the wizard set the former but never
// the latter. The fix is not to also start writing the symbol — every real
// symbol display (the {{ money }} template func, receipts included) already
// derives it from httpx.ActiveCurrency(), keyed off store.currency alone via
// httpx's currency registry (internal/httpx/currency.go) — so the field
// itself is dead and removed. Prove both halves: the wizard-derived symbol
// is correct end to end for a non-GB country, and the dead setting never
// gets persisted at all.
func TestSetupWizardDrivesDisplayedSymbolFromCurrencyCodeAlone(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // process-global, reset for later tests in this package

	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"},
		"country": {"DE"}, "currency": {"EUR"}, "currency_touched": {"1"},
		"tax_rate_pct": {"19"}, "store_name": {"Bäckerei Berlin"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := httpx.ActiveCurrency().Display; got != "€" {
		t.Fatalf("httpx.ActiveCurrency().Display = %q after a DE/EUR wizard commit, want %q — this is what every real money display (receipts included) actually renders", got, "€")
	}
	if _, ok, _ := d.Settings.Get(t.Context(), "store.currency_symbol"); ok {
		t.Fatal("wizard commit wrote store.currency_symbol — this setting is dead (ut-docs#1172) and must never be persisted, live or stale")
	}
}

// TestSetupWizardCurrencyConfirmedOnlyWhenOperatorTouchedCountrySelect is the
// regression test for ut-docs#970 review finding F3: country/currency start
// PRE-FILLED from OS locale + timezone detection (ut-docs#590), not from an
// operator choice, so a submitted non-blank currency field alone proves
// nothing — the wizard can complete with the operator never having opened
// the country step at all. web/ui/pages/setup.html's currency_touched hidden
// field only flips to "1" on a genuine @change on the country select.
func TestSetupWizardCurrencyConfirmedOnlyWhenOperatorTouchedCountrySelect(t *testing.T) {
	t.Run("no currency_touched field (operator never opened the country step)", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if confirmed, ok, _ := d.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); ok && confirmed == "true" {
			t.Fatalf("currency marked confirmed with no currency_touched field — a completed wizard alone must not count as an operator choice")
		}
	})
	t.Run("currency_touched=1 (operator actually used the country select)", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"}, "currency_touched": {"1"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if confirmed, ok, err := d.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); err != nil || !ok || confirmed != "true" {
			t.Fatalf("currency_confirmed = (%q, %v, %v), want (true, true, nil) when the operator touched the country select", confirmed, ok, err)
		}
	})
	t.Run("currency_touched=1 but an unrecognised currency code is rejected, not confirmed", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"GB"}, "currency": {"NOTREAL"}, "tax_rate_pct": {"20"}, "currency_touched": {"1"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if confirmed, ok, _ := d.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); ok && confirmed == "true" {
			t.Fatalf("an unrecognised currency code must not be accepted or marked confirmed")
		}
	})
}

// TestSetupWizardDerivesLocaleFromCountry is the regression test for
// ut-docs#1027: the wizard prefilled currency/tax from the chosen country but
// never derived store.locale, so a German shop shipped running en-US.
// store.locale is derived server-side from the posted "country" against
// country_settings — a posted "locale" field, if any, is ignored (review
// finding: this endpoint is auth-exempt during first boot, so the value
// must come from our own seeded data, never from client-supplied text).
func TestSetupWizardDerivesLocaleFromCountry(t *testing.T) {
	t.Run("DE derives de-DE (non-RTL, safe even with no language pack installed)", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"DE"}, "currency": {"EUR"}, "tax_rate_pct": {"19"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "de-DE" {
			t.Fatalf("store.locale = %q, want de-DE", got)
		}
	})
	t.Run("GB derives en-GB", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "en-GB" {
			t.Fatalf("store.locale = %q, want en-GB", got)
		}
	})
	t.Run("AE derives ar-AE (RTL, but ar ships bundled — safe)", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"AE"}, "currency": {"AED"}, "tax_rate_pct": {"5"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "ar-AE" {
			t.Fatalf("store.locale = %q, want ar-AE", got)
		}
	})
	t.Run("a posted locale field is ignored — server derives from country, not client text", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"DE"}, "currency": {"EUR"}, "tax_rate_pct": {"19"}, "locale": {"xx-NOTREAL"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "de-DE" {
			t.Fatalf("store.locale = %q, want de-DE (posted locale must be ignored)", got)
		}
	})
	t.Run("PK derives ur-PK but ur is NOT bundled (RTL + unavailable) — leaves the existing locale untouched", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		// Seed a genuinely non-blank, non-PK-related locale so "untouched"
		// is a real assertion (review finding: an empty-string baseline
		// makes this pass even with the guard deleted).
		d.UpdateState(func(s *common.RuntimeState) { s.Locale = "tr" })
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"PK"}, "currency": {"PKR"}, "tax_rate_pct": {"18"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "tr" {
			t.Fatalf("store.locale = %q, want unchanged \"tr\" (ur-PK is RTL and not installed, must not be preset)", got)
		}
	})
	t.Run("OTHER (no mapped default) leaves the existing locale untouched", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		d.UpdateState(func(s *common.RuntimeState) { s.Locale = "tr" })
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"},
			"country": {"OTHER"}, "currency": {"USD"}, "tax_rate_pct": {"0"},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := d.CurrentState().Locale; got != "tr" {
			t.Fatalf("store.locale = %q, want unchanged \"tr\"", got)
		}
	})
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

// ut-docs#617: "No" (or simply omitting restore_choice, the default) must be
// a pure no-op — the redirect and the deferred flag both stay exactly as
// they are for every other wizard submission.
func TestSetupWizardRestoreNoIsNoOp(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":            {"2468"},
		"pin_confirm":    {"2468"},
		"country":        {"GB"},
		"currency":       {"GBP"},
		"tax_rate_pct":   {"20"},
		"restore_choice": {"no"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup restore=no: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyRestorePromptStatus); ok {
		t.Fatalf("restore prompt status should stay unset after 'no'")
	}
}

// "Later" persists the deferred flag so Settings → Data can offer a resume
// link, but must not change where the wizard lands — same "/" as any other
// completed setup.
func TestSetupWizardRestoreLaterPersistsDeferredFlag(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":            {"2468"},
		"pin_confirm":    {"2468"},
		"country":        {"GB"},
		"currency":       {"GBP"},
		"tax_rate_pct":   {"20"},
		"restore_choice": {"later"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup restore=later: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyRestorePromptStatus); !ok || v != common.RestorePromptStatusDeferred {
		t.Fatalf("restore prompt status = %q ok=%v, want %q", v, ok, common.RestorePromptStatusDeferred)
	}
}

// "CSV/Excel" lands the new operator straight in the existing catalog
// importer instead of home — no detour through Settings/Catalog navigation
// — and, being immediate rather than deferred, does NOT set the resume flag.
func TestSetupWizardRestoreCSVExcelRedirectsToImport(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":            {"2468"},
		"pin_confirm":    {"2468"},
		"country":        {"GB"},
		"currency":       {"GBP"},
		"tax_rate_pct":   {"20"},
		"restore_choice": {"csv_excel"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/import" {
		t.Fatalf("wizard setup restore=csv_excel: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyRestorePromptStatus); ok {
		t.Fatalf("restore prompt status should stay unset after an immediate csv_excel choice")
	}
}

// getSetup issues a bare GET /setup, optionally carrying a ut_lang cookie —
// used by the detection tests below to simulate a fresh first visit (no
// cookie) vs. a repeat visit (cookie already set from an earlier pick).
func getSetup(mux *http.ServeMux, query, langCookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/setup"+query, nil)
	if langCookie != "" {
		req.AddCookie(&http.Cookie{Name: "ut_lang", Value: langCookie})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ut-docs#590: a genuinely first visit (no ?lang=, no ut_lang cookie) with an
// OS locale/timezone that resolves to a language this till actually ships
// (tr is a core locale) redirects through the existing ?lang= mechanism —
// the same one the step-1 flag buttons already use — rather than rendering
// silently in a language nobody chose.
func TestSetupWizardRedirectsToDetectedAvailableLanguage(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "tr_TR.UTF-8", "Europe/Istanbul")

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=tr" {
		t.Fatalf("GET /setup (tr_TR, Europe/Istanbul): code=%d loc=%q, want 303 -> /setup?lang=tr",
			rec.Code, rec.Header().Get("Location"))
	}

	// Following the redirect, the country step is pre-filled from the same
	// detection (Turkey, by timezone) — still freely changeable.
	rec = getSetup(mux, "?lang=tr", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=tr: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"country: 'TR'", "currency: 'TRY'", "tax: '20'", "taxinc: 'on'"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup?lang=tr body missing %q (country prefill)", want)
		}
	}
}

// The English case is the same redirect path (en is a core locale) — this
// pins that "already the default" doesn't get special-cased into skipping
// the redirect, since that would leave the ut_lang cookie unset and detection
// would silently re-run (and re-decide) on every subsequent load.
func TestSetupWizardRedirectsToDetectedEnglishAndPrefillsGB(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "en_GB.UTF-8", "Europe/London")

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=en" {
		t.Fatalf("GET /setup (en_GB, Europe/London): code=%d loc=%q, want 303 -> /setup?lang=en",
			rec.Code, rec.Header().Get("Location"))
	}

	rec = getSetup(mux, "?lang=en", "")
	body := rec.Body.String()
	for _, want := range []string{"country: 'GB'", "currency: 'GBP'", "tax: '20'", "taxinc: 'on'"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup?lang=en body missing %q (country prefill)", want)
		}
	}
}

// A German till (real field case, ut-docs#589's own motivating example) has
// no redirect to take — "de" isn't one of the locales this till ships today
// (core is ar/en/fa/tr only) — so it renders directly, in English, with a
// plain "de is coming soon" note rather than silently landing on English
// with no explanation. The country step still detects Germany correctly,
// independent of the language gap.
func TestSetupWizardGermanLanguageUnavailableCountryStillDetected(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin")

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup (de_DE, Europe/Berlin): code=%d, want 200 (no available-language redirect)", rec.Code)
	}
	body := rec.Body.String()
	// Assert on the note's own marker, not on the bare string "de": the page
	// already renders `name="code"` for the join step, so a bare
	// strings.Contains(body, "de") can never fail and would pass even with
	// the note removed entirely.
	if !strings.Contains(body, `data-detected-lang="de"`) {
		t.Error("GET /setup body should carry the coming-soon note naming the detected 'de' locale")
	}
	for _, want := range []string{"country: 'DE'", "currency: 'EUR'", "tax: '19'", "taxinc: 'on'"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup body missing %q (country prefill)", want)
		}
	}
	// Recorded for ut-docs#589's child 3 (auto-file a board ticket for a
	// missing language) to act on later.
	if v, ok, _ := d.Settings.Get(t.Context(), "setup.detected_lang_unavailable"); !ok || v != "de" {
		t.Errorf("setup.detected_lang_unavailable = %q ok=%v, want \"de\"", v, ok)
	}
}

// Neither the language nor the timezone/locale region resolves to anything
// this wizard knows — the graceful-fallback path: render in English (the
// existing bare default), no country prefilled (a blank/placeholder select,
// never a guess), and the coming-soon note names whatever was detected.
func TestSetupWizardUnknownLocaleNoCountryGuessed(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "ja_JP.UTF-8", "Asia/Tokyo")

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup (ja_JP, Asia/Tokyo): code=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-detected-lang="ja"`) {
		t.Error("GET /setup body should carry the coming-soon note naming the detected 'ja' locale")
	}
	if strings.Contains(body, "country: 'DE'") || strings.Contains(body, "country: 'GB'") ||
		strings.Contains(body, "country: 'TR'") {
		t.Error("GET /setup should not have guessed a country for an unmapped timezone/locale")
	}
	if !strings.Contains(body, "country: ''") {
		t.Error("GET /setup should leave the country unset (blank), not guess one")
	}
}

// Detection only ever drives the FIRST visit — an explicit ?lang= query
// (the step-1 buttons' own mechanism) or a cookie from an earlier visit both
// mean a choice already happened, so re-detecting would be a nag, not a
// default. Country detection stays independent and still applies.
func TestSetupWizardDetectionSkippedOnceAChoiceExists(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin")

	// Explicit ?lang= present: no redirect loop, no coming-soon note, even
	// though the underlying OS locale (de) would trigger one on a bare visit.
	rec := getSetup(mux, "?lang=fa", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=fa: code=%d", rec.Code)
	}
	// The marker, not the locale key: T resolves the key, so the key string
	// itself never appears in rendered output and asserting its absence can
	// never fail.
	if strings.Contains(rec.Body.String(), "data-detected-lang=") {
		t.Error("an explicit ?lang= must not also show the detected-language note")
	}
	// Country detection is unaffected by the language cookie/query state.
	if !strings.Contains(rec.Body.String(), "country: 'DE'") {
		t.Error("country prefill should still apply alongside an explicit ?lang=")
	}

	// A cookie from an earlier visit: same — no redirect, no note.
	rec = getSetup(mux, "", "en")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup with existing ut_lang cookie: code=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "data-detected-lang=") {
		t.Error("a repeat visit (cookie already set) must not re-show the detected-language note")
	}
}

// Review finding: a failed POST (mistyped/mismatched PIN) re-renders the same
// wizard template, and the country step's hidden currency/tax_rate_pct inputs
// are bound to the same x-data. Detection must NOT re-run on that path — the
// operator has already picked a country, and re-detecting silently replaced
// their deliberate "France, 20%" with the detected "Germany, 19%", which the
// retry would then save without ever showing them the country step again.
func TestSetupWizardPINErrorRerenderKeepsOperatorCountryNotDetected(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin") // detection would say DE / 19%

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch → error re-render
		"country": {"FR"}, "currency": {"EUR"}, "tax_rate_pct": {"20"}, "tax_inclusive": {"on"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{"country: 'FR'", "currency: 'EUR'", "tax: '20'"} {
		if !strings.Contains(body, want) {
			t.Errorf("PIN-error re-render missing %q — the operator's own country pick must survive", want)
		}
	}
	for _, unwanted := range []string{"country: 'DE'", "tax: '19'"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("PIN-error re-render contains %q — detection must not overwrite a submitted choice", unwanted)
		}
	}
}

// The same path with no country submitted at all must fall back to the
// wizard's pre-#590 blank/tax-inclusive-on defaults, not to detection.
func TestSetupWizardPINErrorRerenderWithNoCountryStaysBlank(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin")

	form := url.Values{"pin": {"12"}, "pin_confirm": {"12"}} // too short → error re-render
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "country: ''") || !strings.Contains(body, "taxinc: 'on'") {
		t.Error("PIN-error re-render with no submitted country should stay blank with taxinc on")
	}
	if strings.Contains(body, "country: 'DE'") {
		t.Error("PIN-error re-render must not detect a country the operator never chose")
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
	// ut-docs#662: hermetic against the developer machine's real OS locale —
	// otherwise ut-docs#590's /setup detection redirect fires on any machine
	// with a locale set and this test never even reaches the wizard.
	withOSLocale(t, "", "")
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

// ut-docs#660: the wizard's country list comes from country_settings, not a
// compile-time slice — an operator-added country (no seeded NameKey) must
// show up, falling back to its raw code since {{ T "" }} would render
// nothing.
func TestSetupWizardRendersAdminAddedCountry(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	repo := data.NewCountrySettingsRepo(d.Db)
	if err := repo.Upsert(t.Context(), data.CountrySetting{
		Code: "ZZ", Currency: "ZZZ", TaxRateBP: 500, TaxInclusive: true,
		ArchiveMinDays: data.GlobalArchiveMinDays,
	}); err != nil {
		t.Fatalf("seed custom country: %v", err)
	}

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="ZZ"`) {
		t.Fatalf("GET /setup?lang=en body missing the admin-added ZZ option:\n%s", body)
	}
	if !strings.Contains(body, `data-currency="ZZZ"`) {
		t.Errorf("ZZ option missing its currency prefill data")
	}
	// No NameKey was seeded for this operator-added country — the option's
	// visible label must fall back to the raw code rather than rendering
	// {{ T "" }} (empty/garbage).
	if !regexp.MustCompile(`value="ZZ"[^>]*>ZZ<`).MatchString(body) {
		t.Errorf("ZZ option should render its own code as the label when it has no NameKey:\n%s", body)
	}
}

// An admin's edit to a builtin country's tax rate (via Settings → Country
// settings, #659) must reach the wizard's prefill — the whole point of
// #660 moving the wizard off the hardcoded slice.
func TestSetupWizardPrefillsAdminEditedCountry(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	repo := data.NewCountrySettingsRepo(d.Db)
	gb, ok, err := repo.Get(t.Context(), "GB")
	if err != nil || !ok {
		t.Fatalf("seeded GB missing: ok=%v err=%v", ok, err)
	}
	gb.TaxRateBP = 2150 // 21.5% -- edited away from the seeded 20%
	if err := repo.Upsert(t.Context(), gb); err != nil {
		t.Fatalf("edit GB: %v", err)
	}

	withOSLocale(t, "en_GB.UTF-8", "Europe/London")
	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// 2150bp rounds to 22% (round-half-up; see wizardCountries' doc comment
	// on why the wizard only carries whole-percent precision).
	for _, want := range []string{"country: 'GB'", "currency: 'GBP'", "tax: '22'"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup?lang=en body missing %q after editing GB's tax rate — wizard is not reading country_settings:\n%s", want, body)
		}
	}
	if strings.Contains(body, "tax: '20'") {
		t.Errorf("GET /setup?lang=en still prefills GB's stale 20%% rate — wizard is reading a stale/cached source, not country_settings")
	}
}

// Review finding N2: a country_settings read failure must degrade to the
// builtin defaults, not take down first boot entirely — every OTHER
// failure in this same handler (locale persist, restore prompt, plugin
// install, demo seed) already follows this "never block the wizard"
// posture, and a shop with no till yet has no recovery path if this one
// screen 500s.
func TestSetupWizardCountrySettingsReadFailureFallsBackToBuiltins(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`DROP TABLE country_settings`); err != nil {
		t.Fatalf("drop country_settings to simulate a read failure: %v", err)
	}

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en with country_settings unreadable: code=%d, want 200 (graceful fallback) body=%s",
			rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The builtin GB default (20%, GBP) — same values setupCountries used to
	// hardcode pre-#660 — must still be offered.
	if !strings.Contains(body, `value="GB"`) || !strings.Contains(body, `data-currency="GBP"`) || !strings.Contains(body, `data-tax="20"`) {
		t.Fatalf("GET /setup?lang=en should still offer the builtin GB option when country_settings can't be read:\n%s", body)
	}
}
