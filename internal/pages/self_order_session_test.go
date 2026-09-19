package pages

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// selfOrderClient is one guest's browser: it carries the self_order_session
// cookie the /self-order landing sets across every later request, the way a
// real phone does. Tests before ut-docs#2261 never needed this because the
// kiosk basket was process-global; now the cookie IS the session.
type selfOrderClient struct {
	t       *testing.T
	mux     *http.ServeMux
	cookies map[string]*http.Cookie
}

func newSelfOrderClient(t *testing.T, mux *http.ServeMux) *selfOrderClient {
	return &selfOrderClient{t: t, mux: mux, cookies: map[string]*http.Cookie{}}
}

func (c *selfOrderClient) do(req *http.Request) *httptest.ResponseRecorder {
	c.t.Helper()
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
			continue
		}
		c.cookies[ck.Name] = ck
	}
	return rec
}

func (c *selfOrderClient) get(path string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func (c *selfOrderClient) post(path, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

func (c *selfOrderClient) sessionToken() string {
	if ck := c.cookies[selfOrderSessionCookie]; ck != nil {
		return ck.Value
	}
	return ""
}

// ut-docs#2261 core acceptance: two DIFFERENT tables ordering at the same
// time each get their own basket — no "till busy" screen, no wiped order,
// no cross-contamination, and the bare-kiosk d.KioskEngine is never
// touched by a table session. The scan loop runs both guests concurrently
// so `go test -race` exercises the real handler path, not just the store.
func TestSelfOrder_TwoTablesConcurrent_IsolatedBaskets(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	tableB := createTestTable(t, dp, "T2")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Earl Grey", 250)
	seedStock(t, d, "itm-coffee", 100)
	seedStock(t, d, "itm-tea", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guestA := newSelfOrderClient(t, mux)
	guestB := newSelfOrderClient(t, mux)

	recA := guestA.get("/self-order?table=" + tableA)
	if recA.Code != http.StatusOK || guestA.sessionToken() == "" {
		t.Fatalf("table A landing: code=%d cookie=%q body=%s", recA.Code, guestA.sessionToken(), recA.Body.String())
	}
	guestA.post("/api/self-order/scan", "code=5000001")

	// Table B's guest arrives while A's order is in progress — must never
	// see the old busy interstitial, and must get a DIFFERENT session.
	recB := guestB.get("/self-order?table=" + tableB)
	if recB.Code != http.StatusOK {
		t.Fatalf("table B landing: want 200, got %d: %s", recB.Code, recB.Body.String())
	}
	if strings.Contains(recB.Body.String(), "This till is busy") {
		t.Fatalf("table B must never be blocked by table A's order any more, got: %s", recB.Body.String())
	}
	if !strings.Contains(recB.Body.String(), "Tap to start") {
		t.Fatalf("table B landing must be the normal welcome screen, got: %s", recB.Body.String())
	}
	if guestB.sessionToken() == "" || guestB.sessionToken() == guestA.sessionToken() {
		t.Fatalf("table B session token %q must be set and distinct from A's %q", guestB.sessionToken(), guestA.sessionToken())
	}

	const iters = 25
	var wg sync.WaitGroup
	for _, g := range []struct {
		c    *selfOrderClient
		code string
	}{{guestA, "5000001"}, {guestB, "5000002"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if rec := g.c.post("/api/self-order/scan", "code="+g.code); rec.Code != http.StatusOK {
					t.Errorf("scan %s: %d", g.code, rec.Code)
					return
				}
			}
		}()
	}
	wg.Wait()

	engA, tidA, okA := dp.KioskSessions.Lookup(guestA.sessionToken())
	engB, tidB, okB := dp.KioskSessions.Lookup(guestB.sessionToken())
	if !okA || !okB || tidA != tableA || tidB != tableB || engA == engB {
		t.Fatalf("sessions: A(ok=%v table=%q) B(ok=%v table=%q) same-engine=%v", okA, tidA, okB, tidB, engA == engB)
	}
	if engA.TableID() != tableA || engA.TableLabel() != "T1" || engB.TableID() != tableB || engB.TableLabel() != "T2" {
		t.Fatalf("engine table binding: A=%q/%q B=%q/%q", engA.TableID(), engA.TableLabel(), engB.TableID(), engB.TableLabel())
	}
	linesA, linesB := engA.Lines(), engB.Lines()
	if len(linesA) != 1 || linesA[0].ItemID != "itm-coffee" || linesA[0].Qty != iters+1 {
		t.Fatalf("table A basket: %+v, want only COFFEE x%d", linesA, iters+1)
	}
	if len(linesB) != 1 || linesB[0].ItemID != "itm-tea" || linesB[0].Qty != iters {
		t.Fatalf("table B basket: %+v, want only TEA x%d", linesB, iters)
	}
	// Each guest's cart render is their own table's, keyed off the cookie.
	if body := guestA.get("/api/self-order/cart").Body.String(); !strings.Contains(body, "Flat White") || strings.Contains(body, "Earl Grey") {
		t.Fatalf("guest A cart must show only A's lines: %s", body)
	}
	if body := guestB.get("/api/self-order/cart").Body.String(); !strings.Contains(body, "Earl Grey") || strings.Contains(body, "Flat White") {
		t.Fatalf("guest B cart must show only B's lines: %s", body)
	}
	// The bare walk-up kiosk basket is untouched by either table session.
	if n := len(dp.KioskEngine.Lines()); n != 0 || dp.KioskEngine.TableID() != "" {
		t.Fatalf("d.KioskEngine must stay untouched by table sessions: lines=%d table=%q", n, dp.KioskEngine.TableID())
	}

	// Table A checks out (counter path, forced by the table binding) —
	// only A's session resets; B's order survives intact.
	rec := guestA.post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Order placed") {
		t.Fatalf("table A checkout: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(engA.Lines()); n != 0 {
		t.Fatalf("table A basket after checkout: %d lines, want 0", n)
	}
	if n := len(engB.Lines()); n != 1 {
		t.Fatalf("table B basket must survive A's checkout: %d lines, want 1", n)
	}
	var gotTable string
	if err := d.DB.QueryRow(`SELECT COALESCE(table_id,'') FROM kiosk_counter_orders`).Scan(&gotTable); err != nil || gotTable != tableA {
		t.Fatalf("counter order table_id=%q err=%v, want %q", gotTable, err, tableA)
	}
}

// Same-table decision (ut-docs#2261): a second device scanning the SAME
// table's QR joins the one live session — same cookie token, shared basket,
// and the shop screen shows the non-blocking "joined" banner with the
// current line count. The first guest at a table sees no banner.
func TestSelfOrder_SameTableSecondDevice_JoinsSharedBasketWithBanner(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Earl Grey", 250)
	seedStock(t, d, "itm-coffee", 10)
	seedStock(t, d, "itm-tea", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	first := newSelfOrderClient(t, mux)
	second := newSelfOrderClient(t, mux)

	landing := first.get("/self-order?table=" + tableA)
	if strings.Contains(landing.Body.String(), "joined=") {
		t.Fatalf("first guest at a table must not be told they joined: %s", landing.Body.String())
	}
	if body := first.get("/self-order/shop").Body.String(); strings.Contains(body, "already added") {
		t.Fatalf("first guest's shop screen must show no joined banner: %s", body)
	}
	first.post("/api/self-order/scan", "code=5000001")
	first.post("/api/self-order/scan", "code=5000002")

	joined := second.get("/self-order?table=" + tableA)
	if joined.Code != http.StatusOK {
		t.Fatalf("second device landing: %d %s", joined.Code, joined.Body.String())
	}
	if second.sessionToken() != first.sessionToken() {
		t.Fatalf("second device token %q must equal first's %q (join, not duplicate)", second.sessionToken(), first.sessionToken())
	}
	if !strings.Contains(joined.Body.String(), `href="/self-order/shop?joined=1"`) {
		t.Fatalf("joined landing must send the guest into the shop with the joined flag: %s", joined.Body.String())
	}
	shop := second.get("/self-order/shop?joined=1")
	if shop.Code != http.StatusOK {
		t.Fatalf("shop: %d", shop.Code)
	}
	if !strings.Contains(shop.Body.String(), "2 item(s) already added") {
		t.Fatalf("shop screen must carry the inline joined banner with the live count: %s", shop.Body.String())
	}
	// The banner is inline, not the old blocking interstitial.
	if strings.Contains(shop.Body.String(), "This till is busy") {
		t.Fatal("joined must never render the busy interstitial")
	}
	// Second device's add lands in the shared basket.
	second.post("/api/self-order/scan", "code=5000001")
	eng, _, ok := dp.KioskSessions.Lookup(first.sessionToken())
	if !ok {
		t.Fatal("session vanished")
	}
	lines := eng.Lines()
	if len(lines) != 2 || lines[0].Qty+lines[1].Qty != 3 {
		t.Fatalf("shared basket: %+v, want COFFEE x2 + TEA x1", lines)
	}
	if body := first.get("/api/self-order/cart").Body.String(); !strings.Contains(body, "Flat White") || !strings.Contains(body, "Earl Grey") {
		t.Fatalf("first device must see the second device's add: %s", body)
	}
	// A ?joined=1 flag with NO table session (bare kiosk) never shows the
	// banner — the flag is guest-controllable, the session is not.
	bare := newSelfOrderClient(t, mux)
	bare.get("/self-order")
	if body := bare.get("/self-order/shop?joined=1").Body.String(); strings.Contains(body, "already added") {
		t.Fatalf("bare kiosk must never show the joined banner: %s", body)
	}
}

// A session cookie whose session has gone (idle-evicted, or a till restart
// dropped the in-memory store) must NOT error — the request falls through
// to the bare-kiosk d.KioskEngine exactly as if no cookie existed.
func TestSelfOrder_StaleSessionCookie_FallsBackToBareKiosk(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guest := newSelfOrderClient(t, mux)
	guest.cookies[selfOrderSessionCookie] = &http.Cookie{Name: selfOrderSessionCookie, Value: "deadbeefdeadbeefdeadbeefdeadbeef"}

	if rec := guest.post("/api/self-order/scan", "code=5000001"); rec.Code != http.StatusOK {
		t.Fatalf("scan with a stale cookie: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(dp.KioskEngine.Lines()); n != 1 {
		t.Fatalf("stale cookie must fall through to d.KioskEngine: %d lines, want 1", n)
	}
	if rec := guest.get("/self-order/shop"); rec.Code != http.StatusOK {
		t.Fatalf("shop with a stale cookie: %d", rec.Code)
	}
}

// The bare walk-up kiosk path (no ?table=) is UNCHANGED by ut-docs#2261: it
// still resets and uses d.KioskEngine, mints no table session, and clears
// any leftover session cookie so later requests don't act on a table basket.
func TestSelfOrder_BareKioskVisit_UnchangedAndClearsSessionCookie(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guest := newSelfOrderClient(t, mux)

	guest.get("/self-order?table=" + tableA)
	guest.post("/api/self-order/scan", "code=5000001")
	tok := guest.sessionToken()

	dp.KioskEngine.Scan("5000001") // a walk-up basket in progress on the till itself
	rec := guest.get("/self-order")
	if rec.Code != http.StatusOK {
		t.Fatalf("bare landing: %d", rec.Code)
	}
	if guest.sessionToken() != "" {
		t.Fatalf("bare /self-order must clear the table-session cookie, still have %q", guest.sessionToken())
	}
	if n := len(dp.KioskEngine.Lines()); n != 0 || dp.KioskEngine.TableID() != "" {
		t.Fatalf("bare landing must still start fresh on d.KioskEngine: lines=%d table=%q", n, dp.KioskEngine.TableID())
	}
	guest.post("/api/self-order/scan", "code=5000001")
	if n := len(dp.KioskEngine.Lines()); n != 1 {
		t.Fatalf("after a bare landing, scans must land on d.KioskEngine: %d lines", n)
	}
	// The table's own session is untouched by the bare visit (another
	// device at that table may still be ordering).
	if eng, _, ok := dp.KioskSessions.Lookup(tok); !ok || len(eng.Lines()) != 1 {
		t.Fatalf("table session must survive a bare kiosk visit: ok=%v", ok)
	}
	if n := len(dp.KioskSessions.All()); n != 1 {
		t.Fatalf("bare visit must not mint a session: %d live", n)
	}
}

// The shop page's idle-reset bounce carries the SESSION's table (ut-docs#815
// review, BLOCKER 2) — now read off the per-table engine, not d.KioskEngine.
func TestSelfOrder_ShopIdleResetURL_CarriesSessionTable(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guest := newSelfOrderClient(t, mux)
	guest.get("/self-order?table=" + tableA)
	body := guest.get("/self-order/shop").Body.String()
	if !strings.Contains(body, `"/self-order?table=`+tableA+`"`) {
		t.Fatalf("shop idle-reset URL must carry the session's table: %s", body)
	}
}

// The peripheral call sites (ut-docs#2261 §6): settings/setup broadcast
// reaches every table session, and the auto-update basket guard sees a
// table basket with items.
func TestKioskSessions_PeripheralHooks(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guest := newSelfOrderClient(t, mux)
	guest.get("/self-order?table=" + tableA)
	if dp.KioskSessions.AnyHasItems() {
		t.Fatal("AnyHasItems before any scan")
	}
	guest.post("/api/self-order/scan", "code=5000001")
	if !dp.KioskSessions.AnyHasItems() {
		t.Fatal("AnyHasItems must see the table basket's line — the auto-update guard depends on it")
	}
	cfg := pos.Config{TaxInclusive: true, TaxRateBasisPoints: 700}
	dp.KioskSessions.SetConfigAll(cfg)
	eng, _, _ := dp.KioskSessions.Lookup(guest.sessionToken())
	if eng.Config() != cfg {
		t.Fatalf("SetConfigAll did not reach the table session: %+v", eng.Config())
	}
	// A session minted AFTER a settings change must start at the CURRENT
	// config, not the boot-time one — the factory reads it off the live
	// bare-kiosk engine, which every SetConfig broadcast site keeps current,
	// rather than capturing the RuntimeState pages.Init saw at boot.
	later := pos.Config{TaxInclusive: false, TaxRateBasisPoints: 1900}
	dp.KioskEngine.SetConfig(later)
	tableB := createTestTable(t, dp, "T2")
	late := newSelfOrderClient(t, mux)
	late.get("/self-order?table=" + tableB)
	engB, _, ok := dp.KioskSessions.Lookup(late.sessionToken())
	if !ok || engB.Config() != later {
		t.Fatalf("a session minted after a config change must start at the current config: ok=%v got %+v want %+v", ok, engB.Config(), later)
	}
}

// ut-docs#2261 Tester finding: the session cookie must actually reach the
// mutation endpoints under /api/self-order/*, not just the /self-order
// landing page. This is NOT redundant with the concurrency tests above —
// those use selfOrderClient, whose do() calls req.AddCookie() for every
// stored cookie unconditionally, bypassing RFC 6265 path-matching
// entirely. A real browser (or curl's cookie jar) withholds a cookie
// scoped to Path=X from a request whose path isn't a prefix of X, and
// "/self-order" is NOT a prefix of "/api/self-order/scan" — a real guest's
// scan/cart/checkout requests silently carried no cookie at all and fell
// back to the shared bare d.KioskEngine, the exact cross-table collision
// this card exists to remove, while every httptest-based test kept passing.
// Caught with a real curl cookie jar against a running till; reproduced
// here with net/http/cookiejar (the same RFC 6265 implementation a real
// browser uses) so it never regresses silently again.
func TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 100)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	landing, err := client.Get(srv.URL + "/self-order?table=" + tableA)
	if err != nil {
		t.Fatal(err)
	}
	landing.Body.Close()
	if landing.StatusCode != http.StatusOK {
		t.Fatalf("landing: want 200, got %d", landing.StatusCode)
	}
	u, _ := neturl.Parse(srv.URL)
	if cs := jar.Cookies(u); len(cs) == 0 {
		t.Fatal("no session cookie set after table landing")
	}

	// The regression: a real cookie jar must actually attach the cookie
	// to a POST under /api/self-order/*. If Path scoping is wrong, the
	// jar silently sends the request with NO cookie — the request still
	// returns 200 (it just falls back to the bare kiosk engine), so the
	// only way to see the bug is to check where the scan actually landed.
	scanResp, err := client.Post(srv.URL+"/api/self-order/scan", "application/x-www-form-urlencoded",
		strings.NewReader("code=5000001"))
	if err != nil {
		t.Fatal(err)
	}
	scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("scan: want 200, got %d", scanResp.StatusCode)
	}

	tableEngine, _, ok := dp.KioskSessions.Lookup(jar.Cookies(u)[0].Value)
	if !ok {
		t.Fatal("session lookup failed after scan")
	}
	if got := len(tableEngine.Lines()); got != 1 {
		t.Fatalf("table session engine: got %d lines, want 1 — the scan did not reach the table's own session engine (cookie Path scoping regression)", got)
	}
	if dp.KioskEngine != nil {
		if got := len(dp.KioskEngine.Lines()); got != 0 {
			t.Fatalf("bare d.KioskEngine has %d lines, want 0 — the scan leaked onto the shared bare-kiosk basket instead of the table's session (cookie Path scoping regression)", got)
		}
	}
}

// The registered sweep goroutine honors ctx cancellation and releases the
// WaitGroup — the shape every StartX background job in init.go shares.
func TestStartKioskSessionSweep_StopsOnContextCancel(t *testing.T) {
	dp := &common.Deps{KioskSessions: pos.NewTableSessions(func() *pos.Service {
		return pos.NewServiceWithResolver(pos.Config{}, nil)
	}, time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartKioskSessionSweep(ctx, dp, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("kiosk session sweep goroutine did not exit on context cancellation")
	}
}
