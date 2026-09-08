package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table-claim write-through, replica side (ut-docs#1703): when
// this till is a replica (sync.primary_url + sync.bearer set), the live
// basket's table pick (POST /api/pos/table) and the hold/resume re-claim
// first ask the PRIMARY to claim the table for this till, so two tills can
// never both hold one table; the primary's answer is authoritative, and a
// successful claim is also mirrored into the local table_claims row so this
// till's own floor plan / picker keep reading it as occupied offline. On ANY
// failure reaching the primary (not a replica, network error, non-200,
// malformed body) the pick falls back — silently — to exactly the local
// ClaimTable path, so an offline station keeps working as before
// (offline-first, ADR-0003). Same fake-primary shape as
// order_status_proxy_test.go / tables_sync_proxy_test.go; setReplicaSettings
// is shared with them.

// claimProxyPrimary is a fake primary that answers the claim/release
// endpoints, recording what it was asked.
type claimProxyPrimary struct {
	srv              *httptest.Server
	claimCalls       atomic.Int64
	releaseCalls     atomic.Int64
	releaseAllCalls  atomic.Int64
	lastTableID      atomic.Value
	lastKeepTableIDs atomic.Value
	lastAuth         atomic.Value
	claimed          bool
}

func newClaimProxyPrimary(t *testing.T, claimed bool) *claimProxyPrimary {
	t.Helper()
	p := &claimProxyPrimary{claimed: claimed}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.lastAuth.Store(r.Header.Get("Authorization"))
		p.lastTableID.Store(r.Form.Get("table_id"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/tables/claim":
			p.claimCalls.Add(1)
			fmt.Fprintf(w, `{"data":{"claimed":%v},"error":null}`, p.claimed)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/tables/release":
			p.releaseCalls.Add(1)
			fmt.Fprint(w, `{"data":{"released":true},"error":null}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/tables/release-all":
			p.releaseAllCalls.Add(1)
			p.lastKeepTableIDs.Store(append([]string{}, r.Form["keep_table_id"]...))
			fmt.Fprint(w, `{"data":{"released":true},"error":null}`)
		default:
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func TestTableClaimWriteThrough_NotAReplicaUsesLocalClaimOnly(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	primary := newClaimProxyPrimary(t, true)
	// sync.primary_url unset: never a replica, never a call.

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("expected TableID %q, got %q", t1, got)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatal("local claim must be taken exactly as before")
	}
	if primary.claimCalls.Load() != 0 {
		t.Fatalf("a non-replica must never call a primary, got %d calls", primary.claimCalls.Load())
	}
}

func TestTableClaimWriteThrough_ReplicaClaimsOnPrimaryAndMirrorsLocally(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	primary := newClaimProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("expected TableID %q, got %q", t1, got)
	}
	if primary.claimCalls.Load() != 1 {
		t.Fatalf("the primary must be asked to claim exactly once, got %d", primary.claimCalls.Load())
	}
	if auth, _ := primary.lastAuth.Load().(string); auth != "Bearer b-123" {
		t.Fatalf("primary must be called with the sync bearer, got %q", auth)
	}
	if id, _ := primary.lastTableID.Load().(string); id != t1 {
		t.Fatalf("primary must be asked for table %q, got %q", t1, id)
	}
	// Mirrored locally, so this till's own floor plan / picker still read it
	// as occupied even if the primary drops off afterwards.
	if !tableOccupied(t, dp, t1) {
		t.Fatal("a primary-granted claim must also be mirrored into the local table_claims row")
	}
}

// The core ut-docs#1703 outcome: the primary says another till has the
// table. The pick must be rejected in place (the same "occupied" toast the
// local race already renders), the basket keeps its current table, and
// NOTHING is written locally — the table is genuinely someone else's.
func TestTableClaimWriteThrough_ReplicaRejectedByPrimaryRendersOccupied(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	primary := newClaimProxyPrimary(t, false)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("a primary-refused pick must leave the basket without a table, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "already occupied") {
		t.Fatalf("expected the occupied toast, got: %s", rec.Body.String())
	}
	if tableOccupied(t, dp, t1) {
		t.Fatal("a primary-refused claim must not be written locally")
	}
	if primary.claimCalls.Load() != 1 {
		t.Fatalf("the primary must have been asked exactly once, got %d", primary.claimCalls.Load())
	}
}

func TestTableClaimWriteThrough_ReplicaFallsBackToLocalWhenPrimaryUnreachable(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-123")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("unreachable primary must fall back to the local claim, TableID = %q", got)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatal("the local claim must be taken exactly as today when the primary is unreachable")
	}
}

func TestTableClaimWriteThrough_ReplicaFallsBackWhenPrimaryAnswersNon200(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 || !tableOccupied(t, dp, t1) {
		t.Fatalf("a non-200 primary answer must fall back to the local claim, TableID=%q occupied=%v", got, tableOccupied(t, dp, t1))
	}
	if calls.Load() != 1 {
		t.Fatalf("the primary must actually have been attempted, got %d calls", calls.Load())
	}
}

func TestTableClaimWriteThrough_ReplicaFallsBackWhenPrimaryAnswersMalformedBody(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 || !tableOccupied(t, dp, t1) {
		t.Fatalf("a malformed primary body must fall back to the local claim, TableID=%q occupied=%v", got, tableOccupied(t, dp, t1))
	}
}

// Release write-through: clearing the table on a replica tells the primary
// to release THIS till's claim there AND always drops the local row too.
func TestTableClaimWriteThrough_ReplicaReleasesOnPrimaryAndLocally(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	primary := newClaimProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("pick: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := posPostForm(mux, "/api/pos/table", "table_id="); rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("clear must leave no table, got %q", got)
	}
	if primary.releaseCalls.Load() != 1 {
		t.Fatalf("the primary must be told to release exactly once, got %d", primary.releaseCalls.Load())
	}
	if id, _ := primary.lastTableID.Load().(string); id != t1 {
		t.Fatalf("primary must be told to release %q, got %q", t1, id)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatal("the local mirror row must be released too")
	}
}

// Release is fire-and-forget: a dead primary must never stop the local
// release (or the basket's own state change).
func TestTableClaimWriteThrough_ReleaseStillClearsLocallyWhenPrimaryUnreachable(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	// Claim locally while not yet a replica, then point at a dead primary.
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("pick: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-123")

	if rec := posPostForm(mux, "/api/pos/table", "table_id="); rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("clear must leave no table, got %q", got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatal("the local claim must be released even when the primary is unreachable")
	}
}

// Direct unit coverage of the proxy helpers' ok/claimed contract, without a
// basket in the way.
func TestClaimTableOnPrimary_Contract(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	ctx := context.Background()

	// Not a replica.
	if ok, _ := claimTableOnPrimary(ctx, dp, tableClaimProxyClient, "x"); ok {
		t.Fatal("not a replica must report ok=false")
	}
	if ok := releaseTableClaimOnPrimary(ctx, dp, tableClaimProxyClient, "x"); ok {
		t.Fatal("not a replica must report ok=false on release")
	}

	primary := newClaimProxyPrimary(t, false)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
	ok, claimed := claimTableOnPrimary(ctx, dp, tableClaimProxyClient, "x")
	if !ok || claimed {
		t.Fatalf("a refusing primary must report ok=true claimed=false, got ok=%v claimed=%v", ok, claimed)
	}
	if !releaseTableClaimOnPrimary(ctx, dp, tableClaimProxyClient, "x") {
		t.Fatal("a reachable primary must report ok=true on release")
	}

	// A 200 whose body lacks the data object is a failure, not a "not claimed".
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"error":null}`)
	}))
	defer bad.Close()
	setReplicaSettings(t, dp.Settings, bad.URL, "b-123")
	if ok, _ := claimTableOnPrimary(ctx, dp, tableClaimProxyClient, "x"); ok {
		t.Fatal("a 200 with a null data object must report ok=false")
	}
}

// TestReleaseAllTableClaimsOnPrimary_Contract (ut-docs#1712): the boot-time
// helper's ok contract, mirroring TestClaimTableOnPrimary_Contract above.
func TestReleaseAllTableClaimsOnPrimary_Contract(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	ctx := context.Background()

	// Not a replica: never calls out, reports ok=false.
	if ok := releaseAllTableClaimsOnPrimary(ctx, dp, tableClaimProxyClient, []string{"T1"}); ok {
		t.Fatal("not a replica must report ok=false")
	}

	primary := newClaimProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
	if ok := releaseAllTableClaimsOnPrimary(ctx, dp, tableClaimProxyClient, []string{"T1", "T2"}); !ok {
		t.Fatal("a reachable primary must report ok=true")
	}
	if primary.releaseAllCalls.Load() != 1 {
		t.Fatalf("the primary must be called exactly once, got %d", primary.releaseAllCalls.Load())
	}
	if got := primary.lastKeepTableIDs.Load(); !reflect.DeepEqual(got, []string{"T1", "T2"}) {
		t.Fatalf("the keep list must be forwarded to the primary as-is, got %#v", got)
	}

	// Any failure (non-200, unreachable, malformed body) is ok=false, same
	// contract as every other proxy helper.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"error":null}`)
	}))
	defer bad.Close()
	setReplicaSettings(t, dp.Settings, bad.URL, "b-123")
	if ok := releaseAllTableClaimsOnPrimary(ctx, dp, tableClaimProxyClient, nil); ok {
		t.Fatal("a 200 with a null data object must report ok=false")
	}
}

// The helper the handlers call: a primary-granted claim mirrors locally; a
// primary-refused one writes nothing; a failed primary falls back to
// repo.ClaimTable.
func TestClaimTableWriteThrough_Helper(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()
	t1 := createTestTable(t, dp, "T1")
	t2 := createTestTable(t, dp, "T2")

	granting := newClaimProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, granting.srv.URL, "b-123")
	if claimed, err := claimTableWriteThrough(ctx, dp, repo, t1); err != nil || !claimed {
		t.Fatalf("granted: claimed=%v err=%v", claimed, err)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatal("granted claim must be mirrored locally")
	}

	refusing := newClaimProxyPrimary(t, false)
	setReplicaSettings(t, dp.Settings, refusing.srv.URL, "b-123")
	if claimed, err := claimTableWriteThrough(ctx, dp, repo, t2); err != nil || claimed {
		t.Fatalf("refused: claimed=%v err=%v, want false/nil", claimed, err)
	}
	if tableOccupied(t, dp, t2) {
		t.Fatal("refused claim must not be written locally")
	}
}

// The PRIMARY's own basket must get the same ~2-minute takeover every other
// till gets (independent review, 2026-09-07). The primary's table_claims also
// holds the live claims of REPLICAS, and the primary's own pick never talks to
// a primary — it takes claimTableWriteThrough's local branch. If that branch
// were a plain ClaimTable, a replica that died holding a table would block the
// main till from that table forever: nothing on this path revisits another
// till's row, and the boot sweep deliberately no longer clears it. Reconciling
// on the local branch too is what keeps the manual's promise ("once that till
// has been out of touch for about two minutes, any other till may take the
// table") true on the main till as well.
// touchTill sets a till's last_seen_at — the single input the claim TTL
// judges "is this till online" on. Raw SQL is fine in tests.
func touchTill(t *testing.T, dp *common.Deps, tillID string, seen time.Time) {
	t.Helper()
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`,
		seen.UTC().Format(time.RFC3339), tillID); err != nil {
		t.Fatalf("touch till: %v", err)
	}
}

func TestClaimTableWriteThrough_LocalBranchExpiresAnotherTillsStaleClaim(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()

	// Not a replica: no sync settings, so every claim below takes the local
	// branch — which is exactly how the primary's own basket behaves.
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	var till2 string
	if err := dp.Db.QueryRow(`SELECT id FROM tills WHERE name = 'Till 2'`).Scan(&till2); err != nil {
		t.Fatal(err)
	}
	// InsertTill leaves last_seen_at NULL, which the TTL rule reads as "never
	// seen" — i.e. already stale. Make till 2 genuinely live first, or the
	// "still blocks the primary" half below proves nothing.
	touchTill(t, dp, till2, time.Now().UTC())

	live := createTestTable(t, dp, "T1")
	dead := createTestTable(t, dp, "T2")

	// Till 2 holds both tables on this (primary) database.
	for _, id := range []string{live, dead} {
		if claimed, err := repo.ClaimTableForTill(ctx, id, till2, time.Now().Add(-tillClaimTTL)); err != nil || !claimed {
			t.Fatalf("seed claim %s: claimed=%v err=%v", id, claimed, err)
		}
	}

	// While till 2 is live, the primary's own pick is refused — the claim is
	// genuinely someone else's.
	if claimed, err := claimTableWriteThrough(ctx, dp, repo, live); err != nil || claimed {
		t.Fatalf("a live till's claim must still block the primary, got claimed=%v err=%v", claimed, err)
	}

	// Till 2 goes dark: last_seen_at falls outside the TTL.
	touchTill(t, dp, till2, time.Now().UTC().Add(-30*time.Minute))

	claimed, err := claimTableWriteThrough(ctx, dp, repo, dead)
	if err != nil || !claimed {
		t.Fatalf("the primary must be able to take a dead till's table, got claimed=%v err=%v", claimed, err)
	}
	var owner string
	if err := dp.Db.QueryRow(`SELECT till_id FROM table_claims WHERE table_id = ?`, dead).Scan(&owner); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if owner != "" {
		t.Fatalf("the primary's own claim must be a '' row, got %q", owner)
	}
}

// ...and if the reconciling form of the local claim fails for any reason, the
// pick still takes the plain local claim rather than failing (independent
// review, 2026-09-07). This branch is the one a till runs when nothing else is
// reachable, so adding reconciliation to it must not have made it easier to
// fail than the single INSERT it replaced — offline-first, ADR-0003. Simulated
// here by removing the `tills` table the reconciliation joins against.
//
// The final check reads table_claims directly rather than through
// tableOccupied/ListTablesWithState (ut-docs#1714): that query now ALSO
// joins tills, to surface which till holds a live claim, so it can no
// longer run once this test has dropped the table — a real till never runs
// with tills missing entirely (it's created by a core migration), so this
// stays a faithful check of the thing the test actually verifies (the
// fallback wrote the local claim row), not a weakening of it.
func TestClaimTableWriteThrough_LocalBranchStillClaimsWhenReconcileFails(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()
	t1 := createTestTable(t, dp, "T1")

	if _, err := dp.Db.Exec(`DROP TABLE tills`); err != nil {
		t.Fatalf("drop tills: %v", err)
	}

	claimed, err := claimTableWriteThrough(ctx, dp, repo, t1)
	if err != nil || !claimed {
		t.Fatalf("the local claim must still succeed when reconciliation cannot run, got claimed=%v err=%v", claimed, err)
	}
	var claimCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, t1).Scan(&claimCount); err != nil {
		t.Fatalf("read table_claims: %v", err)
	}
	if claimCount != 1 {
		t.Fatal("the fallback must actually have written the local claim row")
	}
}
