package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2858: an order held on a satellite and completed on the main till
// left the satellite's Open orders badge showing until the sale screen was
// reloaded. The held generation (common.Deps.HeldToken) moves on every
// held_sales link nudge, sent or received, and the sale screen's hidden
// watcher turns a moved token into held-changed.

var watchTokenRe = regexp.MustCompile(`id="open-orders-watch"[^>]*hx-get="/ui/open-orders-badge/watch\?v=([^"]+)"`)

// badgeAndToken GETs the badge and returns its body and the token its
// out-of-band watcher was seeded with.
func badgeAndToken(t *testing.T, mux http.Handler) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/open-orders-badge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/open-orders-badge = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	m := watchTokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("badge render carries no #open-orders-watch watcher: %s", body)
	}
	tok, err := url.QueryUnescape(m[1])
	if err != nil {
		t.Fatalf("watcher token %q: %v", m[1], err)
	}
	return body, tok
}

// heldGen is the counter half of d.HeldToken ("<boot>.<n>").
func heldGen(t *testing.T, d *common.Deps) int64 {
	t.Helper()
	tok := d.HeldToken()
	n, err := strconv.ParseInt(tok[strings.LastIndexByte(tok, '.')+1:], 10, 64)
	if err != nil {
		t.Fatalf("held token %q: %v", tok, err)
	}
	return n
}

// watchFires asks the watch endpoint about token and reports whether it
// answered HX-Trigger: held-changed.
func watchFires(t *testing.T, mux http.Handler, token string) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/open-orders-badge/watch?v="+url.QueryEscape(token), nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("watch = %d, want 204 (htmx must never swap it): %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("watch Cache-Control = %q, want no-store", cc)
	}
	switch h := rec.Header().Get("HX-Trigger"); h {
	case "":
		return false
	case "held-changed":
		return true
	default:
		t.Fatalf("watch HX-Trigger = %q, want held-changed or none", h)
		return false
	}
}

// The card's AC end to end over the real link: an order parked on a
// satellite (written through to the main till) and then resumed on the main
// till clears the satellite's badge within the link's latency, no reload.
func TestReplicaLink_HeldOrderResolvedOnMainClearsTheSatelliteBadge(t *testing.T) {
	f := newSyncLinkFixture(t)
	registerSyncHeldSales(f.mux, f.dp)
	tillID := f.enrol(t, "Pi", "token-abc")
	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	pulls := runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && pulls.Load() >= 1 }) {
		t.Fatal("the satellite never linked to the main till")
	}
	rmux := http.NewServeMux()
	registerOpenOrdersBadge(rmux, replica)

	// Hold on the satellite: the same write-through a Hold tap makes.
	before := heldGen(t, replica)
	ctx := context.Background()
	rrepo := data.NewHeldSalesRepo(replica.Db)
	if out, err := heldSaleWriteThrough(ctx, replica, rrepo, data.HeldSale{
		ID: "hold-pi-1", Label: "Table 4", TotalMinor: 1250, LineCount: 2, Payload: `{}`,
	}); err != nil || out != heldSaleSyncedPrimary {
		t.Fatalf("park on the satellite: outcome %v, err %v", out, err)
	}
	// Two moves: this till's own write-through, and the main till's nudge
	// about it echoed back over the link (it can land before or after the
	// write-through returns). Let both land before the render the next
	// assert must not see disturbed.
	if !waitFor(t, 3*time.Second, func() bool { return heldGen(t, replica) >= before+2 }) {
		t.Fatalf("held generation moved %d, want the local write and the main till's echo", heldGen(t, replica)-before)
	}
	body, token := badgeAndToken(t, rmux)
	if !strings.Contains(body, `data-count="1"`) {
		t.Fatalf("satellite badge must count the parked order: %s", body)
	}
	if watchFires(t, rmux, token) {
		t.Fatal("watcher fired with nothing changed since the badge render")
	}

	// The main till takes the payment: its resume claims the order.
	if _, found, claimed := heldSaleClaimForResume(ctx, f.dp, data.NewHeldSalesRepo(f.dp.Db), "hold-pi-1"); !found || !claimed {
		t.Fatalf("main-till resume: found=%v claimed=%v", found, claimed)
	}
	// Seconds, per the card: the link pushes, nothing polls the main till.
	if !waitFor(t, 3*time.Second, func() bool { return watchFires(t, rmux, token) }) {
		t.Fatal("the satellite's watcher never saw the main till resolve the order")
	}

	// What held-changed makes the badge do: re-fetch, now empty.
	body, fresh := badgeAndToken(t, rmux)
	if !strings.Contains(body, `data-count="0"`) || !strings.Contains(body, " hidden") {
		t.Fatalf("satellite badge must clear once the main till resolved the order: %s", body)
	}
	if watchFires(t, rmux, fresh) {
		t.Fatal("the re-seeded watcher must be quiet until the next change")
	}
}

// The main till's own sale screen: an order a linked till parks through it
// moves its badge too (the sync endpoint nudges held_sales).
func TestOpenOrdersWatch_MainTillSeesAnOrderParkedThroughIt(t *testing.T) {
	f := newSyncLinkFixture(t)
	registerSyncHeldSales(f.mux, f.dp)
	f.enrol(t, "Pi", "token-abc")
	registerOpenOrdersBadge(f.mux, f.dp)
	_, token := badgeAndToken(t, f.mux)

	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/sync/held-sales/upsert",
		strings.NewReader(`{"id":"hold-pi-2","label":"Sarah","total_minor":500,"line_count":1,"payload":"{}"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token-abc")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert through the main till = %d", resp.StatusCode)
	}
	if !watchFires(t, f.mux, token) {
		t.Fatal("the main till's watcher missed an order parked through it")
	}
	body, _ := badgeAndToken(t, f.mux)
	if !strings.Contains(body, `data-count="1"`) {
		t.Fatalf("main-till badge must count the order parked through it: %s", body)
	}
}

// NudgeLink with held_sales moves the token even on a replica (no hub);
// other scopes don't.
func TestNudgeLink_HeldScopeMovesTheHeldToken(t *testing.T) {
	d := &common.Deps{}
	t0 := d.HeldToken()
	d.NudgeLink(fleetlink.ScopeTables, fleetlink.ScopeOrders)
	if d.HeldToken() != t0 {
		t.Fatal("a tables/orders nudge must not move the held token")
	}
	d.NudgeLink(fleetlink.ScopeTables, fleetlink.ScopeHeldSales)
	t1 := d.HeldToken()
	if t1 == t0 {
		t.Fatal("a held_sales nudge must move the held token, Link or not")
	}
	d.MarkHeldChanged()
	if d.HeldToken() == t1 {
		t.Fatal("MarkHeldChanged must move the held token")
	}
}

func TestLinkScopesTouchHeld(t *testing.T) {
	for _, tc := range []struct {
		in   []fleetlink.Scope
		want bool
	}{
		{nil, false},
		{[]fleetlink.Scope{fleetlink.ScopeTables, fleetlink.ScopeAdmin}, false},
		{[]fleetlink.Scope{fleetlink.ScopeTables, fleetlink.ScopeHeldSales}, true},
	} {
		if got := linkScopesTouchHeld(tc.in); got != tc.want {
			t.Errorf("linkScopesTouchHeld(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestOpenOrdersWatch_EndpointContract(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	registerOpenOrdersBadge(mux, d)
	body, token := badgeAndToken(t, mux)
	w := openTag(t, body, `id="open-orders-watch"`)
	for _, want := range []string{`hx-swap-oob="true"`, "hidden", `aria-hidden="true"`, `hx-swap="none"`,
		`hx-trigger="every 3s [document.visibilityState==='visible']"`} {
		if !strings.Contains(w, want) {
			t.Errorf("watcher lost %q: %s", want, w)
		}
	}
	if strings.Contains(w, "hx-target") {
		t.Errorf("watcher never swaps, it must not name a target: %s", w)
	}
	if watchFires(t, mux, token) {
		t.Fatal("current token must not fire")
	}
	if watchFires(t, mux, "") {
		t.Fatal("a missing token must not fire (it could loop)")
	}
	d.MarkHeldChanged()
	if !watchFires(t, mux, token) {
		t.Fatal("a stale token must fire held-changed")
	}
}

// index.html's placeholder is what the badge's out-of-band watcher lands
// on; it sits outside the Open orders button so nothing inherits that
// button's hx-target.
func TestIndexTender_OpenOrdersWatchPlaceholder(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	home := getHome(t, mux)
	i := strings.Index(home, `id="open-orders-watch"`)
	if i < 0 {
		t.Fatal("sale screen lost the #open-orders-watch placeholder")
	}
	btn := strings.Index(home, `data-testid="parked-orders-open"`)
	if btn < 0 {
		t.Fatal("sale screen lost the Open orders button")
	}
	if end := strings.Index(home[btn:], "</button>"); end < 0 || btn+end > i {
		t.Fatal("#open-orders-watch must follow the Open orders button, not sit inside it")
	}
}
