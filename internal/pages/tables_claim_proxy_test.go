package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	srv          *httptest.Server
	claimCalls   atomic.Int64
	releaseCalls atomic.Int64
	lastTableID  atomic.Value
	lastAuth     atomic.Value
	claimed      bool
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
	if !tableOccupied(t, dp, t1) {
		t.Fatal("the fallback must actually have written the local claim row")
	}
}

// --- Boot-time release-all (ut-docs#1712) ---

// releaseAllProxyPrimary is a fake primary that answers
// POST /api/sync/tables/release-all, recording how many times it was
// called, the auth/elapsed_ms it was sent, and whether it should succeed
// (simulating an unreachable/erroring primary at boot vs. one that answers
// once the network is back).
type releaseAllProxyPrimary struct {
	srv           *httptest.Server
	calls         atomic.Int64
	lastAuth      atomic.Value
	lastElapsedMS atomic.Value
	succeed       atomic.Bool
}

func newReleaseAllProxyPrimary(t *testing.T, succeed bool) *releaseAllProxyPrimary {
	t.Helper()
	p := &releaseAllProxyPrimary{}
	p.succeed.Store(succeed)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync/tables/release-all" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		p.calls.Add(1)
		p.lastAuth.Store(r.Header.Get("Authorization"))
		_ = r.ParseForm()
		p.lastElapsedMS.Store(r.Form.Get("elapsed_ms"))
		if !p.succeed.Load() {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"released":true},"error":null}`)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func TestTableClaimBootReleaseTick_NotAReplicaNeverCallsOutAndIsDone(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	// sync.primary_url unset: not a replica.
	primary := newReleaseAllProxyPrimary(t, true)
	_ = primary // never called — asserted below via calls==0

	if done := tableClaimBootReleaseTick(context.Background(), dp, time.Now()); !done {
		t.Fatal("a non-replica till has nothing to release and must report done immediately")
	}
	if n := primary.calls.Load(); n != 0 {
		t.Fatalf("a non-replica till must never call the primary, got %d calls", n)
	}
}

func TestTableClaimBootReleaseTick_UnreachablePrimaryIsNotDone(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	primary := newReleaseAllProxyPrimary(t, false) // 500s every call
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "boot-bearer")

	if done := tableClaimBootReleaseTick(context.Background(), dp, time.Now()); done {
		t.Fatal("a failing primary call must report NOT done, so the caller retries")
	}
	if n := primary.calls.Load(); n != 1 {
		t.Fatalf("expected exactly one call, got %d", n)
	}
}

func TestTableClaimBootReleaseTick_SuccessIsDoneAndSendsBearerAndElapsedMS(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	primary := newReleaseAllProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "boot-bearer")
	bootAt := time.Now()

	if done := tableClaimBootReleaseTick(context.Background(), dp, bootAt); !done {
		t.Fatal("a successful primary call must report done")
	}
	if got := primary.lastAuth.Load(); got != "Bearer boot-bearer" {
		t.Fatalf("expected the sync bearer on the request, got %q", got)
	}
	got, _ := primary.lastElapsedMS.Load().(string)
	ms, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("elapsed_ms must be a plain integer, got %q (%v)", got, err)
	}
	// Called immediately after capturing bootAt — a few ms of real work, not
	// a fixed/absolute timestamp.
	if ms < 0 || ms > 5000 {
		t.Fatalf("elapsed_ms should be a small non-negative value for an immediate call, got %d", ms)
	}
}

// The reported elapsed_ms must keep growing across retries from the SAME
// bootAt instant, never reset/pinned — the actual failure mode independent
// review found and empirically reproduced (2026-09-07): a version that
// recomputed "now" fresh on every tick, instead of measuring elapsed time
// since the ORIGINAL bootAt, passed every other test in this file, because
// nothing asserted that elapsed keeps growing with real wall-clock time. A
// tick that always reports ~0 elapsed would let the primary treat this
// till's own later, legitimate claims as pre-boot too — see
// StartTableClaimBootRelease's doc comment for the full reasoning.
func TestTableClaimBootReleaseTick_ElapsedKeepsGrowingFromTheSameBootAt(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	primary := newReleaseAllProxyPrimary(t, false) // 500s every call — never "done"
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "boot-bearer")
	bootAt := time.Now()

	tableClaimBootReleaseTick(context.Background(), dp, bootAt)
	first, _ := primary.lastElapsedMS.Load().(string)
	firstMS, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		t.Fatalf("parse first elapsed_ms %q: %v", first, err)
	}

	time.Sleep(60 * time.Millisecond)
	tableClaimBootReleaseTick(context.Background(), dp, bootAt) // SAME bootAt, not a fresh one

	if n := primary.calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
	second, _ := primary.lastElapsedMS.Load().(string)
	secondMS, err := strconv.ParseInt(second, 10, 64)
	if err != nil {
		t.Fatalf("parse second elapsed_ms %q: %v", second, err)
	}
	if secondMS-firstMS < 40 {
		t.Fatalf("elapsed_ms must keep growing from the SAME bootAt across retries (first=%dms second=%dms) — a version that recomputes bootAt as \"now\" on each tick would report ~0 both times and pass this check incorrectly if it were weaker than a >=40ms margin", firstMS, secondMS)
	}
}

// The background worker is registered against app.Run's drain WaitGroup
// (internal/pages/init.go), so it must return on ctx.Done() rather than
// leak its goroutine — including from inside
// tableClaimBootReleaseInitialDelay, which is far longer than a shutdown is
// willing to wait. Same shape/reasoning as
// TestStartBasePluginRetryShutsDownOnCtxDone /
// TestStartTSEProvisionRetryShutsDownOnCtxDone.
func TestStartTableClaimBootReleaseShutsDownOnCtxDone(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	StartTableClaimBootRelease(ctx, dp, &wg)
	cancel() // cancel while the worker is still in its initial delay

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartTableClaimBootRelease did not return on ctx.Done() — goroutine leak")
	}
}

// StartTableClaimBootRelease itself must capture bootAt ONCE and reuse it
// across every retry of its OWN goroutine loop — the invariant
// TestTableClaimBootReleaseTick_ElapsedKeepsGrowingFromTheSameBootAt cannot
// cover, because that test drives tableClaimBootReleaseTick directly with
// an explicit bootAt it controls, never through StartTableClaimBootRelease's
// own retry loop. Confirmed empirically (self-review, 2026-09-07): mutating
// ONLY the loop's second call site (`tableClaimBootReleaseTick(ctx, d,
// bootAt)` → `tableClaimBootReleaseTick(ctx, d, time.Now())`, i.e. a bug
// only reachable via a real retry, not the first call) left every other
// test in this file green, including the tick-level one above — proving
// this is a genuinely distinct gap, not a duplicate of it.
//
// Shrinks the package-level retry timing (var, not const — see
// tableClaimBootReleaseInitialDelay's doc comment) so the test observes
// several real retries quickly, then checks the elapsed_ms reported on a
// LATER retry reflects the real total time since the goroutine's own boot
// — a version that recomputes bootAt fresh per tick would report ~0 on
// every single call, retry after retry, however many fire.
func TestStartTableClaimBootRelease_RetriesReportElapsedFromTheSameBoot(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	primary := newReleaseAllProxyPrimary(t, false) // always fails — keeps retrying
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "boot-bearer")

	origDelay, origInterval := tableClaimBootReleaseInitialDelay, tableClaimBootReleaseInterval
	tableClaimBootReleaseInitialDelay = 10 * time.Millisecond
	tableClaimBootReleaseInterval = 40 * time.Millisecond
	t.Cleanup(func() {
		tableClaimBootReleaseInitialDelay = origDelay
		tableClaimBootReleaseInterval = origInterval
	})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartTableClaimBootRelease(ctx, dp, &wg)

	deadline := time.Now().Add(3 * time.Second)
	for primary.calls.Load() < 3 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("timed out waiting for 3 retries")
		}
		time.Sleep(5 * time.Millisecond)
	}
	last, _ := primary.lastElapsedMS.Load().(string)
	cancel()
	wg.Wait()

	lastMS, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		t.Fatalf("parse elapsed_ms %q: %v", last, err)
	}
	// By the 3rd call, real elapsed since the goroutine's own boot is at
	// least initialDelay + 2*interval = 10 + 40 + 40 = 90ms. A comfortable
	// margin below that (not the full 90ms, to absorb scheduler jitter)
	// still decisively fails the "always reports ~0" mutation.
	if lastMS < 60 {
		t.Fatalf("3rd retry's elapsed_ms should reflect real time since this goroutine's own boot (expected roughly >=60ms), got %dms — a version that recomputes bootAt fresh per tick would report ~0 on every retry", lastMS)
	}
}
