package auth

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// ut-docs#2901: a till left untouched on any base.html page must still
// idle-lock. Timer-driven polls are resolved without touching last_seen_at;
// only a real request (or the real-input heartbeat) extends the session.

// backgroundPollPathList is backgroundPollPaths' keys, sorted.
func backgroundPollPathList() []string {
	out := make([]string, 0, len(backgroundPollPaths))
	for p := range backgroundPollPaths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func lastSeenAgo(t *testing.T, db *sql.DB) time.Duration {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT last_seen_at FROM sessions`).Scan(&s); err != nil {
		t.Fatalf("read last_seen: %v", err)
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse last_seen %q: %v", s, err)
	}
	return time.Since(ts)
}

func pollRig(t *testing.T) (*sql.DB, http.Handler, string) {
	t.Helper()
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	token := loginFor(t, svc, "1234")
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), svc)
	return db, h, token
}

func htmxGet(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBackgroundPollsNeverExtendTheSession(t *testing.T) {
	db, h, token := pollRig(t)
	paths := backgroundPollPathList()
	if len(paths) == 0 {
		t.Fatal("no background poll paths registered")
	}
	// Nine minutes untouched, then every poller fires several times (a
	// sale screen polls every 3-5 s): all served, none moves last_seen.
	setLastSeen(t, db, 9*time.Minute)
	for round := 0; round < 3; round++ {
		for _, p := range paths {
			if rec := htmxGet(h, p+"?v=1", token); rec.Code != http.StatusOK {
				t.Fatalf("poll %s inside the window: got %d, want 200", p, rec.Code)
			}
		}
	}
	if ago := lastSeenAgo(t, db); ago < 9*time.Minute-5*time.Second {
		t.Fatalf("background polls extended the session: last_seen %v ago, want ~9m", ago)
	}
	// Past the window the next poll finds the session revoked and sends
	// the page to the keypad.
	setLastSeen(t, db, 11*time.Minute)
	rec := htmxGet(h, "/ui/buttons/version", token)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("poll past the window: got %d HX-Redirect=%q, want 401 /login", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	// And stays locked: no other poll (or a real request) revives it.
	setLastSeen(t, db, 0)
	if rec := htmxGet(h, "/", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session answered %d on a real request", rec.Code)
	}
}

func TestRealRequestStillExtendsTheSession(t *testing.T) {
	for _, p := range []string{"/", "/ui/buttons", "/api/window/input-heartbeat", "/settings"} {
		db, h, token := pollRig(t)
		setLastSeen(t, db, 9*time.Minute)
		if rec := htmxGet(h, p, token); rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d", p, rec.Code)
		}
		if ago := lastSeenAgo(t, db); ago > time.Minute {
			t.Errorf("%s did not extend the session: last_seen %v ago", p, ago)
		}
	}
}

// Display boards are watched, not touched; they keep today's behaviour
// until the display-device auto-lock decision lands (ut-docs#2935).
func TestDisplayBoardPollsStillExtendTheSession(t *testing.T) {
	for _, p := range []string{"/ui/orders", "/ui/kiosk-counter-orders", "/ui/kitchen-display/st-1"} {
		if !displayBoardPoll(p) {
			t.Fatalf("%s should be a display-board poll", p)
		}
		db, h, token := pollRig(t)
		setLastSeen(t, db, 9*time.Minute)
		if rec := htmxGet(h, p, token); rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d", p, rec.Code)
		}
		if ago := lastSeenAgo(t, db); ago > time.Minute {
			t.Errorf("%s did not extend the session: last_seen %v ago", p, ago)
		}
	}
	for _, p := range []string{"/ui/kitchen-display/", "/ui/kitchen-display/a/b", "/ui/orders/x", "/kitchen-display/st-1"} {
		if displayBoardPoll(p) {
			t.Errorf("%s must not match the display-board tier", p)
		}
	}
}

func TestResolveNoTouch(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "op1", "cashier", "1234")
	svc := NewService(db)
	svc.SetIdleLockMinutes(10)
	var locked string
	svc.SetIdleLockAudit(func(_ context.Context, userID string) { locked = userID })
	token := loginFor(t, svc, "1234")

	setLastSeen(t, db, 9*time.Minute)
	if u, ok := svc.ResolveNoTouch(context.Background(), token); !ok || u.ID != "op1" {
		t.Fatalf("inside the window: got %+v %v", u, ok)
	}
	if ago := lastSeenAgo(t, db); ago < 9*time.Minute-5*time.Second {
		t.Fatalf("ResolveNoTouch wrote last_seen (%v ago)", ago)
	}
	setLastSeen(t, db, 11*time.Minute)
	if _, ok := svc.ResolveNoTouch(context.Background(), token); ok {
		t.Fatal("ResolveNoTouch must still revoke an idle session")
	}
	if locked != "op1" {
		t.Errorf("idle-lock audit got %q, want op1", locked)
	}
}

// pollerTag finds an element whose hx-trigger fires on a timer ("every").
var (
	pollTriggerRe = regexp.MustCompile(`hx-trigger=(?:\\?"[^"\\]*|'[^']*)\bevery\b`)
	pollURLRe     = regexp.MustCompile(`hx-(?:get|post)=(?:\\?"([^"\\]*)|'([^']*))`)
	jsPollURLRe   = regexp.MustCompile(`var VERSION_URL = '([^']+)'`)
)

// dynamicPollURLs resolves the templated hx-get values of the pollers that
// have one; each entry lists every path the template is ever given.
var dynamicPollURLs = map[string][]string{
	"web/ui/partials/orders_list.html":  {"/ui/orders", "/ui/kitchen-display/st-1"},
	"web/ui/partials/pairing_wait.html": {"/api/sync/pair-status", "/api/setup/pair-status"},
	// The customer's own order-tracking page: no session at all.
	"web/ui/partials/order_tracking_status.html": {"/o/tok/status"},
}

// TestEveryPollerIsClassified is the guard behind backgroundPoll's
// "every new poller must be added" rule: each timer-driven hx-get in the
// templates and pages (and sell-screen-watch.js's fetch poll) must be
// exempt, a background poll, or a display board.
//
// Limits: it reads markup (both quote styles, and Go string literals
// including fmt-built tags), a tag must not contain '>' inside an attribute,
// and JS timers are only flagged by file (setInterval in web/public, not
// vendor/ subdirectories). EventSource streams (/api/orders/stream) are
// long-lived and opened only by the display boards, so they are out of
// scope here.
func TestEveryPollerIsClassified(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string][]string{} // path -> files
	add := func(file, p string) {
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
		found[p] = append(found[p], file)
	}
	scan := func(dir string, keep func(string) bool) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !keep(path) {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			src := string(b)
			for _, loc := range pollTriggerRe.FindAllStringIndex(src, -1) {
				lineStart := strings.LastIndexByte(src[:loc[0]], '\n') + 1
				if strings.HasPrefix(strings.TrimSpace(src[lineStart:loc[0]]), "//") {
					continue // prose in a Go comment, not markup
				}
				start := strings.LastIndexByte(src[:loc[0]], '<')
				end := strings.IndexByte(src[loc[1]:], '>')
				if start < 0 || end < 0 {
					t.Errorf("%s: timer hx-trigger outside a tag at byte %d", rel, loc[0])
					continue
				}
				tag := src[start : loc[1]+end]
				m := pollURLRe.FindStringSubmatch(tag)
				if m == nil {
					t.Errorf("%s: timer hx-trigger with no hx-get/hx-post: %s", rel, tag)
					continue
				}
				if m[1] == "" {
					m[1] = m[2]
				}
				if strings.Contains(m[1], "{{") {
					dyn, ok := dynamicPollURLs[rel]
					if !ok {
						t.Errorf("%s: templated poll URL %q — list what it resolves to in dynamicPollURLs", rel, m[1])
					}
					for _, p := range dyn {
						add(rel, p)
					}
					continue
				}
				add(rel, m[1])
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	scan("web/ui", func(p string) bool { return strings.HasSuffix(p, ".html") })
	scan("internal", func(p string) bool { return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") })

	js, err := os.ReadFile(filepath.Join(root, "web", "public", "sell-screen-watch.js"))
	if err != nil {
		t.Fatalf("read sell-screen-watch.js: %v", err)
	}
	m := jsPollURLRe.FindStringSubmatch(string(js))
	if m == nil {
		t.Fatal("sell-screen-watch.js: VERSION_URL not found — keep this guard pointed at its poll URL")
	}
	add("web/public/sell-screen-watch.js", m[1])

	// A JS timer is a poller the tag scan can't see. The known ones: the
	// idle-lock timer itself (app.js, no request) and sell-screen-watch.js
	// (VERSION_URL, checked above). A new setInterval anywhere else in
	// web/public must be looked at, and its poll path classified.
	jsTimers := map[string]bool{"app.js": true, "sell-screen-watch.js": true}
	entries, err := os.ReadDir(filepath.Join(root, "web", "public"))
	if err != nil {
		t.Fatalf("read web/public: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") || jsTimers[e.Name()] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, "web", "public", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if strings.Contains(string(b), "setInterval(") {
			t.Errorf("web/public/%s starts a setInterval timer: if it polls the server, classify its path in middleware.go and add the file to jsTimers here", e.Name())
		}
	}

	// The scan must actually see the pollers this card is about.
	for _, want := range []string{"/ui/buttons/version", "/ui/open-orders-badge/watch", "/ui/main-till-status", "/ui/sync-chip"} {
		if _, ok := found[want]; !ok {
			t.Errorf("scanner did not find the %s poller — the scan is broken", want)
		}
	}
	var paths []string
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !exempt(p) && !backgroundPoll(p) && !displayBoardPoll(p) {
			t.Errorf("poller %s (%s) is unclassified: add it to backgroundPollPaths in middleware.go (or displayBoardPoll if it is a watched-not-touched board)",
				p, strings.Join(found[p], ", "))
		}
	}
	// And the list carries nothing stale.
	for _, p := range backgroundPollPathList() {
		if _, ok := found[p]; !ok {
			t.Errorf("backgroundPollPaths lists %s, but nothing polls it any more — remove it", p)
		}
	}
}
