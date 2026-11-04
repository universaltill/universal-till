package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Staff actions on an online order (ut-docs#4768 slice D): accept, pick,
// pack, ship, cancel are queued in the till's outbox, never sent in the
// request, and change neither the order's status nor stock.

type caEnv struct {
	t     *testing.T
	d     *common.Deps
	mux   *http.ServeMux
	cloud *httptest.Server
	hits  *int
}

func newCAEnv(t *testing.T) *caEnv {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	dp := newDesignerTestDeps(t)
	hits := 0
	// A cloud that fails the test if the request path dials it.
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(cloud.Close)
	dp.Cfg.Marketplace = config.MarketplaceConfig{EndpointURL: cloud.URL, StoreID: "store-1", MerchantToken: "tok-1"}
	dp.CloudSyncNow = make(chan struct{}, 1)
	mux := http.NewServeMux()
	registerChannelOrdersPage(mux, dp)
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',10)`,
		`INSERT INTO users (id, username, display_name, role) VALUES ('mgr-1','mgr1','Manager One','manager')`,
		`INSERT INTO roles (role, label, origin) VALUES ('analyst','Analyst','cloud')`,
		`INSERT INTO role_permissions (role, action, granted) VALUES ('analyst','reports',1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return &caEnv{t: t, d: dp, mux: mux, cloud: cloud, hits: &hits}
}

func (e *caEnv) order(ext, status string, rank int) string {
	e.t.Helper()
	o, _ := decodeFixtureForHook(e.t)
	in := o.ChannelOrderInput
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID, in.LocationID = ext, "#"+ext, "dir-"+ext, "loc_main"
	in.Lines[0].CloudItemID, in.Lines[0].SKU = "co-itm", "CO-1"
	res, err := data.NewChannelOrdersRepo(e.d.Db).Apply(context.Background(), in)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.d.Db.Exec(`UPDATE channel_orders SET status=?, fulfilment_rank=? WHERE id=?`, status, rank, res.OrderID); err != nil {
		e.t.Fatal(err)
	}
	return res.OrderID
}

var (
	manager = auth.User{ID: "mgr-1", Role: "manager"}
	cashier = auth.User{ID: "c1", Role: "cashier"}
	analyst = auth.User{ID: "an-1", Role: "analyst"}
)

func (e *caEnv) do(method, path string, u auth.User, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

var htmx = map[string]string{"HX-Request": "true"}

func (e *caEnv) post(id string, u auth.User, form url.Values) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/channel-orders/"+id+"/actions", u, form, htmx)
}

func (e *caEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *caEnv) buttons(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`data-channel-action="([a-z]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestChannelActions_AcceptQueuesRowAndAuditWithoutNetworkOrStatusChange(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "new", 0)
	rec := e.post(id, manager, url.Values{"action": {"accept"}})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `id="channel-order-actions-heading"`) || !strings.Contains(body, "Accept not sent yet") {
		t.Fatalf("%d:\n%.2500s", rec.Code, body)
	}
	if strings.Contains(body, "<html") {
		t.Fatal("an htmx swap must return only the block")
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE order_id=? AND state='pending' AND action='accept'`, id); n != 1 {
		t.Fatalf("outbox rows = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE entity_id=? AND action='channel_order_action_queued' AND actor_id='mgr-1'`, id); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	if *e.hits != 0 {
		t.Fatalf("the request dialled the cloud %d times", *e.hits)
	}
	var status string
	_ = e.d.Db.QueryRow(`SELECT status FROM channel_orders WHERE id=?`, id).Scan(&status)
	if status != "new" || e.count(`SELECT COUNT(*) FROM stock_movements WHERE type LIKE 'channel_%'`) != 1 {
		t.Fatalf("status %s; stock movements must be only the order's own sale", status)
	}
	select {
	case <-e.d.CloudSyncNow:
	default:
		t.Fatal("no check-in kick after queuing")
	}
	// The pending action unlocks pick/pack/ship/cancel behind it, and hides accept.
	if got := strings.Join(e.buttons(body), ","); got != "pick,pack,ship,cancel" {
		t.Fatalf("buttons = %s", got)
	}
}

func TestChannelActions_NoJSRedirectsToTheDetailPage(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "new", 0)
	rec := e.do(http.MethodPost, "/channel-orders/"+id+"/actions", manager, url.Values{"action": {"accept"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/channel-orders/"+id {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 1 {
		t.Fatal("no row")
	}
}

func TestChannelActions_GateRefusals(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "new", 0)
	form := url.Values{"action": {"accept"}}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"cashier":                  e.post(id, cashier, form),
		"reports without stock":    e.post(id, analyst, form),
		"cross-site":               e.do(http.MethodPost, "/channel-orders/"+id+"/actions", manager, form, map[string]string{"HX-Request": "true", "Sec-Fetch-Site": "cross-site"}),
		"foreign origin":           e.do(http.MethodPost, "/channel-orders/"+id+"/actions", manager, form, map[string]string{"HX-Request": "true", "Origin": "https://evil.example"}),
		"dismiss as cashier":       e.do(http.MethodPost, "/channel-orders/"+id+"/actions/x/dismiss", cashier, url.Values{}, htmx),
		"dismiss-all as cashier":   e.do(http.MethodPost, "/channel-orders/blocked/dismiss", cashier, url.Values{"order_id": {id}}, htmx),
		"dismiss cross-site":       e.do(http.MethodPost, "/channel-orders/"+id+"/actions/x/dismiss", manager, url.Values{}, map[string]string{"Sec-Fetch-Site": "cross-site"}),
		"dismiss-all cross-origin": e.do(http.MethodPost, "/channel-orders/blocked/dismiss", manager, url.Values{"order_id": {id}}, map[string]string{"Origin": "https://evil.example"}),
	} {
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
		t.Fatal("a refused request wrote a row")
	}
	// The view stays on Reports alone: the analyst sees the order, with no buttons and the note.
	rec := e.do(http.MethodGet, "/channel-orders/"+id, analyst, nil, nil)
	if rec.Code != 200 || len(e.buttons(rec.Body.String())) != 0 || !strings.Contains(rec.Body.String(), "can only be changed on the main till") {
		t.Fatalf("analyst detail %d buttons %v", rec.Code, e.buttons(rec.Body.String()))
	}
}

func TestChannelActions_AdditionalTillAndUnenrolledTillHaveNoButtons(t *testing.T) {
	for name, mutate := range map[string]func(e *caEnv){
		"additional till": func(e *caEnv) {
			if err := e.d.Settings.Set(context.Background(), "sync.primary_url", "http://main.lan:8080"); err != nil {
				t.Fatal(err)
			}
		},
		"unenrolled": func(e *caEnv) { e.d.Cfg.Marketplace.MerchantToken = "" },
	} {
		e := newCAEnv(t)
		id := e.order("1001", "new", 0)
		mutate(e)
		detail := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil)
		if detail.Code != 200 || len(e.buttons(detail.Body.String())) != 0 || !strings.Contains(detail.Body.String(), "can only be changed on the main till") {
			t.Errorf("%s: detail %d buttons %v", name, detail.Code, e.buttons(detail.Body.String()))
		}
		// A request that arrives from a page shown earlier: the block is re-rendered with the reason, nothing queued.
		rec := e.post(id, manager, url.Values{"action": {"accept"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Online orders can only be changed on the main till.") || len(e.buttons(rec.Body.String())) != 0 {
			t.Errorf("%s: fragment post %d:\n%.800s", name, rec.Code, rec.Body.String())
		}
		// A plain POST (no htmx) is refused outright.
		if rec := e.do(http.MethodPost, "/channel-orders/"+id+"/actions", manager, url.Values{"action": {"accept"}}, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s: plain post = %d", name, rec.Code)
		}
		if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
			t.Errorf("%s: a row was queued", name)
		}
	}
}

func TestChannelActions_UnknownOrderIs404AndUnknownActionIsAnInlineError(t *testing.T) {
	e := newCAEnv(t)
	if rec := e.post("nope", manager, url.Values{"action": {"accept"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", rec.Code)
	}
	id := e.order("1001", "new", 0)
	rec := e.post(id, manager, url.Values{"action": {"explode"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no longer possible") || e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
		t.Fatalf("%d:\n%.800s", rec.Code, rec.Body.String())
	}
	// A step the order cannot take (ship on a new order) is refused too.
	rec = e.post(id, manager, url.Values{"action": {"ship"}, "carrier": {"Royal Mail"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no longer possible") || e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
		t.Fatalf("%d:\n%.800s", rec.Code, rec.Body.String())
	}
}

func TestChannelActions_ShipFieldErrorsWriteNothingAndReopenTheForm(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "accepted", 1)
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"no carrier", url.Values{"carrier": {"  "}}, "Enter the carrier."},
		{"bidi carrier", url.Values{"carrier": {"Royal\u202eMail"}}, "up to 64 characters"},
		{"bad number", url.Values{"carrier": {"X"}, "tracking_number": {"AB#1"}}, "letters, digits, spaces"},
		{"http link", url.Values{"carrier": {"X"}, "tracking_url": {"http://a.example/x"}}, "starts with https://"},
		{"ip link", url.Values{"carrier": {"X"}, "tracking_url": {"https://1.2.3.4/x"}}, "starts with https://"},
	}
	for _, c := range cases {
		c.form.Set("action", "ship")
		rec := e.post(id, manager, c.form)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, `role="alert"`) || !regexp.MustCompile(`<details[^>]*data-form="ship"[^>]* open`).MatchString(body) {
			t.Errorf("%s: %d\n%.3000s", c.name, rec.Code, body)
		}
		// htmx inserts this block before it removes the old one, whose ship panel is
		// still open: an exclusive-accordion name= here makes Chromium close the
		// reopened panel, hiding the error (found in a driven run, ut-docs#4768).
		if regexp.MustCompile(`<details[^>]*name="channel-order-action"`).MatchString(body) {
			t.Errorf("%s: a re-render that reopens a panel must not carry the accordion name", c.name)
		}
		if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
			t.Fatalf("%s: a row was written", c.name)
		}
	}
	// Submitted values come back in the form (escaped).
	rec := e.post(id, manager, url.Values{"action": {"ship"}, "carrier": {"Royal <b>Mail"}, "tracking_number": {"AB#1"}})
	if !strings.Contains(rec.Body.String(), "Royal &lt;b&gt;Mail") || strings.Contains(rec.Body.String(), "<b>Mail") {
		t.Fatalf("form values are not echoed escaped:\n%.3000s", rec.Body.String())
	}
}

func TestChannelActions_ValidShipQueuesTrimmedValuesAndShowsThemAsEscapedText(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "accepted", 1)
	rec := e.post(id, manager, url.Values{"action": {"ship"}, "carrier": {"  Royal Mail "}, "tracking_number": {" AB123 "},
		"tracking_url": {" https://tracking.example/AB123?x=1&y=2 "}})
	if rec.Code != 200 {
		t.Fatalf("%d:\n%.2000s", rec.Code, rec.Body.String())
	}
	var c, n, u string
	if err := e.d.Db.QueryRow(`SELECT carrier, tracking_number, tracking_url FROM channel_order_actions WHERE order_id=?`, id).Scan(&c, &n, &u); err != nil {
		t.Fatal(err)
	}
	if c != "Royal Mail" || n != "AB123" || u != "https://tracking.example/AB123?x=1&y=2" {
		t.Fatalf("stored %q %q %q", c, n, u)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Shipping not sent yet") || !strings.Contains(body, "Royal Mail") || !strings.Contains(body, "https://tracking.example/AB123?x=1&amp;y=2") {
		t.Fatalf("queued shipment not shown:\n%.3000s", body)
	}
	// AC 15 (orchestrator): the tracking link is text, never an anchor.
	if regexp.MustCompile(`<a[^>]*tracking\.example`).MatchString(body) {
		t.Fatal("the tracking link must not be rendered as a link")
	}
	// Nothing left to do after a pending ship: no buttons.
	if len(e.buttons(body)) != 0 {
		t.Fatalf("buttons after a pending ship: %v", e.buttons(body))
	}
}

func TestChannelActions_CancelNeedsAReasonAndRestockIsTickedByDefault(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "accepted", 1)
	detail := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
	if !regexp.MustCompile(`<input[^>]*name="restock"[^>]*checked`).MatchString(detail) {
		t.Fatalf("restock must be ticked by default:\n%.4000s", detail)
	}
	if !regexp.MustCompile(`<option value="" selected[^>]*>Choose a reason`).MatchString(detail) && !strings.Contains(detail, `<option value="">Choose a reason</option>`) {
		t.Fatalf("the reason list starts with an empty choice:\n%.4000s", detail)
	}
	for _, bad := range []string{"", "refund", "OTHER"} {
		rec := e.post(id, manager, url.Values{"action": {"cancel"}, "reason": {bad}, "restock": {"1"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Choose a reason.") || e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
			t.Fatalf("reason %q: %d\n%.1500s", bad, rec.Code, rec.Body.String())
		}
	}
	// Unticked restock is stored as false; the form comes back with the reason kept on an error.
	rec := e.post(id, manager, url.Values{"action": {"cancel"}, "reason": {"fraud"}})
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	var reason string
	var restock int
	if err := e.d.Db.QueryRow(`SELECT cancel_reason, cancel_restock FROM channel_order_actions WHERE order_id=?`, id).Scan(&reason, &restock); err != nil || reason != "fraud" || restock != 0 {
		t.Fatalf("stored %q %d %v", reason, restock, err)
	}
	if !strings.Contains(rec.Body.String(), "Cancel not sent yet") || len(e.buttons(rec.Body.String())) != 0 {
		t.Fatalf("after a pending cancel:\n%.2000s", rec.Body.String())
	}
}

// The rendered buttons are exactly what the status machine allows.
func TestChannelActions_ButtonsMatchTheStatusMachine(t *testing.T) {
	e := newCAEnv(t)
	for i, c := range []struct {
		status string
		rank   int
	}{{"new", 0}, {"accepted", 1}, {"picked", 2}, {"packed", 3}, {"shipped", 4}, {"cancelled", 1}, {"refunded", 0}, {"partially_refunded", 2}} {
		id := e.order("m"+string(rune('a'+i)), c.status, c.rank)
		body := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
		want := data.ChannelOrderAllowedActions(c.status, c.rank, nil)
		if got := e.buttons(body); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: buttons %v, machine %v", c.status, got, want)
		}
	}
}

func TestChannelActions_ButtonWeightsFollowTheNextStep(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "accepted", 1)
	body := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
	// Next step (Mark as picked) is primary; skipping ahead is secondary; opening cancel is not destructive.
	if !regexp.MustCompile(`class="btn" data-channel-action="pick"`).MatchString(body) {
		t.Errorf("pick must be the primary button:\n%.4000s", body)
	}
	if !regexp.MustCompile(`class="btn secondary" data-channel-action="pack"`).MatchString(body) {
		t.Errorf("pack must be secondary")
	}
	if strings.Count(body, "btn danger") != 1 {
		t.Errorf("exactly one danger button (the cancel confirm), got %d", strings.Count(body, "btn danger"))
	}
	for _, want := range []string{`hx-disabled-elt`, `novalidate`, `hx-target="#channel-order-actions"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	// Only the actions card: the shared layout carries dialogs of its own.
	start := strings.Index(body, `id="channel-order-actions"`)
	card := body[start : start+strings.Index(body[start:], "</section>")]
	for _, bad := range []string{"showModal", "<dialog", "hx-confirm", "autofocus", "data-action=\"accept\""} {
		if strings.Contains(card, bad) {
			t.Errorf("the block must not contain %q", bad)
		}
	}
}

func TestChannelActions_OutcomesAreShownTranslatedUntilDismissed(t *testing.T) {
	e := newCAEnv(t)
	repo := data.NewChannelOrderActionsRepo(e.d.Db)
	codes := map[string]string{
		"invalid_transition": "has to be accepted first", "order_cancelled": "already cancelled", "order_refunded": "already refunded",
		"already_shipped": "already shipped", "stale": "more than 30 days", "plan_required": "plan no longer includes",
		"connection_revoked": "no longer connected", "order_changed": "changed after you opened", "unknown_order": "does not know this order",
		"invalid_action": "refused the details", "something_new": "refused the change", "unknown": "refused the change",
	}
	i := 0
	for code, want := range codes {
		i++
		id := e.order("r"+string(rune('a'+i)), "accepted", 1)
		a, _, err := repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id, Action: "pack", Now: nowUTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.AnswerRefused(context.Background(), a, code); err != nil {
			t.Fatal(err)
		}
		body := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
		if !strings.Contains(body, "The online shop did not accept this:") || !strings.Contains(body, want) || !strings.Contains(body, "Mark as packed") {
			t.Errorf("%s: outcome not shown:\n%.3000s", code, body)
		}
		if strings.Contains(body, code+"</") && code == "something_new" {
			t.Errorf("an unknown code must not be shown raw")
		}
		// Dismiss (htmx) removes it; the buttons come back.
		rec := e.do(http.MethodPost, "/channel-orders/"+id+"/actions/"+a+"/dismiss", manager, url.Values{}, htmx)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "did not accept") || len(e.buttons(rec.Body.String())) == 0 {
			t.Errorf("%s: dismiss %d\n%.1500s", code, rec.Code, rec.Body.String())
		}
		if e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE action_id=?`, a) != 0 {
			t.Errorf("%s: row not dismissed", code)
		}
	}
}

func TestChannelActions_WritebackNoteAndPendingCannotBeDismissed(t *testing.T) {
	e := newCAEnv(t)
	repo := data.NewChannelOrderActionsRepo(e.d.Db)
	id := e.order("1001", "accepted", 1)
	a, _, _ := repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id, Action: "ship", Shipment: &data.ChannelShipment{Carrier: "DHL"}, Now: nowUTC()})
	// Still pending: the dismiss route refuses to delete it.
	e.do(http.MethodPost, "/channel-orders/"+id+"/actions/"+a+"/dismiss", manager, url.Values{}, htmx)
	if e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE action_id=?`, a) != 1 {
		t.Fatal("a pending action was dismissed")
	}
	if err := repo.AnswerApplied(context.Background(), a, 3, "skipped"); err != nil {
		t.Fatal(err)
	}
	body := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
	if !strings.Contains(body, "does not take this change automatically") {
		t.Fatalf("note not shown:\n%.3000s", body)
	}
}

func TestChannelActions_BlockedBannerHidesButtonsAndDismissAllBringsThemBack(t *testing.T) {
	e := newCAEnv(t)
	repo := data.NewChannelOrderActionsRepo(e.d.Db)
	id := e.order("1001", "new", 0)
	id2 := e.order("1002", "new", 0)
	_, _, _ = repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id, Action: "accept", Now: nowUTC()})
	_, _, _ = repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id2, Action: "accept", Now: nowUTC()})
	if n, _ := repo.BlockPending(context.Background(), "main_till_required"); n != 2 {
		t.Fatal(n)
	}
	for _, o := range []string{id, id2} {
		body := e.do(http.MethodGet, "/channel-orders/"+o, manager, nil, nil).Body.String()
		if !strings.Contains(body, "no longer the main till") || !strings.Contains(body, "Unsent actions") || len(e.buttons(body)) != 0 ||
			!strings.Contains(body, "Dismiss unsent actions") || !strings.Contains(body, "Accept not sent yet") {
			t.Fatalf("blocked banner:\n%.3500s", body)
		}
	}
	// A tap while blocked is refused with the banner, not queued.
	rec := e.post(id, manager, url.Values{"action": {"cancel"}, "reason": {"other"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no longer the main till") || e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE action='cancel'`) != 0 {
		t.Fatalf("%d", rec.Code)
	}
	rec = e.do(http.MethodPost, "/channel-orders/blocked/dismiss", manager, url.Values{"order_id": {id}}, htmx)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "no longer the main till") || len(e.buttons(rec.Body.String())) == 0 {
		t.Fatalf("dismiss-all %d:\n%.2000s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
		t.Fatal("blocked rows remain")
	}
}

// A former main till (now an additional till) must still be able to clear its blocked rows.
func TestChannelActions_DismissAllWorksOnAFormerMainTill(t *testing.T) {
	e := newCAEnv(t)
	repo := data.NewChannelOrderActionsRepo(e.d.Db)
	id := e.order("1001", "new", 0)
	_, _, _ = repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id, Action: "accept", Now: nowUTC()})
	_, _ = repo.BlockPending(context.Background(), "main_till_required")
	if err := e.d.Settings.Set(context.Background(), "sync.primary_url", "http://main.lan:8080"); err != nil {
		t.Fatal(err)
	}
	body := e.do(http.MethodGet, "/channel-orders/"+id, manager, nil, nil).Body.String()
	if !strings.Contains(body, "Dismiss unsent actions") {
		t.Fatalf("a former main till must offer the dismiss:\n%.3000s", body)
	}
	rec := e.do(http.MethodPost, "/channel-orders/blocked/dismiss", manager, url.Values{"order_id": {id}}, nil)
	if rec.Code != http.StatusSeeOther || e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 0 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestChannelActions_ListShowsPendingAndNotAcceptedChips(t *testing.T) {
	e := newCAEnv(t)
	repo := data.NewChannelOrderActionsRepo(e.d.Db)
	id := e.order("1001", "accepted", 1)
	id2 := e.order("1002", "accepted", 1)
	_, _, _ = repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id, Action: "pack", Now: nowUTC()})
	a2, _, _ := repo.Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: id2, Action: "pack", Now: nowUTC()})
	_ = repo.AnswerRefused(context.Background(), a2, "order_cancelled")
	body := e.do(http.MethodGet, "/channel-orders", manager, nil, nil).Body.String()
	if !strings.Contains(body, `data-status="pending"`) || !strings.Contains(body, "Packed not sent yet") || !strings.Contains(body, "Not accepted") {
		t.Fatalf("list chips missing:\n%.4000s", body)
	}
	// The displayed status is still the stored one.
	if strings.Count(body, `data-status="accepted"`) != 2 {
		t.Fatalf("status chips changed")
	}
}

func TestChannelActions_QueuedAcceptThenCancelThroughTheHandlers(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "new", 0)
	if rec := e.post(id, manager, url.Values{"action": {"accept"}}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := e.post(id, manager, url.Values{"action": {"cancel"}, "reason": {"out_of_stock"}, "restock": {"1"}}); rec.Code != 200 || e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 2 {
		t.Fatalf("%d rows=%d", rec.Code, e.count(`SELECT COUNT(*) FROM channel_order_actions`))
	}
	// A double tap is one row.
	_ = e.post(id, manager, url.Values{"action": {"cancel"}, "reason": {"out_of_stock"}})
	if e.count(`SELECT COUNT(*) FROM channel_order_actions`) != 2 {
		t.Fatal("double tap wrote a second row")
	}
}

func nowUTC() time.Time { return time.Now() }

// Review finding 7: a ship validation error re-renders with the ship form
// open; the (closed) cancel panel still defaults to restocking.
func TestChannelActions_RestockStaysTickedAfterAShipError(t *testing.T) {
	e := newCAEnv(t)
	id := e.order("1001", "accepted", 1)
	rec := e.post(id, manager, url.Values{"action": {"ship"}, "carrier": {"Royal <b>Mail"}, "tracking_number": {"AB#1"}})
	if !regexp.MustCompile(`<input[^>]*name="restock"[^>]*checked`).MatchString(rec.Body.String()) {
		t.Fatalf("restock must stay ticked after a ship error:\n%.4000s", rec.Body.String())
	}
}

// Review finding 6: dismissing on an unknown order is a 404, like the other routes.
func TestChannelActions_DismissOnAnUnknownOrderIs404(t *testing.T) {
	e := newCAEnv(t)
	rec := e.do(http.MethodPost, "/channel-orders/nope/actions/x/dismiss", manager, url.Values{}, htmx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d\n%.800s", rec.Code, rec.Body.String())
	}
}
