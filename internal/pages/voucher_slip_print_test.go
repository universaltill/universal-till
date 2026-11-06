package pages

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4247: the separate gift-voucher slip per issued voucher.

// slipDevice points the till at a device-file printer (an empty temp file the
// job is written into) with the slip setting as given.
func slipDevice(t *testing.T, dp *common.Deps, slip string) string {
	t.Helper()
	dev := filepath.Join(t.TempDir(), "lp0")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kv := map[string]string{
		keyPrinterMode: "device", keyPrinterDevice: dev,
		keyPrinterReceiptPolicy: receiptPolicyAlways,
	}
	if slip != "" {
		kv[keyPrinterVoucherSlip] = slip
	}
	setSettings(t, dp, kv)
	return dev
}

// seedSlipVouchers issues GV-A (25.00, Anna) and GV-B (10.00) in sale1/R001.
func seedSlipVouchers(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 3500, 0, 0)
	now := time.Now().UTC().Format(time.RFC3339)
	repo := data.NewPOSRepo(dp.Db)
	for _, v := range []data.Voucher{
		{ID: "GV-A", HolderLabel: "Anna", OriginalAmountMinor: 2500, BalanceMinor: 2500, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: now},
		{ID: "GV-B", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "GBP", IssuedSaleID: "sale1", CreatedAt: now},
	} {
		if err := repo.CreateVoucher(ctx, nil, v); err != nil {
			t.Fatal(err)
		}
	}
}

func readJob(t *testing.T, dev string) []byte {
	t.Helper()
	got, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPrinterConfig_VoucherSlipDefaultOff(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	if printerConfig(ctx, dp).VoucherSlip {
		t.Fatal("unset must be off")
	}
	for v, want := range map[string]bool{"true": true, "yes": false, "1": false, "TRUE": false, "false": false} {
		setSettings(t, dp, map[string]string{keyPrinterVoucherSlip: v})
		if got := printerConfig(ctx, dp).VoucherSlip; got != want {
			t.Errorf("%q -> %v, want %v", v, got, want)
		}
	}
}

func TestPrintReceiptIn_VoucherSlips(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	dev := slipDevice(t, dp, "true")
	if err := printReceiptIn(ctx, dp, "R001", ""); err != nil {
		t.Fatal(err)
	}
	doc, err := customerReceiptDoc(ctx, dp, "R001", "")
	if err != nil {
		t.Fatal(err)
	}
	slips := voucherSlips(doc)
	if len(slips) != 2 {
		t.Fatalf("voucherSlips = %d, want 2", len(slips))
	}
	want := print.Render(doc)
	receiptLen := len(want)
	for _, s := range slips {
		want = append(want, print.RenderVoucherSlip(s)...)
	}
	got := readJob(t, dev)
	if !bytes.Equal(got, want) {
		t.Fatal("job != receipt + slip A + slip B")
	}
	tail := got[receiptLen:]
	if i, j := bytes.Index(tail, []byte("{BGV-A")), bytes.Index(tail, []byte("{BGV-B")); i < 0 || j < i {
		t.Fatalf("slip order wrong: A at %d, B at %d", i, j)
	}
	if n := bytes.Count(got, []byte{0x1d, 0x56, 0x42, 0x03}); n != 3 {
		t.Fatalf("feed-cut count = %d, want 3 (receipt + 2 slips)", n)
	}
	if !bytes.Contains(tail, []byte("For Anna")) || bytes.Count(tail, []byte("For ")) != 1 {
		t.Fatal("only slip A carries a For row")
	}
}

func TestPrintReceiptIn_VoucherSlipsOffUnchanged(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedReceiptSale(t, dp, "sale2", "R002", "sale", "", 120, 0, 0)

	dev := slipDevice(t, dp, "")
	if err := printReceiptIn(ctx, dp, "R001", ""); err != nil {
		t.Fatal(err)
	}
	doc, _ := customerReceiptDoc(ctx, dp, "R001", "")
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("setting unset: job must equal the plain receipt")
	}

	dev = slipDevice(t, dp, "true")
	if err := printReceiptIn(ctx, dp, "R002", ""); err != nil {
		t.Fatal(err)
	}
	doc, _ = customerReceiptDoc(ctx, dp, "R002", "")
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("no vouchers: job must equal the plain receipt")
	}
}

func TestPrintReceiptIn_VoidVoucherNoSlip(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	if _, err := dp.Db.ExecContext(ctx, `UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GV-A'`); err != nil {
		t.Fatal(err)
	}
	dev := slipDevice(t, dp, "true")
	if err := printReceiptIn(ctx, dp, "R001", ""); err != nil {
		t.Fatal(err)
	}
	got := readJob(t, dev)
	if n := bytes.Count(got, []byte{0x1d, 0x56, 0x42, 0x03}); n != 2 {
		t.Fatalf("feed-cut count = %d, want 2 (receipt + slip B)", n)
	}
	doc, _ := customerReceiptDoc(ctx, dp, "R001", "")
	slips := voucherSlips(doc)
	if len(slips) != 1 || slips[0].Voucher.Code != "GV-B" {
		t.Fatalf("slips = %+v, want only GV-B", slips)
	}
}

func TestPrintReceiptIn_TopUpNoSlip(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedReceiptSale(t, dp, "sale-tu", "R-TU", "sale", "", 500, 0, 0)
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO voucher_transactions(id, voucher_id, sale_id, type, amount) VALUES ('vt-tu', 'GV-A', 'sale-tu', 'top_up', 500)`); err != nil {
		t.Fatal(err)
	}
	doc, err := customerReceiptDoc(ctx, dp, "R-TU", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Vouchers) == 0 {
		t.Fatal("precondition: the top-up block should be on the receipt")
	}
	dev := slipDevice(t, dp, "true")
	if err := printReceiptIn(ctx, dp, "R-TU", ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("a top-up-only sale must print no slip")
	}
}

func TestPrintReceiptIn_VoucherSlipsSuppressed(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	seedSlipVouchers(t, h.dp)
	dev := slipDevice(t, h.dp, "true")
	err := printReceiptIn(context.Background(), h.dp, "R001", "")
	if !errors.Is(err, errCustomerDocumentsSuppressed) {
		t.Fatalf("err = %v, want errCustomerDocumentsSuppressed", err)
	}
	if len(readJob(t, dev)) != 0 {
		t.Fatal("suppressed: nothing may be written")
	}
}

func TestPostSettingsPrinter_VoucherSlip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST %q = %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	stored := func() string {
		v, _, _ := dp.Settings.Get(ctx, keyPrinterVoucherSlip)
		return v
	}
	post("mode=off&voucherSlip=true")
	if stored() != "true" {
		t.Fatalf("stored %q, want true", stored())
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='printer_settings_changed' ORDER BY rowid DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"voucher_slip":true`) {
		t.Fatalf("audit payload %s lacks voucher_slip:true", payload)
	}
	post("mode=off") // older client, field absent
	if stored() != "false" {
		t.Fatalf("absent field stored %q, want false", stored())
	}
	post("mode=off&voucherSlip=true")
	post("mode=off&voucherSlip=on")
	if stored() != "false" {
		t.Fatalf("voucherSlip=on stored %q, want false", stored())
	}
}

func TestSettingsPage_VoucherSlipCheckbox(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	i := strings.Index(body, `name="voucherSlip"`)
	if i < 0 {
		t.Fatal("settings page lacks the voucherSlip checkbox")
	}
	if strings.Contains(body[i:i+200], "checked") {
		t.Fatal("must be unchecked by default")
	}
	if err := d.Settings.Set(context.Background(), keyPrinterVoucherSlip, "true"); err != nil {
		t.Fatal(err)
	}
	body = get()
	i = strings.Index(body, `name="voucherSlip"`)
	if i < 0 || !strings.Contains(body[i:i+200], "checked") {
		t.Fatal("must be checked once persisted")
	}
}

func TestPostSettingsPrinter_VoucherSlipElevationRoundTrip(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registerPrintAPI(mux, d)
	_, cashierID := seedElevationUsers(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}
	rec := postForm(mux, "/api/settings/printer", url.Values{"mode": {"off"}, "voucherSlip": {"true"}}, &cashier)
	if !strings.Contains(rec.Body.String(), "elevation-dialog") ||
		!strings.Contains(rec.Body.String(), `name="voucherSlip" value="true"`) {
		t.Fatalf("elevation prompt must carry voucherSlip=true: %s", rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), keyPrinterVoucherSlip); ok && v == "true" {
		t.Fatal("must not be saved before approval")
	}
}
