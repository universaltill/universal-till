package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5740: "Print voucher slip" on /vouchers/{id} — one slip, no
// receipt, whatever printer.voucher_slip says.

var feedCut = []byte{0x1d, 0x56, 0x42, 0x03}

func vPrintSlip(t *testing.T, mux *http.ServeMux, id string, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/vouchers/"+id+"/print-slip?lang=en", nil)
	req.Header.Set("HX-Request", "true")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func slipAuditRows(t *testing.T, dp *common.Deps, id string) []map[string]any {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT COALESCE(actor_id,''), data_json FROM audit_log WHERE entity_type='voucher' AND entity_id=? AND action='slip_printed' ORDER BY rowid`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, js string
		if err := rows.Scan(&actor, &js); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(js), &m); err != nil {
			t.Fatal(err)
		}
		m["actor"] = actor
		out = append(out, m)
	}
	return out
}

func seedActive(t *testing.T, dp *common.Deps, id, holder string) {
	t.Helper()
	vSeed(t, data.NewPOSRepo(dp.Db), data.Voucher{ID: id, HolderLabel: holder, OriginalAmountMinor: 2500, CreatedAt: "2026-10-01T10:00:00Z"})
}

func TestVoucherSlipReprint_PrintsOneSlipNoReceipt(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "Anna")
	dev := slipDevice(t, dp, "") // printer.voucher_slip OFF: independent of it
	txBefore := countRows(t, dp, `SELECT COUNT(*) FROM voucher_transactions`)

	rec := vPrintSlip(t, mux, "GV-A", &vManager)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.print_slip.done")) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	job := readJob(t, dev)
	for _, want := range []string{"Gift voucher", "For Anna", "GV-A", "Reprinted "} {
		if !bytes.Contains(job, []byte(want)) {
			t.Errorf("job lacks %q", want)
		}
	}
	if n := bytes.Count(job, feedCut); n != 1 {
		t.Errorf("feed+cut count = %d, want 1 (one slip, no receipt)", n)
	}
	if bytes.Contains(job, []byte("Receipt")) {
		t.Error("a receipt was printed")
	}
	if bytes.Contains(job, []byte{0x1b, 0x70}) {
		t.Error("drawer kick in a slip job")
	}
	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 1 || rows[0]["ok"] != true || rows[0]["actor"] != "m1" {
		t.Fatalf("audit = %v", rows)
	}
	var bal int64
	_ = dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id='GV-A'`).Scan(&bal)
	if bal != 2500 || countRows(t, dp, `SELECT COUNT(*) FROM voucher_transactions`) != txBefore {
		t.Error("printing must not touch the balance or ledger")
	}
}

func TestVoucherSlipReprint_ImportedVoucherPrints(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t) // no issuing sale
	seedActive(t, dp, "GV-IMP", "")
	dev := slipDevice(t, dp, "")
	if rec := vPrintSlip(t, mux, "GV-IMP", &vManager); rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(readJob(t, dev), []byte("GV-IMP")) {
		t.Error("imported voucher not printed")
	}
}

func TestVoucherSlipReprint_Forbidden(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	dev := slipDevice(t, dp, "")
	cashier := auth.User{ID: "c1", Role: "cashier"}
	noID := auth.User{ID: "", Role: "manager"}
	for name, u := range map[string]*auth.User{"anon": nil, "cashier": &cashier, "manager without id": &noID} {
		if rec := vPrintSlip(t, mux, "GV-A", u); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, rec.Code)
		}
	}
	if len(readJob(t, dev)) != 0 || len(slipAuditRows(t, dp, "GV-A")) != 0 {
		t.Error("forbidden request printed or audited")
	}
}

func TestVoucherSlipReprint_NotFound(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	dev := slipDevice(t, dp, "")
	rec := vPrintSlip(t, mux, "NOPE", &vManager)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.error.not_found")) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if len(readJob(t, dev)) != 0 {
		t.Error("printed for unknown voucher")
	}
}

func TestVoucherSlipReprint_VoidAndUsedUpRefused(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(dp.Db)
	vSeed(t, repo, data.Voucher{ID: "GV-VOID", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GV-USED", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "redeemed", CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GV-ZERO", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "active", CreatedAt: "2026-10-01T10:00:00Z"})
	dev := slipDevice(t, dp, "")
	for _, id := range []string{"GV-VOID", "GV-USED", "GV-ZERO"} {
		rec := vPrintSlip(t, mux, id, &vManager)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.print_slip.not_printable")) {
			t.Errorf("%s: status %d %s", id, rec.Code, rec.Body.String())
		}
		if len(slipAuditRows(t, dp, id)) != 0 {
			t.Errorf("%s: audited", id)
		}
	}
	if len(readJob(t, dev)) != 0 {
		t.Error("printed a refused slip")
	}
}

func TestVoucherSlipReprint_NoPrinter(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	rec := vPrintSlip(t, mux, "GV-A", &vManager)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.print_slip.no_printer")) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if len(slipAuditRows(t, dp, "GV-A")) != 0 {
		t.Error("audited without reaching a printer")
	}
}

func TestVoucherSlipReprint_PrinterFails(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{
		keyPrinterMode: "device", keyPrinterDevice: filepath.Join(t.TempDir(), "missing", "lp0"),
		keyPrinterReceiptPolicy: receiptPolicyAlways,
	})
	rec := vPrintSlip(t, mux, "GV-A", &vManager)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), httpx.T("en", "settings.printer.test_failed")) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 1 || rows[0]["ok"] != false {
		t.Fatalf("audit = %v", rows)
	}
}

func TestVoucherSlipReprint_Suppressed(t *testing.T) {
	h := newShadowDocsHarness(t, "PT", receiptPolicyAlways)
	registerVouchers(h.mux, h.dp)
	seedActive(t, h.dp, "GV-A", "")
	rec := h.do(http.MethodPost, "/vouchers/GV-A/print-slip", "")
	if rec.Code != http.StatusUnavailableForLegalReasons || !strings.Contains(rec.Body.String(), `data-testid="shadow-documents-refused"`) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if n := len(h.printer.Tickets()); n != 0 {
		t.Errorf("printer got %d jobs", n)
	}
	rows := slipAuditRows(t, h.dp, "GV-A")
	if len(rows) != 1 || rows[0]["refused"] != "shadow_customer_documents" {
		t.Fatalf("audit = %v", rows)
	}
}

func TestVoucherSlipReprint_ReplicaJudgesMainTillRow(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	// Locally active, void on the main till: the main till decides.
	seedActive(t, dp, "GV-A", "")
	vSeed(t, primaryRepo, data.Voucher{ID: "GV-A", OriginalAmountMinor: 2500, BalanceMinor: 0, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})
	dev := slipDevice(t, dp, "")
	if rec := vPrintSlip(t, mux, "GV-A", &vManager); rec.Code != http.StatusConflict {
		t.Fatalf("main till void: status %d %s", rec.Code, rec.Body.String())
	}
	if len(readJob(t, dev)) != 0 {
		t.Error("printed a slip the main till says is void")
	}
	// Main till unreachable: the local row decides and the slip prints.
	setReplicaSettings(t, dp.Settings, unreachableURL(), "b-replica")
	if rec := vPrintSlip(t, mux, "GV-A", &vManager); rec.Code != http.StatusOK {
		t.Fatalf("unreachable main till: status %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(readJob(t, dev), []byte("GV-A")) {
		t.Error("slip not printed with main till unreachable")
	}
}

func TestVoucherDetail_PrintSlipButton(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(dp.Db)
	seedActive(t, dp, "GV-A", "Anna")
	vSeed(t, repo, data.Voucher{ID: "GV-PART", OriginalAmountMinor: 2500, BalanceMinor: 500, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GV-VOID", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GV-USED", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "redeemed", CreatedAt: "2026-10-01T10:00:00Z"})
	slipDevice(t, dp, "") // slip setting off: the button shows regardless
	note := httpx.T("en", "vouchers.print_slip.original_value_note")

	body := vGet(t, mux, "/vouchers/GV-A?lang=en", &vManager, false).Body.String()
	for _, want := range []string{
		`hx-post="/vouchers/GV-A/print-slip"`, `id="voucher-slip-msg"`, `data-testid="voucher-print-slip"`,
		`hx-indicator="#voucher-slip-busy"`, `id="voucher-slip-busy"`, httpx.T("en", "vouchers.print_slip.button"),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("full-balance page lacks %q", want)
		}
	}
	if strings.Contains(body, note) {
		t.Error("original-value note shown at full balance")
	}
	if body := vGet(t, mux, "/vouchers/GV-PART?lang=en", &vManager, false).Body.String(); !strings.Contains(body, note) || !strings.Contains(body, `data-testid="voucher-print-slip"`) {
		t.Error("part-spent page lacks note or button")
	}
	for _, id := range []string{"GV-VOID", "GV-USED"} {
		b := vGet(t, mux, "/vouchers/"+id+"?lang=en", &vManager, false).Body.String()
		if strings.Contains(b, "voucher-slip") || strings.Contains(b, "print-slip") {
			t.Errorf("%s shows the slip action", id)
		}
	}
	setSettings(t, dp, map[string]string{keyPrinterMode: "off"})
	b := vGet(t, mux, "/vouchers/GV-A?lang=en", &vManager, false).Body.String()
	if strings.Contains(b, `data-testid="voucher-print-slip"`) || !strings.Contains(b, `href="/settings#settings-printer"`) ||
		!strings.Contains(b, httpx.T("en", "vouchers.print_slip.no_printer")) {
		t.Error("no-printer branch wrong")
	}
}

// Review (ut-docs#5740): an operator who leaves the page while the printer
// is still busy cancels the request context; the attempt must still be
// audited (the slip may already be on paper), as printReceiptAsync's
// recordPrintFailureCtx does for receipts.
func TestVoucherSlipReprint_AuditSurvivesClientCancel(t *testing.T) {
	mux, dp := newVoucherImportTestMux(t)
	seedActive(t, dp, "GV-A", "")
	setSettings(t, dp, map[string]string{keyPrinterMode: "system", keyPrinterReceiptPolicy: receiptPolicyAlways})
	bin := t.TempDir()
	mark := filepath.Join(bin, "started")
	script := "#!/bin/sh\ncat >/dev/null\n: > \"" + mark + "\"\nexec sleep 5\n"
	if err := os.WriteFile(filepath.Join(bin, "lp"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(mark); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	req := httptest.NewRequest(http.MethodPost, "/vouchers/GV-A/print-slip?lang=en", nil).WithContext(ctx)
	req.Header.Set("HX-Request", "true")
	req = auth.WithUser(req, vManager)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rows := slipAuditRows(t, dp, "GV-A")
	if len(rows) != 1 || rows[0]["ok"] != false {
		t.Fatalf("audit = %v, want one ok=false row after the client went away", rows)
	}
}
