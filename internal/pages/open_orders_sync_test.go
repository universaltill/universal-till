package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Open orders page on a REPLICA (ADR-0093 Decision 3, ut-docs#1920): the
// page merges the local held_sales list with the primary's live list
// (GET /api/sync/held-sales) -- primary wins per id, local-only rows still
// shown -- and ANY failure reaching the primary degrades to local-only,
// silently: the page renders, no error surfaces to the cashier.

// makeOpenOrdersReplica gives the hand-rolled open-orders test DB the
// settings table replicaSyncTarget reads (001_init.sql's shape) and points
// it at primaryURL.
func makeOpenOrdersReplica(t *testing.T, d *common.Deps, primaryURL string) {
	t.Helper()
	if _, err := d.Db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	setReplicaSettings(t, d.Settings, primaryURL, "b-123")
}

// TestOpenOrdersPage_PrimaryUnreachableDegradesToLocalOnly is the
// offline-first half of the merge: a replica whose primary is off must
// still render its own parked orders, with no error banner and no error
// status -- exactly what the page did before this ADR.
func TestOpenOrdersPage_PrimaryUnreachableDegradesToLocalOnly(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	makeOpenOrdersReplica(t, d, deadPrimaryURL())
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at) VALUES
 ('h1','Table 4',1250,3,'{}',NULL,datetime('now'),datetime('now'))`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	body := openOrdersGet(t, mux) // fatals on anything but 200
	if !strings.Contains(body, `data-held-id="h1"`) || !strings.Contains(body, "Table 4") {
		t.Fatalf("an unreachable primary must fall back to the local list, got: %s", body)
	}
	if strings.Contains(body, `class="login-error"`) {
		t.Fatalf("the fallback must be silent -- no error banner, got: %s", body)
	}
	if strings.Contains(body, template.HTMLEscapeString(httpx.T("en", "open_orders.empty"))) {
		t.Fatalf("the local row must render, not the empty state, got: %s", body)
	}

	// The sale screen's parked-orders popup shares the same reader
	// (ut-docs#2137) and must degrade the same way.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/parked-orders", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Table 4") {
		t.Fatalf("the popup must fall back to the local list too, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestOpenOrdersPage_MergesPrimaryRowsPrimaryWinsPerID: an order parked at
// ANOTHER till (present only on the primary) shows here; on an id
// collision the primary's copy wins (its label, here); a local-only row
// (taken during an outage) is still listed.
func TestOpenOrdersPage_MergesPrimaryRowsPrimaryWinsPerID(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	primary := newHeldSaleProxyPrimary(t, true,
		syncHeldSaleRow{ID: "shared", Label: "Primary copy", Payload: `{}`, TotalMinor: 900, LineCount: 2, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 09:05:00"},
		syncHeldSaleRow{ID: "remote", Label: "Parked at till A", Payload: `{}`, TotalMinor: 400, LineCount: 1, CreatedAt: "2026-09-15 09:10:00", UpdatedAt: "2026-09-15 09:10:00"},
	)
	makeOpenOrdersReplica(t, d, primary.srv.URL)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at) VALUES
 ('shared','Stale local copy',100,1,'{}',NULL,'2026-09-15 09:00:00','2026-09-15 09:00:00'),
 ('local-only','Taken offline',500,1,'{}',NULL,'2026-09-15 09:20:00','2026-09-15 09:20:00')`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}

	body := openOrdersGet(t, mux)
	for _, want := range []string{`data-held-id="shared"`, `data-held-id="remote"`, `data-held-id="local-only"`, "Primary copy", "Parked at till A", "Taken offline"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the merged page, got: %s", want, body)
		}
	}
	if strings.Contains(body, "Stale local copy") {
		t.Fatalf("the primary's copy must win on an id collision, got: %s", body)
	}
	if primary.listCalls.Load() != 1 {
		t.Fatalf("the primary must be asked for its list exactly once per render, got %d", primary.listCalls.Load())
	}
}

// TestOpenOrdersResume_RowOnlyOnPrimaryResumesFromThere: the merged list
// is only useful if a row parked at ANOTHER till can actually be opened
// here -- the ADR's stated consequence ("visible and addable from till
// B"). The local table has no such row; resumeHeldSale claims the
// primary's copy (ADR-0093 Amendment B -- which also removes it there: it is
// live on this till now) and restores it onto the live basket under its own
// id (so a re-park goes back under that same id, ut-docs#1918).
func TestOpenOrdersResume_RowOnlyOnPrimaryResumesFromThere(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	primary := newHeldSaleProxyPrimary(t, true,
		syncHeldSaleRow{ID: "remote", Label: "Parked at till A", Payload: `{"lines":[{"sku":"ABC","name":"Apple","qty":1,"price_cents":100}]}`, TotalMinor: 100, LineCount: 1, CreatedAt: "2026-09-15 09:10:00", UpdatedAt: "2026-09-15 09:10:00"},
	)
	makeOpenOrdersReplica(t, d, primary.srv.URL)

	rec := holdTestPost(mux, "/open-orders/resume", "id=remote")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /open-orders/resume = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected a redirect to the sale screen, got %q", got)
	}
	if origin := d.Engine.HeldOrigin(); origin.ID != "remote" || origin.Label != "Parked at till A" || origin.CreatedAt != "2026-09-15 09:10:00" {
		t.Fatalf("the primary's row must be live under its own identity, got %+v", origin)
	}
	if primary.claimCalls.Load() != 1 {
		t.Fatalf("the primary must be asked to claim the resumed row exactly once, got %d", primary.claimCalls.Load())
	}
	if id, _ := primary.lastClaim.Load().(string); id != "remote" {
		t.Fatalf("primary must be asked to claim %q, got %q", "remote", id)
	}

	// Unknown everywhere is still not-found -- the fallback never invents a
	// row.
	rec = holdTestPost(mux, "/open-orders/resume", "id=nowhere")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/open-orders?err=hold.error.not_found" {
		t.Fatalf("an id on neither till must still be not-found, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
