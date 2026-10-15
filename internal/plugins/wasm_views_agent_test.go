package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/agentaction"
)

// ADR-0153 "Who is asking", Decision 1's role rule and Decision 6's
// ceiling (ut-docs#4143): an ai.agent plugin's view_query runs only for
// the operator of a ui ask (or a job started from one whose session is
// still live), only for a view the operator's role may see, and at most
// agentaction.MaxToolCallsPerRequest times per request.

// runGuestEventCtx runs one event of type evType through the guest with
// ctx (an operator, a job) and returns what the guest recorded.
func runGuestEventCtx(ctx context.Context, t *testing.T, w *WasmRuntime, d *sql.DB, pluginID, evType string, payload any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	w.mu.Lock()
	w.db = d
	w.mu.Unlock()
	if _, err := w.HandleEvent(ctx, pluginID, Event{ID: "ev-ask", Type: evType, Timestamp: time.Now(), Payload: body}); err != nil {
		t.Fatalf("HandleEvent %s: %v", evType, err)
	}
	return readGuestResults(t, d, pluginID)
}

var askSeq int

// askAs is a ui.view.ask context for role: a fresh request (chat turn) each
// call unless req is given.
func askAs(role string, req ...string) context.Context {
	askSeq++
	r := "req-" + strconv.Itoa(askSeq)
	if len(req) > 0 {
		r = req[0]
	}
	return WithAskOperator(context.Background(), agentaction.Operator{UserID: "u-" + role, Role: role, Session: "sess-" + role, Request: r})
}

const agentSalesPlugin = "com.test.views.agent4143"

func agentSalesFixture(t *testing.T, pluginID string) (*WasmRuntime, *sql.DB) {
	t.Helper()
	guest := buildViewGuest(t)
	withInstalledManifest(t, pluginID, aiAgentManifest(pluginID,
		[]string{"ai.agent", "view:sales", "ai.send.sales", "view:inventory", "ai.send.catalog", "view:audit", "ai.send.staff"},
		"items.top.v1", "catalog.items.v1", "audit.summary.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "ai.agent", "view:sales", "ai.send.sales", "view:inventory", "ai.send.catalog", "view:audit", "ai.send.staff")
	return w, d
}

func TestViewQuery_AIAgentOutsideUIAskRefused_4143(t *testing.T) {
	w, d := agentSalesFixture(t, agentSalesPlugin)
	// Even with an operator on the context, only a hop-0 ui ask carries
	// it: a schedule tick or another plugin event is refused.
	for _, evType := range []string{"test.event", agentSalesPlugin + ".tick"} {
		before := countDenials(t, d, agentSalesPlugin)
		res := runGuestEventCtx(askAs("manager"), t, w, d, agentSalesPlugin, evType, map[string]any{"view": "catalog.items.v1"})
		if c := viewCode(t, res); c != hostErrDenied {
			t.Fatalf("%s: view_query = %d, want %d (no ui ask)", evType, c, hostErrDenied)
		}
		if after := countDenials(t, d, agentSalesPlugin); after <= before {
			t.Fatalf("%s: refusal not audited (%d → %d)", evType, before, after)
		}
		assertLastDenialPermission(t, d, agentSalesPlugin, AIAgentPermission)
	}
}

func TestViewQuery_AIAgentCashierGetsNoSalesView_4143(t *testing.T) {
	w, d := agentSalesFixture(t, agentSalesPlugin)
	ask := func(role, view string) int {
		return viewCode(t, runGuestEventCtx(askAs(role), t, w, d, agentSalesPlugin, "ui.view.ask", map[string]any{"view": view}))
	}
	if c := ask("manager", "items.top.v1"); c != 2 {
		t.Fatalf("manager items.top.v1 = %d, want 2 ([])", c)
	}
	before := countDenials(t, d, agentSalesPlugin)
	if c := ask("cashier", "items.top.v1"); c != hostErrDenied {
		t.Fatalf("cashier items.top.v1 = %d, want %d", c, hostErrDenied)
	}
	if after := countDenials(t, d, agentSalesPlugin); after <= before {
		t.Fatalf("cashier sales denial not audited (%d → %d)", before, after)
	}
	assertLastDenialPermission(t, d, agentSalesPlugin, "reports")
	// audit.summary.v1 is class staff (activity rows name staff, #2837).
	if c := ask("cashier", "audit.summary.v1"); c != hostErrDenied {
		t.Fatalf("cashier audit.summary.v1 = %d, want %d", c, hostErrDenied)
	}
	assertLastDenialPermission(t, d, agentSalesPlugin, "user_management")
	if c := ask("manager", "audit.summary.v1"); c < 2 {
		t.Fatalf("manager audit.summary.v1 = %d, want a result", c)
	}
	// Catalogue is not role-gated: the cashier sells from it.
	if c := ask("cashier", "catalog.items.v1"); c < 2 {
		t.Fatalf("cashier catalog.items.v1 = %d, want a result", c)
	}
}

func TestViewQuery_AIAgentTurnCeiling_4143(t *testing.T) {
	w, d := agentSalesFixture(t, agentSalesPlugin)
	// A budget of the test's own: the till's one lives 6 minutes, so a
	// fixed request id would carry over into a -count=2 rerun.
	prev := agentaction.ToolCalls
	agentaction.ToolCalls = agentaction.NewToolCallBudget()
	t.Cleanup(func() { agentaction.ToolCalls = prev })
	ctx := askAs("manager", "turn-ceiling-4143")
	res := runGuestEventCtx(ctx, t, w, d, agentSalesPlugin, "ui.view.ask", map[string]any{"view": "catalog.items.v1", "repeat": agentaction.MaxToolCallsPerRequest + 1})
	cs := codes(res["codes"])
	if len(cs) != agentaction.MaxToolCallsPerRequest+1 {
		t.Fatalf("recorded %d codes, want %d", len(cs), agentaction.MaxToolCallsPerRequest+1)
	}
	for i, c := range cs[:agentaction.MaxToolCallsPerRequest] {
		if c < 2 {
			t.Fatalf("call %d = %d, want a result under the ceiling", i+1, c)
		}
	}
	if last := cs[agentaction.MaxToolCallsPerRequest]; last != hostErrQuota {
		t.Fatalf("call %d = %d, want %d", agentaction.MaxToolCallsPerRequest+1, last, hostErrQuota)
	}
	assertLastDenialPermission(t, d, agentSalesPlugin, AIAgentPermission)
	// The ceiling spans the turn: another event of the same request (its
	// job) starts over the limit; a new request starts afresh.
	if c := viewCode(t, runGuestEventCtx(ctx, t, w, d, agentSalesPlugin, "ui.action.ask", map[string]any{"view": "catalog.items.v1"})); c != hostErrQuota {
		t.Fatalf("same request, next event = %d, want %d", c, hostErrQuota)
	}
	if c := viewCode(t, runGuestEventCtx(askAs("manager"), t, w, d, agentSalesPlugin, "ui.view.ask", map[string]any{"view": "catalog.items.v1"})); c < 2 {
		t.Fatalf("new request = %d, want a result", c)
	}
}

// A job started from a ui ask (the page layer stamps the ask's operator and
// a session check on its context) reads for that operator while the
// session is live, and is refused once it has ended.
func TestViewQuery_AIAgentJobFromAskNeedsLiveSession_4143(t *testing.T) {
	w, d := agentSalesFixture(t, agentSalesPlugin)
	live := true
	jobCtx := func() context.Context {
		ctx := WithAskSessionLive(askAs("manager"), func(context.Context) bool { return live })
		return WithJob(ctx, JobCall{Deadline: 10 * time.Second})
	}
	job := agentSalesPlugin + ".ask"
	if c := viewCode(t, runGuestEventCtx(jobCtx(), t, w, d, agentSalesPlugin, job, map[string]any{"view": "items.top.v1"})); c != 2 {
		t.Fatalf("job of a live session = %d, want 2", c)
	}
	live = false
	before := countDenials(t, d, agentSalesPlugin)
	if c := viewCode(t, runGuestEventCtx(jobCtx(), t, w, d, agentSalesPlugin, job, map[string]any{"view": "items.top.v1"})); c != hostErrDenied {
		t.Fatalf("job of an ended session = %d, want %d", c, hostErrDenied)
	}
	if after := countDenials(t, d, agentSalesPlugin); after <= before {
		t.Fatalf("ended-session refusal not audited (%d → %d)", before, after)
	}
	// A job with an operator but no session check (not started by the page
	// layer from an ask) gets no operator: fail closed.
	noCheck := WithJob(askAs("manager"), JobCall{Deadline: 10 * time.Second})
	if c := viewCode(t, runGuestEventCtx(noCheck, t, w, d, agentSalesPlugin, job, map[string]any{"view": "items.top.v1"})); c != hostErrDenied {
		t.Fatalf("job without a session check = %d, want %d", c, hostErrDenied)
	}
}
