package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4214 slice A: the gift voucher item type. Tapping (or scanning) a
// gift_voucher item never adds goods: /api/pos/scan renders the Gift
// voucher popup into #modifier-modal; submitting it adds a voucher basket
// line (qty 1, the entered value, the card number), and checkout turns that
// line into a voucher issue — never a sale line.

func newGiftVoucherPOSDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newPOSTestDeps(t)
	resolver := stubResolver{
		"PLAIN": {SKU: "PLAIN", Name: "Plain Item", Qty: 1, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000},
		"GV":    {SKU: "GV", Name: "Gift voucher", Qty: 1, PriceCents: 2500, ItemID: "itm-gv", TaxRateBP: 700, TaxCodeID: "tax_red", ItemType: "gift_voucher"},
		"GV0":   {SKU: "GV0", Name: "Gift voucher open", Qty: 1, PriceCents: 0, ItemID: "itm-gv0", ItemType: "gift_voucher"},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	engine.SetChargePolicyAsker(&pluginChargePolicyAsker{db: dp.Db})
	dp.Engine = engine
	registerVoucherAPI(mux, dp)
	registerPOSModifiersAPI(mux, dp)
	return mux, dp
}

func gvSubmit(mux *http.ServeMux, code, value, number, extra string) *httptest.ResponseRecorder {
	return posPostForm(mux, "/api/pos/scan", "code="+code+"&gv=1&gv_value_minor="+value+"&gv_code="+number+extra)
}

func assertGiftVoucherPopup(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := rec.Body.String()
	if got := rec.Header().Get("HX-Retarget"); got != "#modifier-modal" {
		t.Fatalf("HX-Retarget = %q, want #modifier-modal; body:\n%s", got, body)
	}
	if got := rec.Header().Get("HX-Trigger-After-Swap"); got != "open-modifier-modal" {
		t.Fatalf("HX-Trigger-After-Swap = %q", got)
	}
	for _, want := range []string{`class="modifier-picker gift-voucher-popup"`, `name="gv_code"`, `name="gv_value_minor"`, `name="gv_holder"`, `name="gv_purpose"`, `value="multi_purpose" checked`, "Leave empty to create a number"} {
		if !strings.Contains(body, want) {
			t.Fatalf("popup missing %q:\n%s", want, body)
		}
	}
	return body
}

func TestScanAPI_GiftVoucherItem_RendersPopup(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	rec := posPostForm(mux, "/api/pos/scan", "code=GV&src=tile")
	body := assertGiftVoucherPopup(t, rec)
	// Prefilled from the item's own price; the card number is focused so a
	// hardware scanner fills it straight away.
	if !strings.Contains(body, `data-prefill-minor="2500"`) {
		t.Fatalf("value must be prefilled from the item price:\n%s", body)
	}
	if !regexp.MustCompile(`name="gv_code"[^>]*autofocus`).MatchString(body) {
		t.Fatalf("the card number field must be the focused one:\n%s", body)
	}
	if !strings.Contains(body, `name="src" value="tile"`) {
		t.Fatalf("src must ride through to the submit:\n%s", body)
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("no line before the popup is submitted, got %d", n)
	}
	// A €0 item opens with an empty value.
	rec = posPostForm(mux, "/api/pos/scan", "code=GV0")
	if body := assertGiftVoucherPopup(t, rec); strings.Contains(body, `data-prefill-minor="0"`) || !strings.Contains(body, `data-prefill-minor=""`) {
		t.Fatalf("a zero-price voucher item must open with no value prefilled:\n%s", body)
	}
}

// Every route into the modifier picker (a tile of an item with modifier
// groups) shows the voucher popup instead for a gift voucher item.
func TestModifierPicker_GiftVoucherItemShowsPopup(t *testing.T) {
	mux, _ := newGiftVoucherPOSDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/pos/modifiers?item=itm-gv&code=GV&src=tile", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `class="modifier-picker gift-voucher-popup"`) {
		t.Fatalf("GET /ui/pos/modifiers for a gift voucher item must render the voucher popup:\n%s", body)
	}
}

func TestScanAPI_GiftVoucherSubmit_AddsVoucherLine(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	rec := gvSubmit(mux, "GV", "3000", "7771234567890", "&gv_holder=Sam&gv_purpose=multi_purpose")
	if rec.Header().Get("HX-Retarget") != "" {
		t.Fatalf("a valid submit renders the basket, not the popup:\n%s", rec.Body.String())
	}
	lines := dp.Engine.Basket().Lines
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	l := lines[0]
	if l.GiftVoucher == nil || l.GiftVoucher.Code != "7771234567890" || l.GiftVoucher.HolderLabel != "Sam" || l.PriceCents.Minor() != 3000 || l.Qty != 1 {
		t.Fatalf("line = %+v voucher=%+v", l, l.GiftVoucher)
	}
	if !strings.Contains(rec.Body.String(), "7771234567890") {
		t.Fatalf("the basket must show the voucher number:\n%s", rec.Body.String())
	}
	// A second tap prompts again — never a qty bump of the first line.
	assertGiftVoucherPopup(t, posPostForm(mux, "/api/pos/scan", "code=GV"))
	if n := len(dp.Engine.Basket().Lines); n != 1 || dp.Engine.Basket().Lines[0].Qty != 1 {
		t.Fatalf("repeat tap changed the basket: %+v", dp.Engine.Basket().Lines)
	}
}

func TestScanAPI_GiftVoucherSubmit_InvalidValueReprompts(t *testing.T) {
	for _, bad := range []string{"", "0", "-5", "abc", "100000001", "1.5"} {
		mux, dp := newGiftVoucherPOSDeps(t)
		rec := gvSubmit(mux, "GV", bad, "123", "")
		body := assertGiftVoucherPopup(t, rec)
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "Enter a value above zero") {
			t.Fatalf("value %q: want the inline value error:\n%s", bad, body)
		}
		if n := len(dp.Engine.Basket().Lines); n != 0 {
			t.Fatalf("value %q: no line may be added, got %d", bad, n)
		}
	}
}

func TestScanAPI_GiftVoucherSubmit_DuplicateNumberRefused(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	// A voucher that already exists in the vouchers table.
	if rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1000}],"issue_vouchers":[{"amount":1000,"code":"4000000000001"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed voucher: %d %s", rec.Code, rec.Body.String())
	}
	rec := gvSubmit(mux, "GV", "2500", "4000000000001", "")
	body := assertGiftVoucherPopup(t, rec)
	if !strings.Contains(body, "already in use") {
		t.Fatalf("an existing voucher number must be refused inline:\n%s", body)
	}
	if !strings.Contains(body, `value="4000000000001"`) {
		t.Fatalf("the refused number stays in the field so the cashier sees it:\n%s", body)
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("no line for a refused number, got %d", n)
	}
	// Already on another voucher line of this sale.
	gvSubmit(mux, "GV", "2500", "5550001", "")
	body = assertGiftVoucherPopup(t, gvSubmit(mux, "GV", "1000", "5550001", ""))
	if !strings.Contains(body, "already in this sale") {
		t.Fatalf("a number already in the basket must be refused inline:\n%s", body)
	}
	if n := len(dp.Engine.Basket().Lines); n != 1 {
		t.Fatalf("want 1 line, got %d", n)
	}
}

var ean13RE = regexp.MustCompile(`^[0-9]{13}$`)

func ean13CheckOK(code string) bool {
	sum := 0
	for i := 0; i < 12; i++ {
		d := int(code[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return (10-sum%10)%10 == int(code[12]-'0')
}

func TestScanAPI_GiftVoucherSubmit_BlankNumberGenerated(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	gvSubmit(mux, "GV", "2500", "", "")
	gvSubmit(mux, "GV", "2500", "", "")
	lines := dp.Engine.Basket().Lines
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	a, b := lines[0].GiftVoucher.Code, lines[1].GiftVoucher.Code
	for _, c := range []string{a, b} {
		if !ean13RE.MatchString(c) || !ean13CheckOK(c) {
			t.Fatalf("generated number %q must be a 13-digit EAN-13", c)
		}
	}
	if a == b {
		t.Fatalf("generated numbers must be unique, got %q twice", a)
	}
}

func TestTender_GiftVoucherOnlySaleIssuesVoucher(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	gvSubmit(mux, "GV", "2500", "7771234567890", "&gv_holder=Sam")
	// #1984's empty-basket guard is untouched: the voucher line makes the
	// basket non-empty, so a plain cash tender (no issue_vouchers field)
	// completes the sale.
	rec := cashTender(mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("voucher-only tender: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Vouchers issued") || !strings.Contains(rec.Body.String(), "7771234567890") {
		t.Fatalf("receipt must list the issued voucher:\n%s", rec.Body.String())
	}
	var balance int64
	var holder, purpose string
	if err := dp.Db.QueryRow(`SELECT balance, holder_label, purpose FROM vouchers WHERE id = '7771234567890'`).Scan(&balance, &holder, &purpose); err != nil {
		t.Fatalf("voucher row: %v", err)
	}
	if balance != 2500 || holder != "Sam" || purpose != data.VoucherPurposeMulti {
		t.Fatalf("voucher = %d/%q/%q", balance, holder, purpose)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sale_lines`); n != 0 {
		t.Fatalf("a voucher is never a sale line (product revenue), got %d sale_lines", n)
	}
	var subtotal, tax, total int64
	if err := dp.Db.QueryRow(`SELECT subtotal, tax_total, total FROM sales`).Scan(&subtotal, &tax, &total); err != nil {
		t.Fatal(err)
	}
	if subtotal != 0 || tax != 0 || total != 2500 {
		t.Fatalf("sale figures %d/%d/%d, want 0/0/2500", subtotal, tax, total)
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("basket must be cleared after the sale, got %d lines", n)
	}
	// Z report: GUTSCHEINE counts the issue.
	var day string
	if err := dp.Db.QueryRow(`SELECT date(created_at, 'localtime') FROM sales`).Scan(&day); err != nil {
		t.Fatal(err)
	}
	rep, err := data.NewPOSRepo(dp.Db).EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.VouchersIssuedCount != 1 || rep.VouchersIssued != 2500 {
		t.Fatalf("GUTSCHEINE issued = %d (count %d), want 2500 (1)", rep.VouchersIssued, rep.VouchersIssuedCount)
	}
}

// The empty-basket guard still refuses a truly empty payment.
func TestTender_EmptyBasketStillRefused(t *testing.T) {
	mux, _ := newGiftVoucherPOSDeps(t)
	if rec := cashTender(mux); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty basket tender = %d, want 400", rec.Code)
	}
}

func TestTender_GiftVoucherPlusGoodsAndDiscount(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatal(err)
	}
	dp.Engine.SetDiscountPercent(5000) // a 50% promotion
	goodsOnly := dp.Engine.Basket()
	gvSubmit(mux, "GV", "2500", "VC-GOODS-1", "")
	b := dp.Engine.Basket()
	// The promotion applies to the goods only (PLAIN 2.00 → 1.00 off);
	// the 25.00 voucher is added on top, untouched.
	if b.Discount.Minor() != 100 || b.Total != goodsOnly.Total.Add(money.FromMinor(2500)) {
		t.Fatalf("basket discount %d total %d, want 100 / %d", b.Discount.Minor(), b.Total.Minor(), goodsOnly.Total.Minor()+2500)
	}
	rec := cashTender(mux)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `toast-message" class="notice error`) {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sale_lines WHERE item_id = 'itm-plain2'`); n != 1 {
		t.Fatalf("goods line: %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sale_lines WHERE item_id = 'itm-gv'`); n != 0 {
		t.Fatalf("the voucher must not be a sale line, got %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM vouchers WHERE id = 'VC-GOODS-1' AND balance = 2500`); n != 1 {
		t.Fatalf("voucher issued at full face value: %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM stock_movements WHERE item_id = 'itm-gv'`); n != 0 {
		t.Fatalf("no stock movement for a voucher, got %d", n)
	}
}

// "Specific item": the #1037 single-purpose issue, taxed at the item's rate.
func TestTender_GiftVoucherSinglePurposeTaxedAtItemRate(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	gvSubmit(mux, "GV", "1000", "VC-SP-1", "&gv_purpose=single_purpose")
	if rec := cashTender(mux); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var purpose string
	var rate int64
	if err := dp.Db.QueryRow(`SELECT purpose, issue_vat_rate_bp FROM vouchers WHERE id = 'VC-SP-1'`).Scan(&purpose, &rate); err != nil {
		t.Fatal(err)
	}
	if purpose != data.VoucherPurposeSingle || rate != 700 {
		t.Fatalf("voucher purpose %q rate %d, want single_purpose / 700", purpose, rate)
	}
	var total, tax int64
	if err := dp.Db.QueryRow(`SELECT total, tax_total FROM sales`).Scan(&total, &tax); err != nil {
		t.Fatal(err)
	}
	if total != 1070 || tax != 70 {
		t.Fatalf("exclusive 10.00 at 7%%: total %d tax %d, want 1070/70", total, tax)
	}
}

// Removing the voucher line removes the voucher — no void/comp/waste
// reason (nothing left the shop) and no shrinkage row.
func TestRemove_GiftVoucherLineNeedsNoReason(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	gvSubmit(mux, "GV", "2500", "VC-RM-1", "")
	key := dp.Engine.Basket().Lines[0].LineKey
	rec := posPostForm(mux, "/api/pos/remove", "key="+key)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("voucher line not removed: %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM shrinkage_events`); n != 0 {
		t.Fatalf("removing a voucher line is not shrinkage, got %d events", n)
	}
}

// A hold/resume keeps the voucher; paying the resumed sale issues it.
func TestHoldResume_GiftVoucherLineIssuesOnPayment(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	gvSubmit(mux, "GV", "2500", "VC-HOLD-1", "&gv_holder=Sam")
	raw, err := json.Marshal(dp.Engine.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	dp.Engine.Reset()
	var snap pos.BasketSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{})
	if rec := cashTender(mux); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM vouchers WHERE id = 'VC-HOLD-1' AND holder_label = 'Sam' AND balance = 2500`); n != 1 {
		t.Fatalf("resumed voucher not issued: %d", n)
	}
}

// A voucher number scanned on the sale screen that is not an item (a
// pre-printed card or a generated number — no "GS-" prefix) is offered as
// payment, the same as a GS- code (ut-docs#1833).
func TestScanAPI_PlainVoucherNumberOfferedAsPayment(t *testing.T) {
	mux, dp := newGiftVoucherPOSDeps(t)
	if rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1000}],"issue_vouchers":[{"amount":1000,"code":"9912345678901"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed voucher: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatal(err)
	}
	rec := posPostForm(mux, "/api/pos/scan", "code=9912345678901")
	b := dp.Engine.Basket()
	if b.VoucherID != "9912345678901" || b.VoucherBalance.Minor() != 1000 {
		t.Fatalf("pending voucher = %q / %d, want the scanned voucher with 1000:\n%s", b.VoucherID, b.VoucherBalance.Minor(), rec.Body.String())
	}
}
