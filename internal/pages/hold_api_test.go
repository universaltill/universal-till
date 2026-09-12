package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// ut-docs#1390: hold releases the live basket's table claim and resume
	// re-claims it, so the claims table (migration 078) is part of every
	// hold/resume round trip, table-assigned or not (release is a no-op
	// DELETE either way) -- column-identical to the migration.
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

// TestResumeHandler_AutoParksBusyBasketThenResumes (ut-docs#1919): resuming
// while the live basket already has items no longer refuses the switch --
// the in-progress sale is parked first (the exact same path a manual Hold
// takes), and the requested order is then loaded, so the cashier never loses
// what they already had rung up.
func TestResumeHandler_AutoParksBusyBasketThenResumes(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// Hold it so there's a real held row, then start a NEW live sale before
	// trying to resume.
	holdReq := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	mux.ServeHTTP(httptest.NewRecorder(), holdReq)
	var targetID string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&targetID); err != nil {
		t.Fatalf("expected a held_sales row: %v", err)
	}
	// A second scan (qty 2, not 1) so the live sale is distinguishable by
	// content from the target it is about to switch away from -- otherwise
	// a broken auto-park that discarded its payload would be
	// indistinguishable from a correct one in the assertions below.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("start a new live sale: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("add a second line to the new live sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+targetID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed on resume, got %q", rec.Header().Get("HX-Trigger"))
	}
	// ut-docs#1919: a distinct toast from a plain resume -- the cashier must
	// be told their own prior sale was held too, not just that a new one
	// loaded (evidence: a real effect with no on-screen cue is a control
	// nobody finds).
	if !strings.Contains(rec.Body.String(), httpx.T("en", "hold.toast.parked_and_resumed")) {
		t.Fatalf("expected the parked-and-resumed toast, got: %s", rec.Body.String())
	}
	// The requested order (qty 1) is now live -- NOT the qty-2 sale that was
	// in progress a moment ago.
	if !dp.Engine.HasItems() {
		t.Fatalf("expected the requested order's basket to be loaded")
	}
	if dp.Engine.HeldOrigin().ID != targetID {
		t.Fatalf("expected the live basket's held origin to be %q, got %+v", targetID, dp.Engine.HeldOrigin())
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 || lines[0].Qty != 1 {
		t.Fatalf("expected the restored basket to be the target's single-qty line, got %+v", lines)
	}
	// The sale that was live when the resume was requested is not lost --
	// it is parked, WITH ITS ACTUAL CONTENT (qty 2, not empty), under a NEW,
	// distinct id (never the target's), findable and resumable later.
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&count); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one held row (the auto-parked in-progress sale), got %d", count)
	}
	var autoParkedID string
	var lineCount, totalMinor int
	if err := dp.Db.QueryRow(`SELECT id, line_count, total_minor FROM held_sales`).Scan(&autoParkedID, &lineCount, &totalMinor); err != nil {
		t.Fatalf("expected the auto-parked row: %v", err)
	}
	if autoParkedID == targetID {
		t.Fatalf("the auto-parked sale must not overwrite the resumed order's row")
	}
	// qty 2 of a 100-minor item at 20% exclusive tax = 240, not 0 and not
	// the target's own 120 -- pins that the auto-parked payload is the
	// PRIOR sale's real content, not an empty/discarded snapshot.
	if lineCount != 1 || totalMinor != 240 {
		t.Fatalf("expected the auto-parked row to hold the prior qty-2 sale (line_count=1 total_minor=240), got line_count=%d total_minor=%d", lineCount, totalMinor)
	}
}

// TestResumeHandler_BusyBasketNotParkedWhenTargetMissing (ut-docs#1919): the
// live basket is only ever parked once the resume is known to be able to
// proceed -- an unknown target must not cost the cashier their in-progress
// sale for nothing.
func TestResumeHandler_BusyBasketNotParkedWhenTargetMissing(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("start a live sale: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id=hold-does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if !dp.Engine.HasItems() {
		t.Fatalf("the live sale must survive an unknown resume target")
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&count); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("an unknown target must not auto-park the live basket, got %d held rows", count)
	}
}

// TestResumeHandler_AlreadyLiveOrderIsANoOp (ut-docs#1919, independent
// review): the live basket can already BE the order being "resumed" -- most
// plausibly when an earlier resume's own repo.Delete failed and left a stale
// row behind (resumeHeldSale's own doc comment, and HeldSalesRepo.Upsert's,
// both already anticipate this). Before the F1 fix, falling through to the
// busy branch would auto-park the LIVE basket's current state into the row
// about to be deleted (via Upsert, same id) and then delete it -- destroying
// whatever the cashier added since the stale row was left behind, leaving
// nothing to recover it from. Tapping an order you are already on must be a
// safe no-op instead.
func TestResumeHandler_AlreadyLiveOrderIsANoOp(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdReq := httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil)
	mux.ServeHTTP(httptest.NewRecorder(), holdReq)
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("expected a held_sales row: %v", err)
	}
	// Resume it: the row is deleted and the live basket's HeldOrigin is now
	// this id -- then simulate the accepted "Delete failed" case by
	// re-inserting the exact same row behind the engine's back (repo.Delete
	// erroring is swallowed by design; this reproduces its effect directly).
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), resumeReq)
	if dp.Engine.HeldOrigin().ID != id {
		t.Fatalf("precondition: expected the live basket's origin to be %q, got %+v", id, dp.Engine.HeldOrigin())
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES (?,?,?,?,?,?,datetime('now'))`,
		id, "Stale", 100, 1, `{}`, nil); err != nil {
		t.Fatalf("reinsert stale row: %v", err)
	}
	// Add another item on top of the resumed order -- this is the value a
	// pre-fix auto-park-then-delete would destroy.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("add to the resumed order: %v", err)
	}
	linesBefore := len(dp.Engine.Basket().Lines)

	// Tap the same order again.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+id)))
	_ = rec

	if got := len(dp.Engine.Basket().Lines); got != linesBefore {
		t.Fatalf("re-tapping the already-live order changed the basket: had %d lines, now %d", linesBefore, got)
	}
	if !dp.Engine.HasItems() {
		t.Fatalf("re-tapping the already-live order must not empty the basket")
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("query held_sales: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the stale row to be left exactly as found (1 row), got %d", count)
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

// TestHeldTableHandler_MoveOntoOwnCurrentTableLeavesClaimUntouched
// (ut-docs#1704, independent review 2026-09-07): the self-move short-circuit
// is not just a convenience -- it is what stops a self-move from asking
// IsTableFree/claimTableWriteThrough about a table this held sale's OWN
// claim already occupies (which, per IsTableFree's own doc comment, they
// must never be asked about: "a claim seen here is by construction someone
// else's") and then releasing that same claim as the "old table" once the
// no-op "move" reports success -- silently stripping the parked order's
// cross-till occupancy. Pins the short-circuit actually skips the
// claim/release path, not just that the table_id value happens to end up
// unchanged either way (the review found this specific test's predecessor
// passed even with the short-circuit condition forced to `true`,
// unconditionally, because it seeded no table_claims row to detect the
// difference).
func TestHeldTableHandler_MoveOntoOwnCurrentTableLeavesClaimUntouched(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}','tbl-1')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	// The claim ut-docs#1704 now keeps alive through the whole park --
	// seeded directly here the way the real hold handler leaves it.
	if claimed, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), "tbl-1"); err != nil || !claimed {
		t.Fatalf("seed held order's own claim: claimed=%v err=%v", claimed, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id=h1&table_id=tbl-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger: held-changed even for a self-move no-op, got %q", rec.Header().Get("HX-Trigger"))
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatal("a self-move onto the held order's own current table must leave its claim untouched, not release it")
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
// a row for tableID -- the live-basket half of table occupancy; the
// held_sales.table_id column is the parked half.
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
	states, err := data.NewPOSRepo(dp.Db).ListTablesWithState(context.Background(), time.Now().Add(-tillClaimTTL))
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

// TestHoldThenResume_KeepsTableClaimThroughHold (ut-docs#1390, behaviour
// changed by ut-docs#1704): a table-assigned live basket carries a
// table_claims row (seeded here the way POST /api/pos/table writes it --
// this harness registers only the hold API). Hold used to hand the
// occupancy to the held_sales row and drop the claim (one occupancy source
// per lifecycle stage); it now KEEPS the claim alive through the whole
// park instead, because that claim -- unlike held_sales -- is the one
// thing a replica already write-throughs to the primary, so it is what
// makes a parked order's table visible cross-till (ut-docs#1704; see
// hold_api.go's own comment on why). Resume re-affirms the SAME row
// (ClaimTableForTill's own-claim re-take, tables_repo.go) rather than
// re-claiming from scratch, BEFORE the held row is deleted, so the table
// never reads free in between. Occupancy as the floor plan sees it
// (ListTablesWithState) stays true throughout -- and, unlike before, so
// does the table_claims row itself, continuously.
func TestHoldThenResume_KeepsTableClaimThroughHold(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','T1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetTable("tbl-1", "T1")
	if claimed, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), "tbl-1"); err != nil || !claimed {
		t.Fatalf("seed live claim: claimed=%v err=%v", claimed, err)
	}

	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	if !holdTestTableClaimed(t, dp, "tbl-1") {
		t.Fatalf("hold must KEEP the live claim (ut-docs#1704) -- it's what makes the parked order visible cross-till")
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
		t.Fatalf("resume must keep the table claimed for the live basket")
	}
	if !holdTestTableOccupied(t, dp, "tbl-1") {
		t.Fatalf("T1 must read occupied after resume, via the live claim")
	}
	if got := dp.Engine.Basket().TableID; got != "tbl-1" {
		t.Fatalf("resumed basket TableID = %q, want tbl-1", got)
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

// holdTestPost posts a form to the hold-API mux and returns the recorder.
func holdTestPost(mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// holdTestOnlyRow returns the single held_sales row, failing if there isn't
// exactly one -- the stable-identity tests below all assert that a re-park
// never leaves a second row behind.
func holdTestOnlyRow(t *testing.T, dp *common.Deps) data.HeldSale {
	t.Helper()
	rows, err := data.NewHeldSalesRepo(dp.Db).List(context.Background())
	if err != nil {
		t.Fatalf("list held_sales: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one held_sales row, got %d: %+v", len(rows), rows)
	}
	return rows[0]
}

// TestHoldHandler_FirstParkMintsFreshIDAndLabel (ut-docs#1918): a basket
// that was never parked (no held-sale origin on the engine) keeps today's
// behaviour exactly -- a fresh hold-<unixnano> id, the label fallback
// chain, created_at from the schema default -- and parking clears the
// engine's origin so the NEXT sale starts clean.
func TestHoldHandler_FirstParkMintsFreshIDAndLabel(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if !dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("a never-parked basket must have no held origin, got %+v", dp.Engine.HeldOrigin())
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	row := holdTestOnlyRow(t, dp)
	if !strings.HasPrefix(row.ID, "hold-") {
		t.Fatalf("first park must mint a fresh hold-<nanos> id, got %q", row.ID)
	}
	if row.Label != "Table 4" {
		t.Fatalf("first park label = %q, want the typed label", row.Label)
	}
	if row.CreatedAt == "" {
		t.Fatalf("first park must stamp created_at (schema default), got empty")
	}
	if !dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("parking must leave the (now empty) basket with no origin, got %+v", dp.Engine.HeldOrigin())
	}
}

// TestResumeThenRepark_ReusesSameIDLabelAndFirstParkedTime (ut-docs#1918):
// the headline behaviour. Park -> resume -> edit -> park again lands under
// the SAME row id, keeps the ORIGINAL label even though the re-park posts a
// different one and a customer has since been attached (the fallback chain
// must not run again), keeps the FIRST park's created_at (age never
// resets), and never leaves a duplicate row -- while the row's contents
// (line count, total, payload) do reflect the edits.
func TestResumeThenRepark_ReusesSameIDLabelAndFirstParkedTime(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("first hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	first := holdTestOnlyRow(t, dp)
	// Backdate the first park so "created_at preserved" is distinguishable
	// from "created_at re-stamped to now" (both would otherwise read as the
	// same second in a fast test).
	if _, err := dp.Db.Exec(`UPDATE held_sales SET created_at = '2026-09-09 10:00:00' WHERE id = ?`, first.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if rec := holdTestPost(mux, "/api/pos/resume", "id="+first.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.HeldOrigin(); got.ID != first.ID || got.Label != "Table 4" || got.CreatedAt != "2026-09-09 10:00:00" {
		t.Fatalf("resume must record the row's identity on the engine, got %+v", got)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("resume still consumes the row while the order is live, got %d rows (err %v)", n, err)
	}

	// Edit the live order, attach a customer, then re-park posting a BLANK
	// label -- none of that may change the order's identity, and a blank
	// field must never re-derive a label from the clock or the customer
	// just attached (see TestResumeThenRepark_ExplicitRenameOverrides below
	// for the case where the cashier DOES type a new one).
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	dp.Engine.SetCustomer("cust1", "Jane Doe")
	rec := holdTestPost(mux, "/api/pos/hold", "label=")
	if rec.Code != http.StatusOK {
		t.Fatalf("re-park: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("re-park must still refresh the held strip, got HX-Trigger %q", rec.Header().Get("HX-Trigger"))
	}
	again := holdTestOnlyRow(t, dp)
	if again.ID != first.ID {
		t.Fatalf("re-park id = %q, want the original %q (a resumed order must keep its identity)", again.ID, first.ID)
	}
	if again.Label != "Table 4" {
		t.Fatalf("re-park label = %q, want the ORIGINAL \"Table 4\" (a blank field must not re-run the fallback chain)", again.Label)
	}
	if again.CreatedAt != "2026-09-09 10:00:00" {
		t.Fatalf("re-park created_at = %q, want the first park's time preserved", again.CreatedAt)
	}
	// One merged line at qty 2: 2 x 100 + 20% tax-exclusive VAT = 240
	// (first park was 1 x 100 + 20% = 120).
	if again.LineCount != 1 || again.TotalMinor != 240 {
		t.Fatalf("re-park must store the EDITED contents (2 x 100 + 20%% = 240), got lines=%d total=%d", again.LineCount, again.TotalMinor)
	}
	if !dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("re-park must clear the engine's origin, got %+v", dp.Engine.HeldOrigin())
	}

	// And it can be resumed again under that same id.
	if rec := holdTestPost(mux, "/api/pos/resume", "id="+first.ID); rec.Code != http.StatusOK {
		t.Fatalf("second resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !dp.Engine.HasItems() {
		t.Fatalf("second resume must restore the basket")
	}
}

// TestResumeThenRepark_ExplicitRenameOverrides (ut-docs#1918, independent
// review finding): the hold dialog still shows an editable label field on
// every park, including a re-park. Freezing the label unconditionally would
// make that field a lie -- a cashier who resumes "12:05", realises it's
// actually table 4, types "Table 4" and submits would see the toast succeed
// while the order silently kept its old name forever. A genuinely typed,
// non-blank label on re-park must win; only a BLANK field keeps the
// original (see TestResumeThenRepark_ReusesSameIDLabelAndFirstParkedTime).
func TestResumeThenRepark_ExplicitRenameOverrides(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label="); rec.Code != http.StatusOK {
		t.Fatalf("first hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	first := holdTestOnlyRow(t, dp)
	if first.Label == "" {
		t.Fatalf("first park with no typed label must fall back (clock or customer), got empty")
	}

	if rec := holdTestPost(mux, "/api/pos/resume", "id="+first.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("re-park: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	again := holdTestOnlyRow(t, dp)
	if again.ID != first.ID {
		t.Fatalf("explicit rename must not change the order's identity, id = %q, want %q", again.ID, first.ID)
	}
	if again.Label != "Table 4" {
		t.Fatalf("re-park label = %q, want the explicitly typed \"Table 4\"", again.Label)
	}
}

// TestResumeThenReset_NextParkIsAFreshOrder (ut-docs#1918): abandoning a
// resumed order (New sale / Reset) ends its identity -- the next basket
// parked on this till is a new order with its own id, never a silent
// overwrite of the abandoned one's row under the old id.
func TestResumeThenReset_NextParkIsAFreshOrder(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestPost(mux, "/api/pos/hold", "label=Table+4")
	first := holdTestOnlyRow(t, dp)
	holdTestPost(mux, "/api/pos/resume", "id="+first.ID)
	dp.Engine.Reset()
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("new sale scan: %v", err)
	}
	holdTestPost(mux, "/api/pos/hold", "label=Walk-in")
	next := holdTestOnlyRow(t, dp)
	if next.ID == first.ID {
		t.Fatalf("a new sale after Reset must park under a fresh id, reused %q", first.ID)
	}
	if next.Label != "Walk-in" {
		t.Fatalf("a new sale's first park must run the label chain, got %q", next.Label)
	}
}

// TestResumeReparkResume_PreservesOrderType (ut-docs#1381 regression guard,
// re-pinned for ut-docs#1918's stable-identity path): the takeaway choice
// must survive not just one hold/resume (TestHoldThenResume_PreservesOrderType
// above) but the new re-park-under-the-same-id write too -- resume, add
// nothing, verify; re-park (Upsert), resume again, verify. A silent revert
// to dine-in here changes the sale's VAT basis (§12 UStG).
func TestResumeReparkResume_PreservesOrderType(t *testing.T) {
	mux, dp := newHoldTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	dp.Engine.SetOrderType(pos.OrderTypeTakeaway)
	holdTestPost(mux, "/api/pos/hold", "")
	first := holdTestOnlyRow(t, dp)

	holdTestPost(mux, "/api/pos/resume", "id="+first.ID)
	if got := dp.Engine.OrderType(); got != pos.OrderTypeTakeaway {
		t.Fatalf("after first resume OrderType() = %q, want %q", got, pos.OrderTypeTakeaway)
	}

	// Re-park with nothing changed: the upsert path, same id.
	holdTestPost(mux, "/api/pos/hold", "")
	again := holdTestOnlyRow(t, dp)
	if again.ID != first.ID {
		t.Fatalf("re-park id = %q, want %q", again.ID, first.ID)
	}
	// heldSaleMayHaveTable is false only for an all-takeaway payload -- a
	// cheap check that the re-parked payload itself still says takeaway.
	if heldSaleMayHaveTable(again.Payload) {
		t.Fatalf("re-parked payload lost its takeaway order type: %s", again.Payload)
	}

	holdTestPost(mux, "/api/pos/resume", "id="+first.ID)
	if got := dp.Engine.OrderType(); got != pos.OrderTypeTakeaway {
		t.Fatalf("after resume -> re-park -> resume OrderType() = %q, want %q (silently reverted to dine-in)", got, pos.OrderTypeTakeaway)
	}
	if got := dp.Engine.HeldOrigin(); got.ID != first.ID {
		t.Fatalf("second resume must re-record the same origin id, got %+v", got)
	}
}
