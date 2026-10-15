package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0153 Decision 1 (ut-docs#2837): a plugin that declares ai.agent needs
// BOTH the view's view:<class> grant AND ai.send.<data class> for every
// view_query, checked at call time so a revoke is live. shop-class views
// need no ai.send.* grant; a view with no class is refused (fail closed).

func aiAgentManifest(pluginID string, perms []string, views ...string) string {
	vu, _ := json.Marshal(views)
	pp, _ := json.Marshal(perms)
	return fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":%s,"views_used":%s}`, pluginID, pp, vu)
}

func TestViewQuery_AIAgentNeedsAISendGrant(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.aiagent"
	withInstalledManifest(t, pluginID, aiAgentManifest(pluginID,
		[]string{"ai.agent", "view:sales", "ai.send.sales"}, "items.top.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "ai.agent", "view:sales", "ai.send.sales")
	repo := data.NewPluginRepo(d)
	ctx := context.Background()
	// An ai.agent read runs only in an operator's ui ask (ut-docs#4143).
	call := func() int {
		return viewCode(t, runGuestEventCtx(askAs("manager"), t, w, d, pluginID, "ui.view.ask", map[string]any{"view": "items.top.v1"}))
	}

	if c := call(); c != 2 {
		t.Fatalf("view:sales + ai.send.sales granted = %d, want 2 ([])", c)
	}

	if err := repo.SetPermission(ctx, pluginID, "ai.send.sales", false); err != nil {
		t.Fatal(err)
	}
	before := countDenials(t, d, pluginID)
	if c := call(); c != hostErrDenied {
		t.Fatalf("ai.send.sales revoked = %d, want %d", c, hostErrDenied)
	}
	if after := countDenials(t, d, pluginID); after <= before {
		t.Fatalf("revoked ai.send.sales left no permission_denied row (%d → %d)", before, after)
	}
	assertLastDenialPermission(t, d, pluginID, "ai.send.sales")

	if err := repo.SetPermission(ctx, pluginID, "ai.send.sales", true); err != nil {
		t.Fatal(err)
	}
	if c := call(); c != 2 {
		t.Fatalf("ai.send.sales re-granted = %d, want 2", c)
	}
}

func TestViewQuery_AIAgentShopClassNeedsNoAISend(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.aishop"
	prev := data.SetCoreViewShopContext(func(context.Context, *sql.DB) (data.ShopContextRow, error) {
		return data.ShopContextRow{StoreName: "S", CurrencyCode: "GBP", CurrencyDecimals: 2, Locale: "en-GB"}, nil
	})
	t.Cleanup(func() { data.SetCoreViewShopContext(prev) })
	withInstalledManifest(t, pluginID, aiAgentManifest(pluginID,
		[]string{"ai.agent", "view:shop"}, "shop.context.v2"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "ai.agent", "view:shop")
	if c := viewCode(t, runGuestEventCtx(askAs("cashier"), t, w, d, pluginID, "ui.view.ask", map[string]any{"view": "shop.context.v2"})); c <= 0 {
		t.Fatalf("ai.agent shop.context.v2 with no ai.send.* = %d, want the result length", c)
	}
}

func TestViewQuery_NonAIAgentUnaffectedByAISend(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.notagent"
	withInstalledManifest(t, pluginID, aiAgentManifest(pluginID, []string{"view:sales"}, "items.top.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "view:sales")
	before := countDenials(t, d, pluginID)
	if c := viewCode(t, runGuestPayload(t, w, d, pluginID, map[string]any{"view": "items.top.v1"})); c != 2 {
		t.Fatalf("non-ai.agent plugin without ai.send.sales = %d, want 2", c)
	}
	if after := countDenials(t, d, pluginID); after != before {
		t.Fatalf("non-ai.agent call audited a denial (%d → %d)", before, after)
	}
}

func TestAIDataClassAllowed(t *testing.T) {
	const pluginID = "com.test.views.aiclass"
	ctx := context.Background()
	d := viewDB(t, pluginID, "ai.send.sales")

	cases := []struct {
		name    string
		aiAgent bool
		class   string
		want    bool
		audited bool
	}{
		{name: "not an agent, unclassified", aiAgent: false, class: "", want: true},
		{name: "not an agent, ungranted class", aiAgent: false, class: data.DataClassStaff, want: true},
		{name: "agent, unclassified view", aiAgent: true, class: "", want: false, audited: true},
		{name: "agent, shop", aiAgent: true, class: data.DataClassShop, want: true},
		{name: "agent, granted class", aiAgent: true, class: data.DataClassSales, want: true},
		{name: "agent, undeclared class", aiAgent: true, class: data.DataClassStaff, want: false, audited: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := countDenials(t, d, pluginID)
			v := data.CoreView{Name: "test.view.v1", Permission: "view:sales", DataClass: c.class}
			if got := aiDataClassAllowed(ctx, d, pluginID, c.aiAgent, v); got != c.want {
				t.Fatalf("aiDataClassAllowed = %v, want %v", got, c.want)
			}
			if after := countDenials(t, d, pluginID); c.audited != (after > before) {
				t.Fatalf("permission_denied rows %d → %d, want audited=%v", before, after, c.audited)
			}
		})
	}
}

func assertLastDenialPermission(t *testing.T, d *sql.DB, pluginID, perm string) {
	t.Helper()
	var payload string
	if err := d.QueryRow(`SELECT COALESCE(data_json, '') FROM audit_log WHERE action = 'permission_denied' AND entity_id = ? ORDER BY rowid DESC LIMIT 1`, pluginID).Scan(&payload); err != nil {
		t.Fatalf("last denial: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p["permission"] != perm {
		t.Fatalf("last denial payload %q, want permission %q", payload, perm)
	}
}
