package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ADR-0093 Amendment B (ut-docs#2712): a held sale must never be tendered
// twice across the fleet. Resume on a replica now goes through the
// primary's atomic POST /api/sync/held-sales/claim (delete + tombstone in
// one transaction) BEFORE the order is restored into the live basket, and
// every primary-side deletion leaves a short-lived tombstone, so neither a
// lost push reply nor two tills racing the same resume can hand the same
// order to two baskets.

func decodeSyncHeldSaleClaim(t *testing.T, rec *httptest.ResponseRecorder) syncHeldSaleClaimResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data  *syncHeldSaleClaimResult `json:"data"`
		Error any                      `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if resp.Data == nil {
		t.Fatalf("expected a data object, got body %q", rec.Body.String())
	}
	return *resp.Data
}

// The endpoint's own contract: bearer-authed like its three siblings, 400
// on a bad body, and the three answers -- claimed (with the full row),
// resolved by another till (known), nothing contradicting the caller's own
// copy (neither: never learned of, or only the caller's own tombstone).
func TestSyncHeldSales_Claim(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")
	repo := data.NewHeldSalesRepo(dp.Db)
	if err := repo.Upsert(t.Context(), data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 700, LineCount: 2}); err != nil {
		t.Fatal(err)
	}

	for _, bearer := range []string{"", "wrong"} {
		if rec := postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("claim bearer %q: status = %d, want 401", bearer, rec.Code)
		}
	}
	for _, body := range []string{`not json`, `{"id":"  "}`} {
		if rec := postSyncHeldSaleJSON(mux, "claim", body, "bearer-t2"); rec.Code != http.StatusBadRequest {
			t.Fatalf("claim %q: status = %d, want 400", body, rec.Code)
		}
	}
	if _, ok, _ := repo.Get(t.Context(), "h1"); !ok {
		t.Fatal("a refused claim must not touch the row")
	}

	got := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, "bearer-t2"))
	if !got.Claimed || got.Row == nil || got.Row.ID != "h1" || got.Row.Payload != `{"lines":[]}` || got.Row.TotalMinor != 700 || got.Row.Label != "Table 4" {
		t.Fatalf("the first claim must win with the full row, got %+v row=%+v", got, got.Row)
	}
	if _, ok, _ := repo.Get(t.Context(), "h1"); ok {
		t.Fatal("a claimed row must be gone from the primary")
	}
	other := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, "bearer-t3"))
	if other.Claimed || !other.Known || other.Row != nil {
		t.Fatalf("another till's claim must be refused as resolved elsewhere, got %+v", other)
	}
	// Independent review of #2712: the claiming till's OWN tombstone is not
	// "resolved elsewhere" -- its re-park of that order may have fallen back
	// to local-only, and the copy it holds is then the shop's newest.
	own := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, "bearer-t2"))
	if own.Claimed || own.Known || own.Row != nil {
		t.Fatalf("the same till's later claim must answer claimed=false, known=false, got %+v", own)
	}
	never := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", `{"id":"never"}`, "bearer-t2"))
	if never.Claimed || never.Known {
		t.Fatalf("an id the primary never learned of must be claimed=false, known=false, got %+v", never)
	}
}

// Test 2 (concurrent resume): several tills POSTing /claim for the same
// order at once -- exactly one gets it. Repeated, since one run can miss
// the race window by chance.
func TestSyncHeldSales_ConcurrentClaimsExactlyOneWins(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	const rounds, racers = 10, 4
	// One enrolled till per racer: it is DIFFERENT tills racing the same
	// order, and a loser is told known=true precisely because the winner's
	// tombstone is another till's.
	for j := 0; j < racers; j++ {
		seedSyncOrdersTill(t, dp, fmt.Sprintf("Till %d", j), fmt.Sprintf("bearer-t%d", j))
	}
	for i := 0; i < rounds; i++ {
		if err := repo.Upsert(t.Context(), data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`}); err != nil {
			t.Fatal(err)
		}
		results := make([]*httptest.ResponseRecorder, racers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < racers; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start
				results[j] = postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, fmt.Sprintf("bearer-t%d", j))
			}(j)
		}
		close(start)
		wg.Wait()
		winners := 0
		for _, rec := range results {
			res := decodeSyncHeldSaleClaim(t, rec)
			if res.Claimed {
				winners++
			} else if !res.Known {
				t.Fatalf("round %d: a losing claim must be told the order was resolved (known=true), got %+v", i, res)
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: exactly one of %d racing claims must win, got %d", i, racers, winners)
		}
	}
}

// Test 4: the plain primary-side /delete (still served for a replica on the
// previous version during a rollout) leaves the same tombstone, so a stale
// mirror of an order resolved through it expires the same way.
func TestSyncHeldSales_DeleteLeavesTombstone(t *testing.T) {
	mux, dp := newSyncHeldSalesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")
	if err := data.NewHeldSalesRepo(dp.Db).Upsert(t.Context(), data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	if rec := postSyncHeldSaleJSON(mux, "delete", `{"id":"h1"}`, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	got := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", `{"id":"h1"}`, "bearer-t3"))
	if got.Claimed || !got.Known {
		t.Fatalf("an order deleted on the primary must answer a later claim as resolved (known=true), got %+v", got)
	}
}

// claimOnPrimaryAs is "another till" taking the order: the same bearer-authed
// POST /claim a real replica's claimHeldSaleOnPrimary makes.
func claimOnPrimaryAs(t *testing.T, primaryURL, bearer, id string) syncHeldSaleClaimResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, primaryURL+"/api/sync/held-sales/claim", strings.NewReader(fmt.Sprintf(`{"id":%q}`, id)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /claim: %v", err)
	}
	defer resp.Body.Close()
	rec := httptest.NewRecorder()
	rec.Code = resp.StatusCode
	if _, err := rec.Body.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return decodeSyncHeldSaleClaim(t, rec)
}

// Test 1 (lost reply -> double tender), the bug itself: till B parks an
// order, the PRIMARY applies it, but till B's client never sees the reply
// (seeded here directly on both sides rather than faking network timing:
// the primary holds the row, till B's local copy is primary_synced=0 --
// indistinguishable from a genuine offline park). Till A then takes and
// tenders the order via /claim. When till B later taps resume on its stale
// copy it must be refused with the existing not-found toast and nothing
// restored -- never a second sale for one order.
func TestResumeOnReplica_LostPushReplyCannotDoubleTender(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	order := data.HeldSale{ID: "hold-lost-reply", Label: "Table 4", Payload: `{"lines":[]}`, TotalMinor: 100, LineCount: 1}
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, order); err != nil || !applied {
		t.Fatalf("seed: the primary applied till B's push: applied=%v err=%v", applied, err)
	}
	if err := data.NewHeldSalesRepo(ct.dp.Db).Upsert(ctx, order); err != nil { // primary_synced=0: the reply was lost
		t.Fatalf("seed: till B's local-only copy: %v", err)
	}

	// Till A -- a DIFFERENT enrolled till (a-456), not till B's own bearer --
	// takes the order (and tenders it -- out of scope here).
	if res := claimOnPrimaryAs(t, ct.primaryURL, "a-456", order.ID); !res.Claimed {
		t.Fatalf("till A's claim must win, got %+v", res)
	}

	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+order.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("till B's resume of an order till A already took must be refused with the not-found toast, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct.dp.Engine.HasItems() || ct.dp.Engine.HeldOrigin().ID == order.ID {
		t.Fatal("double tender: till B restored an order till A already took")
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, order.ID); ok {
		t.Fatal("till B's stale copy of a resolved order must be dropped")
	}
	// And till B's own later claim (e.g. a retried resume) is refused too.
	if res := claimOnPrimaryAs(t, ct.primaryURL, "b-123", order.ID); res.Claimed || !res.Known {
		t.Fatalf("a later claim of the taken order must be refused as resolved, got %+v", res)
	}
}

// Test 2, end to end: till B resumes an order through the real resume
// handler; the claim is what removed it from the primary, so a second till
// arriving afterwards is refused -- and B's basket holds the order exactly
// once.
func TestResumeOnReplica_ClaimsOnPrimaryBeforeRestoring(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 4")

	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("resume: got %d: %s", rec.Code, rec.Body.String())
	}
	if ct.dp.Engine.HeldOrigin().ID != local.ID || !ct.dp.Engine.HasItems() {
		t.Fatalf("the claimed order must be restored, got origin %q", ct.dp.Engine.HeldOrigin().ID)
	}
	if res := claimOnPrimaryAs(t, ct.primaryURL, "a-456", local.ID); res.Claimed || !res.Known {
		t.Fatalf("the resume must have claimed (and tombstoned) the order on the primary, a second till got %+v", res)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); ok {
		t.Fatal("the resumed row must be deleted locally")
	}
}

// Independent review of #2712 -- the everyday resume -> add an item ->
// park-again cycle, with the primary dropping off for the re-park only. The
// resume claimed (and tombstoned) the id on the primary; the re-park lands
// under the SAME id (ut-docs#1918) as a local-only row because the primary
// was unreachable; when the primary is back and the cashier resumes, the
// claim finds no row but a fresh tombstone -- this till's OWN. Its genuinely
// newer copy must NOT be refused and dropped as "resolved elsewhere":
// offline-first (ADR-0003, ADR-0093 Decision 4), an order parked during an
// outage stays resumable. Before the review fix this lost the whole order.
func TestResumeOnReplica_OfflineReparkAfterOnlineResumeStillResumes(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	local := ct.parkOnReplica(t, "Table 4")

	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID); rec.Code != http.StatusOK || ct.dp.Engine.HeldOrigin().ID != local.ID {
		t.Fatalf("online resume: %d origin=%q", rec.Code, ct.dp.Engine.HeldOrigin().ID)
	}

	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")
	if rec := holdTestPost(ct.mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("offline re-park: %d: %s", rec.Code, rec.Body.String())
	}
	if row, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); !ok || row.PrimarySynced {
		t.Fatalf("precondition: the offline re-park must be a local-only row under the same id, got ok=%v %+v", ok, row)
	}
	if _, ok, _ := ct.primaryHeld.Get(context.Background(), local.ID); ok {
		t.Fatal("precondition: the primary must not hold the re-parked row")
	}

	setReplicaSettings(t, ct.dp.Settings, ct.primaryURL, "b-123")
	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("a newer offline re-park of an order this till itself resumed must still resume, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct.dp.Engine.HeldOrigin().ID != local.ID || !ct.dp.Engine.HasItems() {
		t.Fatalf("the re-parked order must be restored, not lost: origin=%q items=%v", ct.dp.Engine.HeldOrigin().ID, ct.dp.Engine.HasItems())
	}
	// The same copy IS stale once ANOTHER till resolves the order: re-park
	// it online (the primary holds it again), let till A claim it, and this
	// till's later resume is refused as before.
	if rec := holdTestPost(ct.mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("online re-park: %d", rec.Code)
	}
	if res := claimOnPrimaryAs(t, ct.primaryURL, "a-456", local.ID); !res.Claimed {
		t.Fatalf("till A's claim of the re-parked order must win, got %+v", res)
	}
	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("once another till took the order, this till's resume must be refused, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct.dp.Engine.HasItems() {
		t.Fatal("double tender: an order another till took was restored here")
	}
}

// Test 3 (offline-first regression guard): an order genuinely parked while
// the primary was unreachable -- no row and no tombstone there -- still
// resumes locally with the primary reachable again (claim answers
// known=false), exactly as before this change.
func TestResumeOnReplica_OfflineParkStillResumesLocally(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")
	offline := ct.parkOnReplica(t, "Offline")
	setReplicaSettings(t, ct.dp.Settings, ct.primaryURL, "b-123")
	if _, ok, _ := ct.primaryHeld.Get(context.Background(), offline.ID); ok {
		t.Fatal("precondition: the offline park must not be on the primary")
	}

	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+offline.ID)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("an offline-parked order must still resume locally, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct.dp.Engine.HeldOrigin().ID != offline.ID || !ct.dp.Engine.HasItems() {
		t.Fatal("the offline-parked order must be restored onto the live basket")
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, offline.ID); ok {
		t.Fatal("the resumed offline row must be deleted locally, as before")
	}
}

// A claim is destructive on the primary, so a resume that claims the order
// and then cannot go through with it (here: a corrupt payload) must put the
// order back rather than lose it -- the pre-claim contract was "a failed
// resume costs nothing".
func TestResumeOnReplica_FailedResumeAfterClaimGivesOrderBack(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	corrupt := data.HeldSale{ID: "hold-corrupt", Label: "Broken", Payload: `not json`, TotalMinor: 100, LineCount: 1}
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, corrupt); err != nil || !applied {
		t.Fatalf("seed: applied=%v err=%v", applied, err)
	}
	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+corrupt.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.failed"))) {
		t.Fatalf("a corrupt payload must fail the resume, got %d: %s", rec.Code, rec.Body.String())
	}
	if back, ok, _ := ct.primaryHeld.Get(ctx, corrupt.ID); !ok || back.Payload != corrupt.Payload || back.Label != corrupt.Label {
		t.Fatalf("a failed resume must give the claimed order back to the primary, got ok=%v %+v", ok, back)
	}
}

// The primary till's OWN resume is a primary-side deletion too: it claims
// the order from its own held_sales (atomic against a replica's /claim) and
// leaves the tombstone, so a replica's lost-reply copy of an order the
// PRIMARY tendered is refused just the same.
func TestResumeOnPrimaryTill_LeavesTombstone(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	registerHoldAPI(mux, dp)
	registerSyncHeldSales(mux, dp)
	seedSyncOrdersTill(t, dp, "Replica", "b-123")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("hold: %d", rec.Code)
	}
	held := holdTestOnlyRow(t, dp)
	if rec := holdTestPost(mux, "/api/pos/resume", "id="+held.ID); rec.Code != http.StatusOK || dp.Engine.HeldOrigin().ID != held.ID {
		t.Fatalf("resume on the primary till: %d origin=%q", rec.Code, dp.Engine.HeldOrigin().ID)
	}
	res := decodeSyncHeldSaleClaim(t, postSyncHeldSaleJSON(mux, "claim", fmt.Sprintf(`{"id":%q}`, held.ID), "b-123"))
	if res.Claimed || !res.Known {
		t.Fatalf("a replica's claim of an order the primary till itself resumed must be refused as resolved, got %+v", res)
	}
}

// claimHeldSaleOnPrimary's ok contract, mirroring
// TestFetchHeldSalesFromPrimary_Contract: ok=false on not-a-replica and on
// every transport failure, so the caller falls back to the local resume.
func TestClaimHeldSaleOnPrimary_Contract(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	ctx := context.Background()
	if ok, _, _, _ := claimHeldSaleOnPrimary(ctx, dp, heldSaleProxyClient, "h1"); ok {
		t.Fatal("not a replica must report ok=false")
	}
	setReplicaSettings(t, dp.Settings, deadPrimaryURL(), "b-123")
	if ok, _, _, _ := claimHeldSaleOnPrimary(ctx, dp, heldSaleProxyClient, "h1"); ok {
		t.Fatal("an unreachable primary must report ok=false")
	}
	for name, h := range map[string]http.HandlerFunc{
		"non-200": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"malformed": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `not json`)
		},
		"claimed without a row": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":{"claimed":true,"known":true,"row":null},"error":null}`)
		},
	} {
		srv := httptest.NewServer(h)
		setReplicaSettings(t, dp.Settings, srv.URL, "b-123")
		if ok, _, _, _ := claimHeldSaleOnPrimary(ctx, dp, heldSaleProxyClient, "h1"); ok {
			t.Fatalf("%s: must report ok=false", name)
		}
		srv.Close()
	}
}
