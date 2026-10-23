package pages

// ut-docs#4337 (ADR-0159 §1/§5): the till's guest_order hook
// (cloudGuestOrder) and the guest_tables device report. Real migrated DB
// (setupSelfOrderShopDeps); every refusal must leave nothing written.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

type guestHarness struct {
	t     *testing.T
	dp    *common.Deps
	d     *db.DB
	token string // T7's guest token
	table string // T7's id
}

func newGuestHarness(t *testing.T) *guestHarness {
	t.Helper()
	dp, d := setupSelfOrderShopDeps(t)
	t.Cleanup(dp.WaitForAsyncWork)
	ctx := context.Background()
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(ctx, "T7", "Garden", 4, "rect", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	var tok string
	if err := d.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, tableID).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-cake", "CAKE", "5000002", "Carrot Cake", 450)
	seedShopItem(t, d, "itm-tea", "TEA", "", "Tea", 999)
	seedShopItem(t, d, "itm-old", "OLD", "5000009", "Retired", 100)
	seedShopItem(t, d, "itm-gv", "GIFT", "5000010", "Gift card", 2500)
	seedShopItem(t, d, "itm-apples", "APPLES", "5000011", "Apples", 300)
	execAll(t, d, []string{
		`UPDATE items SET is_active = 0 WHERE id = 'itm-old'`,
		`UPDATE items SET item_type = 'gift_voucher' WHERE id = 'itm-gv'`,
		`UPDATE items SET is_weighed = 1 WHERE id = 'itm-apples'`,
		// Coffee: an optional extras group (0..2) and a required milk group (1..1).
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-extras','Extras',0,0,2,1)`,
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-milk','Milk',1,1,1,2)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-coffee','g-extras',1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-coffee','g-milk',2)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-shot','g-extras','Extra shot',50,1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-syrup','g-extras','Syrup',40,2)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-vanilla','g-extras','Vanilla',40,3)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-oat','g-milk','Oat',30,1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-whole','g-milk','Whole',0,2)`,
		// A group on another item: its option is foreign to coffee.
		`INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES ('g-cake','Cream',0,0,1,1)`,
		`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-cake','g-cake',1)`,
		`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o-cream','g-cake','Cream',60,1)`,
		// Tea has sizes; cake has none.
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-tea-s','itm-tea','TEA-S','Small',200,1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-tea-l','itm-tea','TEA-L','Large',280,1)`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-cake-x','itm-old','OLD-X','X',100,1)`,
	})
	return &guestHarness{t: t, dp: dp, d: d, token: tok, table: tableID}
}

func (h *guestHarness) order(id string, lines ...cloudsync.GuestOrderLine) cloudsync.GuestOrder {
	return cloudsync.GuestOrder{ID: id, DirectiveID: "dir-" + id, TableToken: h.token, Lines: lines}
}

func (h *guestHarness) count(table string) int {
	h.t.Helper()
	var n int
	if err := h.d.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *guestHarness) apply(o cloudsync.GuestOrder) cloudsync.GuestOrderOutcome {
	h.t.Helper()
	out, err := cloudGuestOrder(context.Background(), h.dp, o)
	if err != nil {
		h.t.Fatalf("cloudGuestOrder(%s): internal error %v", o.ID, err)
	}
	return out
}

func coffee(qty int, opts ...string) cloudsync.GuestOrderLine {
	return cloudsync.GuestOrderLine{ItemID: "itm-coffee", Qty: qty, ModifierOptionIDs: opts}
}

func TestCloudGuestOrder_PlacesHeldTableOrderPricedByTheTill(t *testing.T) {
	h := newGuestHarness(t)
	out := h.apply(h.order("g1",
		coffee(2, "o-shot", "o-oat"),
		cloudsync.GuestOrderLine{ItemID: "itm-tea", VariantID: "v-tea-l", Qty: 1},
		cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1},
	))
	if out.Refusal != "" {
		t.Fatalf("refused: %q", out.Refusal)
	}
	if !strings.HasPrefix(out.DisplayNo, "C-") || out.Currency != "GBP" {
		t.Fatalf("outcome %+v", out)
	}
	// Till prices: coffee 320+50+30 = 400 ×2, tea large 280, cake 450 =
	// 1530 net, + 20% tax (exclusive) = 1836.
	if out.TotalMinor != 1836 {
		t.Fatalf("total %d, want 1836 (the till's own prices and tax)", out.TotalMinor)
	}
	held, ok, err := data.NewHeldSalesRepo(h.d.DB).Get(context.Background(), mustCounterID(t, h, "g1"))
	if err != nil || !ok {
		t.Fatalf("held sale: %v %v", ok, err)
	}
	if held.TableID != h.table || held.TotalMinor != out.TotalMinor || !strings.HasPrefix(held.Label, out.DisplayNo) {
		t.Fatalf("held %+v", held)
	}
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.TableID != h.table || snap.TableLabel != "T7" || snap.DisplayNo != out.DisplayNo || snap.OrderType != "" || len(snap.Lines) != 3 {
		t.Fatalf("parked snapshot: %+v", snap)
	}
	for _, l := range snap.Lines {
		if l.KitchenSentQty != 0 {
			t.Fatalf("no kitchen printer: lines must stay unsent, got %+v", l)
		}
	}
	co, _, _ := data.NewKioskCounterOrdersRepo(h.d.DB).FindByGuestOrderID(context.Background(), "g1")
	if co.Status != data.KioskCounterOrderStatusHeld || co.TableID != h.table || co.DisplayNo != out.DisplayNo {
		t.Fatalf("counter row %+v", co)
	}
}

func mustCounterID(t *testing.T, h *guestHarness, guestOrderID string) string {
	t.Helper()
	co, ok, err := data.NewKioskCounterOrdersRepo(h.d.DB).FindByGuestOrderID(context.Background(), guestOrderID)
	if err != nil || !ok {
		t.Fatalf("no counter order for %s: %v", guestOrderID, err)
	}
	return co.ID
}

// The payload carries no price at all (cloudsync.GuestOrder has no price
// field; decodeGuestOrder drops any): the total follows the TILL's
// catalogue, so a price changed on the till after the guest's page loaded
// is what the order costs.
func TestCloudGuestOrder_PriceComesFromTheTillCatalogue(t *testing.T) {
	h := newGuestHarness(t)
	if _, err := h.d.DB.Exec(`UPDATE items SET base_price = 500 WHERE id = 'itm-cake'`); err != nil {
		t.Fatal(err)
	}
	out := h.apply(h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 2}))
	if out.Refusal != "" || out.TotalMinor != 1200 { // 2 × 500 + 20%
		t.Fatalf("outcome %+v, want total 1200 from the till's current price", out)
	}
}

func TestCloudGuestOrder_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *guestHarness)
		order func(h *guestHarness) cloudsync.GuestOrder
		want  string
	}{
		{"unknown token", nil, func(h *guestHarness) cloudsync.GuestOrder {
			o := h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
			o.TableToken = strings.Repeat("0", 32)
			return o
		}, cloudsync.GuestOrderUnknownTable},
		{"disabled table", func(h *guestHarness) {
			if err := data.NewPOSRepo(h.d.DB).SetTableEnabled(context.Background(), h.table, false); err != nil {
				h.t.Fatal(err)
			}
		}, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
		}, cloudsync.GuestOrderUnknownTable},
		{"missing item", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-nope", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"inactive item", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-old", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"gift voucher", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-gv", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"weighed item", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-apples", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"foreign variant", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-tea", VariantID: "v-cake-x", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"variant on an item without variants", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-cake", VariantID: "v-tea-s", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"retired variant", func(h *guestHarness) {
			execAll(h.t, h.d, []string{`UPDATE item_variants SET is_active = 0 WHERE id = 'v-tea-l'`})
		}, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-tea", VariantID: "v-tea-l", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
		{"no variant chosen for an item that needs one", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-tea", Qty: 1})
		}, cloudsync.GuestOrderBadOrder},
		{"foreign option", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", coffee(1, "o-oat", "o-cream"))
		}, cloudsync.GuestOrderItemUnavailable},
		{"unknown option", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", coffee(1, "o-oat", "o-gone"))
		}, cloudsync.GuestOrderItemUnavailable},
		{"group min (required milk missing)", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", coffee(1, "o-shot"))
		}, cloudsync.GuestOrderBadOrder},
		{"group max (three extras)", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", coffee(1, "o-oat", "o-shot", "o-syrup", "o-vanilla"))
		}, cloudsync.GuestOrderBadOrder},
		{"group max (two milks)", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", coffee(1, "o-oat", "o-whole"))
		}, cloudsync.GuestOrderBadOrder},
		{"one bad line refuses the whole order", nil, func(h *guestHarness) cloudsync.GuestOrder {
			return h.order("g", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1}, cloudsync.GuestOrderLine{ItemID: "itm-gv", Qty: 1})
		}, cloudsync.GuestOrderItemUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			out := h.apply(tc.order(h))
			if out.Refusal != tc.want {
				t.Fatalf("refusal %q, want %q (outcome %+v)", out.Refusal, tc.want, out)
			}
			if n := h.count("kiosk_counter_orders"); n != 0 {
				t.Fatalf("a refused order wrote %d counter order(s)", n)
			}
			if n := h.count("held_sales"); n != 0 {
				t.Fatalf("a refused order parked %d held sale(s)", n)
			}
		})
	}
}

func TestCloudGuestOrder_RedeliveryReturnsTheFirstOrder(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
	first := h.apply(o)
	second := h.apply(o)
	if first.Refusal != "" || first != second {
		t.Fatalf("re-delivery: %+v vs %+v", first, second)
	}
	if n := h.count("kiosk_counter_orders"); n != 1 {
		t.Fatalf("%d counter orders for one guest order", n)
	}
	if n := h.count("held_sales"); n != 1 {
		t.Fatalf("%d held sales for one guest order", n)
	}
}

// ADR-0020 / ADR-0159 §5: the hook builds a fresh basket from the session
// factory and never touches the cashier's Engine or the walk-up
// KioskEngine — their baskets are exactly as they were.
func TestCloudGuestOrder_NeverTouchesTheCashierOrKioskBasket(t *testing.T) {
	h := newGuestHarness(t)
	h.dp.Engine.ScanQty("5000001", 1)
	h.dp.KioskEngine.ScanQty("5000002", 3)
	cashierBefore, _ := json.Marshal(h.dp.Engine.Snapshot())
	kioskBefore, _ := json.Marshal(h.dp.KioskEngine.Snapshot())
	liveBefore := h.dp.SelfOrderSessions.Len()

	if out := h.apply(h.order("g1", coffee(1, "o-whole"), cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 2})); out.Refusal != "" {
		t.Fatalf("refused: %q", out.Refusal)
	}
	cashierAfter, _ := json.Marshal(h.dp.Engine.Snapshot())
	kioskAfter, _ := json.Marshal(h.dp.KioskEngine.Snapshot())
	if string(cashierBefore) != string(cashierAfter) {
		t.Fatalf("cashier basket changed:\n%s\n%s", cashierBefore, cashierAfter)
	}
	if string(kioskBefore) != string(kioskAfter) {
		t.Fatalf("kiosk basket changed:\n%s\n%s", kioskBefore, kioskAfter)
	}
	if h.dp.SelfOrderSessions.Len() != liveBefore {
		t.Fatal("the directive registered a live table session")
	}
}

// The hook lives outside guard-kiosk-engine.sh's scope (it registers no
// route), so this pins the same rule on its file: no `.Engine` or
// `.KioskEngine` selector in code.
func TestGuestOrderDirectiveFile_NeverNamesTheSharedEngines(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(self), "guest_order_directive.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Engine" || sel.Sel.Name == "KioskEngine") {
			t.Errorf("%s: guest_order_directive.go references .%s", fset.Position(sel.Pos()), sel.Sel.Name)
		}
		return true
	})
}

func TestCloudGuestOrder_AsyncTicketMarksSentOnlyIfTheHeldRowIsUnchanged(t *testing.T) {
	t.Run("printed and unchanged: lines marked sent", func(t *testing.T) {
		h := newGuestHarness(t)
		printer := newFakeKitchenPrinter(t)
		if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
			t.Fatal(err)
		}
		if out := h.apply(h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 2})); out.Refusal != "" {
			t.Fatal(out.Refusal)
		}
		h.dp.WaitForAsyncWork()
		if n := len(printer.Settle(1)); n != 1 {
			t.Fatalf("want 1 kitchen ticket, got %d", n)
		}
		held, _, _ := data.NewHeldSalesRepo(h.d.DB).Get(context.Background(), mustCounterID(t, h, "g1"))
		var snap pos.BasketSnapshot
		_ = json.Unmarshal([]byte(held.Payload), &snap)
		if len(snap.Lines) != 1 || snap.Lines[0].KitchenSentQty != snap.Lines[0].Qty {
			t.Fatalf("printed ticket not recorded on the held row: %+v", snap.Lines)
		}
	})
	t.Run("printer down: lines stay unsent", func(t *testing.T) {
		h := newGuestHarness(t)
		if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, "127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
		if out := h.apply(h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})); out.Refusal != "" {
			t.Fatal(out.Refusal)
		}
		h.dp.WaitForAsyncWork()
		held, _, _ := data.NewHeldSalesRepo(h.d.DB).Get(context.Background(), mustCounterID(t, h, "g1"))
		var snap pos.BasketSnapshot
		_ = json.Unmarshal([]byte(held.Payload), &snap)
		if snap.Lines[0].KitchenSentQty != 0 {
			t.Fatalf("an unprinted ticket was marked sent: %+v", snap.Lines)
		}
	})
	t.Run("cashier changed the order meanwhile: never overwritten", func(t *testing.T) {
		h := newGuestHarness(t)
		printer := newFakeKitchenPrinter(t)
		if err := h.dp.Settings.Set(t.Context(), keyPrinterKitchen, printer.addr); err != nil {
			t.Fatal(err)
		}
		// Hold the print until the cashier's re-park has landed.
		release := make(chan struct{})
		gate := beforeAsyncCounterTicket
		beforeAsyncCounterTicket = func() { <-release }
		t.Cleanup(func() { beforeAsyncCounterTicket = gate })

		if out := h.apply(h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})); out.Refusal != "" {
			t.Fatal(out.Refusal)
		}
		id := mustCounterID(t, h, "g1")
		repo := data.NewHeldSalesRepo(h.d.DB)
		held, _, _ := repo.Get(context.Background(), id)
		changed := strings.Replace(held.Payload, `"lines":[`, `"note":"cashier",`+`"lines":[`, 1)
		if changed == held.Payload {
			t.Fatalf("fixture: payload has no lines key: %s", held.Payload)
		}
		held.Payload = changed
		if err := repo.Upsert(context.Background(), held); err != nil {
			t.Fatal(err)
		}
		close(release)
		h.dp.WaitForAsyncWork()
		printer.Settle(1)
		after, _, _ := repo.Get(context.Background(), id)
		if after.Payload != held.Payload {
			t.Fatalf("the async ticket overwrote the cashier's change:\n%s\n%s", held.Payload, after.Payload)
		}
	})
}

func TestRemoteGuestTablesReport(t *testing.T) {
	h := newGuestHarness(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(h.d.DB)
	long := strings.Repeat("é", 40) // 80 bytes
	longID, _ := repo.CreateTable(ctx, long, "Garden", 2, "rect", 100, 100)
	offID, _ := repo.CreateTable(ctx, "Off", "Garden", 2, "rect", 100, 100)
	_ = repo.SetTableEnabled(ctx, offID, false)
	// A table from before migration 076: no token until the report needs one.
	if _, err := h.d.DB.Exec(`UPDATE tables SET guest_token = NULL WHERE id = ?`, longID); err != nil {
		t.Fatal(err)
	}

	rep, ok := remoteGuestTablesReport(ctx, h.dp)
	if !ok || len(rep) != 2 {
		t.Fatalf("report %v %v, want the 2 enabled tables", rep, ok)
	}
	labels := map[string]string{}
	for _, e := range rep {
		tok, _ := e["token"].(string)
		lbl, _ := e["label"].(string)
		if len(tok) != 32 {
			t.Fatalf("token %q", tok)
		}
		labels[lbl] = tok
	}
	if _, ok := labels["Off"]; ok {
		t.Fatal("a disabled table was reported")
	}
	if labels["T7"] != h.token {
		t.Fatalf("T7 token %q, want %q", labels["T7"], h.token)
	}
	clipped := strings.Repeat("é", 32) // 64 bytes on a rune boundary
	tok, ok := labels[clipped]
	if !ok {
		t.Fatalf("long label not clipped to 64 bytes: %v", labels)
	}
	if !utf8.ValidString(clipped) || len(clipped) != 64 {
		t.Fatal("bad clip fixture")
	}
	var stored string
	_ = h.d.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, longID).Scan(&stored)
	if stored != tok {
		t.Fatal("the token minted for an existing table was not stored")
	}
	if got := clipGuestTableLabel("abc"); got != "abc" {
		t.Fatalf("short label changed: %q", got)
	}
	if got := clipGuestTableLabel(strings.Repeat("a", 63) + "é"); got != strings.Repeat("a", 63) {
		t.Fatalf("clip split a rune: %q", got)
	}
}

func TestRemoteGuestTablesReport_CapsAt500(t *testing.T) {
	h := newGuestHarness(t)
	tx, err := h.d.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 505; i++ {
		if _, err := tx.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?, ?, '', 2, 'rect', 100, 100, 1, '', '')`,
			fmt.Sprintf("tb-%03d", i), fmt.Sprintf("X%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rep, ok := remoteGuestTablesReport(context.Background(), h.dp)
	if !ok || len(rep) != maxReportedGuestTables || maxReportedGuestTables != 500 {
		t.Fatalf("report has %d entries, want 500", len(rep))
	}
}

// ut-docs#4392 gap 1: an orphan — the "held" counter row was written but
// the till died (or the park and its cleanup both failed) before the held
// sale was parked. A re-delivered directive must place the order exactly
// once, never answer "applied" for an order staff cannot see.
func (h *guestHarness) orphan(guestOrderID string, age time.Duration) string {
	h.t.Helper()
	id := mustCounterID(h.t, h, guestOrderID)
	if _, err := h.d.DB.Exec(`DELETE FROM held_sales WHERE id = ?`, id); err != nil {
		h.t.Fatal(err)
	}
	created := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := h.d.DB.Exec(`UPDATE kiosk_counter_orders SET created_at = ? WHERE id = ?`, created, id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func TestCloudGuestOrder_RedeliveryReplacesAnOrphanExactlyOnce(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 2})
	first := h.apply(o)
	orphanID := h.orphan("g1", 5*time.Minute)

	second := h.apply(o)
	if second.Refusal != "" || second.TotalMinor != first.TotalMinor || second.DisplayNo == "" {
		t.Fatalf("re-delivery over an orphan: %+v (first %+v)", second, first)
	}
	if n := h.count("kiosk_counter_orders"); n != 1 {
		t.Fatalf("%d counter orders, want 1 (the orphan replaced, not added to)", n)
	}
	if n := h.count("held_sales"); n != 1 {
		t.Fatalf("%d held sales, want the order parked exactly once", n)
	}
	newID := mustCounterID(t, h, "g1")
	held, ok, err := data.NewHeldSalesRepo(h.d.DB).Get(context.Background(), newID)
	if err != nil || !ok || held.TotalMinor != second.TotalMinor || !strings.HasPrefix(held.Label, second.DisplayNo) {
		t.Fatalf("the re-placed order must be visible on Open orders: held=%+v ok=%v err=%v", held, ok, err)
	}
	if _, ok, _ := data.NewKioskCounterOrdersRepo(h.d.DB).Get(context.Background(), orphanID); ok && orphanID != newID {
		t.Fatal("the orphan counter row is still there next to the new one")
	}
	if third := h.apply(o); third != second {
		t.Fatalf("a further re-delivery must answer the re-placed order: %+v vs %+v", third, second)
	}
}

// A cashier mid-resume also leaves a held counter row with no held sale —
// but the resume's claim leaves a tombstone, so it is answered as placed
// and never duplicated.
func TestCloudGuestOrder_RedeliveryDuringAResumeIsNotDuplicated(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
	first := h.apply(o)
	id := mustCounterID(t, h, "g1")
	if _, found, _ := heldSaleClaimForResume(context.Background(), h.dp, data.NewHeldSalesRepo(h.d.DB), id); !found {
		t.Fatal("resume claim found nothing")
	}
	second := h.apply(o)
	if second.Refusal != "" || second.DisplayNo != first.DisplayNo {
		t.Fatalf("re-delivery during a resume: %+v (first %+v)", second, first)
	}
	if n := h.count("held_sales"); n != 0 {
		t.Fatalf("%d held sales: the resumed order was parked again", n)
	}
	if n := h.count("kiosk_counter_orders"); n != 1 {
		t.Fatalf("%d counter orders for one guest order", n)
	}
}

// An orphan older than the tombstone window can no longer be told apart
// from an order resumed (and voided) long ago: refuse, never re-place an
// order that may have been dealt with, never claim it is parked.
func TestCloudGuestOrder_RedeliveryOverAnOldOrphanIsRefused(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
	h.apply(o)
	h.orphan("g1", data.HeldSaleTombstoneWindow+time.Hour)
	if out := h.apply(o); out.Refusal == "" {
		t.Fatalf("an old orphan must be refused, got %+v", out)
	}
	if n := h.count("held_sales"); n != 0 {
		t.Fatalf("%d held sales: an old orphan was re-placed", n)
	}
}

// A counter row this young with no held sale may be another apply still
// between Create and the park: leave the directive pending (internal
// error), never delete a row someone else is about to park.
func TestCloudGuestOrder_RedeliveryOverAnInFlightOrderIsDeferred(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
	h.apply(o)
	id := h.orphan("g1", 0)
	// created_at has whole seconds: pin the process start well before it.
	saved := guestOrderProcessStart
	guestOrderProcessStart = time.Now().Add(-time.Hour)
	t.Cleanup(func() { guestOrderProcessStart = saved })
	if _, err := cloudGuestOrder(context.Background(), h.dp, o); !errors.Is(err, errGuestOrderInFlight) {
		t.Fatalf("an in-flight order must defer the directive, got %v", err)
	}
	if _, ok, _ := data.NewKioskCounterOrdersRepo(h.d.DB).Get(context.Background(), id); !ok {
		t.Fatal("the in-flight counter row was deleted")
	}
}

// A young row left by a PREVIOUS run of the till (it crashed and restarted)
// can have no apply in flight: it is re-placed at once, not deferred past
// the cloud's 10-minute delivery window.
func TestCloudGuestOrder_RedeliveryAfterARestartReplacesAYoungOrphan(t *testing.T) {
	h := newGuestHarness(t)
	o := h.order("g1", cloudsync.GuestOrderLine{ItemID: "itm-cake", Qty: 1})
	h.apply(o)
	h.orphan("g1", 0)
	saved := guestOrderProcessStart
	guestOrderProcessStart = time.Now().Add(time.Minute)
	t.Cleanup(func() { guestOrderProcessStart = saved })
	if out := h.apply(o); out.Refusal != "" || out.DisplayNo == "" {
		t.Fatalf("a restarted till must re-place the orphan: %+v", out)
	}
	if n := h.count("held_sales"); n != 1 {
		t.Fatalf("%d held sales, want the order parked exactly once", n)
	}
}

// ut-docs#4715 / #4705: the guest's page language becomes the parked
// order's customer language, so the receipt printed at payment is in it. A
// language this till has not installed (or none) leaves it "" = shop
// language.
func TestCloudGuestOrder_RecordsTheGuestsLanguageOnTheParkedOrder(t *testing.T) {
	for _, tc := range []struct{ name, locale, want string }{
		{"installed, exact base code", "tr", "tr"},
		{"regional tag maps onto the installed base language", "tr-TR", "tr"},
		{"not installed", "ja-JP", ""},
		{"absent", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHarness(t)
			i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
			if err != nil {
				t.Fatalf("load i18n: %v", err)
			}
			httpx.InitI18n(i18n, "en")
			o := h.order("g-loc", coffee(1, "o-oat"))
			o.Locale = tc.locale
			if out := h.apply(o); out.Refusal != "" {
				t.Fatalf("refused: %q", out.Refusal)
			}
			held, ok, err := data.NewHeldSalesRepo(h.d.DB).Get(context.Background(), mustCounterID(t, h, "g-loc"))
			if err != nil || !ok {
				t.Fatalf("held sale: %v %v", ok, err)
			}
			var snap pos.BasketSnapshot
			if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
				t.Fatal(err)
			}
			if snap.CustomerLocale != tc.want {
				t.Fatalf("CustomerLocale = %q, want %q (available %v)", snap.CustomerLocale, tc.want, httpx.AvailableLocales())
			}
		})
	}
}
