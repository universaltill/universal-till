package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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
	// ut-docs#1390/#1704: the live basket's table claim persists through hold
	// and is re-asserted on resume, and a held-order move transfers it, so the
	// claims table (migration 078) is part of every hold/resume/move round
	// trip, table-assigned or not (release is a no-op DELETE either way) --
	// column-identical to the migration.
	// till_id mirrors migration 008 (ut-docs#1703): resume's re-claim goes
	// through claimTableWriteThrough, whose local branch reconciles stale
	// claims against `tills` -- so this hand-rolled schema needs BOTH the
	// column and the tills table, or that branch errors and silently degrades
	// to the plain ClaimTable fallback, testing the wrong path.
	if _, err := db.Exec(`CREATE TABLE table_claims (table_id TEXT PRIMARY KEY REFERENCES tables(id), claimed_at TEXT NOT NULL, till_id TEXT NOT NULL DEFAULT '');`); err != nil {
		t.Fatalf("create table_claims: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE tills (id TEXT PRIMARY KEY, name TEXT NOT NULL, bearer_hash TEXT UNIQUE, enrolled_at TEXT NOT NULL DEFAULT (datetime('now')), last_seen_at TEXT);`); err != nil {
		t.Fatalf("create tills: %v", err)
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

// TestHoldThenResume_PreservesOrderType (ut-docs#1381, independent review):
// end-to-end through the REAL HTTP hold/resume handlers -- hold.go's own
// TestSnapshotRestoreRoundTrip_PreservesOrderType already pins Service's
// Snapshot()/Restore() contract directly, but not that this package's
// Hold/Resume handlers actually round-trip a real held_sales.payload
// column through json.Marshal/Unmarshal end to end.
func TestHoldThenResume_PreservesOrderType(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetOrderType(pos.OrderTypeTakeaway)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("expected a held_sales row: %v", err)
	}

	// Serve another (dine-in) customer in between.
	_, _ = dp.Engine.Scan("ABC")
	dp.Engine.Reset()

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), resumeReq)

	if got := dp.Engine.OrderType(); got != pos.OrderTypeTakeaway {
		t.Fatalf("resumed OrderType() = %q, want %q (silently reverted to dine-in)", got, pos.OrderTypeTakeaway)
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

	req := httptest.NewRequest(http.MethodPost, "/api/pos/hold", strings.NewReader("label=Tab+1"))
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
	if label != "Tab 1" {
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

// TestHeldStrip_RendersMoveControlToFreeTable (ut-docs#820 review B1): the
// held strip must expose a UI control that reaches POST /api/pos/held/table,
// otherwise the "move a held order to a different table" deliverable (and the
// manual's "tap Move table on the strip" instruction) is backend-only. The
// control offers only free tables, never the order's own current table.
func TestHeldStrip_RendersMoveControlToFreeTable(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetTable("tbl-1", "T1")
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/held", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/pos/held/table"`) {
		t.Fatalf("expected a Move control posting to /api/pos/held/table, got: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "basket.table.move")) {
		t.Fatalf("expected the Move-table label, got: %s", body)
	}
	// The free table T2 is offered; the order's own current table T1 is not a target.
	if !strings.Contains(body, `"table_id":"tbl-2"`) {
		t.Fatalf("expected T2 offered as a move target, got: %s", body)
	}
	if strings.Contains(body, `"table_id":"tbl-1"`) {
		t.Fatalf("the order's own current table must NOT be a move target, got: %s", body)
	}
}

// A shop with no tables configured gets no Move control at all (ADR-0054
// soft-gate) -- the strip is just resume chips, same as before this feature.
func TestHeldStrip_NoMoveControlWhenNoTables(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/held", nil))
	body := rec.Body.String()
	if strings.Contains(body, "/api/pos/held/table") {
		t.Fatalf("no-tables shop must not render a Move control, got: %s", body)
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

// TestHeldTableHandler_TakeawayOrderIgnoresTableMove (ut-docs#1381): a held
// Takeaway order's table assignment can never be set via this endpoint --
// mirrors Service.SetTable's own no-op-while-Takeaway rule, now enforceable
// here because OrderType survives into the held sale's own JSON payload
// (it has no dedicated held_sales column, unlike table_id).
func TestHeldTableHandler_TakeawayOrderIgnoresTableMove(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Takeaway 1',100,1,'{"order_type":"takeaway"}','')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id=h1&table_id=tbl-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place no-op), got %d: %s", rec.Code, rec.Body.String())
	}
	// COALESCE: repo.SetTable stores "" as SQL NULL (nullIfEmpty), which the
	// gate above should still be routing this request into.
	var tableID string
	if err := dp.Db.QueryRow(`SELECT COALESCE(table_id, '') FROM held_sales WHERE id='h1'`).Scan(&tableID); err != nil {
		t.Fatalf("query held_sales.table_id: %v", err)
	}
	if tableID != "" {
		t.Fatalf("a Takeaway held order's table_id must stay empty after an attempted move, got %q", tableID)
	}
}

// TestHeldStrip_NoMoveControlForTakeawayOrder (ut-docs#1381): the Move-table
// control must not render for a held Takeaway order even when a free table
// exists -- same soft-gate ut-docs#1355 already applies to the live sale's
// table picker (registerTablePicker).
func TestHeldStrip_NoMoveControlForTakeawayOrder(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Takeaway 1',100,1,'{"order_type":"takeaway"}','')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/held", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "/api/pos/held/table") {
		t.Fatalf("a Takeaway held order must not render a Move control, got: %s", body)
	}
}

// TestHoldMoveThenResume_ReflectsMovedTable (ut-docs#820) closes the gap
// between the two tests above: TestHeldTableHandler_MovesToFreeTable moves a
// parked order WITHOUT resuming it, and TestHoldThenResume_PreservesTable
// resumes one that was never moved -- so neither covers move-then-resume.
// That chain is where it matters: the move writes only held_sales.table_id,
// while resume restores the basket from held_sales.payload, so a resumed
// order used to silently revert to its PRE-move table and tender the sale
// (and its receipt/kitchen ticket) against the wrong one.
func TestHoldMoveThenResume_ReflectsMovedTable(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
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
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}

	moveReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id=tbl-2"))
	moveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	moveRec := httptest.NewRecorder()
	mux.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", moveRec.Code, moveRec.Body.String())
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeRec := httptest.NewRecorder()
	mux.ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "tbl-2" {
		t.Fatalf("resumed basket TableID = %q, want tbl-2 (the MOVED table)", got)
	}
	if got := dp.Engine.Basket().TableLabel; got != "T2" {
		t.Fatalf("resumed basket TableLabel = %q, want T2 (the MOVED table)", got)
	}
}

// Clearing a parked order's table (move to ""), then resuming it, must leave
// the basket with no table -- the same column-is-authoritative rule as the
// move case above, in its unassign direction.
func TestHoldClearTableThenResume_ReflectsCleared(t *testing.T) {
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
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}

	clearReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id="))
	clearReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clearRec := httptest.NewRecorder()
	mux.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", clearRec.Code, clearRec.Body.String())
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeRec := httptest.NewRecorder()
	mux.ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("resumed basket TableID = %q, want \"\" (table was cleared)", got)
	}
	if got := dp.Engine.Basket().TableLabel; got != "" {
		t.Fatalf("resumed basket TableLabel = %q, want \"\" (table was cleared)", got)
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

// holdTestTableClaimed reports whether the claims table (ut-docs#1390) holds
// a row for tableID -- since ut-docs#1704 the till's claim spans BOTH the
// live-basket and the parked (held_sales.table_id) stages of an order, as
// it is the only occupancy signal that reaches other tills.
func holdTestTableClaimed(t *testing.T, dp *common.Deps, tableID string) bool {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, tableID).Scan(&n); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	return n == 1
}

func holdTestTableOccupied(t *testing.T, dp *common.Deps, tableID string) bool {
	t.Helper()
	states, err := data.NewPOSRepo(dp.Db).ListTablesWithState(context.Background())
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	for _, s := range states {
		if s.ID == tableID {
			return s.Occupied
		}
	}
	t.Fatalf("table %s not listed", tableID)
	return false
}

// holdTestClaimedAt returns the table_claims.claimed_at stamp for tableID --
// a way to tell "the same row survived" from "a row was dropped and later
// re-inserted", which holdTestTableClaimed alone cannot.
func holdTestClaimedAt(t *testing.T, dp *common.Deps, tableID string) string {
	t.Helper()
	var at string
	if err := dp.Db.QueryRow(`SELECT claimed_at FROM table_claims WHERE table_id = ?`, tableID).Scan(&at); err != nil {
		t.Fatalf("claimed_at for %s: %v", tableID, err)
	}
	return at
}

// holdTestSeedClaimedTable puts the live basket on tableID the way the real
// flow does: the engine's table pick plus the table_claims row POST
// /api/pos/table writes (this harness registers only the hold API, so the
// claim is seeded through the same repo primitive that handler uses).
func holdTestSeedClaimedTable(t *testing.T, dp *common.Deps, tableID, label string) {
	t.Helper()
	dp.Engine.SetTable(tableID, label)
	if claimed, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), tableID); err != nil || !claimed {
		t.Fatalf("seed live claim on %s: claimed=%v err=%v", tableID, claimed, err)
	}
}

// holdTestHoldAndGetID parks the live basket through the REAL POST
// /api/pos/hold and returns the resulting held_sales id.
func holdTestHoldAndGetID(t *testing.T, mux *http.ServeMux, dp *common.Deps) string {
	t.Helper()
	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}
	return id
}

// TestHoldThenResume_MovesTableClaimBetweenLiveAndHeld (ut-docs#1390,
// reworked for ut-docs#1704): a table-assigned live basket carries a
// table_claims row (seeded here the way POST /api/pos/table writes it -- this
// harness registers only the hold API). That claim is the ONLY occupancy
// signal that syncs to other tills (held_sales does not), so hold must leave
// it in place -- the held_sales row and the claim coexist for the whole time
// the order is parked -- and resume finds its own row still there (the
// re-claim is an idempotent no-op: same row, same claimed_at, never dropped
// and re-inserted). Occupancy as the floor plan sees it (ListTablesWithState)
// stays true throughout.
func TestHoldThenResume_MovesTableClaimBetweenLiveAndHeld(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestSeedClaimedTable(t, dp, "tbl-1", "T1")
	claimedAt := holdTestClaimedAt(t, dp, "tbl-1")

	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("hold must keep the live claim (it is the only occupancy signal other tills see, ut-docs#1704)")
	}
	if got := holdTestClaimedAt(t, dp, "tbl-1"); got != claimedAt {
		t.Fatalf("hold must leave the claim row untouched, claimed_at %q -> %q", claimedAt, got)
	}
	var heldTable string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales`).Scan(&heldTable); err != nil || heldTable != "tbl-1" {
		t.Fatalf("held_sales.table_id = %q (err %v), want tbl-1", heldTable, err)
	}
	if !holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must still read occupied while parked")
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
	var heldRows int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&heldRows); err != nil || heldRows != 0 {
		t.Fatalf("resume must delete the held row, got %d rows (err %v)", heldRows, err)
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("the table must still be claimed for the live basket after resume")
	}
	if got := holdTestClaimedAt(t, dp, "tbl-1"); got != claimedAt {
		t.Fatalf("resume must find its own claim rather than re-take it, claimed_at %q -> %q", claimedAt, got)
	}
	if !holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must read occupied after resume, via the live claim")
	}
	if got := dp.Engine.Basket().TableID; got != "tbl-1" {
		t.Fatalf("resumed basket TableID = %q, want tbl-1", got)
	}
}

// TestHeldTableHandler_MoveTransfersClaim (ut-docs#1704): moving a parked
// order to another table moves its table_claims row with it -- the source
// table's claim is released and the target's is taken -- so the move is
// visible to other tills, not just in this till's held_sales.table_id. Goes
// through the REAL hold so the source claim is the one hold left in place.
func TestHeldTableHandler_MoveTransfersClaim(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestSeedClaimedTable(t, dp, "tbl-1", "T1")
	id := holdTestHoldAndGetID(t, mux, dp)
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("precondition: the source table's claim must survive the hold")
	}

	moveReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id=tbl-2"))
	moveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	moveRec := httptest.NewRecorder()
	mux.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", moveRec.Code, moveRec.Body.String())
	}
	if moveRec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed, got %q", moveRec.Header().Get("HX-Trigger"))
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id = ?`, id).Scan(&tableID); err != nil || tableID != "tbl-2" {
		t.Fatalf("held_sales.table_id after move = %q (err %v), want tbl-2", tableID, err)
	}
	if holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("the OLD table's claim must be released by the move")
	}
	if !holdTestTableClaimed(t, dp, "tbl-2") {
		t.Fatalf("the NEW table must be claimed by the move")
	}
	if holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must read free after the order moved off it")
	}
	if !holdTestTableOccupied(t, dp, "tbl-2") {
		t.Fatalf("T2 must read occupied after the order moved onto it")
	}
}

// TestHeldTableHandler_ClearTableReleasesClaim (ut-docs#1704): clearing a
// parked order's table (move to "") releases the claim hold left in place
// and takes no new one -- the table reads free to every till afterwards.
func TestHeldTableHandler_ClearTableReleasesClaim(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestSeedClaimedTable(t, dp, "tbl-1", "T1")
	id := holdTestHoldAndGetID(t, mux, dp)
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("precondition: the table's claim must survive the hold")
	}

	clearReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id="))
	clearReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clearRec := httptest.NewRecorder()
	mux.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", clearRec.Code, clearRec.Body.String())
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT COALESCE(table_id, '') FROM held_sales WHERE id = ?`, id).Scan(&tableID); err != nil || tableID != "" {
		t.Fatalf("held_sales.table_id after clear = %q (err %v), want \"\"", tableID, err)
	}
	if holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("clearing the table must release its claim")
	}
	var claims int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM table_claims`).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("clearing the table must take no new claim, got %d claim rows (err %v)", claims, err)
	}
	if holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must read free after the parked order's table was cleared")
	}
}

// TestHeldTableHandler_MoveOntoOwnCurrentTableSucceedsWithLiveClaim
// (ut-docs#1704) is the regression case for the handler's same-table fast
// path: unlike TestHeldTableHandler_MoveOntoOwnCurrentTableSucceeds above,
// whose direct-DB seeding never had a claim to conflict with, a REAL hold
// now leaves the order's own claim in table_claims -- and IsTableFree only
// self-excludes the held_sales row, not the claim, so without the fast path
// the order would read "occupied" against itself and the no-op would be
// rejected. The claim must also be left exactly as it was (not released and
// re-taken), so the table never blips free.
func TestHeldTableHandler_MoveOntoOwnCurrentTableSucceedsWithLiveClaim(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestSeedClaimedTable(t, dp, "tbl-1", "T1")
	id := holdTestHoldAndGetID(t, mux, dp)
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("precondition: the table's claim must survive the hold")
	}
	claimedAt := holdTestClaimedAt(t, dp, "tbl-1")

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id=tbl-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("a self-move must still complete as a no-op success (HX-Trigger), got %q", rec.Header().Get("HX-Trigger"))
	}
	var tableID string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id = ?`, id).Scan(&tableID); err != nil || tableID != "tbl-1" {
		t.Fatalf("held_sales.table_id = %q (err %v), want tbl-1", tableID, err)
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("a self-move must leave the order's own claim in place")
	}
	if got := holdTestClaimedAt(t, dp, "tbl-1"); got != claimedAt {
		t.Fatalf("a self-move must not release-and-re-take the claim, claimed_at %q -> %q", claimedAt, got)
	}
	if !holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must still read occupied after a self-move")
	}
}

// Resuming into an EMPTY basket that nonetheless has a table picked (a
// table pick needs no items) must release that pre-resume claim, or the
// table would stay occupied with nothing on it (ut-docs#1390).
func TestResume_ReleasesEmptyBasketsPriorTableClaim(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','T2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','T1',100,1,'{"lines":[{"sku":"ABC","name":"Apple","qty":1,"priceCents":100}]}','tbl-1')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	// The live basket: no items, but T2 picked (and claimed).
	dp.Engine.SetTable("tbl-2", "T2")
	if claimed, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), "tbl-2"); err != nil || !claimed {
		t.Fatalf("seed live claim: claimed=%v err=%v", claimed, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id=h1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "tbl-1" {
		t.Fatalf("resumed basket TableID = %q, want tbl-1", got)
	}
	if holdTestTableClaimed(t, dp, "tbl-2") {
		t.Fatalf("the empty basket's prior T2 claim must be released on resume")
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("the resumed order's T1 must be claimed")
	}
}
