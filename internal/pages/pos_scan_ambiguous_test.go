package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#4344: /api/pos/scan used to answer the first kind that matched
// (item, voucher, promo, receipt, ...) in a fixed order, so a code that is
// legitimately two things silently became whichever came first. Now every
// kind is looked up; one answer acts as before, two or more show a chooser
// and nothing happens until the cashier picks a kind.

const ambiguousCode = "4006381333931"

func postScanKind(t *testing.T, mux *http.ServeMux, code, src, kind string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"code": {code}, "qty": {"1"}}
	if src != "" {
		form.Set("src", src)
	}
	if kind != "" {
		form.Set("kind", kind)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan %q (src=%q kind=%q): want 200, got %d: %s", code, src, kind, rec.Code, rec.Body.String())
	}
	return rec
}

func seedAmbiguousItem(t *testing.T, d *db.DB, code string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-amb', ?, 'Notebook A5', 350, 1)`, code); err != nil {
		t.Fatal(err)
	}
}

func seedAmbiguousVoucher(t *testing.T, d *db.DB, code string) {
	t.Helper()
	if err := data.NewPOSRepo(d.DB).CreateVoucher(t.Context(), nil, data.Voucher{
		ID: code, OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
}

// chooserKinds returns the data-kind values of the chooser's rows, in order.
func chooserKinds(body string) []string {
	i := strings.Index(body, `class="scan-choices"`)
	if i < 0 {
		return nil
	}
	var kinds []string
	rest := body[i:]
	for {
		j := strings.Index(rest, `data-scan-kind="`)
		if j < 0 {
			return kinds
		}
		rest = rest[j+len(`data-scan-kind="`):]
		kinds = append(kinds, rest[:strings.Index(rest, `"`)])
	}
}

func equalKinds(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestScanAmbiguous_ItemAndVoucherShowsChooser(t *testing.T) {
	for _, src := range []string{"", "scanner", "camera"} {
		t.Run("src="+src, func(t *testing.T) {
			mux, dp, d := setupScanBarcodeDeps(t)
			seedAmbiguousItem(t, d, ambiguousCode)
			seedAmbiguousVoucher(t, d, ambiguousCode)

			rec := postScanKind(t, mux, ambiguousCode, src, "")
			if got := chooserKinds(rec.Body.String()); !equalKinds(got, []string{"item", "voucher"}) {
				t.Fatalf("want chooser rows [item voucher], got %v\n%s", got, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "Notebook A5") {
				t.Errorf("the item row must name the item")
			}
			b := dp.Engine.Basket()
			if len(b.Lines) != 0 || b.VoucherID != "" {
				t.Fatalf("nothing may be added or offered before the cashier picks; lines=%+v voucher=%q", b.Lines, b.VoucherID)
			}
		})
	}
}

func TestScanAmbiguous_PickingAKindActsOnlyOnIt(t *testing.T) {
	t.Run("item", func(t *testing.T) {
		mux, dp, d := setupScanBarcodeDeps(t)
		seedAmbiguousItem(t, d, ambiguousCode)
		seedAmbiguousVoucher(t, d, ambiguousCode)
		rec := postScanKind(t, mux, ambiguousCode, "scanner", "item")
		if kinds := chooserKinds(rec.Body.String()); kinds != nil {
			t.Fatalf("a picked kind must not ask again, got %v", kinds)
		}
		b := dp.Engine.Basket()
		if len(b.Lines) != 1 || b.Lines[0].ItemID != "itm-amb" || b.VoucherID != "" {
			t.Fatalf("kind=item must add the item only; lines=%+v voucher=%q", b.Lines, b.VoucherID)
		}
	})
	t.Run("voucher", func(t *testing.T) {
		mux, dp, d := setupScanBarcodeDeps(t)
		seedAmbiguousItem(t, d, ambiguousCode)
		seedAmbiguousVoucher(t, d, ambiguousCode)
		postScanKind(t, mux, ambiguousCode, "scanner", "voucher")
		b := dp.Engine.Basket()
		if len(b.Lines) != 0 || b.VoucherID != ambiguousCode {
			t.Fatalf("kind=voucher must offer the voucher only; lines=%+v voucher=%q", b.Lines, b.VoucherID)
		}
	})
}

func TestScanAmbiguous_ItemAndPromo(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	seedAmbiguousItem(t, d, "SAVE5")
	if _, err := d.Exec(`INSERT INTO promotions(code, type, value, is_active) VALUES('SAVE5', 'amount', 500, 1)`); err != nil {
		t.Fatal(err)
	}
	rec := postScanKind(t, mux, "SAVE5", "", "")
	if got := chooserKinds(rec.Body.String()); !equalKinds(got, []string{"item", "promo"}) {
		t.Fatalf("want chooser rows [item promo], got %v", got)
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 || !b.Discount.IsZero() {
		t.Fatalf("nothing may happen before the pick; lines=%+v discount=%v", b.Lines, b.Discount)
	}
	postScanKind(t, mux, "SAVE5", "", "promo")
	if b := dp.Engine.Basket(); len(b.Lines) != 0 || b.Discount.IsZero() {
		t.Fatalf("kind=promo must apply the promo only; lines=%+v discount=%v", b.Lines, b.Discount)
	}
}

func TestScanAmbiguous_ItemAndReceipt(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	seedAmbiguousItem(t, d, "R-1001")
	if _, err := d.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-amb','R-1001','completed','sale','EUR',350,0,0,350,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postScanKind(t, mux, "R-1001", "scanner", "")
	if got := chooserKinds(rec.Body.String()); !equalKinds(got, []string{"item", "receipt"}) {
		t.Fatalf("want chooser rows [item receipt], got %v", got)
	}
	if rec.Header().Get("HX-Redirect") != "" || len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("an ambiguous code must neither redirect nor add")
	}
	rec = postScanKind(t, mux, "R-1001", "scanner", "receipt")
	if got := rec.Header().Get("HX-Redirect"); got != "/refund/R-1001" {
		t.Fatalf("kind=receipt must go to the refund page, got HX-Redirect=%q", got)
	}
}

func TestScanAmbiguous_CustomerCodeThatIsAlsoAnItem(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	seedAmbiguousItem(t, d, "CUST-77")
	if _, err := d.Exec(`INSERT INTO customers (id, name) VALUES ('CUST-77', 'Ann Example')`); err != nil {
		t.Fatal(err)
	}
	rec := postScanKind(t, mux, "CUST-77", "", "")
	if got := chooserKinds(rec.Body.String()); !equalKinds(got, []string{"item", "customer"}) {
		t.Fatalf("want chooser rows [item customer], got %v", got)
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 || b.CustomerID != "" {
		t.Fatalf("nothing may happen before the pick; lines=%+v customer=%q", b.Lines, b.CustomerID)
	}
	postScanKind(t, mux, "CUST-77", "", "customer")
	if b := dp.Engine.Basket(); len(b.Lines) != 0 || b.CustomerID != "CUST-77" {
		t.Fatalf("kind=customer must link the customer only; lines=%+v customer=%q", b.Lines, b.CustomerID)
	}
}

// A tile, a suggestion chip and the identify pick always mean the item.
func TestScanAmbiguous_TileNeverAsks(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	seedAmbiguousItem(t, d, ambiguousCode)
	seedAmbiguousVoucher(t, d, ambiguousCode)
	rec := postScanKind(t, mux, ambiguousCode, "tile", "")
	if kinds := chooserKinds(rec.Body.String()); kinds != nil {
		t.Fatalf("a tile tap must never ask, got %v", kinds)
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 || b.VoucherID != "" {
		t.Fatalf("a tile tap adds its item; lines=%+v voucher=%q", b.Lines, b.VoucherID)
	}
}

// A repeat scan of an ambiguous code already in the basket still asks: the
// first pick is not remembered (design note 4 on the card).
func TestScanAmbiguous_RepeatScanStillAsks(t *testing.T) {
	mux, dp, d := setupScanBarcodeDeps(t)
	seedAmbiguousItem(t, d, ambiguousCode)
	seedAmbiguousVoucher(t, d, ambiguousCode)
	postScanKind(t, mux, ambiguousCode, "scanner", "item")
	rec := postScanKind(t, mux, ambiguousCode, "scanner", "")
	if got := chooserKinds(rec.Body.String()); !equalKinds(got, []string{"item", "voucher"}) {
		t.Fatalf("a repeat scan of an ambiguous code must ask again, got %v", got)
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 1 || b.Lines[0].Qty != 1 {
		t.Fatalf("the repeat scan must not add before the pick; lines=%+v", b.Lines)
	}
}
