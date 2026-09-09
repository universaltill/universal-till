package pages

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// dataAPITestManagerPIN is the known PIN seedDataAPIManager sets on the
// manager it creates (ut-docs#1841) — real tests against checkStepUp's
// AuthorizeManager/Can path need a genuine PIN to authenticate, unlike the
// old typed-word confirmation these endpoints used to gate on.
const dataAPITestManagerPIN = "246800"

func newDataAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	// The purge/restore handlers route their refusal messages through
	// httpx.T -- without a real i18n load, T falls back to the raw key and
	// fmt.Sprintf's %s stays unconsumed (independent review, ut-docs#661:
	// this previously let a test assert on that literal fallback text
	// without ever exercising a real translated, interpolated message).
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	seedDataAPIManager(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerDataAPI(mux, dp)
	return mux, dp
}

// seedDataAPIManager creates a manager user with a known, real PIN
// (dataAPITestManagerPIN) so tests can drive checkStepUp's real
// AuthorizeManager/Can path against the four elevation-wired Data
// endpoints (ut-docs#1841) — UT_AUTH=off alone bypasses canPerform
// entirely and has no bearing on checkStepUp, which always needs a real
// PIN regardless of that escape hatch. Returns the manager's real id, so a
// test can assert an audit row's actor/approver against it directly
// instead of a hardcoded literal.
func seedDataAPIManager(t *testing.T, db *sql.DB) string {
	t.Helper()
	authRepo := data.NewAuthRepo(db)
	id, err := authRepo.CreateUser(t.Context(), "dataapi-mgr", "Data API Manager", "manager")
	if err != nil {
		t.Fatalf("seedDataAPIManager: create user: %v", err)
	}
	hash, err := auth.HashPIN(dataAPITestManagerPIN)
	if err != nil {
		t.Fatalf("seedDataAPIManager: hash pin: %v", err)
	}
	if err := authRepo.SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatalf("seedDataAPIManager: set pin: %v", err)
	}
	return id
}

func dataAPIJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	return out
}

// Every endpoint on this API is manager-gated and returns JSON, not an
// HTML redirect (unlike page handlers) -- without UT_AUTH=off and no
// signed-in manager in the request context, every one of them must
// refuse with 403 before touching the DB.
func TestDataAPI_AllEndpointsRequireManager(t *testing.T) {
	mux, _ := newDataAPITestDeps(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/data/reset-transactions"},
		{http.MethodGet, "/api/data/customers"},
		{http.MethodPost, "/api/data/customers/erase"},
		{http.MethodGet, "/api/data/obsolete-items"},
		{http.MethodPost, "/api/data/cleanup-catalog"},
		{http.MethodGet, "/api/data/reset-archives"},
		{http.MethodPost, "/api/data/reset-archives/some-batch/restore"},
		{http.MethodPost, "/api/data/reset-archives/some-batch/purge"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403, got %d: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		body := dataAPIJSONBody(t, rec)
		if body["data"] != nil {
			t.Errorf("%s %s: expected data:null, got %+v", c.method, c.path, body)
		}
		if s, _ := body["error"].(string); s == "" {
			t.Errorf("%s %s: expected a non-empty error message, got %+v", c.method, c.path, body)
		}
	}
}

// ut-docs#1841 (ADR-0087): the typed RESET word is gone — a manager session
// (UT_AUTH=off passes the first canPerform gate trivially) with NO
// override_pin at all must get the elevation prompt, not a 400, and must
// touch no data whatsoever.
func TestResetTransactions_NoPIN_NeedsElevation_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/reset-transactions", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected the elevation prompt's text/html Content-Type, got %q: %s", ct, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected NO mutation without a PIN, got %d sales rows (want 1 untouched)", count)
	}
}

// A wrong PIN is a real failed attempt: the prompt re-renders with the
// invalid-PIN error key, and still no mutation.
func TestResetTransactions_WrongPIN_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {"000000"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid PIN") {
		t.Fatalf("expected the invalid-PIN error (elevation.error_invalid_pin) in the re-rendered prompt, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected NO mutation on a wrong PIN, got %d sales rows (want 1 untouched)", count)
	}
}

func TestResetTransactions_ClearsSalesWhenConfirmed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	msg, _ := data["message"].(string)
	// ADR-0042: the records are archived, not destroyed — the response says
	// so (and must NOT claim anything was permanently cleared/deleted).
	if data == nil || !strings.HasPrefix(msg, "archived 1 sales") {
		t.Fatalf("expected an archived-count message under data, got %+v", body)
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected sales cleared, got %d rows left", count)
	}
	// ...and moved into the archive, tagged with a batch.
	var archived, batches int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if archived != 1 || batches != 1 {
		t.Fatalf("expected 1 archived sale in 1 batch, got %d/%d", archived, batches)
	}
	// The catalog is explicitly NOT part of what reset-transactions
	// touches (docs comment in data_api.go) -- confirm at the HTTP layer,
	// not just in the repo-level test, since this action is irreversible.
	var itm1 int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='itm1'`).Scan(&itm1); err != nil {
		t.Fatal(err)
	}
	if itm1 != 1 {
		t.Fatalf("expected the catalog to survive a transaction reset")
	}
}

// ADR-0042: a reset moves everything into an archive batch; the batch is
// listable and restorable (whole-batch, typed RESTORE confirmation) as long
// as the till has not traded since.
func TestResetArchives_ListAndRestoreRoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// List: { data: { batches: [ {id, created_at, actor_id, sales_count} ] } }
	req := httptest.NewRequest(http.MethodGet, "/api/data/reset-archives", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("list: expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	batches, _ := data["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("list: expected 1 batch, got %+v", body)
	}
	batch, _ := batches[0].(map[string]any)
	id, _ := batch["id"].(string)
	if id == "" {
		t.Fatalf("list: batch must carry a snake_case id, got %+v", batch)
	}
	if sc, _ := batch["sales_count"].(float64); sc != 1 {
		t.Fatalf("list: sales_count = %v, want 1", batch["sales_count"])
	}
	if created, _ := batch["created_at"].(string); created == "" || !strings.Contains(created, "T") {
		t.Fatalf("list: created_at must be ISO-8601, got %+v", batch)
	}
	// ut-docs#698: a batch just archived (created "now") is fully inside
	// its retention window -- not purgeable, and it must carry a
	// retained-until date so the UI can show it.
	if purgeable, _ := batch["purgeable"].(bool); purgeable {
		t.Fatalf("list: freshly-archived batch with real sales must not be purgeable yet, got %+v", batch)
	}
	if ru, _ := batch["retained_until"].(string); ru == "" {
		t.Fatalf("list: gated batch must carry a retained_until date, got %+v", batch)
	}

	// Restore requires step-up re-authentication (ut-docs#1841, ADR-0087) —
	// a wrong PIN is refused with the elevation prompt, not a mutation.
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/restore", url.Values{"override_pin": {"000000"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore with wrong PIN: expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid PIN") {
		t.Fatalf("expected the invalid-PIN error in the re-rendered prompt, got: %s", rec.Body.String())
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var live, archived, remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if live != 1 || archived != 0 || remaining != 0 {
		t.Fatalf("after restore: live=%d archived=%d batches=%d, want 1/0/0", live, archived, remaining)
	}
}

func TestResetArchivesRestore_NotFoundAndConflict(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)

	// Unknown batch → 404, not a 500/panic.
	rec := postForm(mux, "/api/data/reset-archives/no-such-batch/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown batch: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Reset, then trade: restore must refuse with 409 Conflict (ADR-0042 §2).
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var batchID string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('post1','R001','completed','sale','GBP',50,0,0,50,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+batchID+"/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("restore after trading: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if s, _ := body["error"].(string); s == "" {
		t.Fatalf("409 must carry a clear error message, got %+v", body)
	}
	// Refusal touches nothing.
	var live, archived int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 1 || archived != 1 {
		t.Fatalf("after refused restore: live=%d archived=%d, want 1/1", live, archived)
	}
}

// ut-docs#661: POST .../purge is the permanent delete ADR-0042 §3 left
// unbuilt. These pin the handler's own contract (status codes) over the
// fixture DB, which has no country configured -- so every purge here falls
// back to GlobalArchiveMinDays, same as a shop that never picked a country.
//
// ut-docs#1841 (ADR-0087): the typed PURGE word is gone — no override_pin
// at all must get the elevation prompt, and touch nothing.
func TestResetArchivesPurge_NoPIN_NeedsElevation_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge without a PIN: expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected the elevation prompt's text/html Content-Type, got %q: %s", ct, rec.Body.String())
	}
	var remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("a PIN-less purge attempt must touch nothing, got %d batches remaining", remaining)
	}
}

// A wrong PIN on purge is a real failed attempt: prompt re-renders with the
// invalid-PIN error, and still no mutation.
func TestResetArchivesPurge_WrongPIN_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {"000000"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge with wrong PIN: expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid PIN") {
		t.Fatalf("expected the invalid-PIN error (elevation.error_invalid_pin) in the re-rendered prompt, got: %s", rec.Body.String())
	}
	var remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("a wrong-PIN purge attempt must touch nothing, got %d batches remaining", remaining)
	}
}

func TestResetArchivesPurge_UnknownBatchNotFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)
	rec := postForm(mux, "/api/data/reset-archives/no-such-batch/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown batch: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestResetArchivesPurge_WithinGlobalFloorRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// created_at is "now" -- well inside the 10-year global floor, no
	// country configured.
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("purge within window: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	errMsg, _ := body["error"].(string)
	// The batch was archived "now" with no country configured, so the
	// retained-until date is exactly GlobalArchiveMinDays out from today.
	// Asserting the real interpolated date (not just "contains a year")
	// catches both a missing/broken i18n key and a %s left unconsumed by
	// fmt.Sprintf (independent review, ut-docs#661).
	wantDate := time.Now().UTC().AddDate(0, 0, int(data.GlobalArchiveMinDays)).Format("2006-01-02")
	if !strings.Contains(errMsg, wantDate) {
		t.Fatalf("409 message should name the retained-until date %s, got %q", wantDate, errMsg)
	}
	if strings.Contains(errMsg, "%!") {
		t.Fatalf("409 message has an unconsumed format verb -- i18n key/interpolation broken, got %q", errMsg)
	}
	var remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("refused purge must keep the batch, got %d remaining", remaining)
	}
}

func TestResetArchivesPurge_NoTradingHistoryDeletesImmediately(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	// No sale at all -- reset still writes a header row with sales_count=0.
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge of a zero-sales batch: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("no-trading-history batch should purge unconditionally, got %d remaining", remaining)
	}
}

func TestResetArchivesPurge_OutsideWindowDeletes(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE reset_batches SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -3651).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}

	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge outside window: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var remaining, archived int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || archived != 0 {
		t.Fatalf("after purge: batches=%d sales_archive=%d, want 0/0", remaining, archived)
	}
}

// ut-docs#698: GET /api/data/reset-archives must report purgeable=true (and
// a retained_until date, since the sales-count gate still applied) for a
// batch whose retention window has already elapsed -- the same result
// TestResetArchivesPurge_OutsideWindowDeletes proves the POST purge itself
// accepts, but observed read-only, before any purge is attempted.
func TestResetArchivesList_OutsideWindowIsPurgeable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE reset_batches SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -3651).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/reset-archives", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	data, _ := body["data"].(map[string]any)
	batches, _ := data["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("list: expected 1 batch, got %+v", body)
	}
	batch, _ := batches[0].(map[string]any)
	if purgeable, _ := batch["purgeable"].(bool); !purgeable {
		t.Fatalf("list: batch past its retention window must be purgeable, got %+v", batch)
	}
	if ru, _ := batch["retained_until"].(string); ru == "" {
		t.Fatalf("list: a gated batch must still carry the date it became purgeable, got %+v", batch)
	}
}

// TestResetArchivesPurge_CountryConfiguredWindow exercises the full stack
// (handler -> DeleteResetBatch -> country_settings) end-to-end: this needs
// the REAL migrated DB, since the fixture DB in newDataAPITestDeps has no
// country_settings table (see newRealDBDataAPIDeps's own comment).
func TestResetArchivesPurge_CountryConfiguredWindow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRealDBDataAPIDeps(t)
	// GB is seeded builtin by migration 041; override its retention to a
	// small value directly so the test doesn't need to wait out (or fake)
	// the real 10-year floor.
	if _, err := dp.Db.Exec(`UPDATE country_settings SET archive_min_days = 5 WHERE code = 'GB'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// 1 day inside the 5-day window -- refused.
	if _, err := dp.Db.Exec(`UPDATE reset_batches SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -4).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("purge 1 day inside GB's window: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	// 1 day past the 5-day window -- deletes.
	if _, err := dp.Db.Exec(`UPDATE reset_batches SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -6).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge 1 day past GB's window: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// newRealDBDataAPIDeps wires registerDataAPI over a REAL migrated database
// (internal/db.Open), not the seedForPages fixture: that fixture's
// hand-rolled sale_lines table (ui_smoke_test.go) declares only the sale_id
// FK, not item_id -> items — the exact fixture-drift trap the tester skill
// warns about (identical to the demo_seed_opt_in_test.go precedent this
// mirrors). TestResetArchivesRestore_ReferencesRemovedItem below needs the
// REAL FK to be enforced to mean anything.
func newRealDBDataAPIDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "reset-archive-real.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	seedDataAPIManager(t, dbo.DB)
	dp := &common.Deps{
		Db:       dbo.DB,
		Settings: settings.NewStore(dbo.DB),
		Cfg:      &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}},
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		AuthSvc:  auth.NewService(dbo.DB),
	}
	mux := http.NewServeMux()
	registerDataAPI(mux, dp)
	return mux, dp
}

// Independent review, ut-docs#187: with sale_lines emptied by reset, an
// item-cleanup action (catalog cleanup / "Remove sample data") sees no live
// reference and deletes the item an archived batch still points to. Restore
// must then refuse with 422, not a raw 500 carrying a SQL error string.
func TestResetArchivesRestore_ReferencesRemovedItem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRealDBDataAPIDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm-reset187','SKU-RESET187','Widget',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('l1','s1',1,'itm-reset187','Widget',1,100,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var batchID string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	// sale_lines is empty now, so the item looks unreferenced and safe to
	// remove — exactly what "Remove sample data"/catalog cleanup do.
	if _, err := dp.Db.ExecContext(t.Context(), `DELETE FROM items WHERE id='itm-reset187'`); err != nil {
		t.Fatal(err)
	}

	rec = postForm(mux, "/api/data/reset-archives/"+batchID+"/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("restore after item removed: expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if s, _ := body["error"].(string); s == "" {
		t.Fatalf("422 must carry a clear error message, got %+v", body)
	}
	// Refusal touches nothing: live tables stay empty, archive intact.
	var live, archived int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 0 || archived != 1 {
		t.Fatalf("after refused restore: live=%d archived=%d, want 0/1", live, archived)
	}
}

func TestGetCustomers_SearchesByQuery(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name,phone,email) VALUES('cust1','Jane Doe','555','jane@example.com')`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/customers?q=Jane", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("expected Jane Doe in the search results, got: %s", rec.Body.String())
	}
	// Envelope: { "data": { "customers": [...] }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["customers"] == nil {
		t.Fatalf("expected customers nested under data, got %+v", body)
	}
}

func TestEraseCustomer_RequiresIDAndReportsNotFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)

	rec := postForm(mux, "/api/data/customers/erase", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without an id, got %d: %s", rec.Code, rec.Body.String())
	}

	// A real PIN, since the id is what's under test here, not the
	// elevation gate itself (that's TestEraseCustomer_NoPIN_NeedsElevation_NoMutation
	// and TestEraseCustomer_ElevatedByApprover_ErasesAndRecordsApprover below).
	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=does-not-exist&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown customer, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ut-docs#1860 (ADR-0087): erase used to be a one-click flat-403 endpoint —
// the one GDPR/destructive gap #1841's own scope (the four data_api.go
// handlers) didn't cover. No override_pin at all must get the elevation
// prompt, referencing the customer by id (not name/phone/email — those are
// exactly the fields about to be erased), and must touch nothing.
func TestEraseCustomer_NoPIN_NeedsElevation_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name) VALUES('cust1','Jane Doe')`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/customers/erase", url.Values{"id": {"cust1"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected the elevation prompt's text/html Content-Type, got %q: %s", ct, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("expected the elevation summary to reference the customer by id, NOT by name (that's exactly the PII about to be erased), got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cust1") {
		t.Fatalf("expected the elevation summary to reference the customer id, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE id='cust1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected NO mutation without a PIN, still want the customer present, got %d rows", count)
	}
}

// The full elevated-erase + audit-actor assertion lives in
// TestDataManagementEndpoints_ValidPIN_AuditRecordsApprover's "customer-erase"
// case below, alongside the other three checkStepUp-gated endpoints, rather
// than duplicating that table's setup/assertion shape here.

func TestGetObsoleteItems_ListsInactiveNeverSoldItems(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/obsolete-items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Old Discontinued Product") {
		t.Fatalf("expected the obsolete item listed, got: %s", rec.Body.String())
	}
	// itm1 (seedForPages) is active with stock, and must NOT show up.
	if strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("expected the active seeded item to be excluded, got: %s", rec.Body.String())
	}
	// Envelope: { "data": { "items": [...] }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["items"] == nil {
		t.Fatalf("expected items nested under data, got %+v", body)
	}
}

// ut-docs#1841 (ADR-0087): the typed CLEANUP word is gone — no
// override_pin at all must get the elevation prompt, carrying the item
// count in its summary, and touch nothing.
func TestCleanupCatalog_NoPIN_NeedsElevation_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/cleanup-catalog", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected the elevation prompt's text/html Content-Type, got %q: %s", ct, rec.Body.String())
	}
	// The summary states the count (ut-docs#1841's own design) — 1 obsolete
	// item is staged above.
	if !strings.Contains(rec.Body.String(), "1 inactive product") {
		t.Fatalf("expected the elevation summary to state the obsolete-item count, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='obs1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected NO mutation without a PIN, still want the obsolete item present, got %d rows", count)
	}
}

// A wrong PIN is a real failed attempt: prompt re-renders with the
// invalid-PIN error, and still no mutation.
func TestCleanupCatalog_WrongPIN_NoMutation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/cleanup-catalog", url.Values{"override_pin": {"000000"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (elevation prompt), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid PIN") {
		t.Fatalf("expected the invalid-PIN error (elevation.error_invalid_pin) in the re-rendered prompt, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='obs1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected NO mutation on a wrong PIN, still want the obsolete item present, got %d rows", count)
	}
}

func TestCleanupCatalog_RemovesObsoleteItemsWhenConfirmed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/data/cleanup-catalog", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='obs1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the obsolete item removed, still found %d rows", count)
	}
	// itm1 (seedForPages, active) must survive a catalog cleanup.
	var itm1 int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='itm1'`).Scan(&itm1); err != nil {
		t.Fatal(err)
	}
	if itm1 != 1 {
		t.Fatalf("expected the active seeded item to survive cleanup")
	}
}

// ut-docs#1841 (ADR-0087): a valid manager PIN records the APPROVER's id as
// the audit row's actor — not the session user checkStepUp actually
// blocked — with dual attribution (InsertAuditElevated's blocked_actor_id)
// carrying the originally-blocked session user. Uses a REAL second manager
// as the session (not just an auth.WithUser-attached synthetic id) because
// audit_log.blocked_actor_id has a real FK against users(id), enforced
// (PRAGMA foreign_keys=ON) — a synthetic session id would violate it the
// moment dual attribution is exercised.
func TestDataManagementEndpoints_ValidPIN_AuditRecordsApprover(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// resetForApproverTest performs a plain reset-transactions (as the
	// seeded manager, via its own PIN, no approver-attribution assertions
	// of its own) and returns the resulting batch id, for the restore/purge
	// cases below which need a real batch to act on. withSale=false (the
	// purge case) keeps sales_count at 0 so the batch purges unconditionally
	// (TestResetArchivesPurge_NoTradingHistoryDeletesImmediately's own
	// precedent) rather than tripping the real-trading-history retention
	// gate this test has no interest in exercising.
	resetForApproverTest := func(t *testing.T, mux *http.ServeMux, dp *common.Deps, withSale bool) string {
		t.Helper()
		if withSale {
			if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
				t.Fatal(err)
			}
		}
		rec := postForm(mux, "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("setup reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var id string
		if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	cases := []struct {
		name       string
		setup      func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (path string, form url.Values)
		entityType string
		entityID   string
		action     string
	}{
		{
			name: "reset-transactions",
			setup: func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (string, url.Values) {
				if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
					t.Fatal(err)
				}
				return "/api/data/reset-transactions", url.Values{"override_pin": {dataAPITestManagerPIN}}
			},
			entityType: "system", entityID: "transactions", action: "transaction_history_reset",
		},
		{
			name: "reset-archives-restore",
			setup: func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (string, url.Values) {
				id := resetForApproverTest(t, mux, dp, true)
				return "/api/data/reset-archives/" + id + "/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}
			},
			entityType: "system", entityID: "transactions", action: "transaction_history_restored",
		},
		{
			name: "reset-archives-purge",
			setup: func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (string, url.Values) {
				id := resetForApproverTest(t, mux, dp, false)
				return "/api/data/reset-archives/" + id + "/purge", url.Values{"override_pin": {dataAPITestManagerPIN}}
			},
			entityType: "system", entityID: "transactions", action: "transaction_archive_purged",
		},
		{
			name: "cleanup-catalog",
			setup: func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (string, url.Values) {
				if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
					t.Fatal(err)
				}
				return "/api/data/cleanup-catalog", url.Values{"override_pin": {dataAPITestManagerPIN}}
			},
			entityType: "system", entityID: "catalog", action: "catalog_cleanup",
		},
		{
			name: "customer-erase",
			setup: func(t *testing.T, mux *http.ServeMux, dp *common.Deps) (string, url.Values) {
				if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name) VALUES('cust1','Jane Doe')`); err != nil {
					t.Fatal(err)
				}
				return "/api/data/customers/erase", url.Values{"id": {"cust1"}, "override_pin": {dataAPITestManagerPIN}}
			},
			entityType: "customer", entityID: "cust1", action: "customer_erased",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newDataAPITestDeps(t)
			authRepo := data.NewAuthRepo(dp.Db)
			sessionMgrID, err := authRepo.CreateUser(t.Context(), "session-mgr-"+tc.name, "Session Manager", "manager")
			if err != nil {
				t.Fatalf("create session manager: %v", err)
			}

			path, form := tc.setup(t, mux, dp)
			rec := postForm(mux, path, form, &auth.User{ID: sessionMgrID, Role: "manager"})
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			var actorID, blockedActorID sql.NullString
			if err := dp.Db.QueryRow(
				`SELECT actor_id, blocked_actor_id FROM audit_log WHERE entity_type = ? AND entity_id = ? AND action = ? ORDER BY created_at DESC LIMIT 1`,
				tc.entityType, tc.entityID, tc.action,
			).Scan(&actorID, &blockedActorID); err != nil {
				t.Fatalf("query audit_log: %v", err)
			}
			var approverID string
			if err := dp.Db.QueryRow(`SELECT id FROM users WHERE username = 'dataapi-mgr'`).Scan(&approverID); err != nil {
				t.Fatalf("look up seeded manager: %v", err)
			}
			if !actorID.Valid || actorID.String != approverID {
				t.Fatalf("audit actor_id = %v, want the approver's id %q (not the session user %q)", actorID, approverID, sessionMgrID)
			}
			if !blockedActorID.Valid || blockedActorID.String != sessionMgrID {
				t.Fatalf("audit blocked_actor_id = %v, want the originally-blocked session user %q", blockedActorID, sessionMgrID)
			}
		})
	}
}
