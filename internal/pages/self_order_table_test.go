package pages

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// selfOrderGuest simulates ONE guest's browser against the self-order
// handlers (ut-docs#2261, ADR-0103): a real net/http/cookiejar threaded
// through every request, so the ut_self_order_session cookie that
// GET /self-order?table= mints reaches the /api/self-order/* handlers
// exactly the way a browser would send it — including the jar's real
// Path/host matching, so a cookie scoped too narrowly to reach the API
// routes is withheld here just as a browser would withhold it. Two guests
// are two jars; a raw mux.ServeHTTP with no jar is a cookieless request.
type selfOrderGuest struct {
	t   *testing.T
	mux http.Handler
	jar *cookiejar.Jar
}

func newSelfOrderGuest(t *testing.T, mux http.Handler) *selfOrderGuest {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &selfOrderGuest{t: t, mux: mux, jar: jar}
}

func (g *selfOrderGuest) cookieURL(path string) *url.URL {
	// httptest.NewRequest's default Host is example.com; the jar keys on it.
	u, err := url.Parse("http://example.com" + path)
	if err != nil {
		g.t.Fatalf("cookieURL(%q): %v", path, err)
	}
	return u
}

func (g *selfOrderGuest) do(req *http.Request) *httptest.ResponseRecorder {
	g.t.Helper()
	u := g.cookieURL(req.URL.Path)
	for _, c := range g.jar.Cookies(u) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)
	g.jar.SetCookies(u, rec.Result().Cookies())
	return rec
}

func (g *selfOrderGuest) get(path string) *httptest.ResponseRecorder {
	g.t.Helper()
	return g.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func (g *selfOrderGuest) post(path, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return g.do(req)
}

// sessionToken is the guest's current session cookie value, "" when the
// browser holds none (never minted, or cleared by a completed checkout).
func (g *selfOrderGuest) sessionToken() string {
	for _, c := range g.jar.Cookies(g.cookieURL("/self-order")) {
		if c.Name == selfOrderSessionCookie {
			return c.Value
		}
	}
	return ""
}

func createSelfOrderTable(t *testing.T, dp *common.Deps, label string, x int) string {
	t.Helper()
	id, err := data.NewPOSRepo(dp.Db).CreateTable(t.Context(), label, "Terrace", 4, "rect", x, 100)
	if err != nil {
		t.Fatalf("CreateTable %s: %v", label, err)
	}
	return id
}

// ut-docs#4338 (ADR-0159 §2): table ordering moved to the cloud, so a
// ?table= on the till's own /self-order is just the walk-up kiosk start: it
// resets the shared KioskEngine, mints no session cookie and never shows a
// busy screen.
func TestSelfOrder_TableParamTakesWalkUpPath(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableID := createSelfOrderTable(t, dp, "T5", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	rec := g.get("/self-order?table=" + tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=...: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == selfOrderSessionCookie {
			t.Fatalf("?table= must not set %s, got %+v", selfOrderSessionCookie, c)
		}
	}
	if g.sessionToken() != "" {
		t.Fatal("no session cookie may be held after a ?table= landing")
	}
	if n := dp.SelfOrderSessions.Len(); n != 0 {
		t.Fatalf("SelfOrderSessions.Len() = %d, want 0", n)
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want empty", got)
	}
	if body := rec.Body.String(); strings.Contains(body, "table_busy") || strings.Contains(body, "This table is already in use") || strings.Contains(body, "We can’t start a new order") {
		t.Fatalf("no busy screen may render: %s", body)
	}
}

// StartSelfOrderSessionSweep joins app.Run's shutdown drain like every other
// Start* loop: cancelling ctx stops it and wg.Wait returns. A Deps without a
// manager (bare test harnesses) is a no-op that registers nothing.
func TestStartSelfOrderSessionSweep_StopsOnContextCancel(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartSelfOrderSessionSweep(ctx, dp, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep loop did not stop after ctx cancel")
	}

	var wg2 sync.WaitGroup
	StartSelfOrderSessionSweep(context.Background(), &common.Deps{}, &wg2)
	wg2.Wait() // must return immediately: nothing registered for a nil manager
}
