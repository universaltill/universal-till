package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openAuthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
		 role TEXT NOT NULL DEFAULT 'cashier', pin_hash TEXT, is_active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
		 created_at TEXT NOT NULL DEFAULT (datetime('now')), expires_at TEXT NOT NULL, revoked_at TEXT, last_seen_at TEXT)`,
		// Column names/types/PK mirror the real role_permissions table in
		// internal/db/migrations/001_init.sql (the roles/permission_actions
		// FK parents are omitted here since this hand-rolled schema only
		// ever inserts values that would be valid against them — keep it in
		// sync if the real table's shape changes; ut-docs#1425 review F7 —
		// this used to point at migration 039_role_permissions.sql, deleted
		// by the ADR-0074 squash).
		`CREATE TABLE role_permissions (role TEXT NOT NULL, action TEXT NOT NULL, granted INTEGER NOT NULL DEFAULT 0,
		 PRIMARY KEY (role, action))`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	return db
}

func seedOperator(t *testing.T, db *sql.DB, id, role, pin string) {
	t.Helper()
	hash, err := HashPIN(pin)
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, display_name, role, pin_hash) VALUES (?, ?, ?, ?, ?)`,
		id, id, id, role, hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func TestPINHashRoundTrip(t *testing.T) {
	hash, err := HashPIN("4321")
	if err != nil {
		t.Fatalf("HashPIN: %v", err)
	}
	if !VerifyPIN("4321", hash) {
		t.Error("correct PIN rejected")
	}
	if VerifyPIN("1234", hash) {
		t.Error("wrong PIN accepted")
	}
	for _, bad := range []string{"123", "123456789", "12a4", ""} {
		if _, err := HashPIN(bad); err == nil {
			t.Errorf("HashPIN(%q) accepted invalid format", bad)
		}
	}
	for _, malformed := range []string{"", "plain", "pbkdf2$sha256$notanint$x$y", "bcrypt$whatever"} {
		if VerifyPIN("4321", malformed) {
			t.Errorf("VerifyPIN accepted malformed hash %q", malformed)
		}
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	ctx := context.Background()

	u, token, err := svc.Login(ctx, "1122")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if u.ID != "alice" || u.Role != "cashier" {
		t.Fatalf("wrong user: %+v", u)
	}
	if got, ok := svc.Resolve(ctx, token); !ok || got.ID != "alice" {
		t.Fatalf("resolve failed: %+v ok=%v", got, ok)
	}

	svc.Logout(ctx, token)
	if _, ok := svc.Resolve(ctx, token); ok {
		t.Error("revoked session still resolves")
	}

	// Expired sessions do not resolve.
	tok2, _ := svc.CreateSession(ctx, "alice")
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE revoked_at IS NULL`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, ok := svc.Resolve(ctx, tok2); ok {
		t.Error("expired session still resolves")
	}

	// Deactivated users lose their sessions.
	tok3, _ := svc.CreateSession(ctx, "alice")
	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = 'alice'`); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, ok := svc.Resolve(ctx, tok3); ok {
		t.Error("inactive user's session still resolves")
	}
}

func TestLoginLockout(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	ctx := context.Background()

	for i := range 5 {
		if _, _, err := svc.Login(ctx, "9999"); err != ErrInvalidPIN {
			t.Fatalf("attempt %d: err = %v, want ErrInvalidPIN", i, err)
		}
	}
	// Locked out now — even the correct PIN is refused.
	if _, _, err := svc.Login(ctx, "1122"); err != ErrLockedOut {
		t.Fatalf("after lockout: err = %v, want ErrLockedOut", err)
	}
	// Manager approval shares the same device-wide lockout: the override
	// endpoint must not be a brute-force oracle.
	if _, err := svc.AuthorizeManager(ctx, "1122"); err != ErrLockedOut {
		t.Fatalf("AuthorizeManager during lockout: err = %v, want ErrLockedOut", err)
	}
}

func TestAuthorizeManager(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	seedOperator(t, db, "boss", "manager", "9876")
	seedOperator(t, db, "root", "super_admin", "5544")
	svc := NewService(db)
	ctx := context.Background()

	if u, err := svc.AuthorizeManager(ctx, "9876"); err != nil || u.ID != "boss" {
		t.Fatalf("manager pin: u=%+v err=%v", u, err)
	}
	// A super_admin outranks manager/admin (ut-docs#761 review finding 2:
	// before this, promoting an operator to super_admin actually stripped
	// their manager-override capability at checkout).
	if u, err := svc.AuthorizeManager(ctx, "5544"); err != nil || u.ID != "root" {
		t.Fatalf("super_admin pin: u=%+v err=%v", u, err)
	}
	// A cashier's PIN does not authorize a manager action (and counts as a failure).
	if _, err := svc.AuthorizeManager(ctx, "1122"); err != ErrInvalidPIN {
		t.Fatalf("cashier pin: err = %v, want ErrInvalidPIN", err)
	}
	if _, err := svc.AuthorizeManager(ctx, "0000"); err != ErrInvalidPIN {
		t.Fatalf("wrong pin: err = %v, want ErrInvalidPIN", err)
	}
}

// TestUser_IsManager_IncludesSuperAdmin covers ut-docs#761 review finding 2:
// IsManager() gates five pages (promotions, country settings, translations,
// kitchen stations, locations) directly by role, and never learned about
// super_admin — so promoting an operator to super_admin, the highest role,
// silently locked them out of every one of those pages. authz.go's own
// comment used to say promoting to super_admin was inert; this diff is what
// makes it not inert, so this gap had to close in the same change.
//
// Historical note (independent review, 2026-08-23): none of those five pages
// gates on IsManager() any more — ut-docs#901 moved locations (and registers)
// and ut-docs#902 moved promotions/country-settings/translations/kitchen-
// stations onto canPerform(d, r, "settings"). This case still matters: it
// pins the manager/admin/super_admin set that "settings" (039's seed) must
// keep mirroring, and auth_page.go's session chip still reads IsManager().
func TestUser_IsManager_IncludesSuperAdmin(t *testing.T) {
	cases := map[string]bool{
		"cashier":     false,
		"manager":     true,
		"admin":       true,
		"super_admin": true,
	}
	for role, want := range cases {
		if got := (User{Role: role}).IsManager(); got != want {
			t.Errorf("IsManager() for role %q = %v, want %v", role, got, want)
		}
	}
}

func TestNeedsFirstBoot(t *testing.T) {
	db := openAuthTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	if fb, err := svc.NeedsFirstBoot(ctx); err != nil || !fb {
		t.Fatalf("empty DB: firstBoot=%v err=%v, want true", fb, err)
	}
	// A user without a PIN still can't log in.
	if _, err := db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('x','x','x','admin')`); err != nil {
		t.Fatal(err)
	}
	if fb, _ := svc.NeedsFirstBoot(ctx); !fb {
		t.Error("user without PIN should still mean first boot")
	}
	seedOperator(t, db, "boss", "admin", "9876")
	if fb, _ := svc.NeedsFirstBoot(ctx); fb {
		t.Error("operator with PIN present, first boot should be over")
	}
}

func TestMiddleware(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	token, err := svc.CreateSession(context.Background(), "alice")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	var gotUser User
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware(inner, svc)

	do := func(path, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Exempt paths pass without a session.
	for _, p := range []string{"/login", "/healthz", "/public/app.css", "/api/auth/login", "/themes/midnight.css",
		"/self-order", "/self-order/", "/api/self-order/checkout"} {
		if rec := do(p, ""); rec.Code != http.StatusOK {
			t.Errorf("exempt %s = %d, want 200", p, rec.Code)
		}
	}
	// "/self-order-anything-else" must NOT be exempt — the prefix match is
	// deliberately anchored to a path boundary ("/self-order" exact or
	// "/self-order/..."), not a bare string prefix.
	if rec := do("/self-order-not-really", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("non-boundary self-order-like path = %d, want 303 redirect to /login (not exempt)", rec.Code)
	}

	// Pages redirect to /login.
	if rec := do("/", ""); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("page without session = %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// HTMX fragment loads must NOT get a 302 (htmx would swap the whole
	// login page into the fragment slot — the PIN pad appeared inside the
	// nav header). They get 401 + HX-Redirect for a real navigation.
	{
		req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
			t.Errorf("htmx without session = %d hx-redirect=%q, want 401 + /login",
				rec.Code, rec.Header().Get("HX-Redirect"))
		}
		if rec.Code == http.StatusSeeOther {
			t.Error("htmx fragment got a 302 — login page would render inside the fragment")
		}
	}

	// APIs get 401 JSON on the response contract.
	rec := do("/api/pos/scan", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api without session = %d, want 401", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" {
		t.Errorf("401 body wrong: %s (err %v)", rec.Body.String(), err)
	}

	// A live session passes and lands the operator in the context.
	if rec := do("/", token); rec.Code != http.StatusOK {
		t.Fatalf("with session = %d, want 200", rec.Code)
	}
	if gotUser.ID != "alice" {
		t.Errorf("context user = %+v, want alice", gotUser)
	}
	if UserIDFrom := gotUser.ID; UserIDFrom != "alice" {
		t.Errorf("user id = %q", UserIDFrom)
	}

	// Garbage cookies redirect too.
	if rec := do("/", "not-a-token"); rec.Code != http.StatusSeeOther {
		t.Errorf("bad token = %d, want 303", rec.Code)
	}
}

// The first-boot pairing trio (ut-docs#289) must be middleware-exempt the
// same way /api/setup/join is: a brand-new till has NO operators, so no
// session can possibly exist — without the exemption the wizard's
// discovery/pairing calls would all 401 before their own NeedsFirstBoot
// gate (which is the real guard, tested in internal/pages) ever ran.
func TestMiddlewareExemptsFirstBootPairingRoutes(t *testing.T) {
	db := openAuthTestDB(t)
	svc := NewService(db)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware(inner, svc)

	for _, p := range []string{
		"/api/setup/join",
		"/api/setup/discover-primaries",
		"/api/setup/pair-start",
		"/api/setup/pair-status",
		// ut-docs#1092: the wizard's install-a-catalog-language action —
		// found missing from exempt() the same way as the routes above
		// (every bare-mux test green, real app 401ing).
		"/api/setup/language",
		// ut-docs#1507: the wizard's install-the-fiscal-plugin action
		// (ut-docs#1180's step-3 tile). Same first-boot-only window and the
		// same failure mode AGAIN — setup_tax_catalog.go's handler doc even
		// claims "Auth-exempt on the same first-boot-only window as POST
		// /api/setup/language", but the middleware entry was never added, so
		// every Install click on a real German till answered the raw JSON
		// 401 below instead of installing. internal/pages' own tests drive
		// the handler on a bare mux, so they never saw the wall.
		"/api/setup/tax-plugin",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("exempt %s = %d, want 200 (first-boot till can never hold a session)", p, rec.Code)
		}
	}
	// The exemption is an exact-path list, not a prefix: an unlisted
	// sibling under /api/setup/ stays behind the session wall.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/anything-else", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unlisted /api/setup/anything-else = %d, want 401 (exact paths only, no prefix match)", rec.Code)
	}
}

func loginFor(t *testing.T, svc *Service, pin string) string {
	t.Helper()
	_, token, err := svc.Login(context.Background(), pin)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return token
}

func setLastSeen(t *testing.T, db *sql.DB, ago time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE sessions SET last_seen_at = ?`, stamp); err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
}

func TestIdleAutoLock(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	var lockedUser string
	svc.SetIdleLockAudit(func(_ context.Context, userID string) { lockedUser = userID })
	token := loginFor(t, svc, "1234")

	// Fresh session resolves.
	if _, ok := svc.Resolve(context.Background(), token); !ok {
		t.Fatal("fresh session should resolve")
	}
	// Idle past the window → revoked + audited.
	setLastSeen(t, db, 11*time.Minute)
	if _, ok := svc.Resolve(context.Background(), token); ok {
		t.Fatal("idle session should be locked")
	}
	if lockedUser != "op1" {
		t.Errorf("idle lock audit got user %q, want op1", lockedUser)
	}
	// Revocation is permanent — activity can't resurrect it.
	setLastSeen(t, db, 0)
	if _, ok := svc.Resolve(context.Background(), token); ok {
		t.Fatal("revoked session must stay dead")
	}
}

func TestIdleAutoLockDisabled(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db) // window 0 = off
	token := loginFor(t, svc, "1234")
	setLastSeen(t, db, 5*time.Hour)
	if _, ok := svc.Resolve(context.Background(), token); !ok {
		t.Fatal("idle lock disabled: long-idle session should still resolve")
	}
}

func TestIdleActivityRefreshesLastSeen(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	token := loginFor(t, svc, "1234")

	// Old-but-inside-window activity: resolves AND touches last_seen so the
	// session survives past the original stamp + window.
	setLastSeen(t, db, 9*time.Minute)
	if _, ok := svc.Resolve(context.Background(), token); !ok {
		t.Fatal("session inside window should resolve")
	}
	var lastSeen string
	if err := db.QueryRow(`SELECT last_seen_at FROM sessions`).Scan(&lastSeen); err != nil {
		t.Fatalf("read last_seen: %v", err)
	}
	ts, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		t.Fatalf("parse last_seen %q: %v", lastSeen, err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("last_seen not refreshed by activity: %s", lastSeen)
	}
}

func TestTouchIntervalStaysUnderWindow(t *testing.T) {
	cases := []struct {
		window time.Duration
		want   time.Duration
	}{
		{0, time.Minute},                 // off: housekeeping cadence only
		{time.Minute, 15 * time.Second},  // 1-min window: touch every 15s
		{10 * time.Minute, time.Minute},  // capped at a minute
		{480 * time.Minute, time.Minute}, // capped at a minute
	}
	for _, c := range cases {
		if got := touchInterval(c.window); got != c.want {
			t.Errorf("touchInterval(%v) = %v, want %v", c.window, got, c.want)
		}
	}
}

func TestChangeOwnPIN(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	seedOperator(t, db, "op2", "cashier", "5678")
	svc := NewService(db)
	ctx := context.Background()
	token := loginFor(t, svc, "1234")

	// Wrong current PIN → refused and counts toward the lockout.
	if err := svc.ChangeOwnPIN(ctx, "op1", "0000", "4321"); err != ErrInvalidPIN {
		t.Fatalf("wrong current pin: %v, want ErrInvalidPIN", err)
	}
	// New PIN owned by someone else → refused.
	if err := svc.ChangeOwnPIN(ctx, "op1", "1234", "5678"); err != ErrPINTaken {
		t.Fatalf("taken pin: %v, want ErrPINTaken", err)
	}
	// Bad format → refused.
	if err := svc.ChangeOwnPIN(ctx, "op1", "1234", "12"); err == nil {
		t.Fatal("short pin accepted")
	}
	// Success: old PIN dead, new PIN logs in, old session revoked.
	if err := svc.ChangeOwnPIN(ctx, "op1", "1234", "4321"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, ok := svc.Resolve(ctx, token); ok {
		t.Error("old session must be revoked after a PIN change")
	}
	if _, _, err := svc.Login(ctx, "1234"); err != ErrInvalidPIN {
		t.Errorf("old pin still works: %v", err)
	}
	if u, _, err := svc.Login(ctx, "4321"); err != nil || u.ID != "op1" {
		t.Errorf("new pin login: %+v %v", u, err)
	}
}

// TestCan covers Service.Can (#554): granted, denied, and unknown-action —
// the method is a thin pass-through to AuthRepo.HasPermission, so this is
// mainly confirming the plumbing (User.Role reaches the repo query) rather
// than re-testing the seed data itself (covered in internal/data).
func TestCan(t *testing.T) {
	db := openAuthTestDB(t)
	if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES ('manager', 'refund', 1)`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db)
	ctx := context.Background()

	manager := User{ID: "boss", Role: "manager"}
	cashier := User{ID: "alice", Role: "cashier"}

	if can, err := svc.Can(ctx, manager, "refund"); err != nil || !can {
		t.Fatalf("Can(manager, refund) = %v, %v; want true, nil", can, err)
	}
	if can, err := svc.Can(ctx, cashier, "refund"); err != nil || can {
		t.Fatalf("Can(cashier, refund) = %v, %v; want false, nil", can, err)
	}
	if can, err := svc.Can(ctx, manager, "no-such-action"); err != nil || can {
		t.Fatalf("Can(manager, unknown action) = %v, %v; want false, nil", can, err)
	}
}
