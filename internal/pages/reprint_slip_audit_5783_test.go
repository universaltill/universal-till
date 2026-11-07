package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#5783: a journal reprint that also prints the sale's gift voucher
// slips (ut-docs#4247) records how many in the receipt_reprint audit row, so
// the trail shows redeemable codes were printed again.

func reprintAuditData(t *testing.T, slip string) (int, string) {
	t.Helper()
	mux, dp := newPrintAPITestDeps(t)
	seedSlipVouchers(t, dp)
	slipDevice(t, dp, slip)
	req := httptest.NewRequest(http.MethodPost, "/api/print/receipt/R001", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var data string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'receipt_reprint'`).Scan(&data); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	return rec.Code, data
}

func TestReprint_AuditRecordsVoucherSlips(t *testing.T) {
	code, data := reprintAuditData(t, "true")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(data, `"voucher_slips":2`) || !strings.Contains(data, `"ok":true`) {
		t.Fatalf("audit = %s, want ok true and voucher_slips 2", data)
	}
}

func TestReprint_AuditNoVoucherSlipsKeyWhenOff(t *testing.T) {
	code, data := reprintAuditData(t, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if strings.Contains(data, "voucher_slips") {
		t.Fatalf("audit = %s, must not carry voucher_slips when the setting is off", data)
	}
}
