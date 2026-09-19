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
	if strings.Contains(rec.Body.String(), "This table is already in use") {
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

// A guest who moves seats and scans a DIFFERENT table's QR while their
// current session still holds items must keep those items — the same
// rebind-in-place ADR-0054's table picker already does for the cashier,
// not a fresh empty basket (ut-docs#2433, ADR-0103 review finding N2: this
// used to silently discard the guest's in-progress basket).
func TestSelfOrder_SameGuestScansDifferentTable_PreservesBasket(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	tableB := createSelfOrderTable(t, dp, "T2", 200)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order?table=" + tableA)
	g.post("/api/self-order/scan", "code=5000001")
	firstSvc := g.engine(dp)
	if n := len(firstSvc.Lines()); n != 1 {
		t.Fatalf("precondition: guest's basket has %d lines, want 1", n)
	}
	firstToken := g.sessionToken()

	// The guest moves to table B (same browser/cookie) before checking out.
	rec := g.get("/self-order?table=" + tableB)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableB>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatalf("moving to an unheld table must never show busy: %s", rec.Body.String())
	}

	svc := g.engine(dp)
	if svc == nil {
		t.Fatal("guest must still resolve to a live session after moving tables")
	}
	if n := len(svc.Lines()); n != 1 {
		t.Fatalf("basket has %d lines after moving tables, want 1 (must be preserved, not discarded)", n)
	}
	if got := svc.TableID(); got != tableB {
		t.Fatalf("TableID() after move = %q, want new table %q", got, tableB)
	}
	if got := svc.TableLabel(); got != "T2" {
		t.Fatalf("TableLabel() after move = %q, want %q", got, "T2")
	}
	// Same session, rebound in place — not a fresh session replacing it.
	if svc != firstSvc {
		t.Fatal("moving tables must rebind the SAME session, not mint a new one (that's how the basket was lost)")
	}
	if got := g.sessionToken(); got != firstToken {
		t.Fatalf("session cookie changed across a table move: got %q, want unchanged %q", got, firstToken)
	}
	if n := dp.SelfOrderSessions.Len(); n != 1 {
		t.Fatalf("live sessions = %d, want 1 (no orphaned session left behind for the old table)", n)
	}

	// Table A is immediately free for a new scan by someone else.
	other := newSelfOrderGuest(t, mux)
	rec2 := other.get("/self-order?table=" + tableA)
	if strings.Contains(rec2.Body.String(), "This table is already in use") {
		t.Fatal("the vacated table must not still show busy after the guest moved off it")
	}
	if other.engine(dp) == nil || other.engine(dp) == svc {
		t.Fatal("a new scan of the vacated table must get its own independent session")
	}
}

// A guest with items on table A who scans table B while B is already held
// by a DIFFERENT, non-empty, recently-active session must see the busy
// screen — the move must not bypass ADR-0103 Decision 4's busy guard. The
// mover keeps their own table A session and basket untouched, and the
// incumbent on table B is undisturbed (ut-docs#2433 review, S2: the busy
// guard is checked before the move rebinds, but that interaction had no
// direct test).
func TestSelfOrder_MovingOntoBusyTable_ShowsBusyAndKeepsMoverOnOriginalTable(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	tableB := createSelfOrderTable(t, dp, "T2", 200)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// The incumbent already holds table B with a non-empty basket.
	incumbent := newSelfOrderGuest(t, mux)
	incumbent.get("/self-order?table=" + tableB)
	incumbent.post("/api/self-order/scan", "code=5000001")
	incumbentSvc := incumbent.engine(dp)

	// The mover has their own items on table A, then scans table B.
	mover := newSelfOrderGuest(t, mux)
	mover.get("/self-order?table=" + tableA)
	mover.post("/api/self-order/scan", "code=5000001")
	moverSvc := mover.engine(dp)
	moverToken := mover.sessionToken()

	rec := mover.get("/self-order?table=" + tableB)
	if !strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatalf("expected the busy screen when moving onto a held table, got: %s", rec.Body.String())
	}
	if got := mover.sessionToken(); got != moverToken {
		t.Fatalf("mover's session cookie changed on a busy-blocked move: got %q, want unchanged %q", got, moverToken)
	}
	if got := mover.engine(dp); got != moverSvc {
		t.Fatal("mover must still resolve to their own original session, not the incumbent's")
	}
	if got := moverSvc.TableID(); got != tableA {
		t.Fatalf("mover's TableID() after a busy-blocked move = %q, want unchanged original table %q", got, tableA)
	}
	if n := len(moverSvc.Lines()); n != 1 {
		t.Fatalf("mover's basket has %d lines after a busy-blocked move, want unchanged 1", n)
	}
	if got := incumbentSvc.TableID(); got != tableB {
		t.Fatalf("incumbent's TableID() = %q, want unchanged %q", got, tableB)
	}
	if n := len(incumbentSvc.Lines()); n != 1 {
		t.Fatalf("incumbent's basket has %d lines, want unchanged 1", n)
	}
	if n := dp.SelfOrderSessions.Len(); n != 2 {
		t.Fatalf("live sessions = %d, want 2 (mover on A, incumbent on B)", n)
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
	if strings.Contains(rec.Body.String(), "This table is already in use") {
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

// ut-docs#2434 (ADR-0103 review finding N3, correction landed in the ADR
// itself): an EMPTY table-bound session still HOLDS its table. The busy
// guard used to require len(owner.Lines()) > 0, which let two phones
// scanning the same table before either added an item both bind — two
// independent, uncoordinated baskets for one physical table, and two
// kiosk_counter_orders rows at checkout. Dropping the item-count exception
// closes that: a second phone now sees busy the moment ANY other session
// (empty or not) is bound to the table within selfOrderTableBusyMaxIdle —
// exactly the same recency window as the non-empty case, so an abandoned
// EMPTY session still frees the table after that window
// (TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard covers that half).
func TestSelfOrder_SameTableEmptySession_ShowsBusy(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	first := newSelfOrderGuest(t, mux)
	first.get("/self-order?table=" + tableA)
	firstToken := first.sessionToken()
	firstSvc := first.engine(dp)

	second := newSelfOrderGuest(t, mux)
	rec := second.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableA> from a second phone: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatalf("an empty table-bound session still holds the table — must show busy, got: %s", rec.Body.String())
	}
	if second.sessionToken() != "" {
		t.Fatal("a busy-blocked scan must not mint a session cookie")
	}
	if n := dp.SelfOrderSessions.Len(); n != 1 {
		t.Fatalf("live sessions = %d, want 1 (only the first phone's, empty)", n)
	}
	if got := first.sessionToken(); got != firstToken {
		t.Fatalf("first phone's token changed to %q", got)
	}
	if got := first.engine(dp); got != firstSvc {
		t.Fatal("the first phone's session must be unaffected by the second phone's blocked scan")
	}
	// The first phone's own re-scan of their own table is still never busy.
	if rec := first.get("/self-order?table=" + tableA); strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatal("the owning guest must never be blocked from their own (empty) table")
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
	if !strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatalf("expected the busy screen for a table another session holds with items, got: %s", rec.Body.String())
	}
	if other.sessionToken() != "" {
		t.Fatal("a busy-blocked scan must not mint a session cookie")
	}

	// A request carrying no cookie at all (e.g. a browser that blocks
	// cookies) is the same case.
	raw := httptest.NewRecorder()
	mux.ServeHTTP(raw, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	if !strings.Contains(raw.Body.String(), "This table is already in use") {
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
	if rec := owner.get("/self-order?table=" + tableA); strings.Contains(rec.Body.String(), "This table is already in use") {
		t.Fatal("the owning guest must never be blocked from their own table")
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
	reqNow := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
	if bound, busy := bindSelfOrderTableSession(httptest.NewRecorder(), reqNow, dp, tableA, time.Now()); bound || !busy {
		t.Fatalf("right after the owner's scan: want busy (not bound), got bound=%v busy=%v", bound, busy)
	}

	// selfOrderTableBusyMaxIdle later, with zero further activity from the
	// owner, the SAME second phone's scan must no longer be blocked.
	future := time.Now().Add(selfOrderTableBusyMaxIdle + time.Minute)
	reqLater := httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil)
	bound, busy := bindSelfOrderTableSession(httptest.NewRecorder(), reqLater, dp, tableA, future)
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
	if strings.Contains(staleRec.Body.String(), "This table is already in use") {
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
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "This table is already in use") {
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
