package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4705: a self-order customer's language pick (ut-docs#4200) reaches
// the receipt they take away — printed at the kiosk for a card sale, or by
// the cashier for a pay-at-counter order — while every other print stays in
// the shop's language.

// captureReceiptPrints swaps printReceiptFn for a recorder of the locale
// each auto-print asked for, keyed by receipt number.
func captureReceiptPrints(t *testing.T) func() map[string]string {
	t.Helper()
	var mu sync.Mutex
	got := map[string]string{}
	restore := printReceiptFn
	t.Cleanup(func() { printReceiptFn = restore })
	printReceiptFn = func(_ context.Context, _ *common.Deps, receiptNo, customerLocale string) error {
		mu.Lock()
		defer mu.Unlock()
		got[receiptNo] = customerLocale
		return nil
	}
	return func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(got))
		for k, v := range got {
			out[k] = v
		}
		return out
	}
}

// enableAutoPrint configures a receipt printer, so printReceiptAsync runs
// printReceiptFn (receipt policy defaults to always).
func enableAutoPrint(t *testing.T, dp *common.Deps) {
	t.Helper()
	if err := dp.Settings.SetMany(context.Background(), map[string]string{
		keyPrinterMode:   "device",
		keyPrinterDevice: filepath.Join(t.TempDir(), "receipt-printer"),
	}); err != nil {
		t.Fatalf("seed printer settings: %v", err)
	}
}

func TestSelfOrderCheckout_ReceiptPrintsInCustomersPick(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	enableAutoPrint(t, dp)
	prints := captureReceiptPrints(t)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// A customer who picked Turkish.
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order/language?code=tr")
	g.post("/api/self-order/scan", "code=5000001")
	if rec := g.post("/api/self-order/checkout", "method=card"); rec.Code != http.StatusOK {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body.String())
	}
	// The next customer keeps the shop's language.
	g2 := newSelfOrderGuest(t, mux)
	g2.get("/self-order")
	g2.post("/api/self-order/scan", "code=5000001")
	if rec := g2.post("/api/self-order/checkout", "method=card"); rec.Code != http.StatusOK {
		t.Fatalf("second checkout = %d: %s", rec.Code, rec.Body.String())
	}
	dp.WaitForAsyncWork()

	rows, err := d.DB.Query(`SELECT receipt_no FROM sales WHERE status = 'completed' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, no)
	}
	_ = rows.Close()
	if len(receipts) != 2 {
		t.Fatalf("want 2 kiosk sales, got %v", receipts)
	}
	got := prints()
	if loc, ok := got[receipts[0]]; !ok || loc != "tr" {
		t.Fatalf("first customer's receipt printed in %q (printed=%v), want tr", loc, ok)
	}
	if loc, ok := got[receipts[1]]; !ok || loc != "" {
		t.Fatalf("second customer's receipt printed in %q (printed=%v), want the shop's (\"\")", loc, ok)
	}
	if dp.KioskEngine.CustomerLocale() != "" {
		t.Fatalf("the walk-up basket kept a pick after checkout: %q", dp.KioskEngine.CustomerLocale())
	}
}

func TestSelfOrderCounterCheckout_HeldOrderCarriesPickToTender(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeCounter
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	g := newSelfOrderGuest(t, mux)
	g.get("/self-order/language?code=tr")
	g.post("/api/self-order/scan", "code=5000001")
	if rec := g.post("/api/self-order/checkout", ""); rec.Code != http.StatusOK {
		t.Fatalf("counter checkout = %d: %s", rec.Code, rec.Body.String())
	}
	var payload string
	if err := d.DB.QueryRow(`SELECT payload FROM held_sales`).Scan(&payload); err != nil {
		t.Fatalf("held order: %v", err)
	}
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.CustomerLocale != "tr" {
		t.Fatalf("held payload customer_locale = %q, want tr: %s", snap.CustomerLocale, payload)
	}

	// The cashier resumes it: the till's own basket now carries the pick,
	// which completeTender hands to the receipt print.
	eng := dp.Engine
	eng.RestoreHeld(snap, pos.HeldOrigin{})
	if eng.CustomerLocale() != "tr" {
		t.Fatalf("resumed basket CustomerLocale = %q, want tr", eng.CustomerLocale())
	}
}

func TestCustomerReceiptDoc_Language(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "s-4705", "R-4705", "sale", "", 120, 0, 0)
	pinDefaultLocale(t, "en")
	ctx := context.Background()
	receiptLine := func(locale string) string {
		t.Helper()
		doc, err := customerReceiptDoc(ctx, dp, "R-4705", locale)
		if err != nil {
			t.Fatal(err)
		}
		return doc.Meta[0]
	}
	en := receiptLine("")
	if !strings.Contains(en, "R-4705") {
		t.Fatalf("fixture: meta[0] = %q", en)
	}
	tr := receiptLine("tr")
	if tr == en {
		t.Fatalf("fixture: tr must translate receipt.paper.receipt_no (both %q)", tr)
	}
	if got := receiptLine("xx-not-installed"); got != en {
		t.Fatalf("an uninstalled locale printed %q, want the shop's %q", got, en)
	}
	fa := receiptLine("fa")
	if fa == en {
		t.Fatalf("fixture: fa must translate receipt.paper.receipt_no")
	}

	// A Windows-1254 printer renders Turkish but not Persian: Persian falls
	// back to the shop's language rather than a line of '?'.
	if err := dp.Settings.Set(ctx, keyPrinterCharset, "win1254"); err != nil {
		t.Fatal(err)
	}
	if got := receiptLine("tr"); got != tr {
		t.Fatalf("win1254 + tr printed %q, want %q", got, tr)
	}
	if got := receiptLine("fa"); got != en {
		t.Fatalf("win1254 + fa printed %q, want the shop's %q", got, en)
	}
	// Text that is the same in every language and doesn't encode (a store
	// name the printer can't print) is no reason to drop the pick: the
	// shop's own receipt wouldn't print it either.
	if err := dp.Settings.Set(ctx, "store.name", "Michał's Deli"); err != nil {
		t.Fatal(err)
	}
	if got := receiptLine("tr"); got != tr {
		t.Fatalf("an unencodable store name dropped the pick: %q, want %q", got, tr)
	}
	if err := dp.Settings.Set(ctx, "store.name", "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	// A reprint (buildReceiptDoc) is the shop's copy.
	doc, err := buildReceiptDoc(ctx, dp, "R-4705")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Meta[0] != en {
		t.Fatalf("shop copy printed %q, want %q", doc.Meta[0], en)
	}
}

// End to end, no stubs: a kiosk card sale with a Turkish pick writes a
// Turkish receipt to a Windows-1254 receipt printer (a device file here).
func TestSelfOrderCheckout_PrintedReceiptBytesInPick(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	dev := filepath.Join(t.TempDir(), "receipt-printer")
	if err := os.WriteFile(dev, nil, 0o600); err != nil { // the device node a real printer provides
		t.Fatal(err)
	}
	if err := dp.Settings.SetMany(context.Background(), map[string]string{
		keyPrinterMode:    "device",
		keyPrinterDevice:  dev,
		keyPrinterCharset: "win1254",
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	g := newSelfOrderGuest(t, mux)
	g.get("/self-order/language?code=tr")
	g.post("/api/self-order/scan", "code=5000001")
	if rec := g.post("/api/self-order/checkout", "method=card"); rec.Code != http.StatusOK {
		t.Fatalf("checkout = %d: %s", rec.Code, rec.Body.String())
	}
	dp.WaitForAsyncWork()
	raw, err := os.ReadFile(dev)
	if err != nil {
		t.Fatalf("no receipt reached the printer: %v", err)
	}
	fis, _ := charmap.Windows1254.NewEncoder().String("Fiş ")
	if !bytes.Contains(raw, []byte(fis)) || bytes.Contains(raw, []byte("Receipt ")) {
		t.Fatalf("printed receipt is not the Turkish one: %q", raw)
	}
}
