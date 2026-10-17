package cloudsync

// ut-docs#4337 (ADR-0159 §4/§5): the till side of the guest_order
// directive — decode and bounds, the hook contract, main-till scoping, the
// per-tick cap, the result's optional data object, idempotent re-delivery
// and the retry of a result the cloud did not acknowledge.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func guestLine(item string, qty any, opts ...string) map[string]any {
	l := map[string]any{"item_id": item, "qty": qty}
	if opts != nil {
		o := make([]any, 0, len(opts))
		for _, s := range opts {
			o = append(o, s)
		}
		l["modifier_option_ids"] = o
	}
	return l
}

func guestDirective(id, guestOrderID string, lines ...map[string]any) map[string]any {
	ls := make([]any, 0, len(lines))
	for _, l := range lines {
		ls = append(ls, l)
	}
	return map[string]any{"id": id, "type": "guest_order", "payload": map[string]any{
		"guest_order_id": guestOrderID,
		"table_token":    "0123456789abcdef0123456789abcdef",
		"lines":          ls,
		"order_type":     "takeaway",
	}}
}

func toDirective(t *testing.T, m map[string]any) directive {
	t.Helper()
	b, _ := json.Marshal(m)
	var d directive
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDecodeGuestOrder_Bounds(t *testing.T) {
	many := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = guestLine(fmt.Sprintf("itm-%d", i), 1)
		}
		return out
	}
	opts := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("opt-%d", i)
		}
		return out
	}
	cases := map[string]struct {
		d    map[string]any
		want string
	}{
		"one line":          {guestDirective("d", "g1", guestLine("itm-1", 2)), ""},
		"fifty lines":       {guestDirective("d", "g1", many(50)...), ""},
		"zero lines":        {guestDirective("d", "g1"), GuestOrderBadOrder},
		"fifty-one lines":   {guestDirective("d", "g1", many(51)...), GuestOrderBadOrder},
		"qty 0":             {guestDirective("d", "g1", guestLine("itm-1", 0)), GuestOrderBadOrder},
		"qty 99":            {guestDirective("d", "g1", guestLine("itm-1", 99)), ""},
		"qty 100":           {guestDirective("d", "g1", guestLine("itm-1", 100)), GuestOrderBadOrder},
		"qty 1.5":           {guestDirective("d", "g1", guestLine("itm-1", 1.5)), GuestOrderBadOrder},
		"qty negative":      {guestDirective("d", "g1", guestLine("itm-1", -1)), GuestOrderBadOrder},
		"qty string":        {guestDirective("d", "g1", guestLine("itm-1", "2")), GuestOrderBadOrder},
		"qty missing":       {guestDirective("d", "g1", map[string]any{"item_id": "itm-1"}), GuestOrderBadOrder},
		"20 options":        {guestDirective("d", "g1", guestLine("itm-1", 1, opts(20)...)), ""},
		"21 options":        {guestDirective("d", "g1", guestLine("itm-1", 1, opts(21)...)), GuestOrderBadOrder},
		"duplicate option":  {guestDirective("d", "g1", guestLine("itm-1", 1, "o1", "o1")), GuestOrderBadOrder},
		"blank option":      {guestDirective("d", "g1", guestLine("itm-1", 1, " ")), GuestOrderBadOrder},
		"blank item":        {guestDirective("d", "g1", guestLine("", 1)), GuestOrderBadOrder},
		"empty order id":    {guestDirective("d", "", guestLine("itm-1", 1)), GuestOrderBadOrder},
		"65-char order id":  {guestDirective("d", strings.Repeat("a", 65), guestLine("itm-1", 1)), GuestOrderBadOrder},
		"64-char order id":  {guestDirective("d", strings.Repeat("a", 64), guestLine("itm-1", 1)), ""},
		"order id bad char": {guestDirective("d", "g1/../x", guestLine("itm-1", 1)), GuestOrderBadOrder},
	}
	for name, tc := range cases {
		_, refusal := decodeGuestOrder(toDirective(t, tc.d))
		if refusal != tc.want {
			t.Errorf("%s: refusal %q, want %q", name, refusal, tc.want)
		}
	}

	// Lines carried as a JSON string (the cloud's generic payload shape for
	// arrays) decode the same; unknown fields and prices are ignored.
	d := guestDirective("d1", "g1")
	d["payload"].(map[string]any)["lines"] = `[{"item_id":"itm-1","variant_id":"var-1","qty":3,"modifier_option_ids":["o1"],"unit_price_minor":1,"price_minor":1,"extra":true}]`
	o, refusal := decodeGuestOrder(toDirective(t, d))
	if refusal != "" {
		t.Fatalf("string-encoded lines: refusal %q", refusal)
	}
	if o.ID != "g1" || o.DirectiveID != "d1" || o.TableToken != "0123456789abcdef0123456789abcdef" || len(o.Lines) != 1 {
		t.Fatalf("decoded %+v", o)
	}
	if l := o.Lines[0]; l.ItemID != "itm-1" || l.VariantID != "var-1" || l.Qty != 3 || len(l.ModifierOptionIDs) != 1 || l.ModifierOptionIDs[0] != "o1" {
		t.Fatalf("line %+v", l)
	}

	// A missing or non-string table token is a malformed order.
	d = guestDirective("d1", "g1", guestLine("itm-1", 1))
	delete(d["payload"].(map[string]any), "table_token")
	if _, refusal := decodeGuestOrder(toDirective(t, d)); refusal != GuestOrderBadOrder {
		t.Fatalf("no table token: %q", refusal)
	}
}

func TestApplyGuestOrder_HookContract(t *testing.T) {
	ctx := context.Background()
	d := toDirective(t, guestDirective("d1", "g1", guestLine("itm-1", 2)))

	if r := applyGuestOrder(ctx, d, Hooks{}); r.status != "failed" || r.msg != "guest_order is not supported on this till" || r.guestOrderID != "g1" {
		t.Fatalf("nil hook: %+v", r)
	}
	var got GuestOrder
	ok := Hooks{GuestOrder: func(_ context.Context, o GuestOrder) (GuestOrderOutcome, error) {
		got = o
		return GuestOrderOutcome{DisplayNo: "C-7", TotalMinor: 640, Currency: "EUR"}, nil
	}}
	r := applyGuestOrder(ctx, d, ok)
	if r.status != "applied" || r.deferred || r.data == nil || r.data["display_no"] != "C-7" || r.data["total_minor"] != int64(640) || r.data["currency"] != "EUR" {
		t.Fatalf("applied: %+v", r)
	}
	if got.ID != "g1" || len(got.Lines) != 1 || got.Lines[0].Qty != 2 {
		t.Fatalf("hook saw %+v", got)
	}
	refuse := Hooks{GuestOrder: func(context.Context, GuestOrder) (GuestOrderOutcome, error) {
		return GuestOrderOutcome{Refusal: GuestOrderItemUnavailable}, nil
	}}
	if r := applyGuestOrder(ctx, d, refuse); r.status != "failed" || r.msg != GuestOrderItemUnavailable || r.data != nil {
		t.Fatalf("refusal: %+v", r)
	}
	// A refusal outside the three codes never reaches the wire as-is.
	odd := Hooks{GuestOrder: func(context.Context, GuestOrder) (GuestOrderOutcome, error) {
		return GuestOrderOutcome{Refusal: "something else"}, nil
	}}
	if r := applyGuestOrder(ctx, d, odd); r.status != "failed" || r.msg != GuestOrderBadOrder {
		t.Fatalf("odd refusal: %+v", r)
	}
	// An internal error (a DB read failed) leaves the directive pending.
	transient := Hooks{GuestOrder: func(context.Context, GuestOrder) (GuestOrderOutcome, error) {
		return GuestOrderOutcome{}, errors.New("database is locked")
	}}
	if r := applyGuestOrder(ctx, d, transient); !r.deferred {
		t.Fatalf("transient error must defer: %+v", r)
	}
	// A bounds failure never reaches the hook.
	called := false
	spy := Hooks{GuestOrder: func(context.Context, GuestOrder) (GuestOrderOutcome, error) {
		called = true
		return GuestOrderOutcome{}, nil
	}}
	bad := toDirective(t, guestDirective("d2", "g2", guestLine("itm-1", 100)))
	if r := applyGuestOrder(ctx, bad, spy); r.status != "failed" || r.msg != GuestOrderBadOrder || called {
		t.Fatalf("bounds: %+v called=%v", r, called)
	}
}

func TestGuestOrderIsMainTillOnly(t *testing.T) {
	if !mainTillOnlyTypes["guest_order"] {
		t.Fatal("guest_order must be main-till only")
	}
}

// guestCloud is a fake cloud for the guest_order tick tests: each POST
// /v1/stores/sync delivers the next queued batch; results are recorded
// with their raw JSON and answered with resultStatus (200 by default).
type guestCloud struct {
	mu           sync.Mutex
	batches      [][]map[string]any
	results      []map[string]any
	resultStatus func(directiveID string) int
}

func (g *guestCloud) handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	}
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		var dirs []map[string]any
		if len(g.batches) > 0 {
			dirs, g.batches = g.batches[0], g.batches[1:]
		}
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": dirs}})
	})
	for _, p := range []string{"/v1/stores/catalog-snapshot", "/v1/stores/order-tracking-snapshot", "/v1/stores/sales-aggregates", "/v1/stores/report-archives"} {
		mux.HandleFunc(p, ok)
	}
	mux.HandleFunc("/v1/stores/directives/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.results = append(g.results, body)
		status := http.StatusOK
		if g.resultStatus != nil {
			id, _ := body["directive_id"].(string)
			status = g.resultStatus(id)
		}
		g.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		ok(w, r)
	})
	return mux
}

func (g *guestCloud) snapshot() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.results...)
}

func okGuestHook(calls *int) Hooks {
	var mu sync.Mutex
	return Hooks{GuestOrder: func(_ context.Context, o GuestOrder) (GuestOrderOutcome, error) {
		mu.Lock()
		*calls++
		n := *calls
		mu.Unlock()
		return GuestOrderOutcome{DisplayNo: fmt.Sprintf("C-%d", n), TotalMinor: int64(100 * n), Currency: "EUR"}, nil
	}}
}

func TestTick_GuestOrderSkippedOnReplica(t *testing.T) {
	cloud := &guestCloud{batches: [][]map[string]any{{guestDirective("d1", "g1", guestLine("itm-1", 1))}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_replica.db").DB
	if err := data.NewSettingsRepo(db).Set(context.Background(), "sync.primary_url", "http://main:8080"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := Tick(context.Background(), testCfg(srv.URL), db, okGuestHook(&calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(cloud.snapshot()) != 0 {
		t.Fatalf("replica applied/answered a guest_order: calls=%d results=%v", calls, cloud.snapshot())
	}
}

func TestTick_GuestOrderPerTickCapAndResultData(t *testing.T) {
	var batch []map[string]any
	for i := 0; i < 12; i++ {
		batch = append(batch, guestDirective(fmt.Sprintf("d%d", i), fmt.Sprintf("g%d", i), guestLine("itm-1", 1)))
	}
	batch = append(batch, map[string]any{"id": "s1", "type": "set_setting", "payload": map[string]any{"key": "display.osk", "value": "on"}})
	cloud := &guestCloud{batches: [][]map[string]any{batch}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_cap.db").DB
	calls := 0
	hooks := okGuestHook(&calls)
	hooks.SetSetting = func(context.Context, string, string) (string, error) { return "set", nil }
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	if calls != maxGuestOrdersPerTick || maxGuestOrdersPerTick != 10 {
		t.Fatalf("hook called %d times, want 10", calls)
	}
	results := cloud.snapshot()
	guest, other := 0, 0
	for _, r := range results {
		id, _ := r["directive_id"].(string)
		if id == "s1" {
			other++
			if _, has := r["data"]; has {
				t.Fatalf("a non-guest_order result carries data: %v", r)
			}
			continue
		}
		guest++
		dm, ok := r["data"].(map[string]any)
		if !ok || r["status"] != "applied" || dm["display_no"] == "" || dm["currency"] != "EUR" {
			t.Fatalf("guest_order result without data: %v", r)
		}
		if _, ok := dm["total_minor"].(float64); !ok {
			t.Fatalf("total_minor not a number: %v", dm)
		}
		if id == "d10" || id == "d11" {
			t.Fatalf("directive %s past the cap was answered", id)
		}
	}
	if guest != 10 || other != 1 {
		t.Fatalf("results: %d guest, %d other — want 10 and 1: %v", guest, other, results)
	}
}

func TestTick_GuestOrderRedeliveryReturnsFirstResult(t *testing.T) {
	d := guestDirective("d1", "g1", guestLine("itm-1", 1))
	cloud := &guestCloud{batches: [][]map[string]any{{d}, {d}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_redeliver.db").DB
	calls := 0
	hooks := okGuestHook(&calls)
	for i := 0; i < 2; i++ {
		if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("hook called %d times for one guest order, want 1", calls)
	}
	results := cloud.snapshot()
	if len(results) != 2 {
		t.Fatalf("want the result posted on both deliveries, got %v", results)
	}
	a, _ := json.Marshal(results[0]["data"])
	b, _ := json.Marshal(results[1]["data"])
	if string(a) != string(b) || results[1]["status"] != "applied" {
		t.Fatalf("re-delivery result differs: %s vs %s", a, b)
	}
}

func TestTick_GuestOrderResultRetriedUntil200(t *testing.T) {
	status := http.StatusInternalServerError
	cloud := &guestCloud{batches: [][]map[string]any{{guestDirective("d1", "g1", guestLine("itm-1", 1))}}}
	cloud.resultStatus = func(string) int { return status }
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_retry.db").DB
	calls := 0
	hooks := okGuestHook(&calls)
	ctx := context.Background()

	_ = Tick(ctx, testCfg(srv.URL), db, hooks) // applied, post 500
	_ = Tick(ctx, testCfg(srv.URL), db, hooks) // retried, 500 again
	if n := len(cloud.snapshot()); n != 2 {
		t.Fatalf("want 2 attempts after two ticks, got %d", n)
	}
	cloud.mu.Lock()
	status = http.StatusOK
	cloud.mu.Unlock()
	_ = Tick(ctx, testCfg(srv.URL), db, hooks) // retried, 200
	_ = Tick(ctx, testCfg(srv.URL), db, hooks) // nothing left to send
	results := cloud.snapshot()
	if len(results) != 3 {
		t.Fatalf("want exactly 3 attempts (500, 500, 200), got %d: %v", len(results), results)
	}
	if calls != 1 {
		t.Fatalf("the retry re-applied the order: hook calls %d", calls)
	}
	dm, _ := results[2]["data"].(map[string]any)
	if results[2]["directive_id"] != "d1" || dm["display_no"] != "C-1" {
		t.Fatalf("retried result: %v", results[2])
	}
}

func TestTick_GuestOrderResult404StopsRetry(t *testing.T) {
	cloud := &guestCloud{batches: [][]map[string]any{{guestDirective("d1", "g1", guestLine("itm-1", 1))}}}
	cloud.resultStatus = func(string) int { return http.StatusNotFound }
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_404.db").DB
	calls := 0
	hooks := okGuestHook(&calls)
	for i := 0; i < 3; i++ {
		_ = Tick(context.Background(), testCfg(srv.URL), db, hooks)
	}
	if n := len(cloud.snapshot()); n != 1 {
		t.Fatalf("a 404 must end the retries: %d attempts", n)
	}
}

func TestTick_GuestOrderRefusalPostedAndTransientLeftPending(t *testing.T) {
	cloud := &guestCloud{batches: [][]map[string]any{{
		guestDirective("d1", "g1", guestLine("itm-1", 1)),
		guestDirective("d2", "g2", guestLine("itm-2", 1)),
	}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_refuse.db").DB
	hooks := Hooks{GuestOrder: func(_ context.Context, o GuestOrder) (GuestOrderOutcome, error) {
		if o.ID == "g1" {
			return GuestOrderOutcome{Refusal: GuestOrderUnknownTable}, nil
		}
		return GuestOrderOutcome{}, errors.New("database is locked")
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	results := cloud.snapshot()
	if len(results) != 1 || results[0]["directive_id"] != "d1" || results[0]["status"] != "failed" || results[0]["message"] != GuestOrderUnknownTable {
		t.Fatalf("results: %v", results)
	}
	if _, has := results[0]["data"]; has {
		t.Fatalf("a refusal carries data: %v", results[0])
	}
	if _, ok, _ := data.NewGuestOrderResultsRepo(db).Get(context.Background(), "g2"); ok {
		t.Fatal("a transient failure must not store a result")
	}
}

func TestBuildSyncRequest_GuestTablesMainTillOnly(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	hooks := Hooks{DeviceExtra: func(context.Context) map[string]any {
		return map[string]any{GuestTablesKey: []map[string]any{{"token": "t", "label": "T1"}}}
	}}
	if _, ok := buildSyncRequest(ctx, testCfg(""), settings, hooks).devices[0][GuestTablesKey]; !ok {
		t.Fatal("main till dropped guest_tables")
	}
	if err := settings.Set(ctx, "sync.primary_url", "http://main:8080"); err != nil {
		t.Fatal(err)
	}
	if _, ok := buildSyncRequest(ctx, testCfg(""), settings, hooks).devices[0][GuestTablesKey]; ok {
		t.Fatal("a replica's report must omit guest_tables entirely")
	}
}

// A permanent 4xx (the cloud can't use this result, e.g. a 400 for a body
// it can't parse) ends that row's retries like a 404, so one poisoned row
// can't starve every later unposted result behind it (review of
// ut-docs#4337). A 429 or 5xx is still retried.
func TestTick_GuestOrderResultPermanent4xxDoesNotStarveLaterResults(t *testing.T) {
	cloud := &guestCloud{batches: [][]map[string]any{{
		guestDirective("d1", "g1", guestLine("itm-1", 1)),
		guestDirective("d2", "g2", guestLine("itm-1", 1)),
		guestDirective("d3", "g3", guestLine("itm-1", 1)),
	}}}
	attempts := map[string]int{}
	cloud.resultStatus = func(id string) int {
		attempts[id]++
		switch {
		case id == "d1":
			return http.StatusBadRequest
		case id == "d2" && attempts[id] == 1:
			return http.StatusTooManyRequests
		case id == "d3" && attempts[id] == 1:
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "guest_4xx.db").DB
	calls := 0
	hooks := okGuestHook(&calls)
	for i := 0; i < 3; i++ {
		_ = Tick(context.Background(), testCfg(srv.URL), db, hooks)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if attempts["d1"] != 1 {
		t.Fatalf("a 400 must end that result's retries: d1 posted %d times", attempts["d1"])
	}
	if attempts["d2"] != 2 || attempts["d3"] != 2 {
		t.Fatalf("429/5xx results must be retried once each until 200: d2=%d d3=%d", attempts["d2"], attempts["d3"])
	}
}

// ut-docs#4392 gap 2: guest_order goes only through Tick's handleGuestOrder
// (stored result, retry). apply has no guest_order case, so a caller that
// ever routed one there could never place an order past the stored-result
// idempotency: the hook is not run.
func TestApply_NeverRunsTheGuestOrderHook(t *testing.T) {
	d := toDirective(t, guestDirective("d1", "g1", guestLine("itm-1", 1)))
	called := false
	hooks := Hooks{GuestOrder: func(context.Context, GuestOrder) (GuestOrderOutcome, error) {
		called = true
		return GuestOrderOutcome{DisplayNo: "C-1"}, nil
	}}
	if status, _ := apply(context.Background(), d, hooks); status != "failed" || called {
		t.Fatalf("apply(guest_order) = %s, hook called %v", status, called)
	}
}
