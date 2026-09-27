package pages

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2723, the card's own AC: heldSaleGiveBack (held_sale_sync_proxy.go)
// hands a claimed held sale back after a resume fails post-claim. It sets
// h.PrimarySynced = false and writes through; when the primary is
// unreachable (or otherwise fails) for that give-back write, the write-
// through falls back to repo.Upsert -- and Upsert's primary_synced =
// MAX(current, incoming) never LOWERS the flag. So a row this till had
// already confirmed on the primary (primary_synced=1, from an earlier
// successful mirror under the SAME id) stays stuck at 1 even though the
// primary no longer holds it (it was just claimed off it) -- and the next
// successful ReconcileWithPrimary (heldSalesForDisplay's reconcile, or the
// Open orders page's own) then drops it as "resolved elsewhere", losing the
// only surviving copy of a genuinely open order.
//
// Modelled on TestResumeOnReplica_FailedResumeAfterClaimGivesOrderBack: the
// primary's claim succeeds and hands back a corrupt payload, so the resume
// fails after the claim and must give the order back -- but here the
// give-back's OWN write to the primary also fails (its /upsert endpoint
// answers 500), forcing the local-only fallback that exposes the bug.
func TestResumeOnReplica_GiveBackAfterUpsertFailureDoesNotStickPrimarySynced(t *testing.T) {
	const id = "hold-giveback-unsynced"
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/held-sales/claim":
			row := syncHeldSaleRow{ID: id, Label: "Table 4", Payload: `not json`, TotalMinor: 100, LineCount: 1}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  syncHeldSaleClaimResult{Claimed: true, Known: true, Row: &row},
				"error": nil,
			})
		case "/api/sync/held-sales/upsert":
			// The give-back's own write-through to the primary fails outright
			// -- same effect as a connection reset, an outage class outcome
			// (ut-docs#2723), forcing the LOCAL-ONLY fallback.
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	repo := data.NewHeldSalesRepo(dp.Db)
	ctx := context.Background()

	// The replica already holds a CONFIRMED mirror of this id from an
	// earlier successful write-through -- primary_synced=1 -- exactly the
	// state Upsert's MAX would otherwise keep stuck forever.
	if err := repo.Upsert(ctx, data.HeldSale{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1, PrimarySynced: true}); err != nil {
		t.Fatal(err)
	}

	rec := holdTestPost(mux, "/api/pos/resume", "id="+id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.failed"))) {
		t.Fatalf("a corrupt claimed payload must fail the resume, got %d: %s", rec.Code, rec.Body.String())
	}

	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the give-back must still leave the row locally (best-effort, never loses the order outright), got ok=%v err=%v", ok, err)
	}
	if got.PrimarySynced {
		t.Fatalf("ut-docs#2723: a give-back whose own write-through to the primary failed must not leave a stale confirmed-mirror flag stuck at true -- got %+v", got)
	}

	// The card's actual AC: the next successful primary list (this order is
	// gone from it -- it was claimed off the primary and never landed back
	// there) must NOT drop the only surviving copy.
	dropped, err := repo.ReconcileWithPrimary(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("ut-docs#2723: the give-back's local-only copy must survive reconcile, got %d dropped", dropped)
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || !ok {
		t.Fatalf("ut-docs#2723: the order must not be lost -- it was wrongly dropped as a resolved mirror it no longer was, ok=%v err=%v", ok, err)
	}
}

// ut-docs#2723 review: the demotion is for the local-only fallback ONLY. When
// the primary applies the give-back it holds the order again, so the local
// row must stay a confirmed mirror (primary_synced) -- otherwise, once
// another till resolves the order, reconcile would never drop this copy and
// it would linger as a stale open order here.
func TestResumeOnReplica_GiveBackAppliedOnPrimaryStaysConfirmedMirror(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	corrupt := data.HeldSale{ID: "hold-giveback-applied", Label: "Broken", Payload: `not json`, TotalMinor: 100, LineCount: 1}
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, corrupt); err != nil || !applied {
		t.Fatalf("seed: applied=%v err=%v", applied, err)
	}
	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+corrupt.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.failed"))) {
		t.Fatalf("a corrupt payload must fail the resume, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := ct.primaryHeld.Get(ctx, corrupt.ID); !ok {
		t.Fatal("precondition: the give-back must have landed on the primary")
	}
	got, ok, err := data.NewHeldSalesRepo(ct.dp.Db).Get(ctx, corrupt.ID)
	if err != nil || !ok || !got.PrimarySynced {
		t.Fatalf("a give-back the primary applied must leave the local copy a confirmed mirror, got ok=%v %+v err=%v", ok, got, err)
	}
}
