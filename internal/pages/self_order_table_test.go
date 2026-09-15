package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#815: GET /self-order?table=<id> binds the fresh kiosk basket to a
// physical table (carried on d.KioskEngine's own TableID/TableLabel —
// ADR-0054/ut-docs#820's existing pos.Service fields, not a new mechanism)
// so it's available all the way through to checkout. A valid, enabled table
// id must bind; anything else must leave the basket with no table, exactly
// today's behaviour.
func TestSelfOrder_TableParam_BindsValidEnabledTable(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=...: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != tableID {
		t.Fatalf("KioskEngine.TableID() = %q, want %q", got, tableID)
	}
	if got := dp.KioskEngine.TableLabel(); got != "T5" {
		t.Fatalf("KioskEngine.TableLabel() = %q, want %q", got, "T5")
	}
}

// A disabled table must not bind — same "table not usable" rule the guest
// checkout flow (the tables page itself) already enforces elsewhere, and
// exactly the shape of table id an old/reprinted QR could carry after a
// table gets deactivated.
func TestSelfOrder_TableParam_DisabledTableFallsBackUnchanged(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	posRepo := data.NewPOSRepo(d.DB)
	tableID, err := posRepo.CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := posRepo.SetTableEnabled(t.Context(), tableID, false); err != nil {
		t.Fatalf("SetTableEnabled: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<disabled>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\" (disabled table must not bind)", got)
	}
}

// An unknown table id (stale/tampered QR) must not error the page out —
// same unchanged-behaviour fallback as no table param at all.
func TestSelfOrder_TableParam_UnknownIDFallsBackUnchanged(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table=does-not-exist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<unknown>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\"", got)
	}
}

// No ?table param at all: today's behaviour, byte-for-byte — a plain
// regression pin.
func TestSelfOrder_NoTableParam_LeavesNoTableBound(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("KioskEngine.TableID() = %q, want \"\"", got)
	}
}

// ut-docs#815 review finding (blocker 3 mitigation): a DIFFERENT table's
// guest must never silently wipe an in-progress table order — reproduced
// for real before this guard existed (table A's basket vanished, and the
// session rebound to table B, so table A's eventual checkout would have
// landed on table B's floor-plan tile). This is a fail-safe, not real
// concurrency (see ut-docs#2261): the second guest gets a clear "busy"
// screen instead of a silent basket swap.
func TestSelfOrder_DifferentTableWithItems_ShowsBusyInsteadOfWiping(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	posRepo := data.NewPOSRepo(d.DB)
	tableA, err := posRepo.CreateTable(t.Context(), "T1", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable A: %v", err)
	}
	tableB, err := posRepo.CreateTable(t.Context(), "T2", "Terrace", 2, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable B: %v", err)
	}
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	// Table A's guest scans in and adds a real line.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	scanReq := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	scanReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), scanReq)
	if got := dp.KioskEngine.TableID(); got != tableA {
		t.Fatalf("precondition: KioskEngine.TableID() = %q, want tableA %q", got, tableA)
	}
	if n := len(dp.KioskEngine.Lines()); n != 1 {
		t.Fatalf("precondition: KioskEngine.Lines() has %d lines, want 1", n)
	}

	// Table B's guest scans the SAME device before A has checked out.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableB, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableB>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This till is busy") {
		t.Fatalf("expected the busy screen, got: %s", rec.Body.String())
	}
	// Table A's order must be completely untouched.
	if got := dp.KioskEngine.TableID(); got != tableA {
		t.Fatalf("KioskEngine.TableID() = %q, want unchanged tableA %q — table B must never take over a busy basket", got, tableA)
	}
	if n := len(dp.KioskEngine.Lines()); n != 1 {
		t.Fatalf("KioskEngine.Lines() has %d lines, want unchanged 1 — table A's order must not be wiped", n)
	}
}

// Re-scanning your OWN table's code (the idle-reset bounce, or a deliberate
// restart) is never "busy" — it's the same guest, and today's normal reset
// behaviour must be unchanged.
func TestSelfOrder_SameTableRescan_NeverBusy(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	posRepo := data.NewPOSRepo(d.DB)
	tableA, err := posRepo.CreateTable(t.Context(), "T1", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	scanReq := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	scanReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), scanReq)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<same table>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This till is busy") {
		t.Fatalf("re-scanning the same table must never show the busy screen, got: %s", rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != tableA {
		t.Fatalf("KioskEngine.TableID() = %q, want %q", got, tableA)
	}
	if n := len(dp.KioskEngine.Lines()); n != 0 {
		t.Fatalf("KioskEngine.Lines() has %d lines, want 0 — a re-scan is a fresh basket, same as today's unchanged reset behaviour", n)
	}
}

// An empty table-bound basket (guest scanned in but never added anything)
// has nothing to lose — a different table's guest must be able to take the
// till over normally, not get stuck behind a "busy" screen forever.
func TestSelfOrder_DifferentTableNoItemsYet_NotBusy(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	posRepo := data.NewPOSRepo(d.DB)
	tableA, err := posRepo.CreateTable(t.Context(), "T1", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable A: %v", err)
	}
	tableB, err := posRepo.CreateTable(t.Context(), "T2", "Terrace", 2, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable B: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order?table="+tableA, nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order?table="+tableB, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<tableB>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This till is busy") {
		t.Fatalf("an empty table-bound basket has nothing to lose — must not show busy, got: %s", rec.Body.String())
	}
	if got := dp.KioskEngine.TableID(); got != tableB {
		t.Fatalf("KioskEngine.TableID() = %q, want tableB %q", got, tableB)
	}
}
