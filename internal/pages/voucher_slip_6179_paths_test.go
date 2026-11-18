package pages

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#6179: the four slip build paths carry the shop's text and the
// issued / topped-up date. Fixture dates are far from today (guard-fixed-dates).

const (
	slip6179Issued = "2025-03-01T12:00:00Z"
	slip6179Old    = "2025-01-02T12:00:00Z"
	slip6179TopUp  = "2025-02-10T12:00:00Z"
)

// slip6179Date is the date line a stored timestamp must print, computed
// independently of slipDateLine.
func slip6179Date(t *testing.T, key, stored string) string {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(httpx.T(httpx.DefaultLocale(), key), httpx.FormatDateLatin(ts.Local(), httpx.DefaultLocale()))
}

// seedSlip6179 issues GV-A (Anna) and GV-B in sale1/R001 and tops GV-C
// (issued earlier in sale0) up in the same sale, all with old, distinct dates.
func seedSlip6179(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 3500, 0, 0)
	seedReceiptSale(t, dp, "sale0", "R000", "sale", "", 1500, 0, 0)
	repo := data.NewPOSRepo(dp.Db)
	for _, v := range []data.Voucher{
		{ID: "GV-A", HolderLabel: "Anna", OriginalAmountMinor: 2500, BalanceMinor: 2500, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: slip6179Issued},
		{ID: "GV-B", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: slip6179Issued},
		{ID: "GV-C", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "GBP", IssuedSaleID: "sale0", CreatedAt: slip6179Old},
	} {
		if err := repo.CreateVoucher(ctx, nil, v); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := repo.TopUpVoucher(ctx, tx, "GV-C", "sale1", 500, 1_000_000, false, slip6179TopUp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPrintReceiptIn_VoucherSlipTextAndDates(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlip6179(t, dp)
	dev := slipDevice(t, dp, "true")
	plain := mustReceiptDoc(t, ctx, dp, "R001", "")
	setSettings(t, dp, map[string]string{keyReceiptVoucherSlipText: "Valid in store.\nNo cash refund."})

	n, err := printReceiptIn(ctx, dp, "R001", "")
	if err != nil || n != 3 {
		t.Fatalf("slips = %d, err = %v, want 3", n, err)
	}
	job := readJob(t, dev)
	// The receipt segment is byte-equal to the same sale with no slip text.
	wantReceipt := print.Render(plain)
	if !bytes.HasPrefix(job, wantReceipt) {
		t.Fatal("receipt segment changed by the slip text")
	}
	tail := job[len(wantReceipt):]
	if bytes.Count(wantReceipt, []byte("Valid in store.")) != 0 || bytes.Count(wantReceipt, []byte("Issued ")) != 0 {
		t.Error("receipt carries slip content")
	}
	if c := bytes.Count(tail, []byte("Valid in store.")); c != 3 {
		t.Errorf("slip text on %d slips, want 3", c)
	}
	issuedA := slip6179Date(t, "receipt.vouchers.issued_on", slip6179Issued)
	topped := slip6179Date(t, "receipt.vouchers.topped_up_on", slip6179TopUp)
	if c := bytes.Count(tail, []byte(issuedA)); c != 2 {
		t.Errorf("%q printed %d times, want 2 (GV-A, GV-B)", issuedA, c)
	}
	if c := bytes.Count(tail, []byte(topped)); c != 1 {
		t.Errorf("%q printed %d times, want 1", topped, c)
	}
	oldIssue := slip6179Date(t, "receipt.vouchers.issued_on", slip6179Old)
	if bytes.Contains(tail, []byte(oldIssue)) {
		t.Error("the top-up slip printed the voucher's issue date")
	}
	if bytes.Contains(tail, []byte("Reprinted")) {
		t.Error("a sale slip carries Reprinted")
	}
	if today := httpx.FormatDateLatin(time.Now(), httpx.DefaultLocale()); bytes.Contains(tail, []byte(today)) {
		t.Errorf("today's date %q on a slip of an old sale", today)
	}
}

func TestPrintReceiptIn_UnsetTextAndNoDateAddsNothing(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlip6179(t, dp)
	if _, err := dp.Db.Exec(`UPDATE vouchers SET created_at = 'garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE voucher_transactions SET created_at = ''`); err != nil {
		t.Fatal(err)
	}
	dev := slipDevice(t, dp, "true")
	if n, err := printReceiptIn(ctx, dp, "R001", ""); err != nil || n != 3 {
		t.Fatalf("slips = %d, err %v", n, err)
	}
	job := readJob(t, dev)
	if bytes.Contains(job, []byte("Issued")) || bytes.Contains(job, []byte("Topped up")) {
		t.Error("an unparseable timestamp printed a date line")
	}
	doc := mustReceiptDoc(t, ctx, dp, "R001", "")
	want := print.Render(doc)
	for _, s := range voucherSlips(doc) {
		if s.Date != "" || s.Text != nil {
			t.Errorf("slip carries date/text: %+v", s)
		}
		want = append(want, print.RenderVoucherSlip(s)...)
	}
	if !bytes.Equal(job, want) {
		t.Error("job != receipt + slips")
	}
}

func TestVoucherSlipFor_IssuedAndReprinted(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	setSettings(t, dp, map[string]string{keyReceiptVoucherSlipText: "Valid in store."})
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	v := data.Voucher{ID: "GV-A", OriginalAmountMinor: 2500, Currency: "GBP", Status: "active", IssuedSaleID: "sale1", CreatedAt: slip6179Issued}
	s := voucherSlipFor(ctx, dp, v, printerConfig(ctx, dp), now)
	if want := slip6179Date(t, "receipt.vouchers.issued_on", slip6179Issued); s.Date != want {
		t.Errorf("Date = %q, want %q", s.Date, want)
	}
	if want := fmt.Sprintf(httpx.T("en", "receipt.vouchers.reprinted"), httpx.FormatDateLatin(now.Local(), "en")); s.Note != want {
		t.Errorf("Note = %q, want %q (the reprint date never replaces the issue date)", s.Note, want)
	}
	if len(s.Text) != 1 || s.Text[0] != "Valid in store." {
		t.Errorf("Text = %q", s.Text)
	}
}

func TestVoucherSlipFor_ImportedNoIssuedLine(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	v := data.Voucher{ID: "GV-IMP", OriginalAmountMinor: 2500, Currency: "GBP", Status: "active", CreatedAt: slip6179Issued} // no issuing sale
	s := voucherSlipFor(ctx, dp, v, printerConfig(ctx, dp), time.Now())
	if s.Date != "" {
		t.Errorf("imported voucher printed %q", s.Date)
	}
	if !strings.Contains(s.Note, "Reprinted") {
		t.Errorf("Note = %q, want the Reprinted line kept", s.Note)
	}
}

func TestVoucherSlipFor_BadCreatedAtNoLine(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	for _, bad := range []string{"", "garbage", "0001-01-01T00:00:00Z"} {
		v := data.Voucher{ID: "GV-A", OriginalAmountMinor: 2500, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: bad}
		if s := voucherSlipFor(ctx, dp, v, printerConfig(ctx, dp), time.Now()); s.Date != "" {
			t.Errorf("created_at %q printed %q", bad, s.Date)
		}
	}
	v := data.Voucher{ID: "GV-A", OriginalAmountMinor: 2500, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: "2025-03-01 12:00:00"} // legacy layout
	if s := voucherSlipFor(ctx, dp, v, printerConfig(ctx, dp), time.Now()); !strings.Contains(s.Date, "2025") {
		t.Errorf("legacy layout gave %q", s.Date)
	}
}

func TestReceiptDocEncodable_SlipDate(t *testing.T) {
	doc := print.Doc{Charset: "cp858", Vouchers: []print.VoucherBlock{{Label: "Gift voucher", SlipDate: "Issued \u0627"}}}
	if receiptDocEncodable(doc) {
		t.Error("an unencodable slip date must trigger the shop-language fallback")
	}
	doc.Vouchers[0].SlipDate = "Issued 01/03/2025"
	if !receiptDocEncodable(doc) {
		t.Error("an encodable slip date must not")
	}
}

func TestReceiptBrowserSlips_DateAndText(t *testing.T) {
	vs := []receiptVoucherIssue{
		{Code: "A1", Amount: 2500, At: slip6179Issued},
		{Code: "C3", Amount: 500, TopUp: true, At: slip6179TopUp},
		{Code: "D4", Amount: 700, At: ""},
	}
	got := receiptBrowserSlips("en", "My Store", []string{"H1"}, []string{"Valid in store.", "No cash refund."}, vs)
	if got[0].Date != slip6179Date(t, "receipt.vouchers.issued_on", slip6179Issued) {
		t.Errorf("issued date = %q", got[0].Date)
	}
	if got[1].Date != slip6179Date(t, "receipt.vouchers.topped_up_on", slip6179TopUp) {
		t.Errorf("top-up date = %q", got[1].Date)
	}
	if got[2].Date != "" {
		t.Errorf("no timestamp must give no line, got %q", got[2].Date)
	}
	for i, s := range got {
		if len(s.Text) != 2 || s.Text[1] != "No cash refund." {
			t.Errorf("slip %d text = %q", i, s.Text)
		}
	}
}

// slipsTenderDeps is slipsTender with the shop's slip text set; it returns the
// receipt HTML and the deps for DB reads.
func slipsTenderDeps(t *testing.T, text string) (string, *common.Deps) {
	t.Helper()
	mux, dp := newTopUpTestDeps(t)
	pinDefaultLocale(t, "en")
	topUpSeedVoucher(t, dp, "GS-1", 1000)
	setSettings(t, dp, map[string]string{keyPrinterMode: "off", keyPrinterVoucherSlip: "true", keyReceiptVoucherSlipText: text})
	gv := data.ItemIDCode("itm-gv")
	if rec := gvSubmit(mux, gv, "3000", "7771234567890", "&gv_holder=Sam"); rec.Code != http.StatusOK {
		t.Fatalf("gv1: %d %s", rec.Code, rec.Body.String())
	}
	if rec := vTopUp(t, mux, "GS-1", "25", &vManager, true); rec.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
	}
	rec := cashTender(mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), dp
}

func TestTenderReceipt_BrowserSlipDateMatchesPaper(t *testing.T) {
	body, dp := slipsTenderDeps(t, "Valid <b>in</b> store.\nNo cash refund.")
	i := strings.Index(body, `class="voucher-slips-print"`)
	if i < 0 {
		t.Fatalf("no slips:\n%s", body)
	}
	wrap := body[i:]
	var issuedAt, topAt string
	if err := dp.Db.QueryRow(`SELECT created_at FROM vouchers WHERE id = '7771234567890'`).Scan(&issuedAt); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT created_at FROM voucher_transactions WHERE voucher_id = 'GS-1' AND type = 'top_up'`).Scan(&topAt); err != nil {
		t.Fatal(err)
	}
	wantIssued := slipDateLine("en", "receipt.vouchers.issued_on", issuedAt)
	wantTop := slipDateLine("en", "receipt.vouchers.topped_up_on", topAt)
	if wantIssued == "" || wantTop == "" {
		t.Fatalf("stored timestamps did not parse: %q %q", issuedAt, topAt)
	}
	if n := strings.Count(wrap, `<div class="voucher-slip-date">`+html.EscapeString(wantIssued)+`</div>`); n != 1 {
		t.Errorf("issued date line count = %d, want 1 (%q)", n, wantIssued)
	}
	if n := strings.Count(wrap, `<div class="voucher-slip-date">`+html.EscapeString(wantTop)+`</div>`); n != 1 {
		t.Errorf("top-up date line count = %d, want 1 (%q)", n, wantTop)
	}
	if n := strings.Count(wrap, `<div dir="auto">Valid &lt;b&gt;in&lt;/b&gt; store.</div>`); n != 2 {
		t.Errorf("escaped text lines = %d, want 2 slips", n)
	}
	if strings.Contains(wrap, "<b>in</b>") {
		t.Error("shop text not escaped")
	}
}

func TestVoucherSlipBrowser_ShopTextDirAuto(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	vSeed(t, data.NewPOSRepo(dp.Db), data.Voucher{ID: "GV-A", OriginalAmountMinor: 2500, IssuedSaleID: "sale1", CreatedAt: slip6179Issued})
	setSettings(t, dp, map[string]string{keyPrinterMode: "off", keyReceiptVoucherSlipText: "Valid <b>in</b> store.\n\nمرحبا"})
	body := vPrintSlip(t, mux, "GV-A", &vManager).Body.String()
	for _, want := range []string{`<div dir="auto">Valid &lt;b&gt;in&lt;/b&gt; store.</div>`, `<div dir="auto">مرحبا</div>`, `<div dir="auto"></div>`,
		`<div class="voucher-slip-date">` + html.EscapeString(slipDateLine("en", "receipt.vouchers.issued_on", slip6179Issued)) + `</div>`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	// Order: date, Reprinted note, then the text.
	d, n, x := strings.Index(body, `class="voucher-slip-date"`), strings.Index(body, `class="voucher-slip-note"`), strings.Index(body, `class="voucher-slip-text"`)
	if d < 0 || n < d || x < n {
		t.Errorf("order wrong: date %d note %d text %d", d, n, x)
	}
	pinDefaultLocale(t, "ar")
	if body := vPrintSlip(t, mux, "GV-A", &vManager).Body.String(); !strings.Contains(body, `dir="rtl"`) || !strings.Contains(body, `<div dir="auto">مرحبا</div>`) {
		t.Errorf("ar slip lost rtl or dir=auto:\n%s", body)
	}
}
