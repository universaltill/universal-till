package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Review findings on the Online orders work (ut-docs#4768): D1 amounts by the
// order's own currency, D2/D3 layout hooks, D4 currency flag from the till's
// currency setting, and the hook's Problem text and plugin event.

func seedChannelOrder(t *testing.T, repo *data.ChannelOrdersRepo, id, currency string, unit, total int64) string {
	t.Helper()
	in := data.ChannelOrderInput{
		DirectiveID: "dir-" + id, ConnectionID: "0b3b9c34-5c1e-4a3e-8f55-3f7f3a9a7c02", PluginKey: "com.universaltill.channel-demo-with-a-very-long-plugin-key",
		ExternalOrderID: id, Revision: 1, OrderNumber: "#" + id, PlacedAt: "2026-10-22T10:12:00Z", Currency: currency, Status: "new",
		Total: money.FromMinor(total), LocationID: "loc_main",
		Lines: []data.ChannelOrderLineInput{{LineID: "l-1", Name: "Thing " + id, Qty: 1, UnitPrice: money.FromMinor(unit)}},
	}
	res, err := repo.Apply(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return res.OrderID
}

func TestChannelOrdersPage_AmountsFollowTheOrdersOwnCurrency(t *testing.T) {
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	old := httpx.ActiveCurrency().Code
	httpx.InitCurrency("GBP")
	t.Cleanup(func() { httpx.InitCurrency(old) })
	mux := http.NewServeMux()
	registerChannelOrdersPage(mux, dp)
	repo := data.NewChannelOrdersRepo(dp.Db)
	jpy := seedChannelOrder(t, repo, "jpy1", "JPY", 1200, 1200)
	kwd := seedChannelOrder(t, repo, "kwd1", "KWD", 12500, 12500)
	get := func(path string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), auth.User{ID: "m1", Role: "manager"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	list := get("/channel-orders")
	for _, want := range []string{"¥1,200", "KWD 12.500"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	for _, c := range []struct{ id, want string }{{jpy, "¥1,200"}, {kwd, "KWD 12.500"}} {
		if body := get("/channel-orders/" + c.id); strings.Count(body, c.want) < 2 { // line price and total
			t.Errorf("detail lacks %q (line and total):\n%.3000s", c.want, body)
		}
	}
	if tbl := list[strings.Index(list, "channel-orders-table"):]; strings.Contains(tbl, "£") {
		t.Error("an order not in the till's currency was shown in the till's symbol")
	}
}

func TestChannelOrdersPage_PhoneAndTouchLayoutHooks(t *testing.T) {
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerChannelOrdersPage(mux, dp)
	id := seedChannelOrder(t, data.NewChannelOrdersRepo(dp.Db), "w1", "GBP", 100, 100)
	get := func(path string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), auth.User{ID: "m1", Role: "manager"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	list, detail := get("/channel-orders"), get("/channel-orders/"+id)
	if !strings.Contains(list, `class="channel-order-link"`) || !strings.Contains(list, `class="channel-order-scroll"`) {
		t.Error("list: order-number link and scroll wrapper hooks missing")
	}
	if !strings.Contains(detail, `class="channel-order-key"`) || !strings.Contains(detail, `class="channel-order-scroll"`) {
		t.Error("detail: wrapping plugin key and scrolling lines table hooks missing")
	}
	_, thisFile, _, _ := runtime.Caller(0)
	css, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "web", "public", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".channel-order-link", "min-block-size: 44px",
		".channel-order-key", "overflow-wrap: anywhere",
		".channel-order-scroll", "overflow-x: auto",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
}

func TestCloudApplyChannelOrder_CurrencyFlagFollowsTheTillsCurrencySettingOnEveryRevision(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	old := httpx.ActiveCurrency().Code
	t.Cleanup(func() { httpx.InitCurrency(old) })
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',10)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	flag := func() int {
		var m int
		_ = dp.Db.QueryRow(`SELECT currency_mismatch FROM channel_orders`).Scan(&m)
		return m
	}
	o, _ := decodeFixtureForHook(t)
	o.Lines = o.Lines[:1]
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1"
	httpx.InitCurrency("EUR") // the till sells in EUR, the order is GBP
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	if flag() != 1 {
		t.Fatal("GBP order on a EUR till not flagged")
	}
	httpx.InitCurrency("GBP") // the owner changes the till currency
	o.DirectiveID, o.Revision, o.Status = "6f1f8f0e-2b7a-4a37-9d0c-5d1d5f3a0a02", 2, "accepted"
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	if flag() != 0 {
		t.Fatal("the flag was not recomputed on the later revision")
	}
}

func TestCloudApplyChannelOrder_ProblemNamesTheItemWhenTheLineHasNoSKU(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	logging.ResetRecent()
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := decodeFixtureForHook(t)
	o.Lines = o.Lines[:1]
	o.Lines[0].CloudItemID, o.Lines[0].SKU, o.Lines[0].Name = "co-itm", "", "Linen shirt"
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		text = append(text, p.Msg)
	}
	all := strings.Join(text, "\n")
	if !strings.Contains(all, "negative") || !strings.Contains(all, "Linen shirt") || !strings.Contains(all, "co-itm") {
		t.Fatalf("the negative-stock Problem does not name the item:\n%s", all)
	}
	if strings.Contains(all, "for  is now") {
		t.Fatalf("empty SKU in the Problem:\n%s", all)
	}
}

func TestCloudApplyChannelOrder_PublishesStockAdjusted(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	m := &plugins.Manifest{
		ID: "com.example.stock-4768", Name: "Stock 4768", Version: "1.0.0", Entrypoint: "./c",
		Hooks:       []plugins.ManifestHook{{Event: "stock.adjusted", Action: "sync"}},
		Permissions: []string{"events:receive"},
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.GrantPermission(t.Context(), dp.Db, m.ID, "events:receive"); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(dp.Db)
	ch, err := bus.Subscribe(t.Context(), m.ID, []string{"stock.adjusted"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Unsubscribe(m.ID) })
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',10)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := decodeFixtureForHook(t)
	o.Lines = o.Lines[:1]
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1"
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		var got plugins.StockAdjustedEvent
		if err := json.Unmarshal(ev.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if got.ItemID != "co-itm" || got.DeltaQty != -2 || got.Location != "loc_main" {
			t.Fatalf("event = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stock.adjusted event")
	}
}
