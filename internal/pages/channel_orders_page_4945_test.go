package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4945: the order page shows the discount each line carries and the
// sums, so Items - line discounts + shipping - order discount + tax = Total.
func channelOrder4945Page(t *testing.T, id string, mut func(*data.ChannelOrderInput)) string {
	t.Helper()
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerChannelOrdersPage(mux, dp)
	o, _ := decodeFixtureForHook(t)
	in := o.ChannelOrderInput
	in.LocationID = "loc_main"
	in.DirectiveID, in.ExternalOrderID, in.OrderNumber = "d-"+id, "ext-"+id, "#"+id
	in.Lines = nil
	mut(&in)
	var sum money.Money
	for _, l := range in.Lines {
		sum = sum.Add(l.UnitPrice.Scale(l.Qty)).Sub(l.Discount)
	}
	if sum.Add(in.Shipping).Sub(in.OrderDiscount).Add(in.TaxAdded) != in.Total {
		t.Fatalf("fixture does not add up: %v", sum)
	}
	res, err := data.NewChannelOrdersRepo(dp.Db).Apply(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/channel-orders/"+res.OrderID, nil), auth.User{ID: "m1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("detail %d", rec.Code)
	}
	return rec.Body.String()
}

func lines4945(l ...[3]int64) []data.ChannelOrderLineInput {
	names := []string{"Shirt", "Mug", "Gift wrap"}
	var out []data.ChannelOrderLineInput
	for i, v := range l {
		out = append(out, data.ChannelOrderLineInput{LineID: "l-" + names[i], Name: names[i], Qty: v[0],
			UnitPrice: money.FromMinor(v[1]), Discount: money.FromMinor(v[2]), TaxBP: 2000, TaxIncluded: true})
	}
	return out
}

func TestChannelOrderDetail_ShowsLineDiscounts(t *testing.T) {
	want := func(m int64) string { return httpx.FormatMoneyInDisplay(m, "GBP", "en") }
	body := channelOrder4945Page(t, "4945a", func(in *data.ChannelOrderInput) {
		in.Lines = lines4945([3]int64{2, 2500, 500}, [3]int64{1, 1000, 100}, [3]int64{1, 200, 0})
		in.Shipping, in.OrderDiscount, in.TaxAdded, in.Total = money.FromMinor(490), 0, 0, money.FromMinor(6090)
	})
	segs := strings.Split(body, `data-testid="channel-order-line"`)
	if len(segs) != 4 {
		t.Fatalf("want 3 lines, got %d", len(segs)-1)
	}
	cell := `data-testid="channel-order-line-discount">`
	for _, c := range []struct{ seg, want string }{
		{segs[1], cell + want(500) + `</td>`}, {segs[2], cell + want(100) + `</td>`}, {segs[3], cell + `</td>`},
	} {
		if !strings.Contains(c.seg, c.want) {
			t.Errorf("segment lacks %q:\n%.600s", c.want, c.seg)
		}
	}
	for _, w := range []string{
		`data-testid="channel-order-line-discounts">` + want(600),
		`data-testid="channel-order-items-subtotal">` + want(6200),
		">Line discount</th>", "Line discounts</dt>", "Items</dt>", want(6090),
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q", w)
		}
	}
	if strings.Contains(body, "Order discount") {
		t.Error("Order discount row shown with none")
	}
	if strings.Index(body, "Items</dt>") > strings.Index(body, "Shipping</dt>") {
		t.Error("Items must come first in the totals")
	}
}

func TestChannelOrderDetail_OrderDiscountOnly(t *testing.T) {
	want := func(m int64) string { return httpx.FormatMoneyInDisplay(m, "GBP", "en") }
	body := channelOrder4945Page(t, "4945b", func(in *data.ChannelOrderInput) {
		in.Lines = lines4945([3]int64{2, 2500, 0}, [3]int64{1, 200, 0})
		in.Shipping, in.OrderDiscount, in.TaxAdded, in.Total = money.FromMinor(490), money.FromMinor(300), 0, money.FromMinor(5390)
	})
	for _, w := range []string{"Order discount</dt>", want(300), "Items</dt>", want(5200)} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q", w)
		}
	}
	if strings.Contains(body, "Line discounts") || strings.Contains(body, `data-testid="channel-order-line-discounts"`) {
		t.Error("Line discounts row shown with none")
	}
}

func TestChannelOrderDetail_NoDiscounts(t *testing.T) {
	body := channelOrder4945Page(t, "4945c", func(in *data.ChannelOrderInput) {
		in.Lines = lines4945([3]int64{2, 2500, 0}, [3]int64{1, 200, 0})
		in.Shipping, in.OrderDiscount, in.TaxAdded, in.Total = money.FromMinor(490), 0, 0, money.FromMinor(5690)
	})
	for _, w := range []string{"Line discounts", "Order discount", "Items</dt>", "channel-order-items-subtotal"} {
		if strings.Contains(body, w) {
			t.Errorf("undiscounted order shows %q", w)
		}
	}
	if strings.Count(body, `data-testid="channel-order-line-discount"></td>`) != 2 {
		t.Error("every discount cell should be empty")
	}
}
