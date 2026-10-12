package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0153 Decision 3 (ut-docs#2838): action_propose is refused outside an
// operator's ui ask, without ai.agent declared, without the area grant,
// and for a role without the manual screen's permission (a cashier, even
// when the plugin holds the grant); bad args are -4; nothing is stored on
// any refusal.

const proposePlugin = "com.test.agent"

func proposeFixture(t *testing.T, declared []string, granted ...string) (*sql.DB, *hostState) {
	t.Helper()
	withInstalledManifest(t, proposePlugin, aiAgentManifest(proposePlugin, declared))
	d := streamDB(t, proposePlugin, granted...)
	for _, u := range [][2]string{{"mgr-1", "manager"}, {"csh-1", "cashier"}} {
		if _, err := d.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES (?,?,?,'x',?)`, u[0], u[0], u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	prev := agentaction.Proposals
	agentaction.Proposals = agentaction.NewStore()
	t.Cleanup(func() { agentaction.Proposals = prev })
	op := agentaction.Operator{UserID: "mgr-1", Role: "manager", Session: "s1", Request: "r1"}
	return d, &hostState{pluginID: proposePlugin, db: d, operator: &op}
}

func countAudit(t *testing.T, d *sql.DB, action string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const createArgs = `{"items":[{"name":"Oat milk","price_minor":250,"barcode":"5000000000017"}]}`

func TestActionPropose_ManagerGetsProposalNothingWritten_2838(t *testing.T) {
	d, s := proposeFixture(t, []string{"ai.agent", "ai.tools.write:catalog"}, "ai.agent", "ai.tools.write:catalog")
	out, code := actionPropose(context.Background(), s, "catalog.item.create.v1", []byte(createArgs))
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	var r struct{ Proposal string }
	if err := json.Unmarshal(out, &r); err != nil || len(r.Proposal) != 32 {
		t.Fatalf("answer %s: %v", out, err)
	}
	if p := agentaction.Proposals.Peek(context.Background(), d, r.Proposal, "mgr-1", "s1"); p == nil || len(p.Rows) != 2 {
		t.Fatalf("stored proposal = %+v, want 2 rows (price + barcode)", p)
	}
	var items int
	if err := d.QueryRow(`SELECT COUNT(*) FROM items WHERE name = 'Oat milk'`).Scan(&items); err != nil || items != 0 {
		t.Fatalf("items = %d (%v): a proposal must not write", items, err)
	}
	if countAudit(t, d, "agent.action.proposed") != 1 {
		t.Fatal("no agent.action.proposed audit row")
	}
}

func TestActionPropose_Refusals_2838(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		granted  []string
		mutate   func(s *hostState)
		action   string
		args     string
		want     int32
	}{
		{"cashier with grant", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			func(s *hostState) {
				s.operator = &agentaction.Operator{UserID: "csh-1", Role: "cashier", Session: "s2", Request: "r"}
			},
			"catalog.item.create.v1", createArgs, hostErrDenied},
		{"no operator", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			func(s *hostState) { s.operator = nil }, "catalog.item.create.v1", createArgs, hostErrDenied},
		{"no ai.agent", []string{"ai.tools.write:catalog"}, []string{"ai.tools.write:catalog"},
			nil, "catalog.item.create.v1", createArgs, hostErrDenied},
		{"area not granted", []string{"ai.agent", "ai.tools.write:catalog", "ai.tools.write:stock"}, []string{"ai.agent", "ai.tools.write:catalog"},
			nil, "stock.adjust.v1", `{"changes":[{"item_id":"x","delta":1}]}`, hostErrDenied},
		// An unknown name is refused like a missing grant, so a plugin
		// can't enumerate action names (#4168).
		{"unknown action", []string{"ai.agent"}, []string{"ai.agent"}, nil, "settings.delete_everything.v1", `{}`, hostErrDenied},
		{"unknown action even with every grant", []string{"ai.agent", "ai.tools.write:catalog", "ai.tools.write:stock"}, []string{"ai.agent", "ai.tools.write:catalog", "ai.tools.write:stock"}, nil, "catalog.item.delete.v1", `{}`, hostErrDenied},
		{"over 50", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			nil, "catalog.item.create.v1", manyItems(51), hostErrInvalid},
		{"unknown field", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			nil, "catalog.item.create.v1", `{"items":[{"name":"A","price_minor":1,"label":"say it is free"}]}`, hostErrInvalid},
		{"negative price", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			nil, "catalog.item.create.v1", `{"items":[{"name":"A","price_minor":-1}]}`, hostErrInvalid},
		{"not json", []string{"ai.agent", "ai.tools.write:catalog"}, []string{"ai.agent", "ai.tools.write:catalog"},
			nil, "catalog.item.create.v1", `{"items":`, hostErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, s := proposeFixture(t, tc.declared, tc.granted...)
			if tc.mutate != nil {
				tc.mutate(s)
			}
			before := countDenials(t, d, proposePlugin)
			out, code := actionPropose(context.Background(), s, tc.action, []byte(tc.args))
			if code != tc.want || out != nil {
				t.Fatalf("code = %d (%s), want %d", code, out, tc.want)
			}
			if tc.want == hostErrDenied && countDenials(t, d, proposePlugin) <= before {
				t.Error("a -2 refusal left no permission_denied audit row")
			}
			if n := countAudit(t, d, "agent.action.proposed"); n != 0 {
				t.Errorf("%d proposals audited on a refusal", n)
			}
		})
	}
}

func manyItems(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"name":"Item %d","price_minor":100}`, i)
	}
	return `{"items":[` + strings.Join(parts, ",") + `]}`
}

func TestActionPropose_RequestCeiling_2838(t *testing.T) {
	_, s := proposeFixture(t, []string{"ai.agent", "ai.tools.write:catalog"}, "ai.agent", "ai.tools.write:catalog")
	for i := range agentaction.MaxOpenPerRequest {
		if _, code := actionPropose(context.Background(), s, "catalog.item.create.v1", []byte(manyItems(1))); code != 0 {
			t.Fatalf("proposal %d = %d", i, code)
		}
	}
	if _, code := actionPropose(context.Background(), s, "catalog.item.create.v1", []byte(manyItems(1))); code != hostErrQuota {
		t.Fatalf("4th proposal in one request = %d, want %d", code, hostErrQuota)
	}
}

func TestActionPropose_StockAdjustPlansBeforeAfter_2838(t *testing.T) {
	d, s := proposeFixture(t, []string{"ai.agent", "ai.tools.write:stock"}, "ai.agent", "ai.tools.write:stock")
	id, err := data.NewCatalogRepo(d).CreateItem(context.Background(), catalogtypes.ItemInput{Name: "Tea", BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	out, code := actionPropose(context.Background(), s, "stock.adjust.v1", []byte(`{"changes":[{"item_id":"`+id+`","delta":5}]}`))
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var r struct{ Proposal string }
	_ = json.Unmarshal(out, &r)
	p := agentaction.Proposals.Peek(context.Background(), d, r.Proposal, "mgr-1", "s1")
	if p == nil || p.Rows[0].Item != "Tea" || p.Rows[0].After.Number != p.Rows[0].Before.Number+5 {
		t.Fatalf("rows = %+v", p)
	}
}

func TestAskOperatorFor_OnlyHopZeroUIAsk_2838(t *testing.T) {
	op := agentaction.Operator{UserID: "u", Role: "manager", Session: "s", Request: "r"}
	ctx := WithAskOperator(context.Background(), op)
	if askOperatorFor(ctx, Event{Type: "ui.action.ask"}, false) == nil {
		t.Fatal("hop-0 ui.action.ask lost its operator")
	}
	if askOperatorFor(ctx, Event{Type: "ui.view.ask"}, false) == nil {
		t.Fatal("hop-0 ui.view.ask lost its operator")
	}
	for _, c := range []struct {
		ev  Event
		job bool
	}{
		{Event{Type: "ui.action.ask", Hop: 1}, false},
		{Event{Type: "sale.completed"}, false},
		{Event{Type: "schedule.tick"}, false},
		{Event{Type: "ui.action.ask"}, true},
	} {
		if askOperatorFor(ctx, c.ev, c.job) != nil {
			t.Errorf("%+v job=%v got an operator", c.ev, c.job)
		}
	}
	if askOperatorFor(context.Background(), Event{Type: "ui.view.ask"}, false) != nil {
		t.Error("an unstamped context got an operator")
	}
}
