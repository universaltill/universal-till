package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// GET /api/vouchers/{id} (ut-docs#1008): the outstanding liability is
// queryable per voucher — stable identifier, holder label, balance — in the
// repo's { data, error } envelope with snake_case fields.
func newVoucherTestMux(t *testing.T) (*http.ServeMux, *data.POSRepo) {
	t.Helper()
	dbase, err := db.Open(filepath.Join(t.TempDir(), "voucher-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbase.Close() })
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	mux := http.NewServeMux()
	registerVoucherAPI(mux, d)
	return mux, data.NewPOSRepo(dbase.DB)
}

func TestVoucherAPI_BalanceQuery(t *testing.T) {
	mux, repo := newVoucherTestMux(t)

	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-API-1", HolderLabel: "Sample Holder", OriginalAmountMinor: 1500,
		BalanceMinor: 900, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vouchers/GS-API-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/vouchers/GS-API-1 = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data  data.Voucher `json:"data"`
		Error any          `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("error = %v, want null", envelope.Error)
	}
	v := envelope.Data
	if v.ID != "GS-API-1" || v.HolderLabel != "Sample Holder" || v.BalanceMinor != 900 ||
		v.OriginalAmountMinor != 1500 || v.Status != "active" || v.Currency != "EUR" {
		t.Fatalf("voucher payload = %+v", v)
	}
}

func TestVoucherAPI_UnknownVoucher404(t *testing.T) {
	mux, _ := newVoucherTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/vouchers/GS-NOPE", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown voucher = %d, want 404", rec.Code)
	}
	var envelope struct {
		Data  any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "voucher_not_found" {
		t.Fatalf("error envelope = %+v", envelope.Error)
	}
}
