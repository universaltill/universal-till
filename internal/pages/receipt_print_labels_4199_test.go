package pages

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4199: the printed sale receipt's labels follow the shop's
// locale (httpx.DefaultLocale, the printed invoice's convention). An en
// shop's receipt stays byte-for-byte what it was; a tr/fa shop's carries
// no English label.

// seedAllLabelsSale records one sale that makes buildReceiptDoc print every
// label it has: an order number, a mixed dine-in/takeaway order, a
// discount, a service charge, cash (with change and a tip), card and
// voucher payments, a VAT-registered seller, an unsigned-TSE notice, TSE
// evidence and a fiscal-device receipt. Plus a refund of it.
func seedAllLabelsSale(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	setSeller(t, dp)
	exec(`INSERT INTO sales(id, receipt_no, display_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, order_type, created_at, completed_at)
VALUES('sale-all', 'R-ALL', 'A12', 'completed', 'sale', 'GBP', 2000, 100, 0, 2000, 100, 'mixed', datetime('now'), datetime('now'))`)
	exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type)
VALUES('sale-all-l1', 'sale-all', 1, 'itm1', 'Apple', 'ABC', 1, 1000, 0, 0, 1000, 1000, 'takeaway'),
      ('sale-all-l2', 'sale-all', 2, 'itm1', 'Pear', 'DEF', 1, 1000, 0, 0, 1000, 1000, 'dine_in')`)
	exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES
('sale-all-p1','sale-all','cash',1000,'GBP',200,150,datetime('now')),
('sale-all-p2','sale-all','card',500,'GBP',0,0,datetime('now')),
('sale-all-p3','sale-all','voucher',500,'GBP',0,0,datetime('now'))`)
	repo := data.NewPOSRepo(dp.Db)
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale-all", "unsigned_override", map[string]any{}, "2026-08-15T10:31:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale-all", "unsigned_fiscal_signing", map[string]any{}, "2026-08-15T10:31:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordFiscalTSESignature(ctx, data.FiscalTSESignature{
		SaleID: "sale-all", TransactionNumber: 4711, SignatureCounter: 12345,
		SerialNumber: "SER1", StartTime: "2026-08-15T10:31:00Z", LogTime: "2026-08-15T10:31:02Z",
		Signature: "SIG==", SignatureAlgorithm: "ecdsa-plain-SHA256",
	}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO fiscal_device_receipts
(sale_id, device_kind, maker, serial, receipt_no, receipt_kind, z_no, issued_at, created_at)
VALUES ('sale-all', 'okc', 'm', 'DEV1', 'DR-1', 'sale', 7, '2026-08-15 10:31', datetime('now'))`)
	seedReceiptSale(t, dp, "ret-all", "R-RET", "return", "sale-all", 120, 0, 0)
	// The third unsigned-TSE notice ("could not be signed as presented")
	// needs a sale of its own: a sale carries one gap kind or the other.
	seedReceiptSale(t, dp, "sale-cs", "R-CS", "sale", "", 120, 0, 0)
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale-cs", "unsigned_fiscal_cannot_sign", map[string]any{}, "2026-08-15T10:31:00Z", ""); err != nil {
		t.Fatal(err)
	}
}

// receiptLabels flattens every printed text field buildReceiptDoc fills
// with a label (amounts excluded), in print order.
func receiptLabels(doc print.Doc) []string {
	var out []string
	out = append(out, doc.Meta...)
	for _, l := range doc.Lines {
		out = append(out, l.Name)
	}
	for _, kv := range doc.Totals {
		out = append(out, kv.Label)
	}
	for _, kv := range doc.Payments {
		out = append(out, kv.Label)
	}
	return out
}

// buildLabelsDocs returns the all-labels sale's labels, then the refund's
// followed by the cannot-sign sale's.
func buildLabelsDocs(t *testing.T, locale string) (sale, others []string) {
	t.Helper()
	_, dp := newPrintAPITestDeps(t)
	seedAllLabelsSale(t, dp)
	pinDefaultLocale(t, locale)
	ctx := context.Background()
	doc, err := buildReceiptDoc(ctx, dp, "R-ALL")
	if err != nil {
		t.Fatal(err)
	}
	ret, err := buildReceiptDoc(ctx, dp, "R-RET")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := buildReceiptDoc(ctx, dp, "R-CS")
	if err != nil {
		t.Fatal(err)
	}
	return receiptLabels(doc), append(receiptLabels(ret), receiptLabels(cs)...)
}

// The en receipt is pinned label-for-label to what it printed before
// ut-docs#4199 (the date line, which varies, is skipped).
func TestReceiptPrintLabels_EnglishUnchanged(t *testing.T) {
	sale, refund := buildLabelsDocs(t, "en")
	wantSale := []string{
		"Order A12",
		"Receipt R-ALL",
		"", // date
		"1 High Street",
		"VAT No: GB123456789",
		"Mixed dine-in / takeaway",
		"Recorded during a documented TSE outage, under a time-limited owner override.",
		"TSE unreachable — this receipt was recorded without a TSE signature and will not be signed later.",
		"Fiscal device receipt no.: DR-1",
		"Fiscal device serial: DEV1",
		"Fiscal device Z no.: 7",
		"Fiscal device issued at: 2026-08-15 10:31",
		"TSE serial: SER1",
		"TSE transaction no.: 4711",
		"TSE signature counter: 12345",
		"TSE transaction start: 2026-08-15T10:31:00Z",
		"TSE transaction end: 2026-08-15T10:31:02Z",
		"TSE signature algorithm: ecdsa-plain-SHA256",
		"TSE signature: SIG==",
		"Apple (takeaway)",
		"Pear (dine in)",
		"Subtotal",
		"Discount",
		"Tax",
		"Service Charge",
		"TOTAL",
		"Total incl. VAT 0.00%",
		"Cash",
		"Change",
		"Tip",
		"Card",
		"Voucher",
	}
	if len(sale) != len(wantSale) {
		t.Fatalf("expected %d labels, got %d: %q", len(wantSale), len(sale), sale)
	}
	for i, want := range wantSale {
		if i == 2 {
			continue
		}
		if sale[i] != want {
			t.Errorf("label %d: got %q, want %q", i, sale[i], want)
		}
	}
	if refund[0] != "*** REFUND ***" || refund[1] != "For receipt R-ALL" || refund[2] != "Receipt R-RET" {
		t.Errorf("refund meta: got %q", refund[:3])
	}
	const cannotSign = "This sale could not be signed as presented — the receipt was recorded without a TSE signature and will not be signed later."
	found := false
	for _, l := range refund {
		if l == cannotSign {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the cannot-sign notice verbatim, got %q", refund)
	}
}

// englishReceiptLiterals are the en words each translated label used to
// print. None may survive on a tr/fa receipt.
var englishReceiptLiterals = []string{
	"Order ", "Receipt ", "VAT No", "Mixed dine-in", "Recorded during", "TSE unreachable",
	"Fiscal device", "TSE serial", "TSE transaction", "TSE signature", "(takeaway)", "(dine in)",
	"Subtotal", "Discount", "Tax", "Service Charge", "TOTAL", "Cash", "Change", "Tip", "Card",
	"Voucher", "REFUND", "For receipt", "This sale could not",
}

func TestReceiptPrintLabels_TranslatedForTurkishAndPersian(t *testing.T) {
	for _, locale := range []string{"tr", "fa"} {
		sale, refund := buildLabelsDocs(t, locale)
		for _, label := range append(sale, refund...) {
			for _, en := range englishReceiptLiterals {
				if strings.Contains(label, en) {
					t.Errorf("%s: label %q still carries the English %q", locale, label, en)
				}
			}
		}
	}
	// Spot-check that the values, not just the labels, still print.
	sale, refund := buildLabelsDocs(t, "tr")
	joined := strings.Join(append(sale, refund...), "\n")
	for _, want := range []string{"A12", "R-ALL", "GB123456789", "DR-1", "DEV1", "SER1", "4711", "12345", "SIG==", "Ara toplam", "Nakit", "Para üstü"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tr: expected %q on the receipt, got %q", want, joined)
		}
	}
}

// A Turkish shop on EUR prints in Windows-1254 (print.DefaultCharset);
// every label must encode there, or the printer shows '?'.
func TestReceiptPrintLabels_TurkishLabelsEncodeInWindows1254(t *testing.T) {
	if cs := print.DefaultCharset("EUR", "tr"); cs != "win1254" {
		t.Fatalf("expected win1254 for a tr shop on EUR, got %q", cs)
	}
	sale, refund := buildLabelsDocs(t, "tr")
	enc := charmap.Windows1254.NewEncoder()
	for _, label := range append(sale, refund...) {
		if _, err := enc.String(label); err != nil {
			t.Errorf("tr label %q does not encode in Windows-1254: %v", label, err)
		}
	}
}

// End to end: the journal reprint (POST /api/print/receipt/{no}) through the
// real mux to a network printer (a local TCP listener). A Turkish shop on EUR
// gets Windows-1254 bytes carrying the Turkish labels, and no English ones.
func TestReceiptPrintLabels_TurkishReprintBytesOnTheWire(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedAllLabelsSale(t, dp)
	pinDefaultLocale(t, "tr")
	ctx := context.Background()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		b, _ := io.ReadAll(conn)
		got <- b
	}()
	for k, v := range map[string]string{
		"store.currency": "EUR", "store.locale": "tr",
		"printer.mode": "network", "printer.address": ln.Addr().String(),
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/print/receipt/R-ALL", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reprint: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var wire []byte
	select {
	case wire = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the printer never received the receipt")
	}
	enc := charmap.Windows1254.NewEncoder()
	for _, want := range []string{"Ara toplam", "TOPLAM", "Para üstü", "Bahşiş", "Nakit", "Sipariş A12", "Fiş R-ALL", "TSE işlem no: 4711"} {
		b, err := enc.String(want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(wire, []byte(b)) {
			t.Errorf("expected %q (Windows-1254) on the wire", want)
		}
	}
	for _, en := range []string{"Subtotal", "TOTAL", "Change", "Cash", "Receipt ", "TSE transaction"} {
		if bytes.Contains(wire, []byte(en)) {
			t.Errorf("found the English %q on the wire", en)
		}
	}
}

// ut-docs#4626: the legacy "gift" id prints its translated name too.
func TestReceiptMethodLabel_GiftTranslated(t *testing.T) {
	if got := receiptMethodLabel("fa", "gift"); got != "کارت هدیه" {
		t.Errorf("receiptMethodLabel(fa, gift) = %q, want the fa tender.gift_card text", got)
	}
	if got := receiptMethodLabel("en", "sumup"); got != "Sumup" {
		t.Errorf("plugin id keeps titleFirst fallback, got %q", got)
	}
}
