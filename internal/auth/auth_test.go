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
		 created_at TEXT NOT NULL DEFAULT (datetime('now')), expires_at TEXT NOT NULL, revoked_at TEXT)`,
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
	svc := NewService(db)
	ctx := context.Background()

	if u, err := svc.AuthorizeManager(ctx, "9876"); err != nil || u.ID != "boss" {
		t.Fatalf("manager pin: u=%+v err=%v", u, err)
	}
	// A cashier's PIN does not authorize a manager action (and counts as a failure).
	if _, err := svc.AuthorizeManager(ctx, "1122"); err != ErrInvalidPIN {
		t.Fatalf("cashier pin: err = %v, want ErrInvalidPIN", err)
	}
	if _, err := svc.AuthorizeManager(ctx, "0000"); err != ErrInvalidPIN {
		t.Fatalf("wrong pin: err = %v, want ErrInvalidPIN", err)
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
	for _, p := range []string{"/login", "/healthz", "/public/app.css", "/api/auth/login", "/themes/midnight.css"} {
		if rec := do(p, ""); rec.Code != http.StatusOK {
			t.Errorf("exempt %s = %d, want 200", p, rec.Code)
		}
	}

	// Pages redirect to /login.
	if rec := do("/", ""); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("page without session = %d loc=%q", rec.Code, rec.Header().Get("Location"))
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
