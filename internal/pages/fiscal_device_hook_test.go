package pages

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestDeviceAuthorizePayloadExtras(t *testing.T) {
	in := pos.SaleInput{
		Currency:      "TRY",
		TaxInclusive:  true,
		SaleDiscount:  money.FromMinor(100),
		ServiceCharge: money.FromMinor(0),
		Lines: []pos.SaleLineInput{
			{Name: "Çay", Qty: 2, UnitPrice: money.FromMinor(1500), TaxRateBasisPoints: 1000},
			{Name: "Simit", Qty: 1, UnitPrice: money.FromMinor(2000), TaxRateBasisPoints: 100, LineDiscount: money.FromMinor(50)},
		},
	}
	payments := []pos.PaymentInput{{MethodID: "okc", Amount: money.FromMinor(4850)}}
	extras := deviceAuthorizePayloadExtras(in, payments)
	if extras["currency"] != "TRY" || extras["total"] != int64(4850) || extras["tax_inclusive"] != true || extras["sale_discount"] != int64(100) {
		t.Fatalf("extras = %+v", extras)
	}
	lines := extras["lines"].([]map[string]any)
	if len(lines) != 2 || lines[0]["name"] != "Çay" || lines[0]["unit_price"] != int64(1500) || lines[0]["tax_rate_bp"] != 1000 || lines[1]["line_discount"] != int64(50) {
		t.Fatalf("lines = %+v", lines)
	}
	// The whole thing must survive JSON encoding (it rides on the event bus).
	if _, err := json.Marshal(extras); err != nil {
		t.Fatal(err)
	}
}

func TestPickDeviceEvidence_FirstWins(t *testing.T) {
	first := pickDeviceEvidence(nil, json.RawMessage(`{"status":"approved","fiscal_device":{"receipt_no":"1"}}`))
	if first == nil || first.ReceiptNo != "1" {
		t.Fatalf("first = %+v", first)
	}
	kept := pickDeviceEvidence(first, json.RawMessage(`{"fiscal_device":{"receipt_no":"2"}}`))
	if kept != first {
		t.Fatalf("second leg replaced the first receipt: %+v", kept)
	}
	if got := pickDeviceEvidence(nil, json.RawMessage(`{"status":"approved"}`)); got != nil {
		t.Fatalf("no evidence must stay nil, got %+v", got)
	}
}

// recordFiscalDeviceEvidence persists the receipt and, on the first one
// ever, flips fiscal.tse_configured with an audit marker; a later receipt
// leaves an already-true flag alone (no duplicate marker).
func TestRecordFiscalDeviceEvidence_PersistsAndConfirmsOnce(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	createFiscalDeviceReceiptsTable(t, db)
	d := &common.Deps{Db: db, Settings: settings.NewStore(db)}
	repo := data.NewPOSRepo(db)

	recordFiscalDeviceEvidence(t.Context(), d, repo, "sale-1", "cashier", nil) // no evidence: no-op
	if _, ok, _ := repo.GetFiscalDeviceReceipt(t.Context(), "sale-1"); ok {
		t.Fatal("nil evidence must not persist anything")
	}

	ev := &fiscal.DeviceEvidence{Kind: "okc", Maker: "sim", Serial: "SIM-1", ReceiptNo: "0000001", ReceiptKind: "mali_fis", ZNo: 2}
	recordFiscalDeviceEvidence(t.Context(), d, repo, "sale-1", "cashier", ev)
	rec, ok, err := repo.GetFiscalDeviceReceipt(t.Context(), "sale-1")
	if err != nil || !ok || rec.ReceiptNo != "0000001" || rec.Serial != "SIM-1" || rec.ZNo != 2 {
		t.Fatalf("persisted = %+v ok=%v err=%v", rec, ok, err)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); v != "true" {
		t.Fatalf("first receipt must confirm the device, tse_configured = %q", v)
	}
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "sale-1", fiscalDeviceAuditConfirmed); !ok {
		t.Fatal("expected the fiscal_device_confirmed audit marker on the first receipt")
	}

	ev2 := &fiscal.DeviceEvidence{ReceiptNo: "0000002"}
	recordFiscalDeviceEvidence(t.Context(), d, repo, "sale-2", "cashier", ev2)
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "sale-2", fiscalDeviceAuditConfirmed); ok {
		t.Fatal("an already-confirmed device must not be re-confirmed on every receipt")
	}
}

func TestRenderReceipt_DeviceReceiptBlock(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("₺%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Çay", Qty: 1, UnitPrice: 1500, TaxRateBasisPoints: 1000}}
	payments := []pos.PaymentInput{{MethodID: "okc", Amount: 1500}}
	dev := &data.FiscalDeviceReceipt{SaleID: "s", Maker: "beko", Serial: "AV0001234", ReceiptNo: "0000042", ReceiptKind: "mali_fis", ZNo: 7, IssuedAt: "2026-09-03T10:12:00+03:00"}

	html, err := renderReceipt(funcs, "123", lines, payments, 1500, 0, 1500, true, 0, "", 0, nil, false, false, false, false, nil, dev, "Bakkal", receiptDesign{ShowTax: true}, "")
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	// html/template escapes the "+" in the offset, so match the date part only.
	for _, want := range []string{"receipt.fiscal.device.title", "0000042", "AV0001234", "receipt.fiscal.device.z_no", "7", "2026-09-03T10:12:00"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in receipt, got: %s", want, html)
		}
	}
	plain, err := renderReceipt(funcs, "123", lines, payments, 1500, 0, 1500, true, 0, "", 0, nil, false, false, false, false, nil, nil, "Bakkal", receiptDesign{ShowTax: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "receipt.fiscal.device.title") {
		t.Fatal("no device receipt: block must be absent, never placeholders")
	}
}
