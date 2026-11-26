package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#6755 design tests 17 (X report keeps the own-terminal tender on its
// own labelled line) and 18 (refund of an external sale) — Tester.

func TestExternalTender_XReportSeparateLineFromCard(t *testing.T) {
	f := xpFixture(t)
	f.sale(t, "sc", "R-0003", "sale", "reg1", 1111, 0, 3*time.Minute)
	f.pay(t, "pc", "sc", "card", 1111)
	f.sale(t, "se", "R-0004", "sale", "reg1", 2222, 0, 4*time.Minute)
	f.pay(t, "pe", "se", "card_external", 2222)

	rep, err := data.NewPOSRepo(f.dp.Db).ShiftXReport(t.Context(), "sh1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]data.XMethodLine{}
	for _, m := range rep.Methods {
		byID[m.MethodID] = m
	}
	if byID["card"].In.Minor() != 1111 || byID["card_external"].In.Minor() != 2222 {
		t.Fatalf("card and card_external must be separate lines: %+v", rep.Methods)
	}

	rec := f.post(t, "/shifts/x-report", url.Values{"register": {"reg1"}}, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	money := httpx.FuncsFor("en")["money"].(func(any) string)
	body := rec.Body.String()
	// Each label sits in its own row with its own amount.
	extRow := regexp.MustCompile(`<td>Card \(own terminal\)</td><td>` + regexp.QuoteMeta(money(byID["card_external"].In)) + `</td>`)
	cardRow := regexp.MustCompile(`<td>Card</td><td>` + regexp.QuoteMeta(money(byID["card"].In)) + `</td>`)
	if !extRow.MatchString(body) || !cardRow.MatchString(body) {
		t.Fatalf("X fragment lacks separate Card / Card (own terminal) rows:\n%s", body)
	}

	// Printed X uses the locale key, not the raw id.
	p := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, f.dp, p.addr)
	if rec := f.post(t, xpPrintPath, url.Values{}, nil, nil); rec.Code != 200 {
		t.Fatalf("print %d %s", rec.Code, rec.Body.String())
	}
	jobs := p.Settle(1)
	if len(jobs) != 1 || !strings.Contains(jobs[0], "Card (own terminal)") || strings.Contains(jobs[0], "card_external") || strings.Contains(jobs[0], "Card_external") {
		t.Fatalf("printed X label wrong:\n%v", jobs)
	}
}

// The Z/EOD print and the reports/journal tenderLabel both go through
// PaymentMethodKey; pin the rendered English for both paths.
func TestExternalTender_ZAndJournalLabel(t *testing.T) {
	if got := receiptMethodLabel("en", "card_external"); got != "Card (own terminal)" {
		t.Fatalf("Z/receipt label = %q", got)
	}
	tl := httpx.FuncsFor("en")["tenderLabel"].(func(string) string)
	if got := tl("card_external"); got != "Card (own terminal)" {
		t.Fatalf("tenderLabel = %q", got)
	}
}

func TestExternalTender_RefundRecordedOnCardExternal(t *testing.T) {
	mux, dp := readerRig(t)
	registerRefund(mux, dp)
	ext := addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	scan(t, dp)
	if rec := postExtTenderForm(t, mux, extForm(ext.ID, "AB12CD", "4242")); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d %s", rec.Code, rec.Body.String())
	}
	var receipt string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type != 'return'`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receipt+"&qty_0=1&method=card_external"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("refund = %d: %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales WHERE sale_type='return' AND status='completed'`); n != 1 {
		t.Fatalf("completed returns = %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments p JOIN sales s ON s.id=p.sale_id WHERE s.sale_type='return' AND p.method_id='card_external'`); n != 1 {
		t.Fatalf("refund payment on card_external = %d", n)
	}
	// No payment plugin owns card_external: no provider leg row needs attention.
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments p JOIN sales s ON s.id=p.sale_id WHERE s.sale_type='return' AND p.method_id<>'card_external'`); n != 0 {
		t.Fatalf("stray refund legs = %d", n)
	}
	// Refund page offers card_external for this sale.
	g := httptest.NewRecorder()
	mux.ServeHTTP(g, httptest.NewRequest(http.MethodGet, "/refund/"+url.PathEscape(receipt), nil))
	if g.Code != 200 {
		t.Fatalf("GET refund page = %d", g.Code)
	}
	if !strings.Contains(g.Body.String(), `value="card_external"`) {
		t.Errorf("refund page does not offer card_external")
	}
}
