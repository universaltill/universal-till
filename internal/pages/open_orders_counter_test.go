package pages

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ut-docs#2703 (reopened, product owner): "the open orders need to have 2
// sections or tabs, for open orders and pay on the counter, when the
// cashier clicks on them both should act the same: the order shows open on
// the sell screen same as a normal order and cashier can get the payment or
// change it." And: a legacy pay-on-the-counter order showed only "Mark
// collected" -- "no one can get that payment".

// seedTabSplit parks one ordinary till hold, one pay-at-counter held sale
// (its payload carries the kiosk order number, as completeCounterOrderCheckout
// writes it) and one legacy "open" counter row that was never parked.
func seedTabSplit(t *testing.T, d *common.Deps) data.KioskCounterOrder {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('hold-till','Sarah',500,1,'{"lines":[]}',NULL,datetime('now')),
 ('hold-kiosk','C-7 · Takeaway',120,1,'{"lines":[],"display_no":"C-7"}',NULL,datetime('now'))`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}
	legacy, err := data.NewKioskCounterOrdersRepo(d.Db).Create(context.Background(), data.KioskCounterOrder{
		OrderType: "", Lines: []data.KioskCounterOrderLine{{Name: "Avocado Lachs Bagel", Qty: 1}, {Name: "Black Shadow", Qty: 1}},
	})
	if err != nil {
		t.Fatalf("seed legacy counter order: %v", err)
	}
	return legacy
}

func getBody(t *testing.T, mux *http.ServeMux, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// tabSnippet returns the opening tag of the tab control with this testid.
func tabSnippet(t *testing.T, body, testid string) string {
	t.Helper()
	i := strings.Index(body, `data-testid="`+testid+`"`)
	if i < 0 {
		t.Fatalf("tab %s missing: %s", testid, body)
	}
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], "</span>")
	return body[start : i+end+len("</span>")]
}

func TestOpenOrdersPage_TwoTabsSplitCounterOrdersFromHolds(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	legacy := seedTabSplit(t, d)

	hold := getBody(t, mux, "/open-orders")
	if !strings.Contains(hold, `data-held-id="hold-till"`) {
		t.Fatalf("On hold (default) must list the till's own hold: %s", hold)
	}
	if strings.Contains(hold, `data-held-id="hold-kiosk"`) || strings.Contains(hold, legacy.ID) {
		t.Fatalf("On hold must not list pay-at-counter orders: %s", hold)
	}
	holdTab := tabSnippet(t, hold, "open-orders-tab-hold")
	if !strings.Contains(holdTab, `aria-current="page"`) || !strings.Contains(holdTab, ">1<") {
		t.Fatalf("On hold tab must be current and count 1: %s", holdTab)
	}
	counterTab := tabSnippet(t, hold, "open-orders-tab-counter")
	if !strings.Contains(counterTab, ">2<") || !strings.Contains(counterTab, `href="/open-orders?tab=counter"`) {
		t.Fatalf("Pay at the counter tab must count 2 and link to ?tab=counter: %s", counterTab)
	}

	counter := getBody(t, mux, "/open-orders?tab=counter")
	if strings.Contains(counter, `data-held-id="hold-till"`) {
		t.Fatalf("Pay at the counter must not list the till hold: %s", counter)
	}
	if !strings.Contains(counter, `data-held-id="hold-kiosk"`) || !strings.Contains(counter, "C-7") {
		t.Fatalf("Pay at the counter must list the parked kiosk order: %s", counter)
	}
	// The legacy row is a real control that OPENS it -- not "Mark collected".
	if !strings.Contains(counter, `action="/open-orders/counter/open"`) || !strings.Contains(counter, `value="`+legacy.ID+`"`) || !strings.Contains(counter, legacy.DisplayNo) {
		t.Fatalf("legacy counter order must be an open-on-the-till control: %s", counter)
	}
	if strings.Contains(counter, "Mark collected") || strings.Contains(counter, "/collect") {
		t.Fatalf("Mark collected must be gone: %s", counter)
	}
	if !strings.Contains(tabSnippet(t, counter, "open-orders-tab-counter"), `aria-current="page"`) {
		t.Fatalf("?tab=counter must preselect the counter tab: %s", counter)
	}
}

func TestOpenOrdersPage_DefaultsToCounterTabWhenNothingIsOnHold(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at) VALUES
 ('hold-kiosk','C-7 · Takeaway',120,1,'{"lines":[],"display_no":"C-7"}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, mux, "/open-orders")
	if !strings.Contains(tabSnippet(t, body, "open-orders-tab-counter"), `aria-current="page"`) || !strings.Contains(body, `data-held-id="hold-kiosk"`) {
		t.Fatalf("with On hold empty and a counter order waiting, the counter tab must open: %s", body)
	}
	// And an explicit ?tab=hold still wins, showing its empty state.
	hold := getBody(t, mux, "/open-orders?tab=hold")
	if strings.Contains(hold, `data-held-id="hold-kiosk"`) || !strings.Contains(hold, `data-testid="open-orders-empty"`) {
		t.Fatalf("?tab=hold must show the (empty) On hold list: %s", hold)
	}
}

func TestParkedOrdersPopup_TwoTabs(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	legacy := seedTabSplit(t, d)

	hold := getBody(t, mux, "/ui/parked-orders")
	if !strings.Contains(hold, `data-held-id="hold-till"`) || strings.Contains(hold, `data-held-id="hold-kiosk"`) {
		t.Fatalf("popup On hold tab: %s", hold)
	}
	counterTab := tabSnippet(t, hold, "parked-orders-tab-counter")
	if !strings.Contains(counterTab, `hx-get="/ui/parked-orders?tab=counter"`) || !strings.Contains(counterTab, ">2<") {
		t.Fatalf("popup counter tab must fetch ?tab=counter and count 2: %s", counterTab)
	}
	counter := getBody(t, mux, "/ui/parked-orders?tab=counter")
	if !strings.Contains(counter, `data-held-id="hold-kiosk"`) || strings.Contains(counter, `data-held-id="hold-till"`) {
		t.Fatalf("popup counter tab: %s", counter)
	}
	// Held kiosk order: the same resume control as any held sale.
	if !strings.Contains(counter, `hx-post="/api/pos/resume"`) {
		t.Fatalf("popup counter held order must resume like a hold: %s", counter)
	}
	if !strings.Contains(counter, `action="/open-orders/counter/open"`) || !strings.Contains(counter, legacy.ID) {
		t.Fatalf("popup legacy counter order must open on the till: %s", counter)
	}
}

// The badge counts what the two tabs list together.
func TestOpenOrdersBadge_CountsLegacyCounterOrders(t *testing.T) {
	_, d := newOpenOrdersTestMux(t)
	seedTabSplit(t, d)
	mux := http.NewServeMux()
	registerOpenOrdersBadge(mux, d)
	body := getBody(t, mux, "/ui/open-orders-badge")
	if !strings.Contains(body, `data-count="3"`) {
		t.Fatalf("badge must count 2 held + 1 legacy counter order: %s", body)
	}
}

func TestOpenOrderAgeText(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	for _, c := range []struct {
		min  int
		want string
	}{
		{0, "0 min"}, {59, "59 min"}, {60, "1 h"}, {90, "1 h"}, {1439, "23 h"}, {1440, "1 d"}, {22258, "15 d"},
	} {
		if got := openOrderAgeText(c.min, "en"); got != c.want {
			t.Errorf("openOrderAgeText(%d) = %q, want %q", c.min, got, c.want)
		}
	}
}

// The owner (2026-09-28): "remove the page pay at the counter and all the
// leftover from it". The whole production mux must no longer serve the old
// board, its poll fragment or its unpaid "collect" endpoint -- and no menu
// tile may point at it. /open-orders?tab=counter is where those orders are.
func TestOldPayAtCounterBoardIsGone(t *testing.T) {
	for _, m := range uislot.CoreMenu {
		if strings.Contains(m.Href, "kiosk-counter-orders") {
			t.Fatalf("menu still offers the removed board: %+v", m)
		}
	}
	// Every route the pages package registers (the same source scan
	// TestDemoRouteClassification uses).
	for _, p := range registeredRoutePatterns(t) {
		if strings.Contains(p, "kiosk-counter-orders") {
			t.Fatalf("route %q of the removed board is still registered", p)
		}
	}
}

// legacyCounterDeps is a full-schema till: real catalog, modifiers, price
// history and the real price resolver, so conversion prices from the live
// catalog exactly as the sale screen would.
func legacyCounterDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	db := openPagesTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	btn := ui.NewButtonStore(db)
	d := &common.Deps{
		Db:       db,
		BtnStore: btn,
		Engine:   pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, ui.PriceResolverAdapter{Store: btn}),
		State:    common.RuntimeState{Currency: "GBP", TaxRateBP: 2000},
		Settings: settings.NewStore(db),
		Menu:     []common.MenuItem{{Href: "/", Label: "nav.till"}},
	}
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-bagel','BAGEL','Avocado Lachs Bagel',850,1)`,
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-extras','Extras',0,0,2,1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-bagel','g-extras',1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-avo','g-extras','Extra avocado',150,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// The CURRENT price, not the item's configured base price.
	if err := data.NewCatalogRepo(db).SetItemPrice(context.Background(), "itm-bagel", 900); err != nil {
		t.Fatalf("set price: %v", err)
	}
	mux := http.NewServeMux()
	registerOpenOrders(mux, d)
	registerHoldAPI(mux, d)
	registerBasket(mux, d)
	return mux, d
}

func postOpenCounter(mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/open-orders/counter/open", strings.NewReader(url.Values{"id": {id}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOpenLegacyCounterOrder_BecomesThePricedLiveBasket(t *testing.T) {
	mux, d := legacyCounterDeps(t)
	ctx := context.Background()
	repo := data.NewKioskCounterOrdersRepo(d.Db)
	legacy, err := repo.Create(ctx, data.KioskCounterOrder{
		OrderType: "takeaway",
		Lines: []data.KioskCounterOrderLine{
			{Name: " avocado lachs bagel", Qty: 2, Modifiers: []string{"Extra avocado"}},
			{Name: "Black Shadow", Qty: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := postOpenCounter(mux, legacy.ID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("open legacy order = %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("redirect %q must land on the sale screen", loc)
	}
	// The unmatched line rides in the live basket (from the held sale's
	// payload), not in the URL -- the sale screen's basket shows it.
	if got := d.Engine.Basket().AddByHand; len(got) != 1 || got[0].Name != "Black Shadow" || got[0].Qty != 1 {
		t.Fatalf("basket add-by-hand = %+v, want Black Shadow × 1", got)
	}
	if body := getBody(t, mux, "/ui/basket"); !strings.Contains(body, `data-testid="counter-unmatched-notice"`) || !strings.Contains(body, "Black Shadow × 1") {
		t.Fatalf("sale screen basket must show the add-by-hand notice: %s", body)
	}

	b := d.Engine.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("basket lines = %+v, want the one matched bagel line", b.Lines)
	}
	l := b.Lines[0]
	if l.Name != "Avocado Lachs Bagel" || l.Qty != 2 || l.ItemID != "itm-bagel" {
		t.Fatalf("line = %+v, want 2 x Avocado Lachs Bagel (itm-bagel)", l)
	}
	if l.PriceCents.Minor() != 900+150 {
		t.Fatalf("unit price = %d, want the current price 900 + modifier 150", l.PriceCents.Minor())
	}
	if len(l.Modifiers) != 1 || l.Modifiers[0].OptionID != "o-avo" {
		t.Fatalf("modifiers = %+v, want Extra avocado resolved to o-avo", l.Modifiers)
	}
	if d.Engine.OrderType() != pos.OrderTypeTakeaway || d.Engine.OrderDisplayNo() != legacy.DisplayNo {
		t.Fatalf("order type %q / number %q, want takeaway / %s", d.Engine.OrderType(), d.Engine.OrderDisplayNo(), legacy.DisplayNo)
	}
	if origin := d.Engine.HeldOrigin(); origin.ID != legacy.ID {
		t.Fatalf("held origin = %+v, want the counter order's own id (a re-park keeps it in the counter tab)", origin)
	}
	got, _, _ := repo.Get(ctx, legacy.ID)
	if got.Status != data.KioskCounterOrderStatusHeld {
		t.Fatalf("legacy row status = %q, want held", got.Status)
	}

	// A second tap (double tap, or another till) makes no second sale.
	rec = postOpenCounter(mux, legacy.ID)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/open-orders?tab=counter&err=") {
		t.Fatalf("second open = %d -> %q, want back to Open orders with an error", rec.Code, rec.Header().Get("Location"))
	}
	var held int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("held_sales rows = %d (err %v), want 0: the one order is live in the basket", held, err)
	}
	if b2 := d.Engine.Basket(); len(b2.Lines) != 1 || b2.Lines[0].Qty != 2 {
		t.Fatalf("second tap changed the live basket: %+v", b2.Lines)
	}
}

func TestOpenLegacyCounterOrder_AllMatchedHasNoNotice(t *testing.T) {
	mux, d := legacyCounterDeps(t)
	legacy, err := data.NewKioskCounterOrdersRepo(d.Db).Create(context.Background(), data.KioskCounterOrder{
		Lines: []data.KioskCounterOrderLine{{Name: "Avocado Lachs Bagel", Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := postOpenCounter(mux, legacy.ID)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("open = %d -> %q, want plain 303 -> /", rec.Code, loc)
	}
}

func TestOpenLegacyCounterOrder_UnknownIDGoesBackWithError(t *testing.T) {
	mux, _ := legacyCounterDeps(t)
	rec := postOpenCounter(mux, "does-not-exist")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/open-orders?tab=counter&err=hold.error.not_found" {
		t.Fatalf("unknown id = %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}

// ut-docs#2703 (reopened, review finding 1): the unmatched lines used to
// live only in the redirect URL, so a conversion that committed followed by
// a resume that failed (here: the auto-park of a busy basket) lost them --
// the held row kept only the matched lines and every later resume showed no
// notice, silently dropping the customer's item. They now ride in the held
// sale's own payload: the next resume -- and every resume after a re-park --
// still shows them, until the cashier dismisses the notice.
func TestOpenLegacyCounterOrder_UnmatchedSurviveAFailedResume(t *testing.T) {
	mux, d := legacyCounterDeps(t)
	ctx := context.Background()
	legacy, err := data.NewKioskCounterOrdersRepo(d.Db).Create(ctx, data.KioskCounterOrder{
		Lines: []data.KioskCounterOrderLine{{Name: "Avocado Lachs Bagel", Qty: 1}, {Name: "Black Shadow", Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A busy basket whose auto-park fails: any held_sales write other than
	// the conversion itself aborts.
	if base, ok := d.Engine.ResolveBase(data.ItemIDCode("itm-bagel")); ok {
		d.Engine.AddLineWithModifiers(base, 1, nil)
	} else {
		t.Fatal("resolve bagel")
	}
	if _, err := d.Db.Exec(`CREATE TRIGGER fail_park BEFORE INSERT ON held_sales WHEN NEW.id <> '` + legacy.ID + `' BEGIN SELECT RAISE(ABORT, 'park failed'); END`); err != nil {
		t.Fatal(err)
	}
	rec := postOpenCounter(mux, legacy.ID)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/open-orders?tab=counter&err=hold.error.failed" {
		t.Fatalf("open with failing auto-park = %d -> %q, want back with hold.error.failed", rec.Code, loc)
	}
	if _, err := d.Db.Exec(`DROP TRIGGER fail_park`); err != nil {
		t.Fatal(err)
	}
	d.Engine.Reset()

	resume := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader(url.Values{"id": {legacy.ID}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if body := resume(); !strings.Contains(body, `data-testid="counter-unmatched-notice"`) || !strings.Contains(body, "Black Shadow × 1") {
		t.Fatalf("resuming the converted order must still show the add-by-hand notice: %s", body)
	}
	if strings.Contains(getBody(t, mux, "/ui/basket"), "Avocado Lachs Bagel × 1") {
		t.Fatal("a matched line must not be listed as add-by-hand")
	}
	// Re-park and resume again: still there.
	hold := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
		if d.Engine.HasItems() {
			t.Fatalf("hold did not park the order: %s", rec.Body.String())
		}
	}
	hold()
	if body := resume(); !strings.Contains(body, "Black Shadow × 1") {
		t.Fatalf("a re-parked order must keep its add-by-hand notice: %s", body)
	}
	// Dismissed: gone from this basket and from every later resume.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pos/add-by-hand/dismiss", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(getBody(t, mux, "/ui/basket"), `data-testid="counter-unmatched-notice"`) {
		t.Fatal("dismissed notice must not come back on the next basket render")
	}
	hold()
	if body := resume(); strings.Contains(body, `data-testid="counter-unmatched-notice"`) {
		t.Fatalf("a dismissed notice must not come back on resume: %s", body)
	}
}

// Review findings 2 and 3: a line is only added when it can be sold exactly
// as ordered. A required modifier group with no stored choice, a modifier
// name found in two of the item's groups (which price delta?), or a
// non-positive quantity all leave the line to the cashier instead.
func TestLegacyCounterOrderHeldSale_UnsafeLinesAreLeftToTheCashier(t *testing.T) {
	_, d := legacyCounterDeps(t)
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee','COF','Flat White',300,1)`,
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-milk','Milk',1,1,1,1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-coffee','g-milk',1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-oat','g-milk','Oat',40,1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-tea','TEA','Chai',250,1)`,
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-size','Size',0,0,1,1),('g-top','Topping',0,0,1,2)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-tea','g-size',1),('itm-tea','g-top',2)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-l1','g-size','Large',50,1),('o-l2','g-top','Large',120,1)`,
	} {
		if _, err := d.Db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	order := data.KioskCounterOrder{ID: "kco-x", DisplayNo: "C-9", Lines: []data.KioskCounterOrderLine{
		{Name: "Flat White", Qty: 1},                             // required Milk not chosen
		{Name: "Flat White", Qty: 1, Modifiers: []string{"Oat"}}, // requirement met: sold
		{Name: "Chai", Qty: 1, Modifiers: []string{"Large"}},     // ambiguous across Size and Topping
		{Name: "Avocado Lachs Bagel", Qty: 0},                    // qty 0
		{Name: "Avocado Lachs Bagel", Qty: -2},                   // negative qty
	}}
	held, err := legacyCounterOrderHeldSale(context.Background(), d, order)
	if err != nil {
		t.Fatal(err)
	}
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Lines) != 1 || snap.Lines[0].ItemID != "itm-coffee" || snap.Lines[0].PriceCents.Minor() != 340 {
		t.Fatalf("sold lines = %+v, want only the Flat White with Oat at 340", snap.Lines)
	}
	want := []string{"Flat White", "Chai", "Avocado Lachs Bagel", "Avocado Lachs Bagel"}
	if len(snap.AddByHand) != len(want) {
		t.Fatalf("add by hand = %+v, want %v", snap.AddByHand, want)
	}
	for i, w := range want {
		if snap.AddByHand[i].Name != w || snap.AddByHand[i].Qty != order.Lines[map[int]int{0: 0, 1: 2, 2: 3, 3: 4}[i]].Qty {
			t.Fatalf("add by hand[%d] = %+v, want %s as ordered", i, snap.AddByHand[i], w)
		}
	}
	if len(snap.AddByHand[1].Modifiers) != 1 || snap.AddByHand[1].Modifiers[0] != "Large" {
		t.Fatalf("an add-by-hand line keeps its modifiers: %+v", snap.AddByHand[1])
	}
}

// Review finding 6: every re-render of the popup body keeps the tab it was
// made from. The tab panel carries the active tab as an inherited hx-vals,
// so any request from inside it (a Move, and anything added later) posts
// it back; and the tablist uses a roving tabindex (WAI-ARIA tabs: only the
// selected tab is in the Tab order, the arrows move between them).
func TestParkedOrdersPopup_ActiveTabRidesEveryReRender(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	seedTabSplit(t, d)
	body := getBody(t, mux, "/ui/parked-orders?tab=counter")
	if !strings.Contains(html.UnescapeString(body), `role="tabpanel" aria-labelledby="parked-orders-tab-counter" hx-vals='{"tab":"counter"}'`) {
		t.Fatalf("counter tab panel must carry its tab for every request made inside it: %s", body)
	}
	if !strings.Contains(tabSnippet(t, body, "parked-orders-tab-counter"), `tabindex="0"`) || !strings.Contains(tabSnippet(t, body, "parked-orders-tab-hold"), `tabindex="-1"`) {
		t.Fatalf("roving tabindex: only the selected tab is tabbable: %s", body)
	}
	if !strings.Contains(body, `data-arrow-tabs`) {
		t.Fatalf("the tablist must opt in to arrow-key switching: %s", body)
	}
}
