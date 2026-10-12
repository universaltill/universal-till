package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0153 Decision 3 (ut-docs#2838): the confirm/cancel routes. Only the
// operator and session a proposal is bound to may decide it; confirm
// re-checks the grant and role and that the values are still current, and
// applies the batch in one transaction with the operator as actor.

const agentPluginID = "com.test.agentpl"

type agentHarness struct {
	t   *testing.T
	d   *common.Deps
	mux *http.ServeMux
}

func newAgentHarness(t *testing.T) *agentHarness {
	t.Helper()
	d, _ := pluginPageTestDeps(t)
	d.AuthSvc = auth.NewService(d.Db)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	prevData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(prevData) })
	seedTestPlugin(t, d.Db, agentPluginID, "Agent", "1.0.0")
	perms := []string{"ai.agent", "ai.tools.write:catalog", "ai.tools.write:stock"}
	for _, p := range perms {
		if _, err := d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, agentPluginID+p, agentPluginID, p); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(paths.Plugins(), agentPluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pj, _ := json.Marshal(perms)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"`+agentPluginID+`","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":`+string(pj)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"mgr-1", "manager"}, {"mgr-2", "manager"}, {"csh-1", "cashier"}} {
		if _, err := d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES (?,?,?,'x',?)`, u[0], u[0], u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	prev := agentaction.Proposals
	agentaction.Proposals = agentaction.NewStore()
	t.Cleanup(func() { agentaction.Proposals = prev })
	h := &agentHarness{t: t, d: d, mux: http.NewServeMux()}
	registerAgentProposals(h.mux, d)
	return h
}

// propose stores a proposal exactly as action_propose would after its
// checks (the host-side checks are tested in internal/plugins).
func (h *agentHarness) propose(user, role, session, action, args string) string {
	h.t.Helper()
	a, ok := agentaction.Lookup(action)
	if !ok {
		h.t.Fatalf("no action %s", action)
	}
	parsed, rows, state, err := a.Prepare(context.Background(), agentaction.Env{DB: h.d.Db}, []byte(args))
	if err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	op := agentaction.Operator{UserID: user, Role: role, Session: auth.SessionKey(auth.WithSessionKey(context.Background(), session)), Request: "r"}
	id, err := agentaction.Proposals.Put(context.Background(), h.d.Db, agentaction.NewProposal(agentPluginID, op, a, parsed, rows, state))
	if err != nil {
		h.t.Fatalf("put: %v", err)
	}
	return id
}

func (h *agentHarness) post(id, verb, user, role, session string) *httptest.ResponseRecorder {
	return h.postSite(id, verb, user, role, session, "")
}

// postSite is post with a Sec-Fetch-Site header ("" sends none).
func (h *agentHarness) postSite(id, verb, user, role, session, site string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ui/agent/proposals/"+id+"/"+verb, nil)
	req.Header.Set("HX-Request", "true")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	req = auth.WithUser(req, auth.User{ID: user, Role: role})
	req = req.WithContext(auth.WithSessionKey(req.Context(), session))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func (h *agentHarness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.d.Db.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *agentHarness) item(name string) string {
	h.t.Helper()
	id, err := pos.CreateItem(context.Background(), h.d.Db, catalogtypes.ItemInput{Name: name, BasePrice: 100, IsActive: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *agentHarness) level(itemID string) float64 {
	h.t.Helper()
	loc, err := pos.ResolveStockLocationID(context.Background(), h.d.Db, "")
	if err != nil {
		h.t.Fatal(err)
	}
	q, _, err := data.NewPOSRepo(h.d.Db).CurrentQty(context.Background(), nil, loc, itemID, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return q
}

const agentCreateArgs = `{"items":[{"name":"Oat milk","price_minor":250,"barcode":"5000000000017"},{"name":"Rye bread","price_minor":320}]}`

func TestAgentConfirm_CreateAppliesAsOperator_2838(t *testing.T) {
	h := newAgentHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.create.v1", agentCreateArgs)
	rec := h.post(id, "confirm", "mgr-1", "manager", "sess")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Changes applied: 3") {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE name IN ('Oat milk','Rye bread') AND base_price IN (250,320)`); n != 2 {
		t.Fatalf("items created = %d, want 2", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '5000000000017'`); n != 1 {
		t.Fatalf("barcode rows = %d, want 1", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.action.confirmed' AND actor_id = 'mgr-1' AND entity_id = ?`, agentPluginID); n != 1 {
		t.Fatalf("confirmed audit rows = %d, want 1", n)
	}
	// Single use: a second confirm is "no longer valid".
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no longer valid") {
		t.Fatalf("second confirm = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentConfirm_AdjustRecordsMovementAsOperator_2838(t *testing.T) {
	h := newAgentHarness(t)
	tea := h.item("Tea")
	before := h.level(tea)
	id := h.propose("mgr-1", "manager", "sess", "stock.adjust.v1", `{"changes":[{"item_id":"`+tea+`","delta":7,"reason":"count"}]}`)
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	if got := h.level(tea); got != before+7 {
		t.Fatalf("level = %v, want %v", got, before+7)
	}
	if n := h.count(`SELECT COUNT(*) FROM stock_movements m JOIN audit_log a ON a.entity_type = 'inventory' AND a.entity_id = m.id
		WHERE m.item_id = ? AND m.type = 'adjust' AND m.quantity = 7 AND a.actor_id = 'mgr-1'`, tea); n != 1 {
		t.Fatalf("adjust movements by mgr-1 = %d, want 1", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.action.confirmed' AND actor_id = 'mgr-1'`); n != 1 {
		t.Fatalf("confirmed audit rows = %d", n)
	}
}

// Nothing changes on cancel, expiry, a stale before value, another user,
// a cashier, or a revoked grant.
func TestAgentConfirm_RefusalsLeaveDBUnchanged_2838(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(h *agentHarness, tea string)
		user   string
		role   string
		verb   string
		status int
		text   string
		audit  string
	}{
		{"cancel", nil, "mgr-1", "manager", "cancel", http.StatusOK, "Nothing was changed", "agent.action.cancelled"},
		{"other user", nil, "mgr-2", "manager", "confirm", http.StatusConflict, "no longer valid", ""},
		{"stale", func(h *agentHarness, tea string) {
			loc, _ := pos.ResolveStockLocationID(context.Background(), h.d.Db, "")
			if _, err := pos.RecordStockMovement(context.Background(), h.d.Db, pos.StockMovementInput{ItemID: tea, LocationID: loc, Type: "adjust", Quantity: 1, ActorID: "mgr-2"}); err != nil {
				h.t.Fatal(err)
			}
		}, "mgr-1", "manager", "confirm", http.StatusConflict, "values changed", "agent.action.refused"},
		{"grant revoked", func(h *agentHarness, _ string) {
			if err := data.NewPluginRepo(h.d.Db).SetPermission(context.Background(), agentPluginID, "ai.tools.write:stock", false); err != nil {
				h.t.Fatal(err)
			}
		}, "mgr-1", "manager", "confirm", http.StatusForbidden, "no longer allowed", "agent.action.refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAgentHarness(t)
			tea := h.item("Tea")
			id := h.propose("mgr-1", "manager", "sess", "stock.adjust.v1", `{"changes":[{"item_id":"`+tea+`","delta":7}]}`)
			if tc.setup != nil {
				tc.setup(h, tea)
			}
			before := h.level(tea)
			rec := h.post(id, tc.verb, tc.user, tc.role, "sess")
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("%s = %d %s", tc.verb, rec.Code, rec.Body.String())
			}
			if got := h.level(tea); got != before {
				t.Fatalf("level moved %v → %v", before, got)
			}
			if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'inventory' AND actor_id = 'mgr-1'`); n != 0 {
				t.Fatalf("%d movements by the operator", n)
			}
			if tc.audit != "" && h.count(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, tc.audit) != 1 {
				t.Fatalf("no %s audit row", tc.audit)
			}
		})
	}
}

func TestAgentConfirm_CashierRefusedAtRoute_2838(t *testing.T) {
	h := newAgentHarness(t)
	// Even a proposal bound to a cashier (action_propose never stores one)
	// is refused at confirm: the role is re-checked.
	id := h.propose("csh-1", "cashier", "sess", "catalog.item.create.v1", agentCreateArgs)
	rec := h.post(id, "confirm", "csh-1", "cashier", "sess")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier confirm = %d %s", rec.Code, rec.Body.String())
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE name IN ('Oat milk','Rye bread')`); n != 0 {
		t.Fatalf("cashier created %d items", n)
	}
}

func TestAgentConfirm_ExpiredIsInvalid_2838(t *testing.T) {
	h := newAgentHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.create.v1", agentCreateArgs)
	// Another session's proposal cannot be decided either.
	if rec := h.post(id, "confirm", "mgr-1", "manager", "other-session"); rec.Code != http.StatusConflict {
		t.Fatalf("other session = %d", rec.Code)
	}
	agentaction.Proposals = agentaction.NewStore() // a restart: proposals die with the process
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusConflict {
		t.Fatalf("gone proposal = %d", rec.Code)
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE name IN ('Oat milk','Rye bread')`); n != 0 {
		t.Fatalf("%d items created", n)
	}
}

// The confirm card is core's: the action's name, the rows core planned,
// and buttons posting to core's routes. An unknown id is core's notice.
func TestAgentConfirm_CardRendersCoreText_2838(t *testing.T) {
	h := newAgentHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.create.v1", agentCreateArgs)
	raw := `{"document":{"version":1,"components":[{"type":"confirm","proposal":"` + id + `"},{"type":"confirm","proposal":"` + strings.Repeat("0", 32) + `"}]}}`
	doc, err := pluginview.DecodeViewAnswer([]byte(raw), pluginview.Context{PluginID: agentPluginID})
	if err != nil {
		t.Fatal(err)
	}
	v := doc.Prepare("en")
	ctx := auth.WithSessionKey(auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), auth.User{ID: "mgr-1", Role: "manager"}).Context(), "sess")
	resolveAgentConfirms(ctx, h.d, &v, agentPluginID, "en")
	rec := httptest.NewRecorder()
	httpx.RenderWith(pluginViewFiles, httpx.FuncsFor("en"))("pluginview_body", pluginViewBody{Route: "/plugin/x", View: v})(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, want := range []string{"Add new items", "Oat milk", "£2.50", "5000000000017", "Confirm changes (3)",
		`hx-post="/ui/agent/proposals/` + id + `/confirm"`, `hx-post="/ui/agent/proposals/` + id + `/cancel"`,
		`hx-disabled-elt=`, "This change is no longer valid. Ask the assistant again."} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`/plugin/x`)) {
		t.Error("a confirm button targets the plugin's route")
	}
	// Another operator sees only the notice.
	other := auth.WithSessionKey(auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), auth.User{ID: "mgr-2", Role: "manager"}).Context(), "sess")
	v2 := doc.Prepare("en")
	resolveAgentConfirms(other, h.d, &v2, agentPluginID, "en")
	if v2.Components[0].Confirm != nil {
		t.Error("another operator resolved the card")
	}
	// Another plugin's view never shows this plugin's proposal (#2838 review).
	v3 := doc.Prepare("en")
	resolveAgentConfirms(ctx, h.d, &v3, "com.example.other", "en")
	if v3.Components[0].Confirm != nil {
		t.Error("another plugin's view resolved the card")
	}
}

// The ui ask carries the signed-in operator on its context (never the
// payload), with a fresh request id per ask; no session, no operator.
func TestAgentConfirm_UIAskStampsOperator_2838(t *testing.T) {
	h := newViewHarness(t)
	var got []agentaction.Operator
	var mu sync.Mutex
	h.mu.Lock()
	h.answer = nil
	h.mu.Unlock()
	bus := plugins.SharedBus(h.d.Db)
	bus.ResetSubscribers()
	if _, err := bus.SubscribeWithHandler(t.Context(), viewPluginID, []string{pluginViewAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			op, ok := plugins.AskOperatorFrom(ctx)
			mu.Lock()
			if ok {
				got = append(got, op)
			}
			mu.Unlock()
			return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"hi"}}]}}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	ask := func(withSession bool) {
		req := httptest.NewRequest(http.MethodGet, "/plugin/views", nil)
		req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
		if withSession {
			req = req.WithContext(auth.WithSessionKey(req.Context(), "tok"))
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET = %d", rec.Code)
		}
	}
	ask(true)
	ask(true)
	ask(false)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].UserID != "mgr-1" || got[0].Role != "manager" || got[0].Session == "" || got[0].Session == "tok" {
		t.Fatalf("operators = %+v, want two stamped asks (session hashed, not the token)", got)
	}
	if got[0].Request == got[1].Request {
		t.Error("two asks shared a request id")
	}
}

// A cross-site post is refused before the proposal is touched, so a forged
// request can neither apply a change nor burn the operator's proposal
// (ut-docs#4168).
func TestAgentConfirm_CrossSiteRefusedAndProposalKept_4168(t *testing.T) {
	h := newAgentHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.create.v1", agentCreateArgs)
	for _, verb := range []string{"confirm", "cancel"} {
		if rec := h.postSite(id, verb, "mgr-1", "manager", "sess", "cross-site"); rec.Code != http.StatusForbidden {
			t.Fatalf("cross-site %s = %d, want 403", verb, rec.Code)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE name IN ('Oat milk','Rye bread')`); n != 0 {
		t.Fatalf("cross-site post created %d items", n)
	}
	if rec := h.postSite(id, "confirm", "mgr-1", "manager", "sess", "same-origin"); rec.Code != http.StatusOK {
		t.Fatalf("same-origin confirm after the refused ones = %d %s", rec.Code, rec.Body.String())
	}
}

// Many confirms of one proposal at once: exactly one applies it, the rest
// get "no longer valid", and the items exist once (ut-docs#4168).
func TestAgentConfirm_ConcurrentConfirmAppliesOnce_4168(t *testing.T) {
	h := newAgentHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.create.v1", agentCreateArgs)
	const n = 8
	codes := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := h.post(id, "confirm", "mgr-1", "manager", "sess")
			codes[i], bodies[i] = rec.Code, rec.Body.String()
		}()
	}
	close(start)
	wg.Wait()
	// The losers must be refused by the single-use take ("no longer
	// valid"), not later by the stale check: only one request may ever hold
	// the proposal.
	ok, conflict := 0, 0
	for i, c := range codes {
		switch {
		case c == http.StatusOK:
			ok++
		case c == http.StatusConflict && strings.Contains(bodies[i], "no longer valid"):
			conflict++
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("codes = %v: want one 200 and %d 409s", codes, n-1)
	}
	if got := h.count(`SELECT COUNT(*) FROM items WHERE name IN ('Oat milk','Rye bread')`); got != 2 {
		t.Fatalf("items = %d, want 2 (applied once)", got)
	}
	if got := h.count(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'agent.action.%' AND action != 'agent.action.proposed'`); got != 1 {
		t.Fatalf("decision audit rows = %d, want 1", got)
	}
}
