package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5838: the journal reprint can leave the gift voucher slips out.

func postReprint(t *testing.T, mux *http.ServeMux, receipt string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/print/receipt/"+receipt, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func lastReprintAudit(t *testing.T, dp *common.Deps) string {
	t.Helper()
	var s string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'receipt_reprint' ORDER BY rowid DESC LIMIT 1`).Scan(&s); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	return s
}

// T1: no_slips=1 prints exactly the job a slip-less shop would print.
func TestReprintNoSlips_JobIsReceiptOnly(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	dev := slipDevice(t, dp, "true")
	rec := postReprint(t, mux, "R001", url.Values{"no_slips": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := readJob(t, dev)
	dev2 := slipDevice(t, dp, "false")
	if rec := postReprint(t, mux, "R001", nil); rec.Code != http.StatusOK {
		t.Fatalf("baseline status = %d", rec.Code)
	}
	if !bytes.Equal(got, readJob(t, dev2)) {
		t.Fatal("no_slips job must equal the receipt printed with the slip setting off")
	}
}

func TestReprintNoSlips_Audit(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	slipDevice(t, dp, "true")
	postReprint(t, mux, "R001", url.Values{"no_slips": {"1"}})
	a := lastReprintAudit(t, dp)
	if !strings.Contains(a, `"ok":true`) || !strings.Contains(a, `"slips_skipped":true`) || strings.Contains(a, "voucher_slips") {
		t.Fatalf("audit = %s, want ok, slips_skipped, no voucher_slips", a)
	}
}

// T2 + T3: without the flag, or with any value but "1", slips print as before.
func TestReprintNoSlips_OtherValuesArePlainReprint(t *testing.T) {
	for _, form := range []url.Values{nil, {"no_slips": {"true"}}, {"no_slips": {"0"}}, {"no_slips": {""}}} {
		mux, dp := newPrintAPITestDeps(t)
		seedSlipVouchers(t, dp)
		slipDevice(t, dp, "true")
		if rec := postReprint(t, mux, "R001", form); rec.Code != http.StatusOK {
			t.Fatalf("form %v: status = %d", form, rec.Code)
		}
		a := lastReprintAudit(t, dp)
		if !strings.Contains(a, `"voucher_slips":2`) || strings.Contains(a, "slips_skipped") {
			t.Fatalf("form %v: audit = %s, want voucher_slips 2, no slips_skipped", form, a)
		}
	}
}

// T4: a failed no-slips print is still audited as skipped.
func TestReprintNoSlips_FailureAudit(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	slipDevice(t, dp, "true")
	setSettings(t, dp, map[string]string{keyPrinterDevice: filepath.Join(t.TempDir(), "missing-dir", "lp0")})
	rec := postReprint(t, mux, "R001", url.Values{"no_slips": {"1"}})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	a := lastReprintAudit(t, dp)
	if !strings.Contains(a, `"ok":false`) || !strings.Contains(a, `"slips_skipped":true`) {
		t.Fatalf("audit = %s", a)
	}
}

// T8
func TestPrintReceiptWith_SkipSlips(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	slipDevice(t, dp, "true")
	n, err := printReceiptWith(context.Background(), dp, "R001", "", receiptPrintOpts{skipSlips: true})
	if err != nil || n != 0 {
		t.Fatalf("slips = %d, err = %v, want 0", n, err)
	}
}

func journalBody(t *testing.T, dp *common.Deps, receipt string) string {
	t.Helper()
	initPagesI18n(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	mux := http.NewServeMux()
	registerJournal(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/"+receipt, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/%s = %d", receipt, rec.Code)
	}
	return rec.Body.String()
}

const noSlipsBtn = `data-testid="reprint-no-slips"`

// T5 + T6
func TestJournalDetail_NoSlipsButtonVisibility(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	seedReceiptSale(t, dp, "sale-plain", "R-PLAIN", "sale", "", 500, 0, 0)

	slipDevice(t, dp, "true")
	body := journalBody(t, dp, "R001")
	if !strings.Contains(body, noSlipsBtn) {
		t.Fatal("setting on + voucher sale: button missing")
	}
	// The button must actually send no_slips=1, or it would print the slips.
	i := strings.Index(body, noSlipsBtn)
	start := strings.LastIndex(body[:i], "<button")
	if tag := body[start:i]; !strings.Contains(tag, "no_slips") || !strings.Contains(tag, "1") {
		t.Fatalf("no-slips button does not send no_slips=1: %s", tag)
	}
	if strings.Contains(journalBody(t, dp, "R-PLAIN"), noSlipsBtn) {
		t.Fatal("voucher-free sale must not show the button")
	}
	slipDevice(t, dp, "false")
	if strings.Contains(journalBody(t, dp, "R001"), noSlipsBtn) {
		t.Fatal("setting off: button must be absent")
	}
}

// Setting off on a sale whose ledger records the voucher issue and top-up, so
// the journal reads the vouchers anyway: the setting alone must hide the
// button (Tester mutation T-1).
func TestJournalDetail_NoSlipsButtonHiddenWithSettingOff(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedIssueAndTopUpSale(t, dp)
	slipDevice(t, dp, "true")
	if !strings.Contains(journalBody(t, dp, "R001"), noSlipsBtn) {
		t.Fatal("precondition: setting on must show the button")
	}
	slipDevice(t, dp, "false")
	if strings.Contains(journalBody(t, dp, "R001"), noSlipsBtn) {
		t.Fatal("setting off: button must be absent")
	}
}

// T7: the button shows iff a plain reprint would print >= 1 slip.
func TestJournalDetail_NoSlipsButtonMatchesSlipCount(t *testing.T) {
	ctx := context.Background()
	voidTopUp := func(t *testing.T, dp *common.Deps, saleID string) {
		tx, err := dp.Db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := data.NewPOSRepo(dp.Db).VoidVouchersIssuedInSale(ctx, tx, saleID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		receipt string
		seed    func(t *testing.T, dp *common.Deps)
	}{
		{"issued vouchers void", "R001", func(t *testing.T, dp *common.Deps) {
			seedSlipVouchers(t, dp)
			if _, err := dp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0`); err != nil {
				t.Fatal(err)
			}
		}},
		{"top-up reversed", "R-TU", func(t *testing.T, dp *common.Deps) {
			seedSlipVouchers(t, dp)
			seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
			voidTopUp(t, dp, "sale-tu")
		}},
		{"top-up of void voucher", "R-TU", func(t *testing.T, dp *common.Deps) {
			seedSlipVouchers(t, dp)
			seedSlipTopUp(t, dp, "GV-A", "sale-tu", "R-TU", 2000)
			if _, err := dp.Db.Exec(`UPDATE vouchers SET status = 'void' WHERE id = 'GV-A'`); err != nil {
				t.Fatal(err)
			}
		}},
		{"issue and top-up", "R001", seedIssueAndTopUpSale},
		{"issue only", "R001", seedSlipVouchers},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, dp := newPrintAPITestDeps(t)
			c.seed(t, dp)
			slipDevice(t, dp, "true")
			n, err := printReceiptIn(ctx, dp, c.receipt, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(journalBody(t, dp, c.receipt), noSlipsBtn); got != (n > 0) {
				t.Fatalf("button shown = %v but a reprint prints %d slips", got, n)
			}
		})
	}
}
