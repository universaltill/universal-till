package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// The Online orders page (ADR-0169 §9, ut-docs#4768): read-only, Reports-gated,
// customer country only, no action buttons until the action payload exists.
func TestChannelOrdersPage_ListAndDetailRender(t *testing.T) {
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerChannelOrdersPage(mux, dp)
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
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1"
	o.Status = "shipped"
	res, err := data.NewChannelOrdersRepo(dp.Db).Apply(t.Context(), func() data.ChannelOrderInput { i := o.ChannelOrderInput; i.LocationID = "loc_main"; return i }())
	if err != nil {
		t.Fatal(err)
	}
	mgr := auth.User{ID: "m1", Role: "manager"}
	get := func(path string, u auth.User) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	list := get("/channel-orders", mgr)
	if list.Code != 200 || !strings.Contains(list.Body.String(), "#1001") || !strings.Contains(list.Body.String(), "Shipped") ||
		!strings.Contains(list.Body.String(), money.FromMinor(7599).Format()) {
		t.Fatalf("list %d:\n%.1500s", list.Code, list.Body.String())
	}
	detail := get("/channel-orders/"+res.OrderID, mgr)
	body := detail.Body.String()
	// The hook fixture names its first line "Shirt".
	if detail.Code != 200 || !strings.Contains(body, "Shirt") {
		t.Fatalf("detail %d:\n%.1500s", detail.Code, body)
	}
	for _, want := range []string{"Gift wrap", "Not matched to an item", "GB", "my.universaltill.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	// Read-only: no form, no action button for accept/pick/pack/ship.
	for _, bad := range []string{"<form", "Accept</", "Ship</", "<button type=\"submit\""} {
		if strings.Contains(body, bad) {
			t.Errorf("detail must be read-only, found %q", bad)
		}
	}
	// A cashier gets 403 on both and the launcher hides the tile.
	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, p := range []string{"/channel-orders", "/channel-orders/" + res.OrderID} {
		if rec := get(p, cashier); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "#1001") {
			t.Errorf("cashier on %s = %d", p, rec.Code)
		}
	}
}
