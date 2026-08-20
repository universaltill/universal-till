package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	_ "modernc.org/sqlite"
)

// newHoldTestDeps wires just enough for registerHoldAPI: a held_sales
// table (migrations/002_held_sales.sql) and a stub-resolver Engine, no
// catalog/plugins scaffolding needed since hold/resume never touch them.
// i18n is initialised explicitly (rather than relying on some other test
// in the package having already called httpx.InitI18n) so toast-copy
// assertions are deterministic regardless of test run order.
func newHoldTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE held_sales (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', total_minor INTEGER NOT NULL DEFAULT 0, line_count INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL, table_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')));`); err != nil {
		t.Fatalf("create held_sales: %v", err)
	}
	// ut-docs#820: POST /api/pos/held/table validates the target table is
	// free via POSRepo.IsTableFree, which queries `tables` and `held_sales`
	// directly -- needed even by tests that never assign a table.
	if _, err := db.Exec(`CREATE TABLE tables (id TEXT PRIMARY KEY, label TEXT NOT NULL, area_zone TEXT NOT NULL DEFAULT '', seat_count INTEGER NOT NULL DEFAULT 0, shape TEXT NOT NULL DEFAULT 'rect', pos_x INTEGER NOT NULL DEFAULT 0, pos_y INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Db:     db,
		Engine: engine,
		State:  common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
	}
	mux := http.NewServeMux()
	registerHoldAPI(mux, dp)
	return mux, dp
}

func TestHoldHandler_EmptyBasketRejected(t *testing.T) {
	mux, _ := newHoldTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast, not an HTTP error), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "empty") {
		t.Fatalf("expected an empty-basket toast, got: %s", rec.Body.String())
	}
}

func TestHoldThenResume_RestoresBasketAndClearsHeldRow(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed on hold, got %q", rec.Header().Get("HX-Trigger"))
	}
	if dp.Engine.HasItems() {
		t.Fatalf("expected the live basket to be cleared after hold")
	}

	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("expected a held_sales row: %v", err)
	}
	if id == "" {
		t.Fatalf("expected a non-empty held sale id")
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeRec := httptest.NewRecorder()
	mux.ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	if resumeRec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed on resume, got %q", resumeRec.Header().Get("HX-Trigger"))
	}
	if !dp.Engine.HasItems() {
		t.Fatalf("expected the basket to be restored after resume")
	}
	line := dp.Engine.Basket().Lines[0]
	if line.SKU != "ABC" || line.Qty != 1 || line.PriceCents.Minor() != 100 {
		t.Fatalf("expected the restored line to be ABC qty=1 price=100, got SKU=%q qty=%v price=%v", line.SKU, line.Qty, line.PriceCents.Minor())
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&count); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the held_sales row to be consumed on resume, got %d rows left", count)
	}
}

func TestResumeHandler_RequiresID(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	// Assert the HTTP-visible contract (nothing got restored) rather than
	// matching prose: the handler renders httpx.T("hold.error.not_found"),
	// whose English copy ("Held sale not found") never contains the raw
	// key "not_found" -- a locale-copy match here would just couple the
	// test to English strings.
	if dp.Engine.HasItems() {
		t.Fatalf("expected no basket to be restored when id is missing")
	}
}

func TestResumeHandler_UnknownIDRejected(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id=hold-does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if dp.Engine.HasItems() {
		t.Fatalf("expected an unknown id to be refused, not silently restore a basket")
	}
}

func TestResumeHandler_RejectsWhenBasketAlreadyBusy(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// Hold it so there's a real held row, then start a NEW live sale before
	// trying to resume -- the handler must refuse rather than overwrite an
	// in-progress basket.
	holdReq := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	mux.ServeHTTP(httptest.NewRecorder(), holdReq)
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("expected a held_sales row: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("start a new live sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	// This is the only way to confirm the request actually hit the
	// HasItems() busy branch and not the unrelated not-found path -- both
	// otherwise leave the held row untouched.
	if !strings.Contains(rec.Body.String(), httpx.T("en", "hold.error.busy")) {
		t.Fatalf("expected the busy toast, got: %s", rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&count); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the held row to survive a rejected resume, got %d rows", count)
	}
}

func TestHoldHandler_LabelsWithCustomerNameWhenSet(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetCustomer("cust1", "Jane Doe")

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var label string
	if err := dp.Db.QueryRow(`SELECT label FROM held_sales`).Scan(&label); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if label != "Jane Doe" {
		t.Fatalf("expected the held sale to be labelled with the customer name, got %q", label)
	}
}

func TestHoldHandler_ExplicitLabelOverridesCustomerName(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetCustomer("cust1", "Jane Doe")

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", strings.NewReader("label=Table+4"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var label string
	if err := dp.Db.QueryRow(`SELECT label FROM held_sales`).Scan(&label); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if label != "Table 4" {
		t.Fatalf("expected an explicit label to win over the attached customer name, got %q", label)
	}
}

func TestHoldHandler_ExplicitLabelWithNoCustomer(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", strings.NewReader("label=Haaft+1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var label string
	if err := dp.Db.QueryRow(`SELECT label FROM held_sales`).Scan(&label); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if label != "Haaft 1" {
		t.Fatalf("expected the explicit label to be stored even with no customer attached, got %q", label)
	}
}

func TestHoldHandler_LabelTruncatedAtMaxRunes(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	huge := strings.Repeat("é", 5000) // multi-byte rune, exercises rune- not byte-based truncation
	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", strings.NewReader("label="+huge))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var label string
	if err := dp.Db.QueryRow(`SELECT label FROM held_sales`).Scan(&label); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	runes := []rune(label)
	if len(runes) != 64 {
		preview := runes
		if len(preview) > 20 {
			preview = preview[:20]
		}
		t.Fatalf("expected the label truncated to 64 runes, got %d runes (%q…)", len(runes), string(preview))
	}
}

func TestHeldStrip_ListsHeldSalesAsChips(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdReq := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	mux.ServeHTTP(httptest.NewRecorder(), holdReq)

	req := httptest.NewRequest(http.MethodGet, "/ui/held", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/pos/resume"`) {
		t.Fatalf("expected a resume chip in the held strip, got: %s", body)
	}
	if !strings.Contains(body, "1 ×") {
		t.Fatalf("expected the chip to show the 1-line count, got: %s", body)
	}
}

// ut-docs#820: a table assigned to the live basket survives hold -> the
// held_sales row -> resume, and shows on the held-strip chip in between.
func TestHoldThenResume_PreservesTable(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetTable("tbl-1", "T1")

	holdReq := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, holdReq)
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales`).Scan(&tableID); err != nil {
		t.Fatalf("query held_sales.table_id: %v", err)
	}
	if tableID != "tbl-1" {
		t.Fatalf("held sale table_id = %q, want tbl-1", tableID)
	}

	stripReq := httptest.NewRequest(http.MethodGet, "/ui/held", nil)
	stripRec := httptest.NewRecorder()
	mux.ServeHTTP(stripRec, stripReq)
	if !strings.Contains(stripRec.Body.String(), "T1") {
		t.Fatalf("expected the held strip to show the table label T1, got: %s", stripRec.Body.String())
	}

	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeRec := httptest.NewRecorder()
	mux.ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "tbl-1" {
		t.Fatalf("resumed basket TableID = %q, want tbl-1", got)
	}
	if got := dp.Engine.Basket().TableLabel; got != "T1" {
		t.Fatalf("resumed basket TableLabel = %q, want T1", got)
	}
}

// TestHeldTableHandler_MovesToFreeTable (ut-docs#820): the "move a parked
// order to a different table" operation. It updates only the held_sales
// row's table_id, without resuming it into the live basket.
func TestHeldTableHandler_MovesToFreeTable(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}','tbl-1')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id=h1&table_id=tbl-2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed, got %q", rec.Header().Get("HX-Trigger"))
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id='h1'`).Scan(&tableID); err != nil {
		t.Fatalf("query held_sales.table_id: %v", err)
	}
	if tableID != "tbl-2" {
		t.Fatalf("held sale table_id after move = %q, want tbl-2", tableID)
	}
	if !strings.Contains(rec.Body.String(), "T2") {
		t.Fatalf("expected the re-rendered held strip to show T2, got: %s", rec.Body.String())
	}
}

// Moving onto a table another held sale already occupies must be rejected,
// leaving both held sales' table assignments untouched.
func TestHeldTableHandler_RejectsOccupiedTarget(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES
 ('h1','Table 1',100,1,'{}','tbl-1'),
 ('h2','Table 2',100,1,'{}','tbl-2')`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id=h1&table_id=tbl-2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place rejection), got %d: %s", rec.Code, rec.Body.String())
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id='h1'`).Scan(&tableID); err != nil {
		t.Fatalf("query held_sales.table_id: %v", err)
	}
	if tableID != "tbl-1" {
		t.Fatalf("h1's table_id must be unchanged after a rejected move, got %q", tableID)
	}
}

// A held sale may move back onto ITS OWN current table (a no-op from the
// operator's perspective) without being rejected as "occupied" -- the same
// self-occupancy exclusion IsTableFree provides.
func TestHeldTableHandler_MoveOntoOwnCurrentTableSucceeds(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}','tbl-1')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id=h1&table_id=tbl-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id='h1'`).Scan(&tableID); err != nil {
		t.Fatalf("query held_sales.table_id: %v", err)
	}
	if tableID != "tbl-1" {
		t.Fatalf("h1's table_id = %q, want tbl-1", tableID)
	}
}

func TestHeldStrip_EmptyWhenNothingHeld(t *testing.T) {
	mux, _ := newHoldTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/held", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "held-chip") {
		t.Fatalf("expected no chips when nothing is held, got: %s", rec.Body.String())
	}
}
