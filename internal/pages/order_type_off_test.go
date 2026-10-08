package pages

// ut-docs#3632: sale.order_type_prompt = "off" -- a shop that doesn't sell
// food or drink to eat in (off-licence, barber, retail). No dine-in/takeaway
// toggle, no intercept modal, takeaway refused, lines stay "" (dine-in),
// the tax.rate.ask payload says "none", the EOD "by order type" section is
// hidden when there is nothing but "" to show, and the setup wizard picks it
// for retail/service shops.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// setOrderTypeMode publishes a prompt mode for one test and restores the
// documented default afterwards (the flag is a process global).
func setOrderTypeMode(t *testing.T, mode string) {
	t.Helper()
	httpx.InitOrderTypePromptMode(mode)
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })
}

// The dedicated settings endpoint accepts "off" like the other three modes:
// persisted, and live on this till immediately.
func TestOrderTypePromptSettingsEndpoint_AcceptsOff(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setOrderTypeMode(t, "top")

	rec := postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {data.OrderTypePromptModeOff}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("manager order-type-prompt=off: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeOff {
		t.Fatalf("%s = %q, want off", data.OrderTypePromptModeKey, v)
	}
	if !httpx.OrderTypeOff() {
		t.Fatal("live mode after saving off: OrderTypeOff() = false, want true without a restart")
	}
}

// The remote (cloud) settings door validates against the same set, so "off"
// must be accepted there too, and republished live.
func TestCloudSetTillSetting_AcceptsOrderTypeOff(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	setOrderTypeMode(t, "top")
	if _, err := cloudSetTillSetting(t.Context(), dp, nil, data.OrderTypePromptModeKey, data.OrderTypePromptModeOff); err != nil {
		t.Fatalf("cloud set %s=off: %v", data.OrderTypePromptModeKey, err)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeOff {
		t.Fatalf("%s = %q, want off", data.OrderTypePromptModeKey, v)
	}
	if !httpx.OrderTypeOff() {
		t.Fatal("cloud set off did not republish the live mode")
	}
}

// With Off, the sell screen has neither the basket-top toggle nor the
// intercept modal; with the default "top" both are still there.
func TestSellScreen_OrderTypeOffHidesToggleAndModal(t *testing.T) {
	mux, _ := quickPayTestMux(t)

	setOrderTypeMode(t, "top")
	home := getHome(t, mux)
	for _, want := range []string{`data-testid="order-type-takeaway"`, `id="order-type-prompt-modal"`} {
		if !strings.Contains(home, want) {
			t.Fatalf("mode top: %s missing from the sell screen", want)
		}
	}

	httpx.InitOrderTypePromptMode(data.OrderTypePromptModeOff)
	home = getHome(t, mux)
	for _, banned := range []string{`order-type-toggle-group`, `data-testid="order-type-takeaway"`, `data-testid="order-type-dine-in"`, `id="order-type-prompt-modal"`} {
		if strings.Contains(home, banned) {
			t.Fatalf("mode off: %s must not render on the sell screen", banned)
		}
	}
}

// POST /api/pos/order-type refuses takeaway with 409 (JSON envelope) when the
// shop has no eat-in, and the basket stays at "". Dine-in ("") is a no-op
// that is still accepted.
func TestOrderTypeAPI_OffRefusesTakeaway(t *testing.T) {
	mux, d := newPOSTestDeps(t)
	setOrderTypeMode(t, data.OrderTypePromptModeOff)

	rec := posPostForm(mux, "/api/pos/order-type", "order_type=takeaway")
	if rec.Code != http.StatusConflict {
		t.Fatalf("takeaway with mode off: code=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data  any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil || env.Error.Code != "order_type_off" || env.Error.Message == "" {
		t.Fatalf("409 body must be the {data,error} envelope with code order_type_off: err=%v body=%s", err, rec.Body.String())
	}
	if got := d.Engine.Basket().OrderType; got != "" {
		t.Fatalf("basket order type after refused takeaway = %q, want \"\"", got)
	}

	rec = posPostForm(mux, "/api/pos/order-type", "order_type=")
	if rec.Code != http.StatusOK {
		t.Fatalf("dine-in with mode off: code=%d, want 200", rec.Code)
	}
	if got := d.Engine.Basket().OrderType; got != "" {
		t.Fatalf("basket order type after dine-in = %q, want \"\"", got)
	}

	// Sanity: the same request is still honoured in any other mode.
	httpx.InitOrderTypePromptMode("top")
	if rec := posPostForm(mux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK || d.Engine.Basket().OrderType != pos.OrderTypeTakeaway {
		t.Fatalf("takeaway with mode top: code=%d order_type=%q", rec.Code, d.Engine.Basket().OrderType)
	}
}

// tax.rate.ask: with Off, a "" (dine-in) line is sent as order_type "none"
// so a plugin applies the item's own rate; "" keeps meaning dine-in on every
// other shop; a legacy takeaway line (held sale, synced peer) is untouched.
func TestAskTaxRateBP_OrderTypeOffSendsNone(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()
	var seen []string
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			var p taxRateAskPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Errorf("payload: %v", err)
			}
			seen = append(seen, p.OrderType)
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	setOrderTypeMode(t, "top")
	(&pluginTaxRateAsker{db: db}).AskTaxRateBP(line, "")
	httpx.InitOrderTypePromptMode(data.OrderTypePromptModeOff)
	(&pluginTaxRateAsker{db: db}).AskTaxRateBP(line, "")
	(&pluginTaxRateAsker{db: db}).AskTaxRateBP(line, pos.OrderTypeTakeaway)

	want := []string{"", taxAskOrderTypeNone, pos.OrderTypeTakeaway}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("order_type sent to the plugin = %q, want %q", seen, want)
	}
	if taxAskOrderTypeNone != "none" {
		t.Fatalf("taxAskOrderTypeNone = %q, want the wire value \"none\"", taxAskOrderTypeNone)
	}
}

// EOD: with Off and only "" rows the "by order type" section is noise and is
// hidden -- on screen and on the printed Z. A takeaway row (a sale from
// before the switch) keeps it, and other modes are unchanged.
func TestEOD_OrderTypeSectionHiddenWhenOffAndNoTakeaway(t *testing.T) {
	dineOnly := []data.OrderTypeSales{{OrderType: "", Qty: 1, Net: money.FromMinor(10000), Gross: money.FromMinor(10000)}}
	mixed := append([]data.OrderTypeSales{{OrderType: "takeaway", Qty: 1, Net: money.FromMinor(5000), Gross: money.FromMinor(5000)}}, dineOnly...)
	printed := func(rows []data.OrderTypeSales) bool {
		rep := data.EODReport{Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z", SalesCount: 1, OrderTypes: rows}
		return strings.Contains(string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0))), "BY ORDER TYPE")
	}

	setOrderTypeMode(t, data.OrderTypePromptModeOff)
	if printed(dineOnly) {
		t.Error("printed Z: mode off with only dine-in rows must omit BY ORDER TYPE")
	}
	if !printed(mixed) {
		t.Error("printed Z: mode off with a takeaway row must keep BY ORDER TYPE")
	}
	httpx.InitOrderTypePromptMode("top")
	if !printed(dineOnly) {
		t.Error("printed Z: mode top must keep BY ORDER TYPE")
	}

	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r1','eod','2026-01-01',
'{"day":"2026-01-01","sales_count":1,"net":500,"order_types":[{"order_type":"","qty":1,"net":500,"gross":600}]}')`); err != nil {
		t.Fatal(err)
	}
	screen := func() bool {
		rec := getReportsTab(t, mux, "eod", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("eod tab: %d %s", rec.Code, rec.Body.String())
		}
		return strings.Contains(rec.Body.String(), "By order type")
	}
	if !screen() {
		t.Fatal("reports tab: mode top must show the by-order-type section")
	}
	httpx.InitOrderTypePromptMode(data.OrderTypePromptModeOff)
	if screen() {
		t.Fatal("reports tab: mode off with only dine-in rows must hide the by-order-type section")
	}
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE report_archive SET content_json='{"day":"2026-01-01","sales_count":2,"net":900,"order_types":[{"order_type":"","qty":1,"net":500,"gross":600},{"order_type":"takeaway","qty":1,"net":400,"gross":400}]}' WHERE id='r1'`); err != nil {
		t.Fatal(err)
	}
	if !screen() {
		t.Fatal("reports tab: mode off with a takeaway row must still show the section")
	}
}

// Setup wizard: retail and service shops start with Off; cafe/hospitality/
// market_stall/other are unchanged; an already-set value is never overwritten.
func TestSetupWizard_ShopTypePicksOrderTypeOff(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })

	run := func(t *testing.T, shopType, preset string) string {
		t.Helper()
		mux, _, d := newFullAuthDeps(t)
		setOrderTypeMode(t, "top")
		if preset != "" {
			if err := d.Settings.Set(t.Context(), data.OrderTypePromptModeKey, preset); err != nil {
				t.Fatal(err)
			}
		}
		rec := postForm(mux, "/api/setup", url.Values{
			"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
			"tax_rate_pct": {"20"}, "store_name": {"Corner Shop"}, "shop_type": {shopType},
		}, nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("wizard shop_type=%s: code=%d body=%s", shopType, rec.Code, rec.Body.String())
		}
		v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey)
		return v
	}

	for _, st := range []string{"retail", "service"} {
		t.Run(st, func(t *testing.T) {
			if got := run(t, st, ""); got != data.OrderTypePromptModeOff {
				t.Fatalf("shop_type=%s: %s = %q, want off", st, data.OrderTypePromptModeKey, got)
			}
			if !httpx.OrderTypeOff() {
				t.Fatalf("shop_type=%s: live mode not republished as off", st)
			}
		})
	}
	for _, st := range []string{"cafe", "hospitality", "market_stall", "other"} {
		t.Run(st, func(t *testing.T) {
			if got := run(t, st, ""); got != "" {
				t.Fatalf("shop_type=%s: %s = %q, want unset", st, data.OrderTypePromptModeKey, got)
			}
			if httpx.OrderTypeOff() {
				t.Fatalf("shop_type=%s: live mode must not be off", st)
			}
		})
	}
	t.Run("existing value kept", func(t *testing.T) {
		if got := run(t, "retail", data.OrderTypePromptModeAtPay); got != data.OrderTypePromptModeAtPay {
			t.Fatalf("retail with a preset at_pay: %s = %q, want at_pay kept", data.OrderTypePromptModeKey, got)
		}
	})
}

// ut-docs#3632 (review finding 1): the self-order kiosk is auth-exempt, so
// hiding its Dine in / Takeaway toggle is not enough -- under Off the
// kiosk's own order-type endpoint must clamp a takeaway request to "" too,
// or a customer puts a takeaway line (and a takeaway tax ask) on a shop
// whose cashier sales all go out as "none".
func TestSelfOrderShop_OrderTypeOffClampsTakeawayAndHidesToggle(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	setOrderTypeMode(t, data.OrderTypePromptModeOff)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/order-type", strings.NewReader("order_type=takeaway"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST order-type under Off: want 200 (clamped swap), got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.OrderType(); got != "" {
		t.Fatalf("kiosk order type under Off = %q, want \"\" (takeaway clamped)", got)
	}
	if strings.Contains(rec.Body.String(), `hx-post="/api/self-order/order-type"`) {
		t.Fatalf("kiosk cart under Off still renders the dine-in/takeaway toggle: %s", rec.Body.String())
	}
}
