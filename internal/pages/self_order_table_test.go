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
	"github.com/universaltill/universal-till/internal/pos"
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

// engine is the basket this guest's requests resolve to right now: their
// live session when they hold a recognised cookie, else nil.
func (g *selfOrderGuest) engine(dp *common.Deps) *pos.Service {
	svc, _ := dp.SelfOrderSessions.Get(g.sessionToken())
	return svc
}

func createSelfOrderTable(t *testing.T, dp *common.Deps, label string, x int) string {
	t.Helper()
	id, err := data.NewPOSRepo(dp.Db).CreateTable(t.Context(), label, "Terrace", 4, "rect", x, 100)
	if err != nil {
		t.Fatalf("CreateTable %s: %v", label, err)
	}
	return id
}

// ut-docs#815: GET /self-order?table=<id> binds a basket to a physical table
// (ADR-0054/ut-docs#820's existing pos.Service TableID/TableLabel fields) so
// it's available all the way through to checkout. Since ut-docs#2261
// (ADR-0103) that basket is the guest's OWN per-session one, reached through
// the session cookie the response sets — not the till-global KioskEngine,
// which the table path must leave alone.
func TestSelfOrder_TableParam_BindsValidEnabledTable(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableID := createSelfOrderTable(t, dp, "T5", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	rec := g.get("/self-order?table=" + tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=...: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if g.sessionToken() == "" {
		t.Fatal("a valid table scan must set the session cookie")
	}
	eng := g.engine(dp)
	if eng == nil {
		t.Fatal("the session cookie must resolve to a live session")
	}
	if got := eng.TableID(); got != tableID {
		t.Fatalf("session TableID() = %q, want %q", got, tableID)
	}
	if got := eng.TableLabel(); got != "T5" {
		t.Fatalf("session TableLabel() = %q, want %q", got, "T5")
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\" — a table scan must never bind the walk-up kiosk basket", got)
	}
	var setCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == selfOrderSessionCookie {
			setCookie = c
		}
	}
	if setCookie == nil {
		t.Fatal("no Set-Cookie for the session")
	}
	if !setCookie.HttpOnly || setCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie must be HttpOnly + SameSite=Lax (auth_page.go's setSessionCookie shape), got %+v", setCookie)
	}
}

// The session cookie must actually reach the /api/self-order/* handlers —
// the whole browse/customize/cart/checkout flow lives there, not under
// /self-order. Cookie path matching is prefix-based, so a cookie scoped to
// Path=/self-order would be withheld from /api/self-order/... by every
// browser and each API call would silently fall back to the shared
// KioskEngine. The jar applies the same rule, so this pins it for real.
func TestSelfOrder_SessionCookieReachesAPIRoutes(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableID := createSelfOrderTable(t, dp, "T5", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableID)

	for _, p := range []string{"/api/self-order/cart", "/api/self-order/scan", "/api/self-order/checkout", "/self-order/shop"} {
		found := false
		for _, c := range g.jar.Cookies(g.cookieURL(p)) {
			if c.Name == selfOrderSessionCookie {
				found = true
			}
		}
		if !found {
			t.Fatalf("the session cookie would not be sent to %s — its Path scope is too narrow", p)
		}
	}
}

// A disabled table must not bind — same "table not usable" rule the guest
// checkout flow (the tables page itself) already enforces elsewhere, and
// exactly the shape of table id an old/reprinted QR could carry after a
// table gets deactivated. No session is minted for it either.
func TestSelfOrder_TableParam_DisabledTableFallsBackUnchanged(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableID := createSelfOrderTable(t, dp, "T5", 100)
	if err := data.NewPOSRepo(d.DB).SetTableEnabled(t.Context(), tableID, false); err != nil {
		t.Fatalf("SetTableEnabled: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	rec := g.get("/self-order?table=" + tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<disabled>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\" (disabled table must not bind)", got)
	}
	if g.sessionToken() != "" || dp.SelfOrderSessions.Len() != 0 {
		t.Fatalf("a disabled table must not mint a session: cookie=%q live=%d", g.sessionToken(), dp.SelfOrderSessions.Len())
	}
}

// An unknown table id (stale/tampered QR) must not error the page out —
// same unchanged-behaviour fallback as no table param at all.
func TestSelfOrder_TableParam_UnknownIDFallsBackUnchanged(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	rec := g.get("/self-order?table=does-not-exist")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<unknown>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\"", got)
	}
	if g.sessionToken() != "" || dp.SelfOrderSessions.Len() != 0 {
		t.Fatalf("an unknown table must not mint a session: cookie=%q live=%d", g.sessionToken(), dp.SelfOrderSessions.Len())
	}
}

// No ?table param at all: the bare walk-up/physical-kiosk path is byte-for-
// byte today's behaviour (ADR-0103 Decision 2, ADR-0020's one-kiosk-one-
// basket model): straight onto KioskEngine, reset on every landing, no
// session, no cookie.
func TestSelfOrder_NoTableParam_LeavesNoTableBound(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	dp.KioskEngine.AddLineWithModifiers(pos.BasketLine{SKU: "X", Name: "Leftover", ItemID: "x", PriceCents: 100}, 1, nil)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	g := newSelfOrderGuest(t, mux)

	rec := g.get("/self-order")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\"", got)
	}
	if n := len(dp.KioskEngine.Lines()); n != 0 {
		t.Fatalf("a bare landing must still reset the kiosk basket, got %d lines", n)
	}
	if g.sessionToken() != "" || dp.SelfOrderSessions.Len() != 0 {
		t.Fatalf("the walk-up path must never mint a session: cookie=%q live=%d", g.sessionToken(), dp.SelfOrderSessions.Len())
	}
}

// ut-docs#2261 / ADR-0103 Decision 1: a DIFFERENT table's guest gets their
// own independent session and empty basket — the opposite of the old
// single-basket "till busy" fail-safe (ut-docs#815's review), which this
// card replaces with genuine concurrency. Table A's session is untouched.
func TestSelfOrder_DifferentTable_GetsOwnIndependentSession(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	tableB := createSelfOrderTable(t, dp, "T2", 200)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// Table A's guest scans in and adds a real line.
	a := newSelfOrderGuest(t, mux)
	a.get("/self-order?table=" + tableA)
	a.post("/api/self-order/scan", "code=5000001")
	if got := a.engine(dp).TableID(); got != tableA {
		t.Fatalf("precondition: A's TableID() = %q, want tableA %q", got, tableA)
	}
	if n := len(a.engine(dp).Lines()); n != 1 {
		t.Fatalf("precondition: A's basket has %d lines, want 1", n)
	}

	// Table B's guest scans their own QR from their own phone before A has
	// checked out.
	b := newSelfOrderGuest(t, mux)
	rec := b.get("/self-order?table=" + tableB)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableB>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("a different table must get its own session, never the busy screen: %s", rec.Body.String())
	}
	if b.engine(dp) == nil || b.engine(dp) == a.engine(dp) {
		t.Fatal("B must get its own live session, distinct from A's")
	}
	if got := b.engine(dp).TableID(); got != tableB {
		t.Fatalf("B's TableID() = %q, want tableB %q", got, tableB)
	}
	if n := len(b.engine(dp).Lines()); n != 0 {
		t.Fatalf("B's fresh basket has %d lines, want 0", n)
	}
	// Table A's order is completely untouched.
	if got := a.engine(dp).TableID(); got != tableA {
		t.Fatalf("A's TableID() = %q, want unchanged tableA %q", got, tableA)
	}
	if n := len(a.engine(dp).Lines()); n != 1 {
		t.Fatalf("A's basket has %d lines, want unchanged 1", n)
	}
	if n := dp.SelfOrderSessions.Len(); n != 2 {
		t.Fatalf("live sessions = %d, want 2", n)
	}
	if dp.KioskEngine.TableID() != "" || len(dp.KioskEngine.Lines()) != 0 {
		t.Fatal("the walk-up KioskEngine must stay untouched by table sessions")
	}
}

// Re-scanning your OWN table's code (the idle-reset bounce, or a deliberate
// re-open) is never "busy" — it's the same guest, and it RESUMES their
// session: same token, same table, basket kept (ADR-0103 Decision 2).
func TestSelfOrder_SameTableRescan_NeverBusy(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableA)
	g.post("/api/self-order/scan", "code=5000001")
	token := g.sessionToken()

	rec := g.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<same table>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("re-scanning the same table must never show the busy screen, got: %s", rec.Body.String())
	}
	if got := g.sessionToken(); got != token {
		t.Fatalf("a same-table re-scan must resume the session, got a new token %q (was %q)", got, token)
	}
	if got := g.engine(dp).TableID(); got != tableA {
		t.Fatalf("TableID() = %q, want %q", got, tableA)
	}
	if n := len(g.engine(dp).Lines()); n != 1 {
		t.Fatalf("basket has %d lines after a re-scan, want 1 — resuming must not reset the guest's order", n)
	}
	if n := dp.SelfOrderSessions.Len(); n != 1 {
		t.Fatalf("live sessions = %d, want 1 (no duplicate minted on resume)", n)
	}
}

// An EMPTY table-bound session (guest scanned in but never added anything)
// has nothing to lose — a second phone scanning the same table gets its own
// session normally, not a "busy" screen (the narrowed guard is non-empty
// sessions only, same threshold the old cross-table guard used).
func TestSelfOrder_SameTableEmptySession_NotBusy(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	first := newSelfOrderGuest(t, mux)
	first.get("/self-order?table=" + tableA)

	second := newSelfOrderGuest(t, mux)
	rec := second.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableA> from a second phone: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("an empty table-bound session has nothing to lose — must not show busy, got: %s", rec.Body.String())
	}
	if second.engine(dp) == nil || second.engine(dp).TableID() != tableA {
		t.Fatal("the second phone must get its own live session bound to the table")
	}
	if first.engine(dp) == nil || first.engine(dp) == second.engine(dp) {
		t.Fatal("the first phone's session must survive, distinct from the second's")
	}
}

// ADR-0103 Decision 4, the one collision still blocked: the SAME table
// already has a live, NON-EMPTY session owned by a different browser (two
// phones at one table, or a phone with no cookie at all). The busy screen
// (unchanged template, unchanged i18n keys) renders, no session is minted,
// and the first guest's order is untouched.
func TestSelfOrder_SameTableHeldByAnotherSession_ShowsBusy(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	owner := newSelfOrderGuest(t, mux)
	owner.get("/self-order?table=" + tableA)
	owner.post("/api/self-order/scan", "code=5000001")
	ownerToken := owner.sessionToken()

	// A second phone with its own (empty) jar.
	other := newSelfOrderGuest(t, mux)
	rec := other.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<held table>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("expected the busy screen for a table another session holds with items, got: %s", rec.Body.String())
	}
	if other.sessionToken() != "" {
		t.Fatal("a busy-blocked scan must not mint a session cookie")
	}

	// A request carrying no cookie at all (e.g. a browser that blocks
	// cookies) is the same case.
	raw := httptest.NewRecorder()
	mux.ServeHTTP(raw, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	if !strings.Contains(raw.Body.String(), "This table already has an order in progress") {
		t.Fatalf("a cookieless scan of a held table must see the busy screen, got: %s", raw.Body.String())
	}

	if n := dp.SelfOrderSessions.Len(); n != 1 {
		t.Fatalf("live sessions = %d, want 1 (only the owner's)", n)
	}
	if got := owner.sessionToken(); got != ownerToken {
		t.Fatalf("owner's token changed to %q", got)
	}
	if n := len(owner.engine(dp).Lines()); n != 1 {
		t.Fatalf("owner's basket has %d lines, want unchanged 1", n)
	}
	// The owner is still not busy on their own re-scan.
	if rec := owner.get("/self-order?table=" + tableA); strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatal("the owning guest must never be blocked from their own table")
	}
}

// ut-docs#2432 (ut-docs#2261 review finding N1): a cookieless request
// looping GET /self-order?table=<empty, enabled table> minted a brand new
// session every single hit — nothing blocked it, since the busy guard only
// fires for a table with a NON-empty session. Past the per-source rate
// limit, further mint attempts from the same source must see the existing
// busy screen instead of growing SelfOrderSessions without bound.
func TestSelfOrder_RepeatedCookielessScans_RateLimitedInsteadOfUnboundedMinting(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	const attempts = 30 // over the 20/min limiter cap
	busySeen := 0
	for i := 0; i < attempts; i++ {
		// A fresh, cookieless request every time — the attack shape: no
		// cookie jar, so the resume branch never applies and every hit that
		// isn't refused would otherwise mint a new orphaned session.
		req := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: want 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
			busySeen++
		}
	}
	if busySeen == 0 {
		t.Fatal("expected the rate limiter to start showing the busy screen well before 30 unbounded mints")
	}
	if n := dp.SelfOrderSessions.Len(); n >= attempts {
		t.Fatalf("live sessions = %d after %d looped requests — the rate limiter did not bound minting", n, attempts)
	}
	if n := dp.SelfOrderSessions.Len(); n > 20 {
		t.Fatalf("live sessions = %d, want at most the limiter's cap of 20", n)
	}
}

// The rate limiter must only ever gate NEW-session minting — a guest
// resuming their OWN session by cookie (the idle-reset bounce, a page
// refresh) must never be refused, no matter how many times they do it from
// the same source.
func TestSelfOrder_CookieResume_NeverRateLimited(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableA)
	g.post("/api/self-order/scan", "code=5000001")
	token := g.sessionToken()

	const resumes = 30 // over the 20/min mint limiter — must not matter for a resume
	for i := 0; i < resumes; i++ {
		rec := g.get("/self-order?table=" + tableA)
		if rec.Code != http.StatusOK {
			t.Fatalf("resume %d: want 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
			t.Fatalf("resume %d: a cookie-holding resume must never be rate-limited/busy", i)
		}
		if got := g.sessionToken(); got != token {
			t.Fatalf("resume %d: token changed to %q (was %q) — a resume must never re-mint", i, got, token)
		}
	}
	if n := dp.SelfOrderSessions.Len(); n != 1 {
		t.Fatalf("live sessions = %d after %d resumes, want 1 (no duplicate minted)", n, resumes)
	}
}

// ut-docs#2432: the session-count cap (pos.MaxLiveSelfOrderSessions) is the
// backstop behind the per-source rate limiter — it must refuse a mint even
// from a source that has never been seen before (so the limiter itself
// can't be what's blocking it), and must do so cleanly: no panic, no 500,
// the same busy screen as any other refused mint.
func TestSelfOrder_SessionCapAlone_ShowsBusyNotPanic(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)

	// Fill the manager directly to the cap — isolates the cap from the
	// per-source rate limiter, which these calls never go through. Each
	// prefilled session holds a real item: an EMPTY one would just be
	// evicted by Create's evict-oldest-empty step (independent review
	// finding 1), so genuine refusal at the cap can only be exercised when
	// every live session actually has an order in it.
	for i := 0; i < pos.MaxLiveSelfOrderSessions; i++ {
		_, svc, ok := dp.SelfOrderSessions.Create()
		if !ok {
			t.Fatalf("prefill Create refused at i=%d, want it to succeed up to the cap", i)
		}
		svc.AddLineWithModifiers(pos.BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 1000}, 1, nil)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	// A single, first-ever request from this source against a table nothing
	// has touched yet — the rate limiter has no history for it, so only the
	// cap can be why this refuses.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("at the session cap: want 200 (busy screen, not an error), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("at the session cap: expected the busy screen, got: %s", rec.Body.String())
	}
	if n := dp.SelfOrderSessions.Len(); n != pos.MaxLiveSelfOrderSessions {
		t.Fatalf("live sessions = %d, want unchanged at the cap %d (a refused mint must not grow the map)", n, pos.MaxLiveSelfOrderSessions)
	}
}

// ut-docs#2432, independent review finding 1 (HIGH): the ORIGINAL cap fix
// let one source hold the whole shop's ordering hostage by filling the map
// with cookieless, itemless mints — every OTHER table's guest would then
// see the busy screen too, with no real order behind any of it, until the
// 2h idle sweep. This drives the real HTTP handler end to end: fill the
// manager to the cap with sessions indistinguishable from that attack
// (never touched again after minting, exactly what a cookieless GET loop
// produces), then prove a brand-new guest at a DIFFERENT, never-before-seen
// table still gets served, not the busy screen.
func TestSelfOrder_MapFullOfEmptySessions_NewGuestAtDifferentTableStillServed(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	victimTable := createSelfOrderTable(t, dp, "T-victim", 100)

	for i := 0; i < pos.MaxLiveSelfOrderSessions; i++ {
		if _, _, ok := dp.SelfOrderSessions.Create(); !ok {
			t.Fatalf("prefill Create refused at i=%d, want it to succeed up to the cap", i)
		}
	}
	if n := dp.SelfOrderSessions.Len(); n != pos.MaxLiveSelfOrderSessions {
		t.Fatalf("live sessions = %d, want %d after prefill", n, pos.MaxLiveSelfOrderSessions)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/self-order?table="+victimTable, nil)
	req.RemoteAddr = "203.0.113.55:9999" // a source with no rate-limiter history at all
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatal("a map full of EMPTY sessions must never show a real guest at a fresh table the busy screen — evict-oldest-empty must have made room")
	}
	if rec.Result().Cookies() == nil {
		t.Fatal("expected a session cookie to be set — the guest must have actually been bound, not just avoided the busy screen by accident")
	}
	if n := dp.SelfOrderSessions.Len(); n != pos.MaxLiveSelfOrderSessions {
		t.Fatalf("live sessions = %d, want steady at the cap %d (the new session replaced an evicted empty one)", n, pos.MaxLiveSelfOrderSessions)
	}
}

// ut-docs#2261 review finding B1: without a short recency window on the
// busy guard, ONE abandoned cart (scan, add an item, then order at the
// counter instead / walk away) made that table's QR unscannable by anyone
// else for the full 2h memory-bound Sweep window, with no staff-facing way
// to clear it. selfOrderTableBusyMaxIdle narrows that: a session untouched
// past it no longer counts as "holding" the table for a DIFFERENT phone's
// scan, even though it is not evicted — it stays live, keeps its basket,
// and its OWNER's own cookie can still resume it. Driven with an explicit
// `now` on bindSelfOrderTableSession directly (same pattern the idle-sweep
// test below uses on selfOrderSessionSweepTick), no real sleeping.
func TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// Guest A scans, adds an item, then abandons — no checkout, no further
	// requests at all.
	owner := newSelfOrderGuest(t, mux)
	owner.get("/self-order?table=" + tableA)
	owner.post("/api/self-order/scan", "code=5000001")
	ownerToken := owner.sessionToken()

	// Right now, a second phone scanning the same table is still busy —
	// same guarantee TestSelfOrder_SameTableHeldByAnotherSession_ShowsBusy
	// covers, re-asserted here as the "before" baseline for what follows.
	limiter := newPairRateLimiter(time.Minute, 20)
	reqNow := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
	if bound, busy := bindSelfOrderTableSession(httptest.NewRecorder(), reqNow, dp, tableA, time.Now(), limiter); bound || !busy {
		t.Fatalf("right after the owner's scan: want busy (not bound), got bound=%v busy=%v", bound, busy)
	}

	// selfOrderTableBusyMaxIdle later, with zero further activity from the
	// owner, the SAME second phone's scan must no longer be blocked.
	future := time.Now().Add(selfOrderTableBusyMaxIdle + time.Minute)
	reqLater := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
	bound, busy := bindSelfOrderTableSession(httptest.NewRecorder(), reqLater, dp, tableA, future, limiter)
	if busy {
		t.Fatal("a session idle past selfOrderTableBusyMaxIdle must not block a new scan of its table")
	}
	if !bound {
		t.Fatal("the new scan should have bound a fresh session once the old one aged out of the busy guard's recency window")
	}

	// The owner's own abandoned session is untouched by this — still live,
	// still resumable by the owner's own cookie (only a DIFFERENT phone's
	// scan is affected by the recency window, never the owner's own).
	if svc, ok := dp.SelfOrderSessions.Get(ownerToken); !ok {
		t.Fatal("the owner's own abandoned session must still be live (not evicted — only de-prioritized for the busy guard)")
	} else if n := len(svc.Lines()); n != 1 {
		t.Fatalf("the owner's own basket has %d lines, want unchanged 1", n)
	}
}

// The HTTP-level version of the concurrency guarantee (the lower-level one is
// TestSessionBasketManager_ConcurrentSessionsDoNotInterfere in internal/pos):
// two tables' guests, driven through the real handlers with independent
// cookies, interleaving adds — each cart only ever holds what its own guest
// added. Run under -race like the rest of the package.
func TestSelfOrder_TwoTables_BasketsNeverCross(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	tableB := createSelfOrderTable(t, dp, "T2", 200)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Earl Grey", 250)
	seedStock(t, d, "itm-coffee", 10)
	seedStock(t, d, "itm-tea", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	a := newSelfOrderGuest(t, mux)
	b := newSelfOrderGuest(t, mux)
	a.get("/self-order?table=" + tableA)
	b.get("/self-order?table=" + tableB)

	// Interleaved: A coffee, B tea, B tea, A coffee — and in parallel,
	// since two phones really do hit the till at once.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.post("/api/self-order/scan", "code=5000001")
		a.post("/api/self-order/scan", "code=5000001")
	}()
	go func() {
		defer wg.Done()
		b.post("/api/self-order/scan", "code=5000002")
		b.post("/api/self-order/scan", "code=5000002")
	}()
	wg.Wait()

	cartA := a.get("/api/self-order/cart").Body.String()
	cartB := b.get("/api/self-order/cart").Body.String()
	if !strings.Contains(cartA, "Flat White") || strings.Contains(cartA, "Earl Grey") {
		t.Fatalf("A's cart must hold only A's coffee: %s", cartA)
	}
	if !strings.Contains(cartB, "Earl Grey") || strings.Contains(cartB, "Flat White") {
		t.Fatalf("B's cart must hold only B's tea: %s", cartB)
	}
	linesA, linesB := a.engine(dp).Lines(), b.engine(dp).Lines()
	if len(linesA) != 1 || linesA[0].ItemID != "itm-coffee" || linesA[0].Qty != 2 {
		t.Fatalf("A's basket = %+v, want 1 line itm-coffee x2", linesA)
	}
	if len(linesB) != 1 || linesB[0].ItemID != "itm-tea" || linesB[0].Qty != 2 {
		t.Fatalf("B's basket = %+v, want 1 line itm-tea x2", linesB)
	}
	if got := a.engine(dp).TableID(); got != tableA {
		t.Fatalf("A's table = %q, want %q", got, tableA)
	}
	if got := b.engine(dp).TableID(); got != tableB {
		t.Fatalf("B's table = %q, want %q", got, tableB)
	}
	if n := len(dp.KioskEngine.Lines()); n != 0 {
		t.Fatalf("the walk-up KioskEngine picked up %d lines from table sessions", n)
	}
}

// ADR-0103 Decision 5: a completed checkout REMOVES the session (and clears
// the cookie) rather than resetting it in place — the table is immediately
// free, and the guest's next scan starts a fresh session.
func TestSelfOrder_CheckoutCompletionRemovesSession(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // a table session forces the counter path regardless
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableA)
	g.post("/api/self-order/scan", "code=5000001")
	oldToken := g.sessionToken()

	rec := g.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Order placed") {
		t.Fatalf("POST checkout: want 200 + confirmation, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := dp.SelfOrderSessions.Get(oldToken); ok {
		t.Fatal("a completed checkout must remove the session from the manager")
	}
	if g.sessionToken() != "" {
		t.Fatalf("a completed checkout must clear the session cookie, jar still holds %q", g.sessionToken())
	}
	if n := dp.SelfOrderSessions.Len(); n != 0 {
		t.Fatalf("live sessions after checkout = %d, want 0", n)
	}
	if _, _, ok := dp.SelfOrderSessions.TableOwner(tableA); ok {
		t.Fatal("the table must be free right after checkout")
	}

	// Presenting the OLD token by hand (a stale cookie) must not resume
	// anything: a fresh session is minted instead.
	stale := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
	stale.AddCookie(&http.Cookie{Name: selfOrderSessionCookie, Value: oldToken})
	staleRec := httptest.NewRecorder()
	mux.ServeHTTP(staleRec, stale)
	if strings.Contains(staleRec.Body.String(), "This table already has an order in progress") {
		t.Fatal("a stale token must not see busy on a freed table")
	}
	newToken := ""
	for _, c := range staleRec.Result().Cookies() {
		if c.Name == selfOrderSessionCookie {
			newToken = c.Value
		}
	}
	if newToken == "" || newToken == oldToken {
		t.Fatalf("expected a fresh session token, got %q (old %q)", newToken, oldToken)
	}
	fresh, ok := dp.SelfOrderSessions.Get(newToken)
	if !ok || fresh.TableID() != tableA || len(fresh.Lines()) != 0 {
		t.Fatal("the fresh session must be bound to the table with an empty basket")
	}
}

// Decision 5's other half: the idle sweep evicts a stale session (driven
// with an explicit `now`, no real sleeping); the guest's next scan then
// starts fresh instead of resuming. A sweep at the real now evicts nothing.
func TestSelfOrder_IdleSweepEvictsStaleSession(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableA)
	g.post("/api/self-order/scan", "code=5000001")
	oldToken := g.sessionToken()

	if n := dp.SelfOrderSessions.Sweep(selfOrderSessionMaxIdle, time.Now()); n != 0 {
		t.Fatalf("a sweep right now evicted %d live sessions, want 0", n)
	}
	// The real loop's tick, three hours later.
	selfOrderSessionSweepTick(dp, time.Now().Add(3*time.Hour))
	if _, ok := dp.SelfOrderSessions.Get(oldToken); ok {
		t.Fatal("a session idle for 3h must have been swept")
	}

	// The phone still carries the old cookie; the table is free, so the
	// scan mints a new session rather than resuming or blocking.
	rec := g.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "This table already has an order in progress") {
		t.Fatalf("scan after sweep: want 200, not busy; got %d: %s", rec.Code, rec.Body.String())
	}
	if got := g.sessionToken(); got == "" || got == oldToken {
		t.Fatalf("expected a fresh token after the sweep, got %q (old %q)", got, oldToken)
	}
	if eng := g.engine(dp); eng == nil || eng.TableID() != tableA || len(eng.Lines()) != 0 {
		t.Fatal("the post-sweep session must be a fresh, empty, table-bound basket")
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
