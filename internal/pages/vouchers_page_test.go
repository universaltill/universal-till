package pages

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// Gift vouchers page (ut-docs#4251): GET /vouchers (list + search/filter +
// import section), GET /ui/vouchers/list (the HTMX list fragment),
// GET /vouchers/{id} (one voucher's history) and GET /api/vouchers/export.
// Harness: newVoucherImportTestMux (real migrated DB, real auth.Service,
// seeded manager), which registers both this page and the import API.

var vManager = auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

func vGet(t *testing.T, mux *http.ServeMux, path string, user *auth.User, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func vSeed(t *testing.T, repo *data.POSRepo, v data.Voucher) {
	t.Helper()
	if v.BalanceMinor == 0 && v.Status == "" {
		v.BalanceMinor = v.OriginalAmountMinor
	}
	if v.Currency == "" {
		v.Currency = "GBP"
	}
	if err := repo.CreateVoucher(context.Background(), nil, v); err != nil {
		t.Fatalf("CreateVoucher %s: %v", v.ID, err)
	}
}

func TestVouchersPage_ForbiddenForCashierAndAnonymous(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	vSeed(t, data.NewPOSRepo(d.Db), data.Voucher{ID: "GS-1", OriginalAmountMinor: 500, CreatedAt: "2026-10-01T10:00:00Z"})
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	for _, path := range []string{"/vouchers", "/ui/vouchers/list", "/vouchers/GS-1", "/api/vouchers/export"} {
		for _, u := range []*auth.User{nil, &cashier} {
			rec := vGet(t, mux, path, u, false)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("GET %s as %v = %d, want 403", path, u, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "GS-1") {
				t.Fatalf("GET %s as %v leaked voucher data: %s", path, u, rec.Body.String())
			}
		}
	}
}

func TestVouchersPage_OldSettingsImportRouteIsGone(t *testing.T) {
	mux, _ := newVoucherImportTestMux(t)
	rec := vGet(t, mux, "/settings/vouchers/import", &vManager, false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /settings/vouchers/import = %d, want 404 (moved to /vouchers)", rec.Code)
	}
}

func TestVouchersPage_ListsEveryVoucherWithDetails(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('sale-1','R-0001',2500,2500)`); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	vSeed(t, repo, data.Voucher{ID: "GS-SOLD", HolderLabel: "Alice Example", OriginalAmountMinor: 2500, BalanceMinor: 1500, IssuedSaleID: "sale-1", CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-IMPORTED", HolderLabel: "Bob", OriginalAmountMinor: 1000, CreatedAt: "2026-10-02T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-USED", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "redeemed", CreatedAt: "2026-10-03T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-SINGLE", HolderLabel: "<b>Carol</b>", OriginalAmountMinor: 700, BalanceMinor: 0, Status: "void", Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-04T10:00:00Z"})

	rec := vGet(t, mux, "/vouchers?lang=en", &vManager, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vouchers = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		httpx.T("en", "vouchers.title"),
		"GS-SOLD", "GS-IMPORTED", "GS-USED", "GS-SINGLE",
		"Alice Example",
		httpx.FormatMoneyDisplay(2500, "en"), httpx.FormatMoneyDisplay(1500, "en"),
		httpx.T("en", "catalog.active"),
		httpx.T("en", "vouchers.status.redeemed"),
		httpx.T("en", "vouchers.status.void"),
		httpx.T("en", "pos.gift_voucher.use_specific"),
		`href="/journal/R-0001"`,
		`href="/vouchers/GS-SOLD"`,
		`id="vouchers-import-form"`, // import moved onto this page
		`hx-post="/api/vouchers/import"`,
		`href="/help/vouchers"`,
		`href="/api/vouchers/export"`,
		`type="search"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /vouchers missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<b>Carol</b>") {
		t.Fatal("holder label must be HTML-escaped")
	}
	// Newest first.
	if strings.Index(body, "GS-SINGLE") > strings.Index(body, "GS-SOLD") {
		t.Fatal("list must be newest first")
	}
	if strings.Contains(body, httpx.T("en", "vouchers.replica_note")) {
		t.Fatal("the replica note must not show on a main till")
	}
}

func TestVouchersPage_FragmentSearchAndStatusFilter(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-ALPHA", HolderLabel: "Alice", OriginalAmountMinor: 100, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-BETA", HolderLabel: "Bob", OriginalAmountMinor: 100, BalanceMinor: 0, Status: "redeemed", CreatedAt: "2026-10-02T10:00:00Z"})

	rec := vGet(t, mux, "/ui/vouchers/list?lang=en&q=ALI", &vManager, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `id="vouchers-import-form"`) {
		t.Fatalf("fragment must be just the list: %s", body)
	}
	if !strings.Contains(body, "GS-ALPHA") || strings.Contains(body, "GS-BETA") {
		t.Fatalf("q=ALI must match Alice's voucher only: %s", body)
	}
	// The export link carries the current filter.
	if !strings.Contains(body, `href="/api/vouchers/export?q=ALI"`) {
		t.Fatalf("export link must carry the filter: %s", body)
	}

	body = vGet(t, mux, "/ui/vouchers/list?lang=en&status=redeemed", &vManager, true).Body.String()
	if strings.Contains(body, "GS-ALPHA") || !strings.Contains(body, "GS-BETA") {
		t.Fatalf("status=redeemed: %s", body)
	}
	if !strings.Contains(body, `href="/api/vouchers/export?status=redeemed"`) {
		t.Fatalf("export link must carry the status: %s", body)
	}

	// An unknown status shows everything rather than erroring.
	body = vGet(t, mux, "/ui/vouchers/list?lang=en&status=bogus", &vManager, true).Body.String()
	if !strings.Contains(body, "GS-ALPHA") || !strings.Contains(body, "GS-BETA") {
		t.Fatalf("unknown status must fall back to all: %s", body)
	}

	// No match: says so (not the first-run empty state).
	body = vGet(t, mux, "/ui/vouchers/list?lang=en&q=zzz", &vManager, true).Body.String()
	if !strings.Contains(body, httpx.T("en", "vouchers.empty_filtered")) {
		t.Fatalf("no-match message missing: %s", body)
	}
}

// ut-docs#4257: the filtered list is addressable — an htmx search replaces
// the page URL (HX-Replace-Url) so Back/reload keep it.
func TestVouchersPage_FragmentReplacesURLWithNormalizedFilter(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	vSeed(t, data.NewPOSRepo(d.Db), data.Voucher{ID: "GS-1", HolderLabel: "Müller", OriginalAmountMinor: 100, CreatedAt: "2026-10-01T10:00:00Z"})

	cases := []struct {
		name, path string
		hx         bool
		want       string
	}{
		{"query and status", "/ui/vouchers/list?lang=en&q=M%C3%BCller&status=active", true, "/vouchers?q=M%C3%BCller&status=active"},
		{"no filter", "/ui/vouchers/list?lang=en", true, "/vouchers"},
		{"bogus status not echoed", "/ui/vouchers/list?lang=en&q=x&status=bogus", true, "/vouchers?q=x"},
		{"query is trimmed", "/ui/vouchers/list?lang=en&q=%20abc%20", true, "/vouchers?q=abc"},
		{"not htmx", "/ui/vouchers/list?lang=en&q=x", false, ""},
	}
	for _, c := range cases {
		rec := vGet(t, mux, c.path, &vManager, c.hx)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.name, rec.Code)
		}
		if got := rec.Header().Get("HX-Replace-Url"); got != c.want {
			t.Fatalf("%s: HX-Replace-Url = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVouchersPage_UnicodeSearchFromURLKeepsInputValue(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-M", HolderLabel: "Müller", OriginalAmountMinor: 100, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-S", HolderLabel: "Schmidt", OriginalAmountMinor: 100, CreatedAt: "2026-10-02T10:00:00Z"})

	body := vGet(t, mux, "/vouchers?lang=en&q="+url.QueryEscape("MÜLLER"), &vManager, false).Body.String()
	if !strings.Contains(body, "GS-M") || strings.Contains(body, "GS-S") {
		t.Fatalf("q=MÜLLER must list Müller's voucher only: %s", body)
	}
	if !strings.Contains(body, `name="q" value="MÜLLER"`) {
		t.Fatalf("search input must keep its value: %s", body)
	}
}

func TestVouchersPage_ExportRowOnlyWhenThereAreVouchers(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	const link = `/api/vouchers/export`
	for _, path := range []string{"/vouchers?lang=en", "/ui/vouchers/list?lang=en"} {
		if body := vGet(t, mux, path, &vManager, true).Body.String(); strings.Contains(body, link) || strings.Contains(body, httpx.T("en", "vouchers.export_help")) {
			t.Fatalf("GET %s on an empty list must not offer the export: %s", path, body)
		}
	}
	vSeed(t, data.NewPOSRepo(d.Db), data.Voucher{ID: "GS-1", OriginalAmountMinor: 100, CreatedAt: "2026-10-01T10:00:00Z"})
	for _, path := range []string{"/vouchers?lang=en", "/ui/vouchers/list?lang=en"} {
		if body := vGet(t, mux, path, &vManager, true).Body.String(); !strings.Contains(body, link) {
			t.Fatalf("GET %s with a voucher must offer the export: %s", path, body)
		}
	}
}

func TestVouchersPage_EmptyStateSaysWhatToDo(t *testing.T) {
	mux, _ := newVoucherImportTestMux(t)
	body := vGet(t, mux, "/vouchers?lang=en", &vManager, false).Body.String()
	if !strings.Contains(body, httpx.T("en", "vouchers.empty")) {
		t.Fatalf("empty state missing: %s", body)
	}
}

func TestVouchersPage_ListIsBoundedWithNote(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < vouchersPageLimit+1; i++ {
		vSeed(t, repo, data.Voucher{ID: fmt.Sprintf("GS-%04d", i), OriginalAmountMinor: 100, CreatedAt: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)})
	}
	body := vGet(t, mux, "/ui/vouchers/list?lang=en", &vManager, true).Body.String()
	note := fmt.Sprintf(httpx.T("en", "vouchers.limit_note"), vouchersPageLimit)
	if !strings.Contains(body, note) {
		t.Fatalf("limit note %q missing", note)
	}
	if strings.Contains(body, ">GS-0000<") {
		t.Fatal("the oldest voucher must be beyond the page bound")
	}
	if !strings.Contains(body, fmt.Sprintf(">GS-%04d<", vouchersPageLimit)) {
		t.Fatal("the newest voucher must be listed")
	}

	// The export is not bounded by the page limit.
	rec := vGet(t, mux, "/api/vouchers/export", &vManager, false)
	recs, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(recs) != vouchersPageLimit+2 { // header + every voucher
		t.Fatalf("export rows = %d, want %d", len(recs), vouchersPageLimit+2)
	}
}

func TestVouchersPage_DetailShowsHistory(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	ctx := context.Background()
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('sale-a','R-A',0,0), ('sale-b','R-B',0,0)`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	vSeed(t, repo, data.Voucher{ID: "GS/7", HolderLabel: "Dana", OriginalAmountMinor: 3000, BalanceMinor: 2000, IssuedSaleID: "sale-a", CreatedAt: "2026-10-01T09:00:00Z"})
	for _, tx := range []data.VoucherTransaction{
		{ID: "t1", VoucherID: "GS/7", SaleID: "sale-a", Type: "issue", AmountMinor: 3000, CreatedAt: "2026-10-01T09:00:00Z"},
		{ID: "t2", VoucherID: "GS/7", SaleID: "sale-b", Type: "redemption", AmountMinor: 1000, CreatedAt: "2026-10-05T09:00:00Z"},
	} {
		if err := repo.RecordVoucherTransaction(ctx, nil, tx); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rec := vGet(t, mux, "/vouchers/"+url.PathEscape("GS/7")+"?lang=en", &vManager, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	issued := strings.Index(body, httpx.T("en", "vouchers.history.type.issue"))
	redeemed := strings.Index(body, httpx.T("en", "vouchers.history.type.redemption"))
	if issued < 0 || redeemed < 0 || issued > redeemed {
		t.Fatalf("history must list issue then redemption: %s", body)
	}
	for _, want := range []string{"GS/7", "Dana", `href="/journal/R-A"`, `href="/journal/R-B"`, httpx.FormatMoneyDisplay(1000, "en"), `href="/vouchers"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q: %s", want, body)
		}
	}
}

func TestVouchersPage_DetailUnknownIs404Translated(t *testing.T) {
	mux, _ := newVoucherImportTestMux(t)
	rec := vGet(t, mux, "/vouchers/"+url.PathEscape(`<script>x</script>`)+"?lang=en", &vManager, false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, httpx.T("en", "vouchers.error.not_found")) {
		t.Fatalf("404 must carry the translated message: %s", body)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Fatal("the operator-typed id must never be echoed unescaped")
	}
}

func TestVouchersPage_ReplicaShowsNonModalNote(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	if err := d.Settings.Set(context.Background(), "sync.primary_url", "http://main-till.local:8080"); err != nil {
		t.Fatalf("set primary: %v", err)
	}
	body := vGet(t, mux, "/vouchers?lang=en", &vManager, false).Body.String()
	if !strings.Contains(body, httpx.T("en", "vouchers.replica_note")) {
		t.Fatalf("replica note missing: %s", body)
	}
	if !strings.Contains(body, `class="sync-banner" role="status"`) {
		t.Fatal("the replica note must be a non-modal status banner")
	}
}

func TestVouchersExport_CSVShapeFilterAndFormulaGuard(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", HolderLabel: "=HYPERLINK(\"x\")", OriginalAmountMinor: 2500, BalanceMinor: 1050, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "@GS-2", HolderLabel: "Bob", OriginalAmountMinor: 700, BalanceMinor: 0, Status: "void", Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-02T10:00:00Z"})

	rec := vGet(t, mux, "/api/vouchers/export", &vManager, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	wantDisp := `attachment; filename="vouchers-` + time.Now().Format("2006-01-02") + `.csv"`
	if got := rec.Header().Get("Content-Disposition"); got != wantDisp {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisp)
	}
	recs, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Join(recs[0], ",") != "code,balance,label,original_amount,status,purpose,created_at" {
		t.Fatalf("header = %v", recs[0])
	}
	if len(recs) != 3 {
		t.Fatalf("rows = %v", recs)
	}
	// Newest first: @GS-2 then GS-1.
	if strings.Join(recs[1], "|") != "'@GS-2|0.00|Bob|7.00|void|single_purpose|2026-10-02T10:00:00Z" {
		t.Fatalf("row 1 = %v", recs[1])
	}
	if strings.Join(recs[2], "|") != "GS-1|10.50|'=HYPERLINK(\"x\")|25.00|active|multi_purpose|2026-10-01T10:00:00Z" {
		t.Fatalf("row 2 = %v", recs[2])
	}

	// Filter honoured.
	recs, _ = csv.NewReader(strings.NewReader(vGet(t, mux, "/api/vouchers/export?status=active", &vManager, false).Body.String())).ReadAll()
	if len(recs) != 2 || recs[1][0] != "GS-1" {
		t.Fatalf("status filter: %v", recs)
	}
	recs, _ = csv.NewReader(strings.NewReader(vGet(t, mux, "/api/vouchers/export?q=bob", &vManager, false).Body.String())).ReadAll()
	if len(recs) != 2 || recs[1][0] != "'@GS-2" {
		t.Fatalf("q filter: %v", recs)
	}
}

// Round trip: what one till exports, another till's importer accepts —
// except single-purpose rows, refused with their own translated reason
// (re-importing them as multi-purpose would tax them twice).
func TestVouchersExport_RoundTripsThroughImport(t *testing.T) {
	src, sd := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(sd.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-RT1", HolderLabel: "-Alice", OriginalAmountMinor: 2500, BalanceMinor: 1250, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-RT2", OriginalAmountMinor: 900, CreatedAt: "2026-10-02T10:00:00Z", Purpose: data.VoucherPurposeSingle})
	exported := vGet(t, src, "/api/vouchers/export", &vManager, false).Body.String()

	dst, dd := newVoucherImportTestMux(t)
	body, ct := voucherImportMultipart(t, exported, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/vouchers/import?lang=en", body)
	req.Header.Set("Content-Type", ct)
	req = auth.WithUser(req, vManager)
	rec := httptest.NewRecorder()
	dst.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import of export = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers_import.status.single_purpose")) {
		t.Fatalf("single-purpose row must be refused with its translated reason: %s", rec.Body.String())
	}
	got, err := data.NewPOSRepo(dd.Db).GetVoucherBalance(context.Background(), nil, "GS-RT1")
	if err != nil {
		t.Fatalf("GS-RT1 not imported: %v", err)
	}
	if got.BalanceMinor != 1250 || got.HolderLabel != "-Alice" || got.Purpose != data.VoucherPurposeMulti {
		t.Fatalf("imported = %+v", got)
	}
	if _, err := data.NewPOSRepo(dd.Db).GetVoucherBalance(context.Background(), nil, "GS-RT2"); err == nil {
		t.Fatal("a single-purpose voucher must never be imported")
	}
}
