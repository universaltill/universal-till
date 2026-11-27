package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0215, ut-docs#6581 / #6583: the JSON state-machine API the delivery
// inbox UI (S4) and the kitchen screen (S5) build on. No UI here.

func (e *caEnv) deliveryOrder(ext, status string, mutate func(*data.ChannelOrderInput)) string {
	e.t.Helper()
	o, _ := decodeFixtureForHook(e.t)
	in := o.ChannelOrderInput
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID, in.LocationID = ext, "#"+ext, "dir-"+ext, "loc_main"
	in.Status = status
	in.Lines = in.Lines[:1]
	in.Lines[0].CloudItemID, in.Lines[0].SKU = "co-itm", "CO-1"
	in.Shipping, in.Total = 0, money.FromMinor(7000)
	in.Note = "No nuts"
	in.Delivery = &data.ChannelDeliveryInput{Service: "collection", AcceptBy: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), PrepMinutes: 15}
	if mutate != nil {
		mutate(&in)
	}
	res, err := data.NewChannelOrdersRepo(e.d.Db).Apply(context.Background(), in)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.OrderID
}

func (e *caEnv) api(method, path, body string, u auth.User) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func deliveryErrCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func newDeliveryEnv(t *testing.T) *caEnv {
	e := newCAEnv(t)
	if _, err := e.d.Db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('c1','cash1','Cashier One','cashier')`); err != nil {
		t.Fatal(err)
	}
	registerDeliveryAPI(e.mux, e.d)
	return e
}

func TestDeliveryAPI_AcceptQueuesOneRowForAnySignedInRole(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d1", "new", nil)
	// A cashier (no reports, no stock_management) may accept: floor work.
	rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"accept","prep_minutes":20}`, cashier)
	if rec.Code != 200 || out["error"] != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	d := out["data"].(map[string]any)
	if d["action_id"] == "" || d["duplicate"] != false {
		t.Fatalf("data = %v", d)
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE order_id=? AND action='accept' AND prep_minutes=20 AND state='pending'`, id); n != 1 {
		t.Fatalf("outbox rows = %d", n)
	}
	// The status did not change: the cloud's answer moves it.
	order := d["order"].(map[string]any)
	if order["status"] != "new" || order["pending_action"] != "accept" {
		t.Fatalf("order = %v", order)
	}
	// Double tap is one row.
	rec, out = e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"accept","prep_minutes":20}`, cashier)
	if rec.Code != 200 || out["data"].(map[string]any)["duplicate"] != true {
		t.Fatalf("second tap: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE order_id=?`, id); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	select {
	case <-e.d.CloudSyncNow:
	default:
		t.Fatal("a tap must kick a check-in")
	}
	if *e.hits != 0 {
		t.Fatal("the request path must not dial the cloud")
	}
}

func TestDeliveryAPI_RejectNeedsAReason(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d2", "new", nil)
	for _, body := range []string{`{"action":"reject"}`, `{"action":"reject","reason":"because"}`, `{"action":"reject","reason":"busy","prep_minutes":5}`} {
		rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", body, manager)
		if rec.Code != 400 || deliveryErrCode(out) != "invalid_request" {
			t.Fatalf("%s -> %d %s", body, rec.Code, rec.Body.String())
		}
	}
	rec, _ := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"reject","reason":"busy"}`, manager)
	if rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions WHERE order_id=? AND action='reject' AND reject_reason='busy'`, id); n != 1 {
		t.Fatal("reject row missing")
	}
}

func TestDeliveryAPI_InvalidTransitionIs409AndQueuesNothing(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d3", "ready", nil)
	rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"accept"}`, manager)
	if rec.Code != 409 || deliveryErrCode(out) != "invalid_transition" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions`); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	rec, out = e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"ship"}`, manager)
	if rec.Code != 400 || deliveryErrCode(out) != "invalid_request" {
		t.Fatalf("unknown action: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeliveryAPI_ReplayOrderIsTheTapOrder(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d4", "new", nil)
	for _, a := range []string{`{"action":"accept"}`, `{"action":"preparing"}`, `{"action":"ready"}`} {
		if rec, _ := e.api("POST", "/api/delivery/orders/"+id+"/actions", a, manager); rec.Code != 200 {
			t.Fatalf("%s: %d %s", a, rec.Code, rec.Body.String())
		}
	}
	rows, err := data.NewChannelOrderActionsRepo(e.d.Db).SendableBatch(context.Background(), 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "accept" {
		t.Fatalf("one in flight per order, oldest first: %+v %v", rows, err)
	}
	var order []string
	all, _ := data.NewChannelOrderActionsRepo(e.d.Db).ForOrder(context.Background(), id)
	for _, a := range all {
		order = append(order, a.Action)
	}
	if strings.Join(order, ",") != "accept,preparing,ready" {
		t.Fatalf("queue = %v", order)
	}
}

func TestDeliveryAPI_AdditionalTillGets403MainTillRequired(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d5", "new", nil)
	if _, err := e.d.Db.Exec(`INSERT INTO settings(key,value) VALUES('sync.primary_url','http://main.local:8080')`); err != nil {
		t.Fatal(err)
	}
	rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"accept"}`, manager)
	if rec.Code != 403 || deliveryErrCode(out) != "main_till_required" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM channel_order_actions`); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestDeliveryAPI_RefusesCrossOriginUnknownOrderAndBadBodies(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d6", "new", nil)
	req := httptest.NewRequest("POST", "/api/delivery/orders/"+id+"/actions", strings.NewReader(`{"action":"accept"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, manager))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if rec, out := e.api("POST", "/api/delivery/orders/nope/actions", `{"action":"accept"}`, manager); rec.Code != 404 || deliveryErrCode(out) != "order_not_found" {
		t.Fatalf("unknown order: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{``, `not json`, `{"action":"accept","extra":1}`, `{"action":"accept"} {"x":1}`} {
		if rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", body, manager); rec.Code != 400 || deliveryErrCode(out) != "invalid_request" {
			t.Fatalf("body %q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// A webshop order is not a delivery order: this API refuses it.
	web := e.order("w1", "new", 0)
	if rec, out := e.api("POST", "/api/delivery/orders/"+web+"/actions", `{"action":"accept"}`, manager); rec.Code != 404 || deliveryErrCode(out) != "order_not_found" {
		t.Fatalf("webshop order: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeliveryAPI_BlockedActionsAnswer409(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("d7", "new", nil)
	if rec, _ := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"accept"}`, manager); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if _, err := data.NewChannelOrderActionsRepo(e.d.Db).BlockPending(context.Background(), "main_till_required"); err != nil {
		t.Fatal(err)
	}
	rec, out := e.api("POST", "/api/delivery/orders/"+id+"/actions", `{"action":"preparing"}`, manager)
	if rec.Code != 409 || deliveryErrCode(out) != "actions_blocked" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestDeliveryAPI_ListAndSummaryShowTheStateTheUIDrawsFrom(t *testing.T) {
	e := newDeliveryEnv(t)
	n1 := e.deliveryOrder("n1", "new", nil)
	e.deliveryOrder("n2", "preparing", nil)
	e.deliveryOrder("old", "collected", func(in *data.ChannelOrderInput) {})
	e.order("web", "new", 0) // a webshop order never appears
	if _, err := e.d.Db.Exec(`UPDATE channel_orders SET status_changed_at=? WHERE external_order_id='old'`, time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	rec, out := e.api("GET", "/api/delivery/orders", "", cashier)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	orders := out["data"].(map[string]any)["orders"].([]any)
	if len(orders) != 2 {
		t.Fatalf("open orders = %d (collected 5 h ago and the webshop order are out): %s", len(orders), rec.Body.String())
	}
	var first map[string]any
	for _, o := range orders {
		if o.(map[string]any)["id"] == n1 {
			first = o.(map[string]any)
		}
	}
	if first == nil || first["service"] != "collection" || first["note"] != "No nuts" || first["prep_minutes"] != 15.0 {
		t.Fatalf("order = %v", first)
	}
	acts := first["allowed_actions"].([]any)
	if len(acts) != 2 || acts[0] != "accept" || acts[1] != "reject" {
		t.Fatalf("allowed_actions = %v", acts)
	}
	lines := first["lines"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["name"] == "" {
		t.Fatalf("lines = %v", lines)
	}
	for _, banned := range []string{"unit_price", "total", "price", "customer", "phone", "address", "email", "tip", "fees"} {
		if strings.Contains(rec.Body.String(), `"`+banned) {
			t.Errorf("the board view must carry no %q field", banned)
		}
	}
	rec, out = e.api("GET", "/api/delivery/summary", "", cashier)
	s := out["data"].(map[string]any)
	if rec.Code != 200 || s["waiting"] != 1.0 || s["nearest_accept_by"] == "" || s["main_till"] != true {
		t.Fatalf("summary = %v", s)
	}
}

func TestDeliveryAPI_AdditionalTillMayReadButSeesMainTillFlag(t *testing.T) {
	e := newDeliveryEnv(t)
	e.deliveryOrder("r1", "new", nil)
	if _, err := e.d.Db.Exec(`INSERT INTO settings(key,value) VALUES('sync.primary_url','http://main.local:8080')`); err != nil {
		t.Fatal(err)
	}
	rec, out := e.api("GET", "/api/delivery/summary", "", cashier)
	if rec.Code != 200 || out["data"].(map[string]any)["main_till"] != false {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestDeliveryAPI_AvailabilityAndServiceNeedStockManagementAndAreAudited(t *testing.T) {
	e := newDeliveryEnv(t)
	if rec, out := e.api("POST", "/api/delivery/availability", `{"item_id":"co-itm","mode":"today"}`, cashier); rec.Code != 403 {
		t.Fatalf("cashier: %d %v", rec.Code, out)
	}
	rec, out := e.api("POST", "/api/delivery/availability", `{"item_id":"co-itm","mode":"indefinite"}`, manager)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if d := out["data"].(map[string]any); d["unavailable_until"] != data.UnavailableIndefinitely {
		t.Fatalf("data = %v", d)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action='item_unavailable_set' AND actor_id='mgr-1'`); n != 1 {
		t.Fatalf("audit = %d", n)
	}
	rec, _ = e.api("POST", "/api/delivery/availability", `{"item_id":"co-itm","mode":"clear"}`, manager)
	if rec.Code != 200 {
		t.Fatalf("clear: %d", rec.Code)
	}
	rec, out = e.api("POST", "/api/delivery/availability", `{"item_id":"co-itm","mode":"until","until":"`+time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)+`"}`, manager)
	if rec.Code != 200 {
		t.Fatalf("until: %d %s", rec.Code, rec.Body.String())
	}
	rec, out = e.api("GET", "/api/delivery/availability", "", cashier)
	if un := out["data"].(map[string]any)["unavailable"].([]any); rec.Code != 200 || len(un) != 1 || un[0].(map[string]any)["item_id"] != "co-itm" {
		t.Fatalf("availability list: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"item_id":"nope","mode":"indefinite"}`, `{"item_id":"co-itm","mode":"until","until":"yesterday"}`, `{"item_id":"co-itm","mode":"sideways"}`} {
		if rec, _ := e.api("POST", "/api/delivery/availability", body, manager); rec.Code != 400 && rec.Code != 404 {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	rec, out = e.api("POST", "/api/delivery/service", `{"state":"paused","reason":"busy","prep_minutes":45}`, manager)
	if rec.Code != 200 || out["data"].(map[string]any)["state"] != "paused" {
		t.Fatalf("service: %d %s", rec.Code, rec.Body.String())
	}
	rec, out = e.api("GET", "/api/delivery/service", "", cashier)
	if rec.Code != 200 || out["data"].(map[string]any)["prep_minutes"] != 45.0 {
		t.Fatalf("service get: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.api("POST", "/api/delivery/service", `{"state":"closed"}`, manager); rec.Code != 400 {
		t.Fatalf("bad state: %d", rec.Code)
	}
	if e.count(`SELECT COUNT(*) FROM delivery_signals WHERE state='pending'`) < 4 {
		t.Fatal("each change must queue a neutral signal")
	}
}

// The existing Online orders pages show a delivery order's new statuses and
// facts but offer none of the webshop fulfilment actions on it (S4 draws the
// delivery controls); the note is escaped text.
func TestChannelOrderDetail_DeliveryOrderShowsFactsAndNoWebshopActions(t *testing.T) {
	e := newDeliveryEnv(t)
	id := e.deliveryOrder("p1", "preparing", func(in *data.ChannelOrderInput) {
		in.Note = `<b>No nuts</b>`
		in.Lines[0].Modifiers = []data.ChannelLineModifier{{ExternalID: "m", Name: "Large", Qty: 1, PriceDelta: money.FromMinor(100)}}
	})
	rec := e.do("GET", "/channel-orders/"+id, manager, nil, nil)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	for _, want := range []string{"Preparing", "Collection", "Large", "&lt;b&gt;No nuts&lt;/b&gt;", "Note from the customer"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q:\n%.3000s", want, body)
		}
	}
	if strings.Contains(body, "<b>No nuts</b>") {
		t.Error("the note must be escaped")
	}
	if got := e.buttons(body); len(got) != 0 {
		t.Fatalf("webshop actions offered on a delivery order: %v", got)
	}
	list := e.do("GET", "/channel-orders", manager, nil, nil).Body.String()
	if !strings.Contains(list, "Preparing") {
		t.Fatalf("list lacks the delivery status text")
	}
}

func TestChannelOrderDetail_LateCancelAndUnmappedRejectAreSaid(t *testing.T) {
	e := newDeliveryEnv(t)
	late := e.deliveryOrder("p2", "preparing", nil)
	o, _, _ := data.NewChannelOrdersRepo(e.d.Db).Get(context.Background(), late)
	if _, err := e.d.Db.Exec(`UPDATE channel_orders SET status='cancelled', late_cancel=1 WHERE id=?`, o.ID); err != nil {
		t.Fatal(err)
	}
	if body := e.do("GET", "/channel-orders/"+late, manager, nil, nil).Body.String(); !strings.Contains(body, "Cancelled after cooking started") {
		t.Fatalf("late cancel not shown:\n%.2000s", body)
	}
	rej := e.deliveryOrder("p3", "new", func(in *data.ChannelOrderInput) {
		in.Lines = append(in.Lines, data.ChannelOrderLineInput{LineID: "l-9", SKU: "NOPE", Name: "Mystery", Qty: 1})
	})
	if body := e.do("GET", "/channel-orders/"+rej, manager, nil, nil).Body.String(); !strings.Contains(body, "not in your catalogue") {
		t.Fatalf("unmapped reject not said:\n%.2000s", body)
	}
}
