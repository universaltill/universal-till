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
