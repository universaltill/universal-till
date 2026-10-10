package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0121 §5 (ut-docs#3158): view_query — a plugin reads a core read view
// only when the view is in its manifest views_used AND it holds the view's
// view:<class> grant. Driven through a real wasip1 guest
// (testdata/view_guest).

func buildViewGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "view_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/view_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 view guest: %v\n%s", err, raw)
	}
	return out
}

func viewsManifest(pluginID string, views ...string) string {
	vu, _ := json.Marshal(views)
	return fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","views_used":%s}`, pluginID, vu)
}

// viewDB is a migrated DB with the test plugin, its storage grant (the
// guest records results through storage_set), perms, and a few audit rows
// so audit.summary.v1 has something to return.
func viewDB(t *testing.T, pluginID string, perms ...string) *sql.DB {
	t.Helper()
	d := streamDB(t, pluginID, perms...)
	for i, a := range []string{"login", "login", "void", "no_sale"} {
		if _, err := d.Exec(`INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, created_at)
			VALUES (?, NULL, 'till', '-', ?, datetime('now'))`, fmt.Sprintf("view-test-%d", i), a); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}
	return d
}

func newViewRuntime(t *testing.T, guest, pluginID string) *WasmRuntime {
	t.Helper()
	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	return w
}

func viewCode(t *testing.T, res map[string]any) int {
	t.Helper()
	c, ok := res["code"].(float64)
	if !ok {
		t.Fatalf("guest recorded no code: %v", res)
	}
	return int(c)
}

func countDenials(t *testing.T, d *sql.DB, pluginID string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'permission_denied' AND entity_id = ?`, pluginID).Scan(&n); err != nil {
		t.Fatalf("count denials: %v", err)
	}
	return n
}

func TestViewQueryReturnsTheViewResult(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.ok"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "audit.summary.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "view:audit")

	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1", "args": `{"days":7}`})
	code := viewCode(t, res)
	if code <= 0 {
		t.Fatalf("view_query = %d, want the result length", code)
	}

	v, _ := data.LookupCoreView("audit.summary.v1")
	want, err := data.RunCoreView(context.Background(), d, v, map[string]int{"days": 7}, data.CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	if got := res["result"].(string); got != string(want) || code != len(want) {
		t.Fatalf("guest got %d bytes %s, want %d bytes %s", code, got, len(want), want)
	}
	var rows []data.AuditActionCount
	if err := json.Unmarshal(want, &rows); err != nil || len(rows) != 3 {
		t.Fatalf("audit.summary.v1 rows = %v (%v), want 3 action groups", rows, err)
	}
}

func TestViewQueryRefusals(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.deny"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "audit.summary.v1", "items.top.v1", "nosuch.view.v1"))
	w := newViewRuntime(t, guest, pluginID)

	cases := []struct {
		name    string
		perms   []string
		view    string
		args    string
		want    int
		audited bool
	}{
		{name: "grant missing", perms: nil, view: "audit.summary.v1", want: hostErrDenied, audited: true},
		{name: "wrong class granted", perms: []string{"view:sales"}, view: "audit.summary.v1", want: hostErrDenied, audited: true},
		{name: "granted but not in views_used", perms: []string{"view:sales"}, view: "sales.by_day.v1", want: hostErrDenied, audited: true},
		{name: "unknown view", perms: []string{"view:sales"}, view: "nosuch.view.v1", want: hostErrNotFound},
		{name: "empty name", perms: []string{"view:sales"}, view: "", want: hostErrInvalid},
		{name: "days out of range", perms: []string{"view:sales"}, view: "items.top.v1", args: `{"days":366}`, want: hostErrInvalid},
		{name: "limit out of range", perms: []string{"view:sales"}, view: "items.top.v1", args: `{"limit":0}`, want: hostErrInvalid},
		{name: "unknown argument", perms: []string{"view:sales"}, view: "items.top.v1", args: `{"store":"x"}`, want: hostErrInvalid},
		{name: "arguments not an object", perms: []string{"view:sales"}, view: "items.top.v1", args: `[1]`, want: hostErrInvalid},
		{name: "in range", perms: []string{"view:sales"}, view: "items.top.v1", args: `{"days":30,"limit":5}`, want: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := viewDB(t, pluginID, c.perms...)
			before := countDenials(t, d, pluginID)
			res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": c.view, "args": c.args})
			if got := viewCode(t, res); got != c.want {
				t.Fatalf("view_query = %d, want %d (result %v)", got, c.want, res["result"])
			}
			if c.want < 0 && res["result"] != "" {
				t.Fatalf("refused call still wrote %q", res["result"])
			}
			if after := countDenials(t, d, pluginID); c.audited != (after > before) {
				t.Fatalf("permission_denied audit rows %d → %d, want audited=%v", before, after, c.audited)
			}
		})
	}

	t.Run("grant revoked live", func(t *testing.T) {
		d := viewDB(t, pluginID, "view:audit")
		if c := viewCode(t, runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})); c <= 0 {
			t.Fatalf("granted call = %d", c)
		}
		if err := data.NewPluginRepo(d).SetPermission(context.Background(), pluginID, "view:audit", false); err != nil {
			t.Fatal(err)
		}
		if c := viewCode(t, runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})); c != hostErrDenied {
			t.Fatalf("after revoke = %d, want %d", c, hostErrDenied)
		}
	})
}

func TestViewQueryWithoutInstalledManifestIsDenied(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.nomanifest"
	withInstalledManifest(t, "com.test.someone.else", viewsManifest("com.test.someone.else", "audit.summary.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "view:audit")
	if c := viewCode(t, runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})); c != hostErrDenied {
		t.Fatalf("view_query with no installed manifest = %d, want %d", c, hostErrDenied)
	}
}

func TestViewQueryBufferABIAndCap(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.abi"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "audit.summary.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "view:audit")

	full := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})
	n := viewCode(t, full)
	if n <= 8 {
		t.Fatalf("full result length %d too short for the test", n)
	}

	t.Run("undersized buffer gets the full length and a prefix", func(t *testing.T) {
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1", "cap": 8})
		if c := viewCode(t, res); c != n {
			t.Fatalf("undersized call = %d, want the full length %d", c, n)
		}
		if got := res["result"].(string); got != full["result"].(string)[:8] {
			t.Fatalf("undersized buffer holds %q, want the first 8 bytes", got)
		}
	})

	t.Run("result over the cap is refused, not truncated", func(t *testing.T) {
		prev := viewResultCap
		viewResultCap = n - 1
		t.Cleanup(func() { viewResultCap = prev })
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})
		if c := viewCode(t, res); c != hostErrQuota {
			t.Fatalf("over-cap call = %d, want %d", c, hostErrQuota)
		}
		if res["result"] != "" {
			t.Fatalf("over-cap call wrote %q", res["result"])
		}
	})

	t.Run("empty result is an empty array", func(t *testing.T) {
		if _, err := d.Exec(`DELETE FROM audit_log WHERE id LIKE 'view-test-%'`); err != nil {
			t.Fatal(err)
		}
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1", "args": `{"days":1}`})
		if got := strings.TrimSpace(res["result"].(string)); viewCode(t, res) != 2 || got != "[]" {
			t.Fatalf("empty view = %d %q, want 2 []", viewCode(t, res), got)
		}
	})
}

func TestViewQueryPerEventCallCap(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.calls"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "audit.summary.v1"))
	w := newViewRuntime(t, guest, pluginID)
	d := viewDB(t, pluginID, "view:audit")

	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1", "repeat": viewCallsPerEvent + 1})
	codes := codes(res["codes"])
	if len(codes) != viewCallsPerEvent+1 {
		t.Fatalf("recorded %d codes, want %d", len(codes), viewCallsPerEvent+1)
	}
	for i, c := range codes[:viewCallsPerEvent] {
		if c <= 0 {
			t.Fatalf("call %d = %d, want a result under the cap", i, c)
		}
	}
	if last := codes[viewCallsPerEvent]; last != hostErrQuota {
		t.Fatalf("call %d = %d, want %d past the per-event cap", viewCallsPerEvent+1, last, hostErrQuota)
	}
	// The cap is per event: the next event starts again.
	if c := viewCode(t, runGuestPayload(t, w, d, pluginID, map[string]any{"view": "audit.summary.v1"})); c <= 0 {
		t.Fatalf("first call of a new event = %d, want a result", c)
	}
}

// catalog.items.v1 (ADR-0149 §6, ut-docs#3698) runs through the same gate:
// view:inventory and views_used, or a denial the audit log records.
func TestViewQueryCatalogItems(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.catalog"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "catalog.items.v1"))
	w := newViewRuntime(t, guest, pluginID)

	d := viewDB(t, pluginID, "view:inventory")
	if _, err := d.Exec(`INSERT INTO items(id, sku, name, base_price) VALUES('cv-1', 'OL-1', 'Oat latte', 350)`); err != nil {
		t.Fatal(err)
	}
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "catalog.items.v1", "args": `{"limit":10}`})
	want := `[{"id":"cv-1","sku":"OL-1","name":"Oat latte","category_id":"","category":"","price_minor":350,"unit":"each","weighed":false,"active":true}]`
	if code := viewCode(t, res); code != len(want) || res["result"] != want {
		t.Fatalf("view_query = %d %v, want %s", code, res["result"], want)
	}

	d = viewDB(t, pluginID, "view:sales")
	before := countDenials(t, d, pluginID)
	res = runGuestPayload(t, w, d, pluginID, map[string]any{"view": "catalog.items.v1"})
	if code := viewCode(t, res); code != hostErrDenied {
		t.Fatalf("without view:inventory = %d, want %d", code, hostErrDenied)
	}
	if countDenials(t, d, pluginID) <= before {
		t.Fatal("the denial was not audited")
	}
}

// users.list.v1 (ADR-0149 §6, ut-docs#3976) is the new view:users class:
// it runs only with that grant, and never carries a PIN, username or role.
func TestViewQueryUsersList(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.users"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "users.list.v1"))
	w := newViewRuntime(t, guest, pluginID)

	d := viewDB(t, pluginID, "view:users")
	if _, err := d.Exec(`DELETE FROM users WHERE id = 'kiosk'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO users(id, username, display_name, role, pin_hash, is_active)
		VALUES('uv-1', 'sam-login', 'Sam', 'manager', 'pin-hash-secret', 0)`); err != nil {
		t.Fatal(err)
	}
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "users.list.v1"})
	want := `[{"id":"uv-1","display_name":"Sam","active":false}]`
	if code := viewCode(t, res); code != len(want) || res["result"] != want {
		t.Fatalf("view_query = %d %v, want %s", code, res["result"], want)
	}

	d = viewDB(t, pluginID, "view:sales")
	before := countDenials(t, d, pluginID)
	res = runGuestPayload(t, w, d, pluginID, map[string]any{"view": "users.list.v1"})
	if code := viewCode(t, res); code != hostErrDenied {
		t.Fatalf("without view:users = %d, want %d", code, hostErrDenied)
	}
	if countDenials(t, d, pluginID) <= before {
		t.Fatal("the denial was not audited")
	}
}

// sales.receipts.v1 (ADR-0149 §6, ut-docs#3976) runs through the same gate:
// view:sales and views_used, or a denial the audit log records.
func TestViewQuerySalesReceipts(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.receipts"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "sales.receipts.v1"))
	w := newViewRuntime(t, guest, pluginID)

	d := viewDB(t, pluginID, "view:sales")
	// Created now: today's business date at the default midnight start
	// (stored whole seconds never round across midnight).
	now := time.Now()
	created := now.UTC().Format("2006-01-02 15:04:05")
	if _, err := d.Exec(`INSERT INTO sales(id, receipt_no, sale_type, till_id, currency, subtotal, total, created_at)
		VALUES('rv-1', 'R-1', 'sale', 't1', 'EUR', 500, 500, ?)`, created); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, tip_amount, masked_pan)
		VALUES('rp-1', 'rv-1', 'card', 500, 40, '************4242')`); err != nil {
		t.Fatal(err)
	}
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "sales.receipts.v1", "args": `{"limit":10}`})
	want := `[{"id":"rv-1","receipt_no":"R-1","sale_type":"sale","business_date":"` + now.Format("2006-01-02") +
		`","created_at":"` + created + `","till_id":"t1","cashier_id":"","currency":"EUR","subtotal_minor":500,` +
		`"discount_minor":0,"tax_minor":0,"total_minor":500,"tip_minor":40,"fiscal_signed":false,"lines":[],` +
		`"payments":[{"method_id":"card","amount_minor":500,"tip_minor":40}]}]`
	if code := viewCode(t, res); code != len(want) || res["result"] != want {
		t.Fatalf("view_query = %d %v, want %s", code, res["result"], want)
	}

	d = viewDB(t, pluginID, "view:users")
	before := countDenials(t, d, pluginID)
	res = runGuestPayload(t, w, d, pluginID, map[string]any{"view": "sales.receipts.v1"})
	if code := viewCode(t, res); code != hostErrDenied {
		t.Fatalf("without view:sales = %d, want %d", code, hostErrDenied)
	}
	if countDenials(t, d, pluginID) <= before {
		t.Fatal("the denial was not audited")
	}
}

// shop.context.v1 (ut-docs#4034) shares view:sales: it runs with that grant
// and is denied, audited, without it. Its row comes from the source
// internal/pages installs; this package stubs it.
func TestViewQueryShopContext(t *testing.T) {
	guest := buildViewGuest(t)
	const pluginID = "com.test.views.shop"
	withInstalledManifest(t, pluginID, viewsManifest(pluginID, "shop.context.v1"))
	w := newViewRuntime(t, guest, pluginID)
	prev := data.SetCoreViewShopContext(func(context.Context, *sql.DB) (data.ShopContextRow, error) {
		return data.ShopContextRow{StoreName: "Kissa", TillName: "Bar", CurrencyCode: "JPY", Locale: "ja"}, nil
	})
	t.Cleanup(func() { data.SetCoreViewShopContext(prev) })

	d := viewDB(t, pluginID, "view:sales")
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"view": "shop.context.v1"})
	want := `[{"store_name":"Kissa","till_name":"Bar","currency_code":"JPY","currency_decimals":0,"locale":"ja"}]`
	if code := viewCode(t, res); code != len(want) || res["result"] != want {
		t.Fatalf("view_query = %d %v, want %s", code, res["result"], want)
	}

	d = viewDB(t, pluginID, "view:inventory")
	before := countDenials(t, d, pluginID)
	res = runGuestPayload(t, w, d, pluginID, map[string]any{"view": "shop.context.v1"})
	if code := viewCode(t, res); code != hostErrDenied {
		t.Fatalf("without view:sales = %d, want %d", code, hostErrDenied)
	}
	if countDenials(t, d, pluginID) <= before {
		t.Fatal("the denial was not audited")
	}
}
