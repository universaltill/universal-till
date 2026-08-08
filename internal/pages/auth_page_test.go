package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func newAuthTestMux(t *testing.T) (*http.ServeMux, *auth.Service, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
		 role TEXT NOT NULL DEFAULT 'cashier', pin_hash TEXT, is_active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
		 created_at TEXT NOT NULL DEFAULT (datetime('now')), expires_at TEXT NOT NULL, revoked_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL,
		 entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT,
		 is_active INTEGER NOT NULL DEFAULT 1)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	svc := auth.NewService(db)
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

func TestFirstBootSetupThenLogin(t *testing.T) {
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

	// Cashiers cannot open the users page or its APIs.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/users", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier /users = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/users", url.Values{"username": {"x"}, "display_name": {"x"}, "role": {"cashier"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}

	// Admin creates a cashier and sets their PIN.
	if rec := postForm(mux, "/api/users", url.Values{"username": {"jo"}, "display_name": {"Jo"}, "role": {"cashier"}}, &admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d", rec.Code)
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
	if rec := postForm(mux, "/api/users/"+joID+"/pin", url.Values{"pin": {"1234"}}, &admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("set pin = %d", rec.Code)
	}
	if _, _, err := svc.Login(t.Context(), "1234"); err != nil {
		t.Fatalf("jo cannot log in after PIN set: %v", err)
	}

	// A PIN already owned by someone else is refused (PIN-only login).
	if rec := postForm(mux, "/api/users/"+adminID+"/pin", url.Values{"pin": {"1234"}}, &admin); rec.Header().Get("Location") != "/users?err=users.error.pin_taken" {
		t.Fatalf("duplicate pin: loc=%q", rec.Header().Get("Location"))
	}

	// The last active admin with a PIN cannot be deactivated.
	if rec := postForm(mux, "/api/users/"+adminID+"/active", url.Values{"active": {"0"}}, &admin); rec.Header().Get("Location") != "/users?err=users.error.last_admin" {
		t.Fatalf("last admin: loc=%q", rec.Header().Get("Location"))
	}

	// Deactivating jo revokes jo's sessions.
	_, tok, err := svc.Login(t.Context(), "1234")
	if err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/users/"+joID+"/active", url.Values{"active": {"0"}}, &admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate = %d", rec.Code)
	}
	if _, ok := svc.Resolve(t.Context(), tok); ok {
		t.Error("deactivated user's session still resolves")
	}
}
