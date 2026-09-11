package pages

import (
	"bytes"
	"database/sql"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Opening voucher-balance CSV import (ut-docs#1834). Test harness mirrors
// promotions_page_test.go's newPromotionsTestMux: a fully migrated DB
// (openPagesTestDB, needed for the real vouchers/voucher_transactions
// tables) plus a real auth.Service and a seeded manager user, so
// canPerform(d, r, "settings") exercises the real role-permission check
// rather than the UT_AUTH=off bypass.
func newVoucherImportTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	httpx.InitCurrency("GBP")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('m1', 'm1', 'Manager', 'manager')`); err != nil {
		t.Fatalf("seed test manager: %v", err)
	}
	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerVoucherImport(mux, d)
	return mux, d
}

// voucherImportMultipart builds a multipart body carrying the CSV under
// "file" plus optional extra fields (e.g. "commit": "1") — same shape as
// import_page_test.go's own multipartCSV, just under this page's field
// names.
func voucherImportMultipart(t *testing.T, csv string, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if csv != "" {
		fw, err := w.CreateFormFile("file", "vouchers.csv")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write([]byte(csv)); err != nil {
			t.Fatalf("write csv: %v", err)
		}
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

const voucherImportCSV = "code,balance,label\n" +
	"GS-M1,25.00,Alice\n" +
	"GS-M2,10.50,Bob\n"

func postVoucherImport(mux *http.ServeMux, body *bytes.Buffer, ct string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/vouchers/import", body)
	req.Header.Set("Content-Type", ct)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Unauthenticated / non-manager: 403, nothing written — both the page and
// the API route.
func TestVoucherImportPage_Forbidden(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	// GET the page, unauthenticated.
	req := httptest.NewRequest(http.MethodGet, "/settings/vouchers/import", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET unauthenticated = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// GET the page, cashier session.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings/vouchers/import", nil), cashier)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET as cashier = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// POST commit=1, cashier session — must be refused AND write nothing.
	body, ct := voucherImportMultipart(t, voucherImportCSV, map[string]string{"commit": "1"})
	rec = postVoucherImport(mux, body, ct, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST commit as cashier = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M1"); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("cashier's refused commit must write nothing: GetVoucherBalance err = %v, want ErrVoucherNotFound", err)
	}

	// POST commit=1, unauthenticated — same must-write-nothing guarantee.
	body, ct = voucherImportMultipart(t, voucherImportCSV, map[string]string{"commit": "1"})
	rec = postVoucherImport(mux, body, ct, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST commit unauthenticated = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M2"); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("unauthenticated refused commit must write nothing: GetVoucherBalance err = %v, want ErrVoucherNotFound", err)
	}
}

// A manager can actually reach and render the page (not just be refused
// from it) — the positive-path counterpart to TestVoucherImportPage_Forbidden.
func TestVoucherImportPage_ManagerGetsPageOK(t *testing.T) {
	mux, _ := newVoucherImportTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings/vouchers/import", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET as manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="vouchers-import-form"`) {
		t.Fatalf("page body missing the upload form: %s", rec.Body.String())
	}
}

// Preview (commit unset) must write NOTHING.
func TestVoucherImportPage_PreviewDoesNotWrite(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	body, ct := voucherImportMultipart(t, voucherImportCSV, nil) // no commit field at all
	rec := postVoucherImport(mux, body, ct, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "GS-M1") && !strings.Contains(rec.Body.String(), "2") {
		t.Fatalf("preview should summarise the parsed rows, got: %s", rec.Body.String())
	}
	// The preview must carry a confirm form with the file bytes embedded,
	// so the operator can actually commit next.
	if !strings.Contains(rec.Body.String(), `name="file_b64"`) {
		t.Fatalf("preview missing the embedded file_b64 confirm field: %s", rec.Body.String())
	}

	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M1"); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("preview must write nothing: GetVoucherBalance(GS-M1) err = %v, want ErrVoucherNotFound", err)
	}
	if _, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M2"); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("preview must write nothing: GetVoucherBalance(GS-M2) err = %v, want ErrVoucherNotFound", err)
	}
}

// Commit creates vouchers with the correct BalanceMinor, empty
// IssuedSaleID, status 'active'.
func TestVoucherImportPage_CommitCreatesVouchers(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	body, ct := voucherImportMultipart(t, voucherImportCSV, map[string]string{"commit": "1"})
	rec := postVoucherImport(mux, body, ct, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	repo := data.NewPOSRepo(d.Db)
	v1, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M1")
	if err != nil {
		t.Fatalf("GS-M1 not created: %v", err)
	}
	if v1.BalanceMinor != 2500 || v1.OriginalAmountMinor != 2500 || v1.Status != "active" || v1.IssuedSaleID != "" || v1.HolderLabel != "Alice" {
		t.Fatalf("GS-M1 = %+v, want balance=2500 status=active IssuedSaleID=\"\" holder=Alice", v1)
	}
	v2, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M2")
	if err != nil {
		t.Fatalf("GS-M2 not created: %v", err)
	}
	if v2.BalanceMinor != 1050 || v2.IssuedSaleID != "" {
		t.Fatalf("GS-M2 = %+v, want balance=1050 IssuedSaleID=\"\"", v2)
	}

	// Its voucher_transactions row: type 'issue', sale_id NULL (empty) — the
	// exact shape internal/data/voucher_repo.go's EOD-exclusion fix depends
	// on (ut-docs#1834).
	var txType string
	var saleID sql.NullString
	if err := d.Db.QueryRow(`SELECT type, sale_id FROM voucher_transactions WHERE voucher_id = 'GS-M1'`).Scan(&txType, &saleID); err != nil {
		t.Fatalf("voucher_transactions lookup: %v", err)
	}
	if txType != "issue" || saleID.Valid {
		t.Fatalf("voucher_transactions row: type=%q sale_id valid=%v (%q), want type=issue sale_id=NULL", txType, saleID.Valid, saleID.String)
	}

	// Audited.
	var auditCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'voucher' AND action = 'import'`).Scan(&auditCount); err != nil {
		t.Fatalf("audit lookup: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit rows for import = %d, want 1", auditCount)
	}
}

// Re-committing the SAME file a second time must NOT double-credit any
// voucher (balance unchanged) and must report every row as a rejected
// collision, not silently merge/overwrite.
func TestVoucherImportPage_RecommitSameFileDoesNotDoubleCredit(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	repo := data.NewPOSRepo(d.Db)

	body, ct := voucherImportMultipart(t, voucherImportCSV, map[string]string{"commit": "1"})
	rec := postVoucherImport(mux, body, ct, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("first commit: code %d body %s", rec.Code, rec.Body.String())
	}
	v1Before, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M1")
	if err != nil {
		t.Fatalf("GS-M1 not created on first commit: %v", err)
	}

	// Re-commit the byte-identical file.
	body2, ct2 := voucherImportMultipart(t, voucherImportCSV, map[string]string{"commit": "1"})
	rec2 := postVoucherImport(mux, body2, ct2, &manager)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second commit: code %d body %s", rec2.Code, rec2.Body.String())
	}

	v1After, err := repo.GetVoucherBalance(t.Context(), nil, "GS-M1")
	if err != nil {
		t.Fatalf("GS-M1 lookup after re-commit: %v", err)
	}
	if v1After.BalanceMinor != v1Before.BalanceMinor {
		t.Fatalf("balance changed on re-commit: before=%d after=%d, want unchanged", v1Before.BalanceMinor, v1After.BalanceMinor)
	}

	// Both rows of the second commit must be reported as rejected
	// collisions, not silently dropped/merged.
	if !strings.Contains(rec2.Body.String(), "GS-M1") || !strings.Contains(rec2.Body.String(), "GS-M2") {
		t.Fatalf("second commit response must report BOTH colliding codes, got: %s", rec2.Body.String())
	}

	// Still only one voucher_transactions 'issue' row per voucher — the
	// second commit's collision must not have recorded a second issue.
	var txCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-M1' AND type = 'issue'`).Scan(&txCount); err != nil {
		t.Fatalf("count voucher_transactions: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("voucher_transactions 'issue' rows for GS-M1 = %d, want 1 (no double-credit)", txCount)
	}
}

// A row with a bad/missing balance is rejected and reported; valid rows in
// the same file still import.
func TestVoucherImportPage_BadBalanceRowRejected_ValidRowsStillImport(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	repo := data.NewPOSRepo(d.Db)

	csv := "code,balance,label\n" +
		"GS-GOOD,15.00,Carol\n" +
		"GS-BAD,notanumber,Dave\n" +
		",5.00,NoCode\n"

	body, ct := voucherImportMultipart(t, csv, map[string]string{"commit": "1"})
	rec := postVoucherImport(mux, body, ct, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	good, err := repo.GetVoucherBalance(t.Context(), nil, "GS-GOOD")
	if err != nil {
		t.Fatalf("GS-GOOD not created despite being a valid row: %v", err)
	}
	if good.BalanceMinor != 1500 {
		t.Fatalf("GS-GOOD balance = %d, want 1500", good.BalanceMinor)
	}
	if _, err := repo.GetVoucherBalance(t.Context(), nil, "GS-BAD"); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("GS-BAD must not have been created: err = %v", err)
	}
	if !strings.Contains(rec.Body.String(), "GS-BAD") {
		t.Fatalf("response must report the bad-balance row, got: %s", rec.Body.String())
	}

	var voucherCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM vouchers`).Scan(&voucherCount); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if voucherCount != 1 {
		t.Fatalf("vouchers created = %d, want 1 (only GS-GOOD)", voucherCount)
	}
}
