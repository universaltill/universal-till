package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#815: GET /self-order?table=<id> binds the kiosk basket to a
// physical table (carried on the engine's own TableID/TableLabel —
// ADR-0054/ut-docs#820's existing pos.Service fields, not a new mechanism)
// so it's available all the way through to checkout. Since ut-docs#2261
// that engine is the table's OWN session engine, named by the
// self_order_session cookie — never d.KioskEngine, which stays the bare
// walk-up kiosk's basket. A valid, enabled table id must bind; anything
// else must fall through to the bare-kiosk path with no session minted,
// exactly today's behaviour.
func TestSelfOrder_TableParam_BindsValidEnabledTable(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableID, err := data.NewPOSRepo(d.DB).CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	guest := newSelfOrderClient(t, mux)

	rec := guest.get("/self-order?table=" + tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=...: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	tok := guest.sessionToken()
	if tok == "" {
		t.Fatal("a valid table must set the self_order_session cookie")
	}
	engine, gotTable, ok := dp.KioskSessions.Lookup(tok)
	if !ok || gotTable != tableID {
		t.Fatalf("Lookup(cookie): ok=%v table=%q, want %q", ok, gotTable, tableID)
	}
	if got := engine.TableID(); got != tableID {
		t.Fatalf("session engine TableID() = %q, want %q", got, tableID)
	}
	if got := engine.TableLabel(); got != "T5" {
		t.Fatalf("session engine TableLabel() = %q, want %q", got, "T5")
	}
	if got := dp.KioskEngine.TableID(); got != "" {
		t.Fatalf("d.KioskEngine.TableID() = %q, want \"\" — the walk-up kiosk basket never binds a table any more", got)
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
	guest := newSelfOrderClient(t, mux)

	rec := guest.get("/self-order?table=" + tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<disabled>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if guest.sessionToken() != "" {
		t.Fatal("a disabled table must not mint a session cookie")
	}
	if n := len(dp.KioskSessions.All()); n != 0 {
		t.Fatalf("a disabled table must not mint a session: %d live", n)
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
	guest := newSelfOrderClient(t, mux)

	rec := guest.get("/self-order?table=does-not-exist")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<unknown>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if guest.sessionToken() != "" || len(dp.KioskSessions.All()) != 0 {
		t.Fatal("an unknown table must not mint a session")
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
	if n := len(dp.KioskSessions.All()); n != 0 {
		t.Fatalf("a bare visit must not mint a session: %d live", n)
	}
}

// Re-scanning your OWN table's code (the idle-reset bounce, or a deliberate
// re-open) JOINS the table's live session (ut-docs#2261) — the basket is
// kept, not reset, and the same cookie token comes back. Before #2261 this
// was a fresh basket on the shared engine; the cross-table "busy" guard
// tests that lived here are superseded by self_order_session_test.go's
// TestSelfOrder_TwoTablesConcurrent_IsolatedBaskets.
func TestSelfOrder_SameTableRescan_JoinsKeepsBasket(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createTestTable(t, dp, "T1")
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	guest := newSelfOrderClient(t, mux)

	guest.get("/self-order?table=" + tableA)
	tok := guest.sessionToken()
	guest.post("/api/self-order/scan", "code=5000001")

	rec := guest.get("/self-order?table=" + tableA)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order?table=<same table>: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if guest.sessionToken() != tok {
		t.Fatalf("re-scan must keep the same session token: %q != %q", guest.sessionToken(), tok)
	}
	engine, gotTable, ok := dp.KioskSessions.Lookup(tok)
	if !ok || gotTable != tableA || engine.TableID() != tableA {
		t.Fatalf("session after re-scan: ok=%v table=%q engine table=%q", ok, gotTable, engine.TableID())
	}
	if n := len(engine.Lines()); n != 1 {
		t.Fatalf("re-scan must join the live session and keep its basket: %d lines, want 1", n)
	}
}
