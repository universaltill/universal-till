package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#2901: the idle auto-lock's client timer navigates to /login. That
// visit is not activity: /login must not refresh the session and bounce the
// till back to "/", and once the server window has passed it must show the
// keypad.
func TestLoginVisitDoesNotExtendAnIdleSession(t *testing.T) {
	withOSLocale(t, "", "")
	mux, svc, d := newAuthTestMux(t)
	rec := postForm(mux, "/api/auth/setup", url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}}, nil)
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatalf("setup did not sign in: code=%d", rec.Code)
	}
	svc.SetIdleLockMinutes(10)
	setAgo := func(ago time.Duration) {
		t.Helper()
		if _, err := d.Db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-ago).UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	getLogin := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Inside the window a live session still skips the keypad…
	setAgo(9 * time.Minute)
	if rec := getLogin(); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("live session on /login: code=%d loc=%q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}
	// …but that visit did not reset the idle clock.
	var s string
	if err := d.Db.QueryRow(`SELECT last_seen_at FROM sessions`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if ts, _ := time.Parse(time.RFC3339, s); time.Since(ts) < 9*time.Minute-5*time.Second {
		t.Fatalf("/login refreshed last_seen (%s)", s)
	}
	// Past the window: the keypad, and the session is gone for good.
	setAgo(11 * time.Minute)
	if rec := getLogin(); rec.Code != http.StatusOK {
		t.Fatalf("idle session on /login: code=%d loc=%q, want the keypad (200)", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := svc.Resolve(t.Context(), cookie); ok {
		t.Fatal("idle session survived /login")
	}
}
