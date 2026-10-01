package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ut-docs#2935: a display board (kitchen display, /orders) keeps running
// when nobody touches it, but an idle session on it is narrowed to the
// board — anything else needs the PIN again.

func boardReq(h http.Handler, method, path, referer, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("HX-Request", "true")
	if referer != "" {
		req.Header.Set("Referer", "http://till.local:8080"+referer)
	}
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDisplayBoardPollsNeverExtendTheSession(t *testing.T) {
	for _, p := range []string{"/ui/orders", "/ui/kitchen-display/st-1", "/api/orders/stream"} {
		db, h, token := pollRig(t)
		setLastSeen(t, db, 9*time.Minute)
		if rec := boardReq(h, http.MethodGet, p, "", token); rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d", p, rec.Code)
		}
		if ago := lastSeenAgo(t, db); ago < 9*time.Minute-5*time.Second {
			t.Errorf("%s extended the session: last_seen %v ago, want ~9m", p, ago)
		}
	}
}

func TestIdleBoardKeepsRunningButNothingElse(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	audits := 0
	svc.SetIdleLockAudit(func(context.Context, string) { audits++ })
	token := loginFor(t, svc, "1234")
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), svc)

	setLastSeen(t, db, 11*time.Minute)
	const board = "/kitchen-display/st-1"
	for _, c := range []struct{ method, path, referer string }{
		{http.MethodGet, "/ui/kitchen-display/st-1", board},
		{http.MethodGet, "/api/orders/stream", board},
		{http.MethodGet, board, ""}, // a reload of the board page itself
		{http.MethodGet, "/orders", ""},
		{http.MethodGet, "/ui/orders", "/orders"},
		{http.MethodPost, "/api/orders/R-1/status", board},
		{http.MethodPost, "/api/print/kitchen", board},
		{http.MethodGet, "/ui/net-status", board},                   // status chips keep working
		{http.MethodPost, "/api/window/input-heartbeat", board},     // a kitchen tap after a lull
		{http.MethodPost, "/api/window/input-heartbeat", "/orders"}, // same on the order board
	} {
		if rec := boardReq(h, c.method, c.path, c.referer, token); rec.Code != http.StatusOK {
			t.Fatalf("%s %s (from %q) on an idle board: got %d, want 200", c.method, c.path, c.referer, rec.Code)
		}
	}
	if ago := lastSeenAgo(t, db); ago < 11*time.Minute-5*time.Second {
		t.Errorf("board traffic extended a narrowed session: last_seen %v ago", ago)
	}
	if audits != 1 {
		t.Errorf("idle-lock audit fired %d times, want once (when the session was narrowed)", audits)
	}
	if _, ok := svc.ResolveNoTouch(context.Background(), token); ok {
		t.Fatal("a board-only session must not resolve for /login's check or any handler")
	}
	// ResolveNoTouch above revoked it; every board request now goes to the keypad.
	if rec := boardReq(h, http.MethodGet, "/ui/kitchen-display/st-1", board, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("board poll after revoke: got %d, want 401", rec.Code)
	}
}

func TestBoardOnlySessionLocksOnAnythingElse(t *testing.T) {
	cases := []struct{ method, path, referer string }{
		{http.MethodGet, "/settings", "/kitchen-display/st-1"},
		{http.MethodGet, "/", "/orders"},
		{http.MethodPost, "/api/pos/refund", "/orders"},
		{http.MethodGet, "/orders/R-1", "/orders"},                // a receipt, not the board
		{http.MethodGet, "/ui/net-status", "/settings"},           // a chip on another page
		{http.MethodPost, "/api/orders/R-1/status", "/"},          // a board action off the board
		{http.MethodPost, "/api/print/kitchen", ""},               // no referer: not from the board
		{http.MethodPost, "/api/window/input-heartbeat", "/sale"}, // a tap elsewhere
	}
	for _, c := range cases {
		db, h, token := pollRig(t)
		setLastSeen(t, db, 11*time.Minute)
		if rec := boardReq(h, http.MethodGet, "/ui/orders", "/orders", token); rec.Code != http.StatusOK {
			t.Fatalf("narrowing poll: got %d", rec.Code)
		}
		setLastSeen(t, db, 0) // narrowed is narrowed, however recent the last touch
		if rec := boardReq(h, c.method, c.path, c.referer, token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s (from %q) on a board-only session: got %d, want 401", c.method, c.path, c.referer, rec.Code)
		}
		if rec := boardReq(h, http.MethodGet, "/ui/orders", "/orders", token); rec.Code != http.StatusUnauthorized {
			t.Errorf("after %s %s the session must be revoked, board poll got %d", c.method, c.path, rec.Code)
		}
	}
}

func TestIdleSessionOffTheBoardStillRevokes(t *testing.T) {
	for _, c := range []struct{ method, path, referer string }{
		{http.MethodGet, "/ui/net-status", "/settings"},
		{http.MethodGet, "/ui/net-status", ""},
		{http.MethodPost, "/api/print/kitchen", "/"},
		{http.MethodPost, "/api/window/input-heartbeat", "/"},
		{http.MethodGet, "/settings", ""},
	} {
		db, h, token := pollRig(t)
		setLastSeen(t, db, 11*time.Minute)
		if rec := boardReq(h, c.method, c.path, c.referer, token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s (from %q) past the window: got %d, want 401", c.method, c.path, c.referer, rec.Code)
		}
		if rec := boardReq(h, http.MethodGet, "/ui/orders", "/orders", token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s must have revoked, not narrowed: board poll got %d", c.method, c.path, rec.Code)
		}
	}
}

func TestLiveSessionOnABoardIsUnchanged(t *testing.T) {
	// Inside the window a board action is a real request: it extends the
	// session, and the rest of the till stays reachable.
	db, h, token := pollRig(t)
	setLastSeen(t, db, 9*time.Minute)
	if rec := boardReq(h, http.MethodPost, "/api/orders/R-1/status", "/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("status tap: got %d", rec.Code)
	}
	if ago := lastSeenAgo(t, db); ago > time.Minute {
		t.Errorf("a real board action did not extend the session: %v ago", ago)
	}
	if rec := boardReq(h, http.MethodGet, "/settings", "/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("settings from a live board session: got %d", rec.Code)
	}
}

func TestDisplayBoardMatchers(t *testing.T) {
	for _, p := range []string{"/orders", "/kitchen-display/st-1"} {
		if !displayBoardPage(p) {
			t.Errorf("%s should be a display-board page", p)
		}
	}
	for _, p := range []string{"/orders/R-1", "/kitchen-display/", "/kitchen-display/a/b", "/ui/orders", "", "/kitchen-stations"} {
		if displayBoardPage(p) {
			t.Errorf("%s must not be a display-board page", p)
		}
	}
	for _, p := range []string{"/ui/orders", "/ui/kitchen-display/st-1"} {
		if !displayBoardPoll(p) {
			t.Errorf("%s should be a display-board poll", p)
		}
	}
	for _, p := range []string{"/ui/kitchen-display/", "/ui/kitchen-display/a/b", "/ui/orders/x", "/kitchen-display/st-1"} {
		if displayBoardPoll(p) {
			t.Errorf("%s must not match the display-board poll tier", p)
		}
	}
	for _, p := range []string{"/api/orders//status", "/api/orders/a/b/status", "/api/orders/R-1", "/api/orders/R-1/status/x"} {
		req := httptest.NewRequest(http.MethodPost, "http://till"+p, nil)
		req.Header.Set("Referer", "http://till/orders")
		if displayBoardRequest(req) {
			t.Errorf("POST %s must not be a board request", p)
		}
	}
	// A GET of a board action is not a board request.
	req := httptest.NewRequest(http.MethodGet, "/api/print/kitchen", nil)
	req.Header.Set("Referer", "http://till/orders")
	if displayBoardRequest(req) {
		t.Error("GET /api/print/kitchen must not be a board request")
	}
}

// Review finding (ut-docs#2935): a full reload of an idle board fires the
// shell's load-time requests too; each must stay inside board scope, or the
// reload drops the kitchen screen to the PIN pad.
func TestIdleBoardSurvivesAFullReload(t *testing.T) {
	db, h, token := pollRig(t)
	setLastSeen(t, db, 11*time.Minute)
	const board = "/kitchen-display/st-1"
	if rec := boardReq(h, http.MethodGet, board, "", token); rec.Code != http.StatusOK {
		t.Fatalf("board page: got %d", rec.Code)
	}
	for p := range shellGetPaths {
		if rec := boardReq(h, http.MethodGet, p, board, token); rec.Code != http.StatusOK {
			t.Errorf("shell GET %s from an idle board: got %d, want 200", p, rec.Code)
		}
	}
	for p := range shellPostPaths {
		if rec := boardReq(h, http.MethodPost, p, board, token); rec.Code != http.StatusOK {
			t.Errorf("shell POST %s from an idle board: got %d, want 200", p, rec.Code)
		}
	}
	if rec := boardReq(h, http.MethodGet, "/ui/kitchen-display/st-1", board, token); rec.Code != http.StatusOK {
		t.Fatalf("board poll after the reload: got %d, want 200", rec.Code)
	}
}

// Only the shell's own requests ride along from a board — never another
// page's pollers.
func TestBoardScopeExcludesOtherPagesPollers(t *testing.T) {
	for p := range backgroundPollPaths {
		if shellGetPaths[p] {
			continue
		}
		db, h, token := pollRig(t)
		setLastSeen(t, db, 11*time.Minute)
		if rec := boardReq(h, http.MethodGet, p, "/orders", token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s from a board on an idle session: got %d, want 401", p, rec.Code)
		}
	}
}

func TestNarrowedAuditsOnceAndAutoLockOffUnnarrows(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	audits := 0
	svc.SetIdleLockAudit(func(context.Context, string) { audits++ })
	token := loginFor(t, svc, "1234")
	setLastSeen(t, db, 11*time.Minute)
	for i := 0; i < 3; i++ {
		if _, ok := svc.ResolveFor(context.Background(), token, false, true); !ok {
			t.Fatal("board request on an idle session must be served")
		}
	}
	if audits != 1 {
		t.Fatalf("audit fired %d times, want 1", audits)
	}
	svc.SetIdleLockMinutes(0)
	if _, ok := svc.Resolve(context.Background(), token); !ok {
		t.Fatal("with auto-lock off, a narrowed session must be a normal session again")
	}
}

// TestShellRequestsAreBoardScoped keeps shellGetPaths in step with the
// shell: every hx-get that base.html or nav.html fires by itself (load or
// timer) and the status-bar light's fetch must be listed.
func TestShellRequestsAreBoardScoped(t *testing.T) {
	root := filepath.Join("..", "..")
	tag := regexp.MustCompile(`<[^>]*hx-trigger="[^"]*\b(?:load|every)\b[^"]*"[^>]*>`)
	get := regexp.MustCompile(`hx-get="([^"?{]+)`)
	seen := 0
	for _, f := range []string{"web/ui/layouts/base.html", "web/ui/partials/nav.html"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range tag.FindAllString(string(b), -1) {
			m := get.FindStringSubmatch(tg)
			if m == nil {
				continue
			}
			seen++
			if !shellGetPaths[m[1]] {
				t.Errorf("%s fires GET %s on its own: add it to shellGetPaths in middleware.go", f, m[1])
			}
		}
		if f == "web/ui/layouts/base.html" {
			if m := netStatusURLRe.FindStringSubmatch(string(b)); m == nil || !shellGetPaths[m[1]] {
				t.Errorf("base.html's status-bar light poll must be in shellGetPaths")
			}
			if !strings.Contains(string(b), "'/api/diag/reload-reason'") || !shellPostPaths["/api/diag/reload-reason"] {
				t.Errorf("base.html's reload-reason beacon must be in shellPostPaths")
			}
		}
	}
	if seen < 5 {
		t.Fatalf("scan found only %d shell requests — the scan is broken", seen)
	}
}
