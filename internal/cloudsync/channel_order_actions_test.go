package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// The main till's queued channel order actions go up in the tick (ut-docs#4768
// slice D; reference/contracts/channel-order-action.md "Upload order").

type actionsCloud struct {
	mu       sync.Mutex
	events   []string // "sync", "actions"
	bodies   []map[string]any
	status   int
	code     string
	revision int64 // the revision an applied action brings
	dirs     []map[string]any
}

func (c *actionsCloud) handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	}
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.events = append(c.events, "sync")
		dirs := c.dirs
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": dirs}})
	})
	mux.HandleFunc("/v1/stores/channel/orders/actions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, "actions")
		c.bodies = append(c.bodies, body)
		if c.status != 0 && c.status != 200 {
			w.WriteHeader(c.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]any{"code": c.code, "message": "x"}})
			return
		}
		var results []map[string]any
		for _, a := range body["actions"].([]any) {
			cur := channelPayloadAt(c.revision, "accepted")
			results = append(results, map[string]any{"action_id": a.(map[string]any)["action_id"], "status": "applied",
				"revision": c.revision, "writeback": "not_applicable", "current": cur})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": results}, "error": nil})
	})
	for _, p := range []string{"/v1/stores/catalog-snapshot", "/v1/stores/order-tracking-snapshot", "/v1/stores/sales-aggregates", "/v1/stores/report-archives", "/v1/stores/directives/result"} {
		mux.HandleFunc(p, ok)
	}
	return mux
}

func (c *actionsCloud) set(status int, code string) {
	c.mu.Lock()
	c.status, c.code = status, code
	c.mu.Unlock()
}

func (c *actionsCloud) snapshotEvents() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

func channelPayloadAt(rev int64, status string) map[string]any {
	p := map[string]any{}
	b, _ := json.Marshal(fixturePayloadRaw())
	_ = json.Unmarshal(b, &p)
	p["revision"], p["status"] = float64(rev), status
	return p
}

func fixturePayloadRaw() map[string]any {
	var p map[string]any
	b, _ := os.ReadFile("testdata/channel_order_v1.json")
	_ = json.Unmarshal(b, &p)
	return p
}

// seedPendingAction records the fixture order (revision 1) and queues an accept.
func seedPendingAction(t *testing.T, d *sql.DB) string {
	t.Helper()
	dec, bad := decodeChannelOrder(directive{ID: "dir-seed", Type: "channel_order", Payload: fixturePayloadRaw()})
	if bad != "" {
		t.Fatal(bad)
	}
	dec.LocationID = "loc_main"
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-1','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm1','LS-M-BLU','Shirt',3500,'tc-1',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('inv1','itm1','loc_main',10)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := data.NewChannelOrdersRepo(d).Apply(context.Background(), dec.ChannelOrderInput)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := data.NewChannelOrderActionsRepo(d).Enqueue(context.Background(), data.ChannelOrderActionInput{OrderID: res.OrderID, Action: "accept", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// repoApplyHook is the production hook's core: apply through the repo.
func repoApplyHook(d *sql.DB, log *[]string, dups *int) Hooks {
	var mu sync.Mutex
	return Hooks{ApplyChannelOrder: func(ctx context.Context, o ChannelOrderDirective) (string, error) {
		in := o.ChannelOrderInput
		in.LocationID = "loc_main"
		res, err := data.NewChannelOrdersRepo(d).Apply(ctx, in)
		mu.Lock()
		defer mu.Unlock()
		*log = append(*log, in.DirectiveID)
		if err == nil && res.Duplicate {
			*dups++
		}
		return "ok", err
	}}
}

func resetChannelActionState() {
	channelActionMu.Lock()
	channelActionHoldUntil, channelActionBackoff = time.Time{}, 0
	channelActionMu.Unlock()
	channelActionWarned.Store(false)
}

// T-tick-order: the check-in answers first, then the actions go up, and the
// answer's `current` is applied BEFORE the same tick's channel_order_update
// directive, which then reports a duplicate.
func TestTick_ChannelActionsUploadBeforeDirectivesAreApplied(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_order.db").DB
	actionID := seedPendingAction(t, d)
	cloud := &actionsCloud{revision: 2, dirs: []map[string]any{
		{"id": "dir-upd-2", "type": "channel_order_update", "payload": channelPayloadAt(2, "accepted")},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	var calls []string
	dups := 0
	if err := Tick(context.Background(), testCfg(srv.URL), d, repoApplyHook(d, &calls, &dups)); err != nil {
		t.Fatal(err)
	}
	ev := cloud.snapshotEvents()
	if len(ev) < 2 || ev[0] != "sync" || ev[1] != "actions" {
		t.Fatalf("events = %v; the actions must follow the check-in answer", ev)
	}
	if len(calls) != 2 || calls[0] != "action:"+actionID || calls[1] != "dir-upd-2" {
		t.Fatalf("apply order = %v; the action's current must be applied before the directive", calls)
	}
	if dups != 1 {
		t.Fatalf("duplicates = %d; the directive of the same revision must be ignored", dups)
	}
	var status string
	var rev int64
	_ = d.QueryRow(`SELECT status, revision FROM channel_orders`).Scan(&status, &rev)
	if status != "accepted" || rev != 2 {
		t.Fatalf("order = %s r%d", status, rev)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM channel_order_actions`).Scan(&n)
	if n != 0 {
		t.Fatalf("answered rows = %d, want deleted", n)
	}
	var q float64
	_ = d.QueryRow(`SELECT quantity FROM inventory WHERE id='inv1'`).Scan(&q)
	if q != 8 {
		t.Fatalf("stock = %v, want 8 (moved once, by the first revision)", q)
	}
}

func TestUploadChannelOrderActions_GateCases(t *testing.T) {
	cases := map[string]func(t *testing.T, d *sql.DB, cfg *config.Config, h *Hooks){
		"replica": func(t *testing.T, d *sql.DB, _ *config.Config, _ *Hooks) {
			if err := data.NewSettingsRepo(d).Set(context.Background(), "sync.primary_url", "http://main.lan:8080"); err != nil {
				t.Fatal(err)
			}
		},
		"no hook":      func(_ *testing.T, _ *sql.DB, _ *config.Config, h *Hooks) { h.ApplyChannelOrder = nil },
		"unenrolled":   func(_ *testing.T, _ *sql.DB, cfg *config.Config, _ *Hooks) { cfg.Marketplace.MerchantToken = "" },
		"nothing sent": nil,
	}
	for name, mutate := range cases {
		resetChannelActionState()
		d := openMigratedDB(t, "ca_gate.db").DB
		if name != "nothing sent" {
			seedPendingAction(t, d)
		}
		cloud := &actionsCloud{revision: 2}
		srv := httptest.NewServer(cloud.handler())
		cfg := testCfg(srv.URL)
		var calls []string
		dups := 0
		hooks := repoApplyHook(d, &calls, &dups)
		if mutate != nil {
			mutate(t, d, cfg, &hooks)
		}
		// The tick passes the gate for a replica itself; the upload func must hold the gate too (defence in depth).
		uploadChannelOrderActions(context.Background(), cfg, d, hooks)
		srv.Close()
		if ev := cloud.snapshotEvents(); len(ev) != 0 {
			t.Errorf("%s: the cloud was called: %v", name, ev)
		}
	}
}

func TestTick_AReplicaNeverPostsChannelActions(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_replica.db").DB
	seedPendingAction(t, d)
	if err := data.NewSettingsRepo(d).Set(context.Background(), "sync.primary_url", "http://main.lan:8080"); err != nil {
		t.Fatal(err)
	}
	cloud := &actionsCloud{revision: 2}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	var calls []string
	dups := 0
	if err := Tick(context.Background(), testCfg(srv.URL), d, repoApplyHook(d, &calls, &dups)); err != nil {
		t.Fatal(err)
	}
	for _, e := range cloud.snapshotEvents() {
		if e == "actions" {
			t.Fatal("a replica posted channel actions")
		}
	}
}

func TestTick_AReplicaBlocksItsPendingChannelActionsAndRaisesOneProblem(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_replica_block.db").DB
	seedPendingAction(t, d)
	if err := data.NewSettingsRepo(d).Set(context.Background(), "sync.primary_url", "http://main.lan:8080"); err != nil {
		t.Fatal(err)
	}
	cloud := &actionsCloud{revision: 2}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	var calls []string
	dups := 0
	hooks := repoApplyHook(d, &calls, &dups)
	logging.ResetRecent()
	for i := 0; i < 3; i++ {
		if err := Tick(context.Background(), testCfg(srv.URL), d, hooks); err != nil {
			t.Fatal(err)
		}
	}
	var state, code string
	_ = d.QueryRow(`SELECT state, result_code FROM channel_order_actions`).Scan(&state, &code)
	if state != "blocked" || code != "main_till_required" {
		t.Fatalf("row = %s %s", state, code)
	}
	if n := warnsContaining("not the main till; 1 unsent"); n != 1 {
		t.Fatalf("warnings = %d, want exactly 1 across 3 ticks", n)
	}
	if n := countEvents(cloud, "actions"); n != 0 {
		t.Fatalf("posts = %d", n)
	}
}

func TestUploadChannelOrderActions_UnenrolledTillBlocksItsPendingActions(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_unenrolled_block.db").DB
	seedPendingAction(t, d)
	cfg := testCfg("http://127.0.0.1:1")
	cfg.Marketplace.MerchantToken = ""
	var calls []string
	dups := 0
	logging.ResetRecent()
	uploadChannelOrderActions(context.Background(), cfg, d, repoApplyHook(d, &calls, &dups))
	var state string
	_ = d.QueryRow(`SELECT state FROM channel_order_actions`).Scan(&state)
	if state != "blocked" || warnsContaining("1 unsent") != 1 {
		t.Fatalf("state = %s warns = %d", state, warnsContaining("1 unsent"))
	}
}

func TestUploadChannelOrderActions_BackoffAfterATransientFailure(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_backoff.db").DB
	id := seedPendingAction(t, d)
	cloud := &actionsCloud{revision: 2}
	cloud.set(503, "actions_unavailable")
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	cfg := testCfg(srv.URL)
	var calls []string
	dups := 0
	hooks := repoApplyHook(d, &calls, &dups)
	logging.ResetRecent()

	uploadChannelOrderActions(context.Background(), cfg, d, hooks)
	uploadChannelOrderActions(context.Background(), cfg, d, hooks) // inside the 30 s hold
	if n := countEvents(cloud, "actions"); n != 1 {
		t.Fatalf("posts inside the backoff = %d, want 1", n)
	}
	// After the hold, retry (still failing): the same action id goes again; one Warn only.
	channelActionMu.Lock()
	channelActionHoldUntil = time.Now().Add(-time.Second)
	channelActionMu.Unlock()
	uploadChannelOrderActions(context.Background(), cfg, d, hooks)
	if n := countEvents(cloud, "actions"); n != 2 {
		t.Fatalf("posts after the backoff = %d, want 2", n)
	}
	cloud.mu.Lock()
	first := cloud.bodies[0]["actions"].([]any)[0].(map[string]any)["action_id"]
	second := cloud.bodies[1]["actions"].([]any)[0].(map[string]any)["action_id"]
	cloud.mu.Unlock()
	if first != id || second != id {
		t.Fatalf("ids = %v %v, want %s twice", first, second, id)
	}
	if n := warnsContaining("not sent"); n != 1 {
		t.Fatalf("warnings = %d, want exactly 1 (then Info)", n)
	}
	var state string
	_ = d.QueryRow(`SELECT state FROM channel_order_actions`).Scan(&state)
	if state != "pending" {
		t.Fatalf("row = %s", state)
	}
	// The cloud recovers: the next run after the hold applies it and resets the backoff.
	cloud.set(200, "")
	channelActionMu.Lock()
	channelActionHoldUntil = time.Now().Add(-time.Second)
	channelActionMu.Unlock()
	uploadChannelOrderActions(context.Background(), cfg, d, hooks)
	if channelActionBackoff != 0 {
		t.Fatalf("backoff not reset: %v", channelActionBackoff)
	}
	_ = d.QueryRow(`SELECT COUNT(*) FROM channel_order_actions`).Scan(new(int))
}

func TestUploadChannelOrderActions_OtherFailuresKeepRowsAndBackOff(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
	}{{401, "unauthorized"}, {402, "subscription_read_only"}, {409, "channel_not_enabled"}, {404, ""}, {405, "method_not_allowed"}, {403, "something_new"}, {500, ""}} {
		resetChannelActionState()
		d := openMigratedDB(t, "ca_other.db").DB
		seedPendingAction(t, d)
		cloud := &actionsCloud{revision: 2}
		cloud.set(c.status, c.code)
		srv := httptest.NewServer(cloud.handler())
		var calls []string
		dups := 0
		uploadChannelOrderActions(context.Background(), testCfg(srv.URL), d, repoApplyHook(d, &calls, &dups))
		srv.Close()
		var state string
		_ = d.QueryRow(`SELECT state FROM channel_order_actions`).Scan(&state)
		if state != "pending" || channelActionBackoff == 0 {
			t.Errorf("%d %s: state=%s backoff=%v", c.status, c.code, state, channelActionBackoff)
		}
	}
}

func TestUploadChannelOrderActions_BadRequestWarnsEveryTime(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_400.db").DB
	seedPendingAction(t, d)
	cloud := &actionsCloud{revision: 2}
	cloud.set(400, "invalid_request")
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	var calls []string
	dups := 0
	hooks := repoApplyHook(d, &calls, &dups)
	logging.ResetRecent()
	for i := 0; i < 2; i++ {
		channelActionMu.Lock()
		channelActionHoldUntil = time.Time{}
		channelActionMu.Unlock()
		uploadChannelOrderActions(context.Background(), testCfg(srv.URL), d, hooks)
	}
	if n := warnsContaining("malformed"); n != 2 {
		t.Fatalf("400 warnings = %d, want 2 (every time)", n)
	}
	var state string
	_ = d.QueryRow(`SELECT state FROM channel_order_actions`).Scan(&state)
	if state != "pending" {
		t.Fatalf("row = %s", state)
	}
}

func TestUploadChannelOrderActions_MainTillRequiredBlocksAndWarnsWithTheCount(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_403.db").DB
	seedPendingAction(t, d)
	cloud := &actionsCloud{revision: 2}
	cloud.set(403, "main_till_required")
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	var calls []string
	dups := 0
	hooks := repoApplyHook(d, &calls, &dups)
	logging.ResetRecent()
	uploadChannelOrderActions(context.Background(), testCfg(srv.URL), d, hooks)
	var state, code string
	_ = d.QueryRow(`SELECT state, result_code FROM channel_order_actions`).Scan(&state, &code)
	if state != "blocked" || code != "main_till_required" {
		t.Fatalf("row = %s %s", state, code)
	}
	if n := warnsContaining("not the main till; 1 unsent"); n != 1 {
		t.Fatalf("warnings = %d", n)
	}
	// Blocked rows are never sent again, and a stop is not a failure: no backoff.
	uploadChannelOrderActions(context.Background(), testCfg(srv.URL), d, hooks)
	if n := countEvents(cloud, "actions"); n != 1 {
		t.Fatalf("posts = %d", n)
	}
	if channelActionBackoff != 0 {
		t.Fatalf("backoff = %v", channelActionBackoff)
	}
}

func TestUploadChannelOrderActions_StoreMismatchAndDeviceCredentialWarnNamingTheCode(t *testing.T) {
	for code, want := range map[string]string{"store_mismatch": "store_mismatch", "device_credential_required": "pair this till again"} {
		resetChannelActionState()
		d := openMigratedDB(t, "ca_403b.db").DB
		seedPendingAction(t, d)
		cloud := &actionsCloud{revision: 2}
		cloud.set(403, code)
		srv := httptest.NewServer(cloud.handler())
		var calls []string
		dups := 0
		logging.ResetRecent()
		uploadChannelOrderActions(context.Background(), testCfg(srv.URL), d, repoApplyHook(d, &calls, &dups))
		srv.Close()
		if n := warnsContaining(want); n != 1 {
			t.Errorf("%s: warnings naming %q = %d", code, want, n)
		}
	}
}

func TestUploadChannelOrderActions_RefusedCurrentIsRecordedWithAWarning(t *testing.T) {
	resetChannelActionState()
	d := openMigratedDB(t, "ca_badcur.db").DB
	seedPendingAction(t, d)
	cloud := &actionsCloud{revision: 2}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	// A hook that would accept anything; the decoder must refuse a current with customer data.
	called := 0
	hooks := Hooks{ApplyChannelOrder: func(context.Context, ChannelOrderDirective) (string, error) { called++; return "", nil }}
	cloud.mu.Lock()
	cloud.revision = 2
	cloud.mu.Unlock()
	// Make the answer's current invalid by bumping schema_version through a custom handler.
	bad := http.NewServeMux()
	bad.HandleFunc("/v1/stores/channel/orders/actions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a := body["actions"].([]any)[0].(map[string]any)
		cur := channelPayloadAt(2, "accepted")
		cur["customer_email"] = "a@b.c"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": []map[string]any{
			{"action_id": a["action_id"], "status": "applied", "revision": 2, "writeback": "not_applicable", "current": cur}}}})
	})
	bsrv := httptest.NewServer(bad)
	defer bsrv.Close()
	logging.ResetRecent()
	uploadChannelOrderActions(context.Background(), testCfg(bsrv.URL), d, hooks)
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM channel_order_actions`).Scan(&n)
	if n != 0 || called != 0 {
		t.Fatalf("rows = %d, hook calls = %d; the verdict must be recorded and the bad payload never applied", n, called)
	}
	if w := warnsContaining("the cloud's current order was refused"); w != 1 {
		t.Fatalf("warnings = %d", w)
	}
}

func countEvents(c *actionsCloud, name string) int {
	n := 0
	for _, e := range c.snapshotEvents() {
		if e == name {
			n++
		}
	}
	return n
}

func warnsContaining(sub string) int {
	n := 0
	for _, p := range logging.Recent() {
		if p.Level == "warn" || p.Level == "WARN" || strings.EqualFold(p.Level, "warn") {
			if strings.Contains(p.Msg, sub) {
				n++
			}
		}
	}
	return n
}
