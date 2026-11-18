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
	n, err := printReceiptIn(ctx, dp, "R001", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("printReceiptIn slips = %d, want 2", n)
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
	if n, err := printReceiptIn(ctx, dp, "R001", ""); err != nil || n != 0 {
		t.Fatalf("setting off: slips = %d, err = %v, want 0, nil", n, err)
	}
	doc, _ := customerReceiptDoc(ctx, dp, "R001", "")
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("setting unset: job must equal the plain receipt")
	}

	dev = slipDevice(t, dp, "true")
	if _, err := printReceiptIn(ctx, dp, "R002", ""); err != nil {
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
	if _, err := printReceiptIn(ctx, dp, "R001", ""); err != nil {
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

// seedSlipTopUp records a real top-up of voucherID by a new completed sale
// (ledger row + balance, through TopUpVoucher).
func seedSlipTopUp(t *testing.T, dp *common.Deps, voucherID, saleID, receiptNo string, amount int64) {
	t.Helper()
	ctx := context.Background()
	seedReceiptSale(t, dp, saleID, receiptNo, "sale", "", amount, 0, 0)
	repo := data.NewPOSRepo(dp.Db)
	tx, err := dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TopUpVoucher(ctx, tx, voucherID, saleID, amount, 1_000_000, false, time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// ut-docs#5793: a top-up gets its own slip.
func TestPrintReceiptIn_TopUpSlip(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
	dev := slipDevice(t, dp, "true")
	n, err := printReceiptIn(ctx, dp, "R-TU", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("slips = %d, want 1", n)
	}
	doc, err := customerReceiptDoc(ctx, dp, "R-TU", "")
	if err != nil {
		t.Fatal(err)
	}
	slips := voucherSlips(doc)
	if len(slips) != 1 {
		t.Fatalf("voucherSlips = %d, want 1", len(slips))
	}
	want := append(print.Render(doc), print.RenderVoucherSlip(slips[0])...)
	got := readJob(t, dev)
	if !bytes.Equal(got, want) {
		t.Fatal("job != receipt + top-up slip")
	}
	b := doc.Vouchers[0]
	for _, w := range []string{"Gift voucher top-up", b.Amount, "For Anna", b.Balance} {
		if !bytes.Contains(got[len(print.Render(doc)):], []byte(w)) {
			t.Errorf("slip lacks %q", w)
		}
	}
	if !strings.HasPrefix(b.Amount, "+") || !strings.Contains(b.Balance, "45.00") {
		t.Errorf("block amount/balance = %q / %q", b.Amount, b.Balance)
	}
	if !bytes.Contains(got, []byte("{BGV-A")) {
		t.Error("slip lacks the CODE128 for GV-A")
	}
	if n := bytes.Count(got, []byte{0x1d, 0x56, 0x42, 0x03}); n != 2 {
		t.Fatalf("feed-cut count = %d, want 2", n)
	}
}

func TestPrintReceiptIn_TopUpSlipNoHolder(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedSlipTopUp(t, dp, "GV-B", "sale-tu", "R-TU", 500)
	slipDevice(t, dp, "true")
	doc := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	slips := voucherSlips(doc)
	if len(slips) != 1 {
		t.Fatalf("voucherSlips = %d, want 1", len(slips))
	}
	if bytes.Contains(print.RenderVoucherSlip(slips[0]), []byte("For ")) {
		t.Error("no holder: no For row")
	}
}

func TestPrintReceiptIn_TopUpReversedNoSlip(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
	tx, err := dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := data.NewPOSRepo(dp.Db).VoidVouchersIssuedInSale(ctx, tx, "sale-tu"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	doc := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	if len(doc.Vouchers) != 1 || doc.Vouchers[0].Void != "VOID - top-up taken back" {
		t.Fatalf("precondition: receipt block must be marked as a top-up taken back: %+v", doc.Vouchers)
	}
	dev := slipDevice(t, dp, "true")
	if n, err := printReceiptIn(ctx, dp, "R-TU", ""); err != nil || n != 0 {
		t.Fatalf("slips = %d, err = %v, want 0", n, err)
	}
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("job must equal the plain receipt")
	}
}

func TestPrintReceiptIn_TopUpOfVoidVoucherNoSlip(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
	if _, err := dp.Db.ExecContext(ctx, `UPDATE vouchers SET status = 'void' WHERE id = 'GV-A'`); err != nil {
		t.Fatal(err)
	}
	doc := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	// ut-docs#5794: the receipt block agrees with the slip — a top-up of a
	// voucher void since prints the void notice, and the code as text only.
	if len(doc.Vouchers) != 1 || doc.Vouchers[0].Void != "VOID - not valid" {
		t.Fatalf("receipt block must be marked void: %+v", doc.Vouchers)
	}
	// ESC/POS GS k 73 starts a CODE128 symbol. Compare against the same
	// receipt with the voucher active: the void one prints one symbol fewer
	// (the voucher's) and still prints the code as text.
	gsK := []byte{0x1d, 0x6b, 0x49}
	voidOut := print.Render(doc)
	if !bytes.Contains(voidOut, []byte("GV-A")) {
		t.Fatal("the void top-up must still print its code as text")
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE vouchers SET status = 'active' WHERE id = 'GV-A'`); err != nil {
		t.Fatal(err)
	}
	activeOut := print.Render(mustReceiptDoc(t, ctx, dp, "R-TU", ""))
	if got, want := bytes.Count(voidOut, gsK), bytes.Count(activeOut, gsK)-1; got != want {
		t.Fatalf("CODE128 symbols = %d, want %d: a top-up of a void voucher must not print a scannable symbol", got, want)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE vouchers SET status = 'void' WHERE id = 'GV-A'`); err != nil {
		t.Fatal(err)
	}
	dev := slipDevice(t, dp, "true")
	if n, err := printReceiptIn(ctx, dp, "R-TU", ""); err != nil || n != 0 {
		t.Fatalf("slips = %d, err = %v, want 0", n, err)
	}
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("job must equal the plain receipt")
	}
}

// seedIssueAndTopUpSale: R001 issues GV-A/GV-B (seedSlipVouchers) and also
// tops up GV-C, which another sale issued.
func seedIssueAndTopUpSale(t *testing.T, dp *common.Deps) {
	t.Helper()
	seedSlipVouchers(t, dp)
	seedReceiptSale(t, dp, "sale0", "R000", "sale", "", 1500, 0, 0)
	if err := data.NewPOSRepo(dp.Db).CreateVoucher(context.Background(), nil, data.Voucher{ID: "GV-C", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "GBP", IssuedSaleID: "sale0", CreatedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := data.NewPOSRepo(dp.Db).TopUpVoucher(ctx, tx, "GV-C", "sale1", 500, 1_000_000, false, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPrintReceiptIn_IssueAndTopUpSlipsInOrder(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedIssueAndTopUpSale(t, dp)
	dev := slipDevice(t, dp, "true")
	n, err := printReceiptIn(ctx, dp, "R001", "")
	if err != nil || n != 3 {
		t.Fatalf("slips = %d, err = %v, want 3", n, err)
	}
	doc := mustReceiptDoc(t, ctx, dp, "R001", "")
	slips := voucherSlips(doc)
	if len(slips) != 3 {
		t.Fatalf("voucherSlips = %d, want 3", len(slips))
	}
	got := readJob(t, dev)
	tail := got[len(print.Render(doc)):]
	a, b, c := bytes.Index(tail, []byte("{BGV-A")), bytes.Index(tail, []byte("{BGV-B")), bytes.Index(tail, []byte("{BGV-C"))
	if a < 0 || b < a || c < b {
		t.Fatalf("slip order wrong: A %d B %d C %d", a, b, c)
	}
	if v := slips[0].Voucher; v.Label != "Gift voucher" || strings.HasPrefix(v.Amount, "+") || v.Balance != "" {
		t.Errorf("issue slip block changed: %+v", v)
	}
	if v := slips[2].Voucher; v.Label != "Gift voucher top-up" || !strings.HasPrefix(v.Amount, "+") || v.Balance == "" {
		t.Errorf("top-up slip block wrong: %+v", v)
	}
}

func TestPrintReceiptIn_TopUpSlipsOff(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedIssueAndTopUpSale(t, dp)
	dev := slipDevice(t, dp, "")
	if n, err := printReceiptIn(ctx, dp, "R001", ""); err != nil || n != 0 {
		t.Fatalf("slips = %d, err = %v, want 0", n, err)
	}
	doc := mustReceiptDoc(t, ctx, dp, "R001", "")
	if !bytes.Equal(readJob(t, dev), print.Render(doc)) {
		t.Fatal("setting off: job must equal the plain receipt")
	}
}

// R9: a reprint shows the balance right after the top-up, not today's.
func TestVoucherSlips_TopUpBalanceIsHistorical(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
	before := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	repo := data.NewPOSRepo(dp.Db)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GV-A", 1000, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, data.VoucherTransaction{ID: "red1", VoucherID: "GV-A", SaleID: "sale1", Type: "redemption", AmountMinor: 1000, CreatedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	after := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	s0, s1 := voucherSlips(before), voucherSlips(after)
	if len(s0) != 1 || len(s1) != 1 || s1[0].Voucher.Balance != s0[0].Voucher.Balance {
		t.Fatalf("balance changed after redemption: %+v vs %+v", s0, s1)
	}
}

func TestPrintReceiptIn_VoucherSlipsSuppressed(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	seedSlipVouchers(t, h.dp)
	dev := slipDevice(t, h.dp, "true")
	n, err := printReceiptIn(context.Background(), h.dp, "R001", "")
	if n != 0 {
		t.Fatalf("suppressed: slips = %d, want 0", n)
	}
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

// Tester (ut-docs#5793, R9): the test above seeds vouchers without an 'issue'
// ledger row, so it only exercises the replica branch of the balance-after
// read. A voucher issued on this till has one (internal/pos/sales.go); pin
// the historical slip balance on that path too.
func TestVoucherSlips_TopUpBalanceIsHistorical_IssueRow(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	seedSlipVouchers(t, dp)
	repo := data.NewPOSRepo(dp.Db)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.RecordVoucherTransaction(ctx, nil, data.VoucherTransaction{ID: "iss-a", VoucherID: "GV-A", SaleID: "sale1", Type: "issue", AmountMinor: 2500, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
	before := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GV-A", 1000, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, data.VoucherTransaction{ID: "red1", VoucherID: "GV-A", SaleID: "sale2", Type: "redemption", AmountMinor: 1000, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	after := mustReceiptDoc(t, ctx, dp, "R-TU", "")
	s0, s1 := voucherSlips(before), voucherSlips(after)
	if len(s0) != 1 || len(s1) != 1 {
		t.Fatalf("slips = %d / %d, want 1 / 1", len(s0), len(s1))
	}
	if !strings.Contains(s0[0].Voucher.Balance, "45.00") || s1[0].Voucher.Balance != s0[0].Voucher.Balance {
		t.Fatalf("slip balance = %q before, %q after a later redemption; want 45.00 both times", s0[0].Voucher.Balance, s1[0].Voucher.Balance)
	}
}

// mustReceiptDoc builds the customer receipt doc and fails the test on error.
func mustReceiptDoc(t *testing.T, ctx context.Context, dp *common.Deps, receiptNo, locale string) print.Doc {
	t.Helper()
	doc, err := customerReceiptDoc(ctx, dp, receiptNo, locale)
	if err != nil {
		t.Fatalf("customerReceiptDoc(%s): %v", receiptNo, err)
	}
	return doc
}
