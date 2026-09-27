package pages

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3034 (follow-up of #2723): two remaining ways a claimed held sale
// can end up stuck at primary_synced=1 even though this till alone holds it
// -- and the next successful ReconcileWithPrimary then drops it as
// "resolved elsewhere", losing the order outright.
//
// Scenario 1 (heldSaleGiveBack, tx-level): #2723's own fix wrote the
// give-back's local fallback as TWO statements -- repo.Upsert (whose MAX
// semantics leave an already-confirmed row at 1) followed by a separate
// repo.MarkLocalOnly (which then clears it to 0). A concurrent
// ReconcileWithPrimary landing in the gap between those two commits would
// see the row at 1 -- a "confirmed mirror" the primary no longer lists --
// and drop it right there, before the second statement ever ran. The fix
// (UpsertLocalOnly) folds both into ONE statement, so no commit ever leaves
// the row exposed at 1. This test proves the absence of that intermediate
// exposure directly, with a SQLite trigger that records every commit
// leaving the row at primary_synced=1, rather than only asserting the final
// state (which a two-statement fix would also reach, just unsafely).
//
// Scenario 2 (re-park of a claimed id): resumeHeldSale claims the order off
// the primary (claimed=true), then its own local repo.Delete may fail
// (error ignored -- "a stale row is the lesser evil") and leave the OLD,
// already-primary_synced=1 row behind. The cashier re-parks the same
// HeldOrigin.ID; parkCurrentBasket -> heldSaleWriteThrough; the primary's
// own /upsert also fails -> the local fallback (pre-fix: plain repo.Upsert)
// keeps primary_synced at 1 via MAX -- even though this till just took the
// order off the shop's authority and no other copy is confirmed anywhere.
// The fix threads pos.HeldOrigin.Claimed through parkCurrentBasket into
// heldSaleWriteThrough, whose local fallback for a claimed id calls
// UpsertLocalOnly instead of Upsert.

// validSingleLineHeldPayload is a decodable pos.BasketSnapshot with one
// line, so the resumed basket both round-trips cleanly (Scenario 1's
// corrupt-payload case wants the OPPOSITE) and, for Scenario 2, leaves
// HasItems() true afterward so POST /api/pos/hold's own guard lets the
// re-park through.
const validSingleLineHeldPayload = `{"lines":[{"sku":"ABC","name":"Apple","qty":1,"price_cents":100}],"total":100}`

// TestResumeOnReplica_GiveBackNeverCommitsAnIntermediateConfirmedMirror is
// Scenario 1.
func TestResumeOnReplica_GiveBackNeverCommitsAnIntermediateConfirmedMirror(t *testing.T) {
	const id = "hold-atomic-giveback"
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/held-sales/claim":
			// A corrupt payload: the resume fails right after the claim and
			// must give the order back.
			row := syncHeldSaleRow{ID: id, Label: "Table 4", Payload: `not json`, TotalMinor: 100, LineCount: 1}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  syncHeldSaleClaimResult{Claimed: true, Known: true, Row: &row},
				"error": nil,
			})
		case "/api/sync/held-sales/upsert":
			// The give-back's own write-through to the primary fails outright,
			// forcing the LOCAL-ONLY fallback this test is about.
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

	// The replica already holds a CONFIRMED mirror of this id (primary_synced
	// = 1) from an earlier successful write-through -- exactly the state a
	// give-back's Upsert step would otherwise re-commit before demoting it.
	if err := repo.Upsert(ctx, data.HeldSale{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1, PrimarySynced: true}); err != nil {
		t.Fatal(err)
	}

	// A test-only trigger pair: records every commit that leaves this row at
	// primary_synced = 1, INSERT or UPDATE alike. Each HeldSalesRepo write is
	// its own single, unwrapped ExecContext (no explicit BEGIN), so it
	// commits (and this trigger fires, or doesn't) the instant that
	// statement returns -- exactly the granularity a concurrent
	// ReconcileWithPrimary could interleave at.
	if _, err := dp.Db.Exec(`CREATE TABLE held_sale_atomic_exposures (id TEXT NOT NULL, op TEXT NOT NULL, seen_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create exposures log: %v", err)
	}
	if _, err := dp.Db.Exec(`
CREATE TRIGGER held_sale_atomic_expose_insert AFTER INSERT ON held_sales
WHEN NEW.primary_synced = 1
BEGIN INSERT INTO held_sale_atomic_exposures (id, op) VALUES (NEW.id, 'insert'); END`); err != nil {
		t.Fatalf("create insert trigger: %v", err)
	}
	if _, err := dp.Db.Exec(`
CREATE TRIGGER held_sale_atomic_expose_update AFTER UPDATE ON held_sales
WHEN NEW.primary_synced = 1
BEGIN INSERT INTO held_sale_atomic_exposures (id, op) VALUES (NEW.id, 'update'); END`); err != nil {
		t.Fatalf("create update trigger: %v", err)
	}

	rec := holdTestPost(mux, "/api/pos/resume", "id="+id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.failed"))) {
		t.Fatalf("a corrupt claimed payload must fail the resume, got %d: %s", rec.Code, rec.Body.String())
	}

	var exposures int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sale_atomic_exposures WHERE id = ?`, id).Scan(&exposures); err != nil {
		t.Fatalf("count exposures: %v", err)
	}
	if exposures != 0 {
		t.Fatalf("ut-docs#3034: the give-back must never commit an intermediate state exposing %s as a confirmed mirror (primary_synced=1) that a concurrent reconcile could drop -- got %d such commit(s)", id, exposures)
	}

	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok || got.PrimarySynced {
		t.Fatalf("the give-back's final row must be primary_synced=false, got ok=%v %+v err=%v", ok, got, err)
	}

	dropped, err := repo.ReconcileWithPrimary(ctx, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("ut-docs#3034: the give-back's local-only copy must survive reconcile, got %d dropped", dropped)
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || !ok {
		t.Fatalf("the order must not be lost, ok=%v err=%v", ok, err)
	}
}

// TestResumeOnReplica_ReparkOfClaimedIDDoesNotStickPrimarySynced is
// Scenario 2.
func TestResumeOnReplica_ReparkOfClaimedIDDoesNotStickPrimarySynced(t *testing.T) {
	const id = "hold-reclaim-repark"
	var upsertRefused bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/held-sales/claim":
			// A VALID payload: the resume succeeds and the claim genuinely
			// takes the order off the primary.
			row := syncHeldSaleRow{ID: id, Label: "Table 4", Payload: validSingleLineHeldPayload, TotalMinor: 100, LineCount: 1}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  syncHeldSaleClaimResult{Claimed: true, Known: true, Row: &row},
				"error": nil,
			})
		case "/api/sync/held-sales/upsert":
			// The re-park's own write-through to the primary also fails,
			// forcing the local-only fallback this test is about.
			upsertRefused = true
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

	// A stale CONFIRMED mirror already sitting locally, exactly as it would
	// be after an earlier successful write-through of the SAME id.
	if err := repo.Upsert(ctx, data.HeldSale{ID: id, Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1, PrimarySynced: true}); err != nil {
		t.Fatal(err)
	}

	// Block the resume's own local cleanup delete, so it fails exactly as
	// the card describes ("error ignored... may fail and leave a
	// primary_synced=1 row"), leaving that stale confirmed row behind.
	if _, err := dp.Db.Exec(`
CREATE TRIGGER held_sale_repark_block_delete BEFORE DELETE ON held_sales
BEGIN SELECT RAISE(ABORT, 'delete blocked'); END`); err != nil {
		t.Fatalf("create delete-blocking trigger: %v", err)
	}

	resumeRec := holdTestPost(mux, "/api/pos/resume", "id="+id)
	if resumeRec.Code != http.StatusOK || strings.Contains(resumeRec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) ||
		strings.Contains(resumeRec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.failed"))) {
		t.Fatalf("resume of a validly-claimed order must succeed even though its own local delete fails, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	if !dp.Engine.HasItems() {
		t.Fatal("test setup: the resumed basket must carry the one line from validSingleLineHeldPayload")
	}
	// Precondition: the blocked delete really did leave the stale confirmed
	// row behind (repo.Delete's error is swallowed -- "a stale row is the
	// lesser evil").
	if got, ok, err := repo.Get(ctx, id); err != nil || !ok || !got.PrimarySynced {
		t.Fatalf("precondition: the blocked delete must leave a stale primary_synced=true row behind, got ok=%v %+v err=%v", ok, got, err)
	}

	if _, err := dp.Db.Exec(`DROP TRIGGER held_sale_repark_block_delete`); err != nil {
		t.Fatalf("drop delete-blocking trigger: %v", err)
	}

	// Re-park the same order (still the live basket's HeldOrigin) through the
	// ordinary Hold endpoint, with the primary's own /upsert also failing.
	holdRec := holdTestPost(mux, "/api/pos/hold", "")
	if holdRec.Code != http.StatusOK {
		t.Fatalf("re-park: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	if !upsertRefused {
		t.Fatal("test setup: the re-park must actually have reached (and been refused by) the primary's /upsert")
	}

	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the re-parked order must still be here locally, ok=%v err=%v", ok, err)
	}
	if got.PrimarySynced {
		t.Fatalf("ut-docs#3034: a local-only re-park of an id THIS TILL just claimed off the primary must not keep a stale confirmed-mirror flag stuck at true -- got %+v", got)
	}

	dropped, err := repo.ReconcileWithPrimary(ctx, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("ut-docs#3034: the re-parked order must survive reconcile, got %d dropped", dropped)
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || !ok {
		t.Fatalf("the order must not be lost, ok=%v err=%v", ok, err)
	}
}

// TestResumeOnReplica_NonClaimedReparkKeepsExistingBehaviour is the design's
// guard test: a re-park that fell back to a LOCAL, never-claimed row (the
// simplest case -- primary unreachable throughout, so heldSaleClaimForResume
// never claims anything and claimed stays false end to end) must keep
// behaving exactly as before this card: still falls back to the plain,
// unguarded local Upsert, still ends up primary_synced=false (nothing here
// was ever confirmed on the primary to begin with), never routed through
// UpsertLocalOnly's demotion path at all.
func TestResumeOnReplica_NonClaimedReparkKeepsExistingBehaviour(t *testing.T) {
	mux, dp := newHoldCrossTillReplica(t, deadPrimaryURL(), "b-123")
	repo := data.NewHeldSalesRepo(dp.Db)
	ctx := context.Background()

	const id = "hold-nonclaimed-repark"
	// Parked while the primary was already unreachable (primary_synced stays
	// false) -- the legitimate outage-taken row.
	if err := repo.Upsert(ctx, data.HeldSale{ID: id, Label: "Table 4", Payload: validSingleLineHeldPayload, TotalMinor: 100, LineCount: 1}); err != nil {
		t.Fatal(err)
	}

	rec := holdTestPost(mux, "/api/pos/resume", "id="+id)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("an outage-taken row must still resume from local (not claimed), got %d: %s", rec.Code, rec.Body.String())
	}
	if !dp.Engine.HasItems() {
		t.Fatal("test setup: the resumed basket must carry items")
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || ok {
		t.Fatalf("a plain local resume must still delete the row locally, ok=%v err=%v", ok, err)
	}

	holdRec := holdTestPost(mux, "/api/pos/hold", "")
	if holdRec.Code != http.StatusOK {
		t.Fatalf("re-park: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the re-parked order must be here locally, ok=%v err=%v", ok, err)
	}
	if got.PrimarySynced {
		t.Fatalf("an outage-only re-park (never claimed, primary never confirmed) must stay primary_synced=false exactly as before, got %+v", got)
	}
}
