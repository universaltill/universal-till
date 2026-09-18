package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Cross-till held-sale write-through, replica side (ADR-0093, ut-docs#1920):
// when this till is a replica (sync.primary_url + sync.bearer set), a
// park / re-park first pushes the full row to the PRIMARY's guarded upsert
// and a resume first tells the primary to delete it; a primary-applied
// write is also mirrored into the local held_sales row so this till's own
// strip / popup / Open orders page keep reading it offline. On ANY failure
// reaching the primary (not a replica, network error, non-200, malformed
// body) -- AND on the primary's predicate refusal -- the write falls back,
// silently, to exactly the local path, so an offline station keeps working
// as before (offline-first, ADR-0003). Same fake-primary shape as
// tables_claim_proxy_test.go; setReplicaSettings is shared with it.

// heldSaleProxyPrimary is a fake primary that answers the held-sale
// endpoints, recording what it was asked.
type heldSaleProxyPrimary struct {
	srv         *httptest.Server
	upsertCalls atomic.Int64
	deleteCalls atomic.Int64
	listCalls   atomic.Int64
	lastAuth    atomic.Value
	lastUpsert  atomic.Value // syncHeldSaleRow
	lastDelete  atomic.Value // string id
	lastUpdated atomic.Value // string, the updated_at this fake primary answered with
	applied     bool
	listRows    []syncHeldSaleRow
}

// newHeldSaleProxyPrimary's fake upsert handler mimics the real primary's
// own stamping (ut-docs#2271, sync_held_sales.go): a blank incoming
// updated_at is stamped with THIS fake primary's clock, never the caller's,
// and the applied value is always the one handed back on the wire -- so a
// test asserting the replica's local mirror matches "the primary's value"
// can read it from lastUpdated rather than from whatever (if anything) it
// sent.
func newHeldSaleProxyPrimary(t *testing.T, applied bool, listRows ...syncHeldSaleRow) *heldSaleProxyPrimary {
	t.Helper()
	p := &heldSaleProxyPrimary{applied: applied, listRows: listRows}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.lastAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/held-sales/upsert":
			p.upsertCalls.Add(1)
			var row syncHeldSaleRow
			_ = json.NewDecoder(r.Body).Decode(&row)
			p.lastUpsert.Store(row)
			updatedAt := row.UpdatedAt
			if updatedAt == "" {
				updatedAt = time.Now().UTC().Format(heldSaleTimeLayout)
			}
			p.lastUpdated.Store(updatedAt)
			fmt.Fprintf(w, `{"data":{"applied":%v,"updated_at":%q},"error":null}`, p.applied, updatedAt)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/held-sales/delete":
			p.deleteCalls.Add(1)
			var in syncHeldSaleDeleteRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			p.lastDelete.Store(in.ID)
			fmt.Fprint(w, `{"data":{"deleted":true},"error":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/sync/held-sales":
			p.listCalls.Add(1)
			rows := p.listRows
			if rows == nil {
				rows = []syncHeldSaleRow{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "error": nil})
		default:
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// deadPrimaryURL is a URL nothing listens on any more.
func deadPrimaryURL() string {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := dead.URL
	dead.Close()
	return u
}

func heldSaleRowOnLocal(t *testing.T, dp *common.Deps, id string) (data.HeldSale, bool) {
	t.Helper()
	h, ok, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return h, ok
}

var proxyTestHeldSale = data.HeldSale{ID: "h1", Label: "Table 4", TotalMinor: 1250, LineCount: 3, Payload: `{"lines":[]}`}

func TestHeldSaleWriteThrough_NotAReplicaUsesLocalOnly(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	primary := newHeldSaleProxyPrimary(t, true)
	// sync.primary_url unset: never a replica, never a call.

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedLocalOnly {
		t.Fatalf("not a replica must write locally only: outcome=%v err=%v", outcome, err)
	}
	if got, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok || got.Payload != `{"lines":[]}` || got.UpdatedAt == "" || got.PrimarySynced {
		t.Fatalf("the local row must be written exactly as before, with updated_at stamped and never marked primary_synced, got ok=%v %+v", ok, got)
	}
	if primary.upsertCalls.Load() != 0 {
		t.Fatalf("a non-replica must never call a primary, got %d calls", primary.upsertCalls.Load())
	}
	if primaryDeleted, err := heldSaleDeleteWriteThrough(context.Background(), dp, repo, "h1"); err != nil || primaryDeleted {
		t.Fatalf("not a replica must delete locally only: primaryDeleted=%v err=%v", primaryDeleted, err)
	}
	if _, ok := heldSaleRowOnLocal(t, dp, "h1"); ok {
		t.Fatal("the local row must be deleted")
	}
}

func TestHeldSaleWriteThrough_ReplicaUpsertsOnPrimaryAndMirrorsLocally(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	primary := newHeldSaleProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedPrimary {
		t.Fatalf("expected the primary to take the write: outcome=%v err=%v", outcome, err)
	}
	if primary.upsertCalls.Load() != 1 {
		t.Fatalf("the primary must be asked to upsert exactly once, got %d", primary.upsertCalls.Load())
	}
	if auth, _ := primary.lastAuth.Load().(string); auth != "Bearer b-123" {
		t.Fatalf("primary must be called with the sync bearer, got %q", auth)
	}
	sent, _ := primary.lastUpsert.Load().(syncHeldSaleRow)
	if sent.ID != "h1" || sent.Label != "Table 4" || sent.TotalMinor != 1250 || sent.LineCount != 3 || sent.Payload != `{"lines":[]}` {
		t.Fatalf("the full row must reach the primary, got %+v", sent)
	}
	if sent.UpdatedAt != "" {
		t.Fatalf("the write-through must NOT pre-stamp updated_at on the wire (ut-docs#2271) -- this till's own clock is exactly what let a slow-clocked till lose the guard to a fast-clocked one; the PRIMARY is the one clock that stamps it now, got %q", sent.UpdatedAt)
	}
	primaryUpdatedAt, _ := primary.lastUpdated.Load().(string)
	if primaryUpdatedAt == "" {
		t.Fatal("the fake primary must have stamped and reported an updated_at")
	}
	// Mirrored locally, carrying the SAME updated_at the primary now holds,
	// so this till's own strip / popup / Open orders page still read it even
	// if the primary drops off afterwards.
	got, ok := heldSaleRowOnLocal(t, dp, "h1")
	if !ok || got.Payload != `{"lines":[]}` {
		t.Fatalf("a primary-applied write must be mirrored into the local row, got ok=%v %+v", ok, got)
	}
	if got.UpdatedAt != primaryUpdatedAt {
		t.Fatalf("the local mirror must carry the primary's updated_at %q, got %q", primaryUpdatedAt, got.UpdatedAt)
	}
	// ADR-0093 Amendment A: a mirror of a primary-applied write is marked
	// confirmed-on-primary, so a later successful list can tell it apart
	// from an outage-taken row.
	if !got.PrimarySynced {
		t.Fatal("a primary-applied write's local mirror must be primary_synced")
	}
}

// The predicate refusal (ADR-0093): the primary answers applied=false --
// a NEWER write for this id already landed there. Reported back distinctly
// (so a caller can tell it from an outage), and the write still proceeds
// against the local row only, exactly like every other fallback.
func TestHeldSaleWriteThrough_ReplicaRefusedByPrimaryIsReportedAndFallsBackLocally(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	primary := newHeldSaleProxyPrimary(t, false)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil {
		t.Fatalf("a refusal is not an error: %v", err)
	}
	if outcome != heldSaleSyncRefused {
		t.Fatalf("a primary refusal must be reported as heldSaleSyncRefused, got %v", outcome)
	}
	if primary.upsertCalls.Load() != 1 {
		t.Fatalf("the primary must have been asked exactly once, got %d", primary.upsertCalls.Load())
	}
	if got, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok || got.Payload != `{"lines":[]}` {
		t.Fatalf("a refused write must still land locally (the pre-ADR behaviour), got ok=%v %+v", ok, got)
	}
}

func TestHeldSaleWriteThrough_ReplicaFallsBackToLocalWhenPrimaryUnreachable(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	setReplicaSettings(t, dp.Settings, deadPrimaryURL(), "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedLocalOnly {
		t.Fatalf("fallback must be silent: outcome=%v err=%v", outcome, err)
	}
	if got, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok || got.Payload != `{"lines":[]}` || got.PrimarySynced {
		t.Fatalf("an unreachable primary must fall back to the local write, never marked primary_synced, got ok=%v %+v", ok, got)
	}
	// Delete: the local row always goes, primary or not.
	if primaryDeleted, err := heldSaleDeleteWriteThrough(context.Background(), dp, repo, "h1"); err != nil || primaryDeleted {
		t.Fatalf("delete with an unreachable primary: primaryDeleted=%v err=%v", primaryDeleted, err)
	}
	if _, ok := heldSaleRowOnLocal(t, dp, "h1"); ok {
		t.Fatal("the local row must be deleted even when the primary is unreachable")
	}
}

func TestHeldSaleWriteThrough_ReplicaFallsBackWhenPrimaryAnswersNon200(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedLocalOnly {
		t.Fatalf("fallback must be silent: outcome=%v err=%v", outcome, err)
	}
	if got, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok || got.Payload != `{"lines":[]}` {
		t.Fatalf("a non-200 primary answer must fall back to the local write, got ok=%v %+v", ok, got)
	}
	if calls.Load() != 1 {
		t.Fatalf("the primary must actually have been attempted, got %d calls", calls.Load())
	}
}

func TestHeldSaleWriteThrough_ReplicaFallsBackWhenPrimaryAnswersMalformedBody(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedLocalOnly {
		t.Fatalf("fallback must be silent: outcome=%v err=%v", outcome, err)
	}
	if got, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok || got.Payload != `{"lines":[]}` {
		t.Fatalf("a malformed primary body must fall back to the local write, got ok=%v %+v", ok, got)
	}

	// A 200 whose body lacks the data object is a failure too, not a "refused".
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"error":null}`)
	}))
	defer bad.Close()
	setReplicaSettings(t, dp.Settings, bad.URL, "b-123")
	if ok, _, _ := upsertHeldSaleOnPrimary(context.Background(), dp, heldSaleProxyClient, proxyTestHeldSale); ok {
		t.Fatal("a 200 with a null data object must report ok=false")
	}
}

// TestHeldSaleWriteThrough_PreFixPrimaryOmittingUpdatedAtStillMirrors is the
// mixed-version window of a ut-docs#2271 rollout (independent review): a
// REPLICA on this version talking to a primary still on the PRE-#2271
// build. That older primary applies the write and answers
// `{"applied":true}` with NO updated_at field at all, which decodes to "".
//
// The write itself is already correct against such a primary -- its
// UpsertIfNewer COALESCEs a blank updated_at to its OWN datetime('now'),
// so the guard is measured against the primary's clock either way, which
// is the entire point of the fix. What must not happen is the replica
// treating that missing field as an authoritative "" and mirroring a
// blank/garbage timestamp locally: the local row must still land, still be
// marked primary_synced, and still carry a usable updated_at.
func TestHeldSaleWriteThrough_PreFixPrimaryOmittingUpdatedAtStillMirrors(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	var sent atomic.Value
	// A pre-#2271 primary: applies, reports applied only, never updated_at.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var row syncHeldSaleRow
		_ = json.NewDecoder(r.Body).Decode(&row)
		sent.Store(row)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"applied":true},"error":null}`)
	}))
	defer old.Close()
	setReplicaSettings(t, dp.Settings, old.URL, "b-123")

	outcome, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale)
	if err != nil || outcome != heldSaleSyncedPrimary {
		t.Fatalf("an applied write must still report synced-on-primary: outcome=%v err=%v", outcome, err)
	}
	// Still no replica-side stamping, even against an old primary -- the
	// blank is exactly what makes that primary's own COALESCE take over.
	if row, _ := sent.Load().(syncHeldSaleRow); row.UpdatedAt != "" {
		t.Fatalf("the wire row must stay unstamped against a pre-fix primary too, got %q", row.UpdatedAt)
	}
	got, ok := heldSaleRowOnLocal(t, dp, "h1")
	if !ok || got.Payload != `{"lines":[]}` {
		t.Fatalf("the row must still be mirrored locally, got ok=%v %+v", ok, got)
	}
	if !got.PrimarySynced {
		t.Fatalf("an applied write is a confirmed sighting on the primary, got %+v", got)
	}
	if strings.TrimSpace(got.UpdatedAt) == "" {
		t.Fatalf("a primary that omits updated_at must not leave the local mirror with a blank one, got %+v", got)
	}
	if _, err := time.Parse(heldSaleTimeLayout, got.UpdatedAt); err != nil {
		t.Fatalf("the mirrored updated_at %q must still be a usable %s timestamp: %v", got.UpdatedAt, heldSaleTimeLayout, err)
	}

	// And the case that actually distinguishes "report nothing" from
	// "report a blank": a caller that DID supply an updated_at. The old
	// primary stores that value verbatim (its COALESCE only fires on a
	// blank), so the local mirror must keep it too -- a missing field on
	// the wire must never be read as an authoritative "" that silently
	// re-derives the row's timestamp from this till's own clock, which is
	// the very thing ut-docs#2271 set out to stop.
	explicit := proxyTestHeldSale
	explicit.ID = "h-explicit"
	explicit.UpdatedAt = "2026-09-15 10:00:00"
	if _, err := heldSaleWriteThrough(context.Background(), dp, repo, explicit); err != nil {
		t.Fatal(err)
	}
	gotExplicit, ok := heldSaleRowOnLocal(t, dp, "h-explicit")
	if !ok {
		t.Fatal("the caller-timestamped row must be mirrored locally")
	}
	if gotExplicit.UpdatedAt != "2026-09-15 10:00:00" {
		t.Fatalf("against a primary that reports no updated_at, the local mirror must keep the value the caller sent (which is what that primary stored), got %q", gotExplicit.UpdatedAt)
	}
}

// Delete write-through: resuming on a replica tells the primary to delete
// the row AND always drops the local row too.
func TestHeldSaleWriteThrough_ReplicaDeletesOnPrimaryAndLocally(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	repo := data.NewHeldSalesRepo(dp.Db)
	primary := newHeldSaleProxyPrimary(t, true)
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")

	if _, err := heldSaleWriteThrough(context.Background(), dp, repo, proxyTestHeldSale); err != nil {
		t.Fatal(err)
	}
	primaryDeleted, err := heldSaleDeleteWriteThrough(context.Background(), dp, repo, "h1")
	if err != nil || !primaryDeleted {
		t.Fatalf("delete: primaryDeleted=%v err=%v", primaryDeleted, err)
	}
	if primary.deleteCalls.Load() != 1 {
		t.Fatalf("the primary must be told to delete exactly once, got %d", primary.deleteCalls.Load())
	}
	if id, _ := primary.lastDelete.Load().(string); id != "h1" {
		t.Fatalf("primary must be told to delete %q, got %q", "h1", id)
	}
	if _, ok := heldSaleRowOnLocal(t, dp, "h1"); ok {
		t.Fatal("the local mirror row must be deleted too")
	}
}

// Direct unit coverage of the list fetch's ok contract, without a page in
// the way -- mirrors TestClaimTableOnPrimary_Contract.
func TestFetchHeldSalesFromPrimary_Contract(t *testing.T) {
	_, dp := newPOSTestDeps(t)
	ctx := context.Background()

	if _, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient); ok {
		t.Fatal("not a replica must report ok=false")
	}

	primary := newHeldSaleProxyPrimary(t, true, syncHeldSaleRow{ID: "p1", Label: "Primary's", Payload: `{}`, TotalMinor: 700, LineCount: 2, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 09:00:00"})
	setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
	rows, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient)
	if !ok || len(rows) != 1 || rows[0].ID != "p1" || rows[0].TotalMinor != 700 || rows[0].CreatedAt != "2026-09-15 09:00:00" {
		t.Fatalf("a reachable primary must report ok=true with its rows, got ok=%v %+v", ok, rows)
	}
	if auth, _ := primary.lastAuth.Load().(string); auth != "Bearer b-123" {
		t.Fatalf("primary must be called with the sync bearer, got %q", auth)
	}

	setReplicaSettings(t, dp.Settings, deadPrimaryURL(), "b-123")
	if _, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient); ok {
		t.Fatal("an unreachable primary must report ok=false")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer bad.Close()
	setReplicaSettings(t, dp.Settings, bad.URL, "b-123")
	if _, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient); ok {
		t.Fatal("a malformed primary body must report ok=false")
	}
}

// mergeHeldSales: the primary's copy wins per id (it is authoritative by
// construction under ADR-0093), a row that exists only locally (e.g. taken
// during an outage) is still shown, and the result keeps the strip's
// oldest-first order.
func TestMergeHeldSales_PrimaryWinsPerIDAndLocalOnlyRowsSurvive(t *testing.T) {
	local := []data.HeldSale{
		{ID: "shared", Label: "Stale local copy", Payload: `{"v":"local"}`, CreatedAt: "2026-09-15 09:00:00"},
		{ID: "local-only", Label: "Taken offline", Payload: `{}`, CreatedAt: "2026-09-15 09:30:00"},
	}
	primary := []data.HeldSale{
		{ID: "primary-only", Label: "Parked at till A", Payload: `{}`, CreatedAt: "2026-09-15 08:00:00"},
		{ID: "shared", Label: "Newer primary copy", Payload: `{"v":"primary"}`, CreatedAt: "2026-09-15 09:00:00"},
	}
	merged := mergeHeldSales(local, primary)
	if len(merged) != 3 {
		t.Fatalf("expected 3 rows, got %+v", merged)
	}
	byID := map[string]data.HeldSale{}
	for _, h := range merged {
		byID[h.ID] = h
	}
	if byID["shared"].Payload != `{"v":"primary"}` || byID["shared"].Label != "Newer primary copy" {
		t.Fatalf("the primary's copy must win on an id collision, got %+v", byID["shared"])
	}
	if _, ok := byID["local-only"]; !ok {
		t.Fatal("a row that exists only locally must still be shown")
	}
	if _, ok := byID["primary-only"]; !ok {
		t.Fatal("a row parked at another till must be shown")
	}
	if merged[0].ID != "primary-only" || merged[1].ID != "shared" || merged[2].ID != "local-only" {
		t.Fatalf("expected oldest-first order (created_at), got %v %v %v", merged[0].ID, merged[1].ID, merged[2].ID)
	}
}

// End to end through the REAL hold API on a replica against a REAL
// registerSyncHeldSales primary on its own migrated database (same harness
// shape as hold_cross_till_test.go): park on till B lands the order on the
// primary's held_sales -- the core ADR-0093 outcome, "an order parked at
// till A is visible from till B while both can reach the primary" -- and
// resuming it on B deletes it there again.
func TestHoldOnReplica_ParkLandsOnPrimaryAndResumeDeletesThere(t *testing.T) {
	primaryDB := openPagesTestDB(t)
	t.Cleanup(func() { primaryDB.Close() })
	primaryDp := &common.Deps{Db: primaryDB}
	primaryMux := http.NewServeMux()
	registerSyncHeldSales(primaryMux, primaryDp)
	primary := httptest.NewServer(primaryMux)
	t.Cleanup(primary.Close)
	seedSyncOrdersTill(t, primaryDp, "Replica", "b-123")
	primaryRepo := data.NewHeldSalesRepo(primaryDB)

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec := holdTestPost(mux, "/api/pos/hold", "label=Table+4"); rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	local := holdTestOnlyRow(t, dp)
	onPrimary, ok, err := primaryRepo.Get(context.Background(), local.ID)
	if err != nil || !ok {
		t.Fatalf("the parked order must land on the primary's held_sales: ok=%v err=%v", ok, err)
	}
	if onPrimary.Label != "Table 4" || onPrimary.LineCount != 1 || onPrimary.Payload != local.Payload || onPrimary.UpdatedAt != local.UpdatedAt {
		t.Fatalf("the primary's row must match the replica's mirror, primary=%+v local=%+v", onPrimary, local)
	}

	if rec := holdTestPost(mux, "/api/pos/resume", "id="+local.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := primaryRepo.Get(context.Background(), local.ID); ok {
		t.Fatal("resuming on the replica must delete the row on the primary too")
	}
	if rows, _ := data.NewHeldSalesRepo(dp.Db).List(context.Background()); len(rows) != 0 {
		t.Fatalf("resuming must delete the local row, got %+v", rows)
	}

	// Re-park: goes back under the SAME id (ut-docs#1918) on the primary
	// too, with the first-parked created_at preserved there.
	if rec := holdTestPost(mux, "/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-park: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	again, ok, err := primaryRepo.Get(context.Background(), local.ID)
	if err != nil || !ok {
		t.Fatalf("the re-parked order must land on the primary under its original id: ok=%v err=%v", ok, err)
	}
	if again.Label != "Table 4" {
		t.Fatalf("a re-park must keep the first-parked label on the primary, got %+v", again)
	}
	// created_at is compared with a tolerance, not byte-for-byte (ut-docs#2389):
	// heldSaleWriteThrough posts the first park to the PRIMARY first with
	// created_at == "" (no HeldOrigin yet), so the primary's own Upsert
	// COALESCEs it to ITS OWN datetime('now'). The primary's response
	// (syncHeldSaleUpsertResult) echoes back only `applied` + `updated_at`,
	// not created_at, so mirrorHeldSaleFromPrimary then writes the
	// REPLICA's local row (what local.CreatedAt captures below) via a
	// SECOND, independent datetime('now') read. These two clock reads
	// normally land in the same wall-clock second but can straddle a
	// boundary under real CI scheduling jitter between the two writes --
	// primary is written first, so it reads as the OLDER of the two on a
	// straddle, exactly the direction of the one observed failure (a 1s
	// gap, no code change anywhere near this path, next run clean). A
	// wider drift would mean the preserved value isn't the first-parked
	// time any more, so the tolerance stays tight; the exact-match
	// guarantee itself is still covered byte-for-byte elsewhere against a
	// seeded clock (hold_api_test.go's created_at assertions,
	// small_repos_test.go's Upsert insert/update semantics), so relaxing
	// this end-to-end assertion loses no real regression coverage.
	wantCreatedAt, err := time.Parse(heldSaleTimeLayout, local.CreatedAt)
	if err != nil {
		t.Fatalf("parse local.CreatedAt %q: %v", local.CreatedAt, err)
	}
	gotCreatedAt, err := time.Parse(heldSaleTimeLayout, again.CreatedAt)
	if err != nil {
		t.Fatalf("parse again.CreatedAt %q: %v", again.CreatedAt, err)
	}
	if diff := gotCreatedAt.Sub(wantCreatedAt).Abs(); diff > 2*time.Second {
		t.Fatalf("a re-park must keep the first-parked created_at (within 2s) on the primary, got %+v want created_at≈%q", again, local.CreatedAt)
	}
}

// heldSaleCrossTill is a REAL primary + REAL replica pair for the ADR-0093
// Amendment A end-to-end tests below: the primary mounts the same
// bearer-authed sync surface production does (tables, table claims, held
// sales) on its own migrated database; the replica is registerHoldAPI +
// registerOpenOrders on its own migrated database, pointed at it. Same
// harness shape as hold_cross_till_test.go, extended with the held-sale
// endpoints so a park / move / resume on the replica really lands on, and
// reads back from, the primary.
type heldSaleCrossTill struct {
	primaryURL  string
	primaryHeld *data.HeldSalesRepo
	primaryPOS  *data.POSRepo
	mux         *http.ServeMux
	dp          *common.Deps
}

func newHeldSaleCrossTill(t *testing.T) heldSaleCrossTill {
	t.Helper()
	primaryDB := openPagesTestDB(t)
	t.Cleanup(func() { primaryDB.Close() })
	primaryDp := &common.Deps{Db: primaryDB}
	primaryMux := http.NewServeMux()
	registerSyncTables(primaryMux, primaryDp)
	registerSyncTablesClaim(primaryMux, primaryDp)
	registerSyncHeldSales(primaryMux, primaryDp)
	primary := httptest.NewServer(primaryMux)
	t.Cleanup(primary.Close)
	seedSyncOrdersTill(t, primaryDp, "Replica", "b-123")

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	dp.Menu = []common.MenuItem{{Href: "/", Label: "nav.till"}}
	registerOpenOrders(mux, dp)
	return heldSaleCrossTill{
		primaryURL:  primary.URL,
		primaryHeld: data.NewHeldSalesRepo(primaryDB),
		primaryPOS:  data.NewPOSRepo(primaryDB),
		mux:         mux,
		dp:          dp,
	}
}

// parkOnReplica scans one ABC and parks it through the real hold handler,
// returning the replica's local row (which, with a reachable primary, is
// the mirror of what just landed there).
func (ct heldSaleCrossTill) parkOnReplica(t *testing.T, label string) data.HeldSale {
	t.Helper()
	if _, err := ct.dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec := holdTestPost(ct.mux, "/api/pos/hold", "label="+url.QueryEscape(label)); rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rows, err := data.NewHeldSalesRepo(ct.dp.Db).List(context.Background())
	if err != nil || len(rows) == 0 {
		t.Fatalf("list replica held_sales after park: rows=%d err=%v", len(rows), err)
	}
	for _, h := range rows {
		if h.Label == label {
			return h
		}
	}
	t.Fatalf("no local row labelled %q after park, got %+v", label, rows)
	return data.HeldSale{}
}

// TestResumeOnReplica_PrefersPrimaryPayloadOverStaleLocalMirror is
// Amendment A's F1, exactly the reviewer's repro: till B parks an order (1
// line, mirrored locally), till A picks it up on the primary and adds a
// second line, and till B taps resume. The Open orders page already showed
// till A's newer copy (Decision 3, primary wins) -- the resume must restore
// THAT payload, not the stale local mirror, or the added item silently
// vanishes and the sale undercharges.
func TestResumeOnReplica_PrefersPrimaryPayloadOverStaleLocalMirror(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	local := ct.parkOnReplica(t, "Table 4")
	if local.LineCount != 1 {
		t.Fatalf("precondition: the parked order is 1 line, got %+v", local)
	}

	// Till A: resume on the primary, add a second Apple, re-park -- on the
	// primary the row now carries 2 lines / 200 under a newer updated_at.
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(local.Payload), &snap); err != nil {
		t.Fatalf("decode parked payload: %v", err)
	}
	second := snap.Lines[0]
	second.LineKey = "line-added-at-till-a"
	snap.Lines = append(snap.Lines, second)
	newerPayload, _ := json.Marshal(snap)
	newer := local
	newer.Payload = string(newerPayload)
	newer.LineCount = 2
	newer.TotalMinor = 2 * local.TotalMinor
	newer.UpdatedAt = time.Now().UTC().Add(time.Minute).Format(heldSaleTimeLayout)
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, newer); err != nil || !applied {
		t.Fatalf("till A's newer write must apply on the primary: applied=%v err=%v", applied, err)
	}
	// Decision 3 already holds: till B's Open orders page lists the newer copy.
	rec := httptest.NewRecorder()
	ct.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-held-id="`+local.ID+`"`) {
		t.Fatalf("open orders on till B must list the order, got %d: %s", rec.Code, rec.Body.String())
	}

	// Till B resumes it. F1: the live basket must hold till A's 2 lines.
	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID); rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := ct.dp.Engine.Snapshot()
	if len(got.Lines) != 2 || got.Total.Minor() != 2*local.TotalMinor {
		t.Fatalf("F1: resuming on till B must restore the PRIMARY's newer payload (2 lines / %d), not the stale local mirror; got %d lines / %d", 2*local.TotalMinor, len(got.Lines), got.Total.Minor())
	}
	if _, ok, _ := ct.primaryHeld.Get(ctx, local.ID); ok {
		t.Fatal("the resumed row must be deleted on the primary")
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); ok {
		t.Fatal("the resumed row must be deleted locally")
	}
}

// TestResumeOnReplica_RowResolvedOnPrimaryIsNotFoundAndMirrorDropped is
// Amendment A's F2 on the resume path: till B parks (mirror synced), till A
// resumes-and-cashes-out on the primary (row gone there), till B taps
// resume on a stale page. The primary explicitly answers "not found", so
// the ghost mirror is dropped and the resume is refused exactly like an
// unknown id -- never re-rung after the money already moved.
func TestResumeOnReplica_RowResolvedOnPrimaryIsNotFoundAndMirrorDropped(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	local := ct.parkOnReplica(t, "Table 4")
	if err := ct.primaryHeld.Delete(ctx, local.ID); err != nil {
		t.Fatalf("till A cashes out: delete on primary: %v", err)
	}

	rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+local.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("F2: resuming an order the primary already resolved must be refused with the existing not-found toast, got: %s", rec.Body.String())
	}
	if ct.dp.Engine.HasItems() {
		t.Fatalf("F2: the ghost order must NOT be restored onto the live basket, got %+v", ct.dp.Engine.Snapshot().Lines)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, local.ID); ok {
		t.Fatal("F2: the local mirror of a row the primary resolved must be dropped")
	}
	if rec := holdTestPost(ct.mux, "/open-orders/resume", "id="+local.ID); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/open-orders?err=hold.error.not_found" {
		t.Fatalf("the Open orders page's own resume route must take the same not-found path, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestOpenOrdersOnReplica_DropsMirrorResolvedOnPrimaryKeepsOutageTakenRow is
// Amendment A's F2 on the list path, both halves: a local row the primary
// once confirmed (mirrored on a successful write-through) but no longer
// lists was resolved elsewhere -- dropped locally on the next successful
// list; a local row the primary NEVER learned about (parked while it was
// unreachable) is the legitimate outage-taken case and is kept and still
// listed, exactly as before.
func TestOpenOrdersOnReplica_DropsMirrorResolvedOnPrimaryKeepsOutageTakenRow(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	mirrored := ct.parkOnReplica(t, "Mirrored")

	// Primary drops off: this park is taken via the local-only fallback.
	setReplicaSettings(t, ct.dp.Settings, deadPrimaryURL(), "b-123")
	offline := ct.parkOnReplica(t, "Offline")
	if _, ok, _ := ct.primaryHeld.Get(ctx, offline.ID); ok {
		t.Fatal("precondition: the outage-taken row must not be on the primary")
	}
	setReplicaSettings(t, ct.dp.Settings, ct.primaryURL, "b-123")

	// Till A resumes-and-cashes-out the mirrored order on the primary.
	if err := ct.primaryHeld.Delete(ctx, mirrored.ID); err != nil {
		t.Fatalf("delete on primary: %v", err)
	}

	rec := httptest.NewRecorder()
	ct.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /open-orders = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-held-id="`+mirrored.ID+`"`) {
		t.Fatalf("F2: an order the primary resolved must not be listed as open on till B, got: %s", body)
	}
	if !strings.Contains(body, `data-held-id="`+offline.ID+`"`) {
		t.Fatalf("an outage-taken local-only row must still be listed, got: %s", body)
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, mirrored.ID); ok {
		t.Fatal("F2: the local mirror of a row the primary resolved must be dropped after a successful list")
	}
	if _, ok := heldSaleRowOnLocal(t, ct.dp, offline.ID); !ok {
		t.Fatal("the outage-taken local-only row must be kept")
	}
	// And it resumes fine: the primary answers "not found" for it too, but
	// it was never a mirror, so that answer means "never synced", not
	// "resolved" -- offline-first, unchanged.
	if rec := holdTestPost(ct.mux, "/api/pos/resume", "id="+offline.ID); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), template.HTMLEscapeString(httpx.T("en", "hold.error.not_found"))) {
		t.Fatalf("an outage-taken row must still resume locally, got %d: %s", rec.Code, rec.Body.String())
	}
	if !ct.dp.Engine.HasItems() {
		t.Fatal("the outage-taken row must be restored onto the live basket")
	}
}

// TestHeldTableMoveOnReplica_WritesThroughToPrimary is Amendment A's F3:
// POST /api/pos/held/table (move a parked order to another table) must go
// through the same write-through as park / add-to-order, so the move
// reaches the primary's row (every other till's Open orders page and the
// held strip then agree on the order's table) and bumps updated_at like
// every other mutation. Before the fix it was a local-only UPDATE.
func TestHeldTableMoveOnReplica_WritesThroughToPrimary(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	t2, err := ct.primaryPOS.CreateTable(ctx, "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	// The floor plan already synced this table's row to the replica
	// (admin bundle); mirror that here, as hold_cross_till_test.go does.
	if _, err := ct.dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		t2, "T2", "", 4, "rect", 200, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}
	local := ct.parkOnReplica(t, "Table 4")
	if local.TableID != "" {
		t.Fatalf("precondition: parked with no table, got %+v", local)
	}
	// Backdate the local row so a same-second move still observably bumps
	// updated_at (same trick TestHeldSalesRepo_WritesStampUpdatedAt uses).
	if _, err := ct.dp.Db.Exec(`UPDATE held_sales SET updated_at = '2020-01-01 00:00:00' WHERE id = ?`, local.ID); err != nil {
		t.Fatal(err)
	}

	if rec := holdTestPost(ct.mux, "/api/pos/held/table", "id="+local.ID+"&table_id="+t2); rec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	onPrimary, ok, err := ct.primaryHeld.Get(ctx, local.ID)
	if err != nil || !ok {
		t.Fatalf("the moved order must still be on the primary: ok=%v err=%v", ok, err)
	}
	if onPrimary.TableID != t2 {
		t.Fatalf("F3: a table move on a replica must reach the primary's row (table_id=%q), got %q", t2, onPrimary.TableID)
	}
	after, ok := heldSaleRowOnLocal(t, ct.dp, local.ID)
	if !ok || after.TableID != t2 {
		t.Fatalf("the local row must carry the new table too, got ok=%v %+v", ok, after)
	}
	if after.UpdatedAt <= "2020-01-01 00:00:00" || after.UpdatedAt != onPrimary.UpdatedAt {
		t.Fatalf("F3: a move must bump updated_at and mirror the primary's value; local=%q primary=%q", after.UpdatedAt, onPrimary.UpdatedAt)
	}
	if after.Payload != local.Payload || after.CreatedAt != local.CreatedAt || after.Label != local.Label {
		t.Fatalf("a move must touch only the table (payload / created_at / label unchanged), got %+v", after)
	}
}

// heldSaleForResume's own contract (ADR-0093 Amendment A), against the fake
// primary so each degradation is pinned individually: the primary is asked
// FIRST and its payload wins when it lists the id; "answered but not
// listed" drops a mirror and refuses, but leaves an outage-taken
// (never-synced) row resumable; and EVERY failure reaching the primary --
// not a replica, network error, non-200, malformed body -- is the local
// row, unchanged from before. Resume must never block or fail on a primary
// that happens to be off.
func TestHeldSaleForResume_PrimaryFirstThenLocalOnAnyFailure(t *testing.T) {
	ctx := context.Background()
	stale := data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{"v":"stale-local"}`, TotalMinor: 250, LineCount: 1, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 09:00:00"}
	fresh := syncHeldSaleRow{ID: "h1", Label: "Table 4", Payload: `{"v":"primary"}`, TotalMinor: 550, LineCount: 2, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 09:05:00"}

	t.Run("primary lists the id: its payload wins and is mirrored synced", func(t *testing.T) {
		_, dp := newPOSTestDeps(t)
		repo := data.NewHeldSalesRepo(dp.Db)
		primary := newHeldSaleProxyPrimary(t, true, fresh)
		setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
		// Seeded with its OWN older updated_at (UpsertIfNewer stores the
		// caller's value; Upsert would stamp now, which is later than the
		// fake primary's fixed 09:05 and would exercise the backwards-clock
		// fallback instead of the plain mirror).
		if applied, err := repo.UpsertIfNewer(ctx, stale); err != nil || !applied {
			t.Fatalf("seed stale local row: applied=%v err=%v", applied, err)
		}
		got, found := heldSaleForResume(ctx, dp, repo, "h1")
		if !found || got.Payload != `{"v":"primary"}` || got.TotalMinor != 550 {
			t.Fatalf("the primary's copy must be what gets resumed, got found=%v %+v", found, got)
		}
		if primary.listCalls.Load() != 1 {
			t.Fatalf("the primary must be asked exactly once, got %d", primary.listCalls.Load())
		}
		local, ok := heldSaleRowOnLocal(t, dp, "h1")
		if !ok || local.Payload != `{"v":"primary"}` || local.UpdatedAt != "2026-09-15 09:05:00" || !local.PrimarySynced {
			t.Fatalf("the primary's copy must be mirrored locally, primary_synced, got ok=%v %+v", ok, local)
		}
	})

	t.Run("primary answers without the id: a mirror is dropped and refused", func(t *testing.T) {
		_, dp := newPOSTestDeps(t)
		repo := data.NewHeldSalesRepo(dp.Db)
		primary := newHeldSaleProxyPrimary(t, true) // lists nothing
		setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
		mirror := stale
		mirror.PrimarySynced = true
		if err := repo.Upsert(ctx, mirror); err != nil {
			t.Fatal(err)
		}
		if got, found := heldSaleForResume(ctx, dp, repo, "h1"); found {
			t.Fatalf("a mirror the primary no longer lists was resolved elsewhere and must not resume, got %+v", got)
		}
		if _, ok := heldSaleRowOnLocal(t, dp, "h1"); ok {
			t.Fatal("the resolved mirror must be dropped locally")
		}
	})

	t.Run("primary answers without the id: an outage-taken row still resumes", func(t *testing.T) {
		_, dp := newPOSTestDeps(t)
		repo := data.NewHeldSalesRepo(dp.Db)
		primary := newHeldSaleProxyPrimary(t, true) // lists nothing
		setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
		if err := repo.Upsert(ctx, stale); err != nil { // PrimarySynced false: never confirmed
			t.Fatal(err)
		}
		got, found := heldSaleForResume(ctx, dp, repo, "h1")
		if !found || got.Payload != `{"v":"stale-local"}` {
			t.Fatalf("a row the primary never learned of must resume from local, got found=%v %+v", found, got)
		}
		if _, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok {
			t.Fatal("the outage-taken row must be left in place for the caller's own delete")
		}
	})

	t.Run("unknown everywhere is not found", func(t *testing.T) {
		_, dp := newPOSTestDeps(t)
		repo := data.NewHeldSalesRepo(dp.Db)
		primary := newHeldSaleProxyPrimary(t, true)
		setReplicaSettings(t, dp.Settings, primary.srv.URL, "b-123")
		if _, found := heldSaleForResume(ctx, dp, repo, "nowhere"); found {
			t.Fatal("an id on neither till must be not-found")
		}
	})

	// Every failure reaching the primary: local, unchanged -- even when the
	// local row IS a mirror (its primary_synced only matters when the
	// primary actually answered).
	failures := map[string]func(t *testing.T, dp *common.Deps){
		"not a replica": func(t *testing.T, dp *common.Deps) {},
		"primary unreachable": func(t *testing.T, dp *common.Deps) {
			setReplicaSettings(t, dp.Settings, deadPrimaryURL(), "b-123")
		},
		"primary answers non-200": func(t *testing.T, dp *common.Deps) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
			}))
			t.Cleanup(srv.Close)
			setReplicaSettings(t, dp.Settings, srv.URL, "b-123")
		},
		"primary answers malformed body": func(t *testing.T, dp *common.Deps) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `not json`)
			}))
			t.Cleanup(srv.Close)
			setReplicaSettings(t, dp.Settings, srv.URL, "b-123")
		},
	}
	for name, arrange := range failures {
		t.Run(name+": falls back to the local row", func(t *testing.T) {
			_, dp := newPOSTestDeps(t)
			repo := data.NewHeldSalesRepo(dp.Db)
			arrange(t, dp)
			mirror := stale
			mirror.PrimarySynced = true
			if err := repo.Upsert(ctx, mirror); err != nil {
				t.Fatal(err)
			}
			got, found := heldSaleForResume(ctx, dp, repo, "h1")
			if !found || got.Payload != `{"v":"stale-local"}` {
				t.Fatalf("fallback must be the local row, silently, got found=%v %+v", found, got)
			}
			if _, ok := heldSaleRowOnLocal(t, dp, "h1"); !ok {
				t.Fatal("a failure reaching the primary must never drop the local row")
			}
			if _, found := heldSaleForResume(ctx, dp, repo, "nowhere"); found {
				t.Fatal("an unknown id is still not-found on the fallback path")
			}
		})
	}
}

// TestHeldTableMoveOnReplica_DoesNotClobberPrimaryNewerPayload is Amendment
// A's F10 (round-2 review): till B parks an order (mirror synced), till A
// adds a line to it on the primary, then till B moves it to a new table.
// The move's lookup must be the primary-first heldSaleForResume, not
// repo.Get's stale local mirror -- writing the local (pre-till-A-add) row
// through would silently erase till A's added line shop-wide.
func TestHeldTableMoveOnReplica_DoesNotClobberPrimaryNewerPayload(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	t2, err := ct.primaryPOS.CreateTable(ctx, "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if _, err := ct.dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		t2, "T2", "", 4, "rect", 200, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}
	local := ct.parkOnReplica(t, "Table 4")
	if local.LineCount != 1 {
		t.Fatalf("precondition: the parked order is 1 line, got %+v", local)
	}

	// Till A: add a second line on the primary, same pattern as the F1 test.
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(local.Payload), &snap); err != nil {
		t.Fatalf("decode parked payload: %v", err)
	}
	second := snap.Lines[0]
	second.LineKey = "line-added-at-till-a"
	snap.Lines = append(snap.Lines, second)
	newerPayload, _ := json.Marshal(snap)
	newer := local
	newer.Payload = string(newerPayload)
	newer.LineCount = 2
	newer.TotalMinor = 2 * local.TotalMinor
	// Deliberately NOT a future offset (unlike the F1 test, which never
	// writes again afterward): the move below is itself a real write-through
	// moments later and must not be forced to lose the guard against an
	// artificially future till-A timestamp -- it only needs to be at least
	// as new, which real elapsed time already gives it (equal-applies is
	// exactly the idempotent-retry case UpsertIfNewer's own test pins).
	newer.UpdatedAt = time.Now().UTC().Format(heldSaleTimeLayout)
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, newer); err != nil || !applied {
		t.Fatalf("till A's newer write must apply on the primary: applied=%v err=%v", applied, err)
	}

	// Till B, still showing the stale 1-line strip, moves the order to T2.
	if rec := holdTestPost(ct.mux, "/api/pos/held/table", "id="+local.ID+"&table_id="+t2); rec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	onPrimary, ok, err := ct.primaryHeld.Get(ctx, local.ID)
	if err != nil || !ok {
		t.Fatalf("the order must still be on the primary: ok=%v err=%v", ok, err)
	}
	if onPrimary.LineCount != 2 || onPrimary.TotalMinor != 2*local.TotalMinor {
		t.Fatalf("F10: a table move must not overwrite the primary's newer payload; primary now has line_count=%d total_minor=%d (till A's added line is GONE), want 2 / %d",
			onPrimary.LineCount, onPrimary.TotalMinor, 2*local.TotalMinor)
	}
	if onPrimary.TableID != t2 {
		t.Fatalf("the move itself must still land: table_id=%q, want %q", onPrimary.TableID, t2)
	}
}

// TestHeldSaleWriteThrough_RefusalStillMarksPrimarySynced is Amendment A's
// F11 (round-2 review): a predicate refusal is itself proof the primary
// holds this id (it refused BECAUSE it has a newer row) -- so the local
// row written on that branch must be marked primary_synced, or it is
// indistinguishable from a genuinely outage-taken row and survives
// ReconcileWithPrimary (and heldSaleForResume's ghost-drop) forever, even
// after the primary later resolves it on another till.
func TestHeldSaleWriteThrough_RefusalStillMarksPrimarySynced(t *testing.T) {
	ct := newHeldSaleCrossTill(t)
	ctx := context.Background()
	local := ct.parkOnReplica(t, "Table 4")

	// Till A writes a NEWER row directly on the primary -- this till's next
	// write-through for the same id must be refused by the predicate guard.
	newer := local
	newer.UpdatedAt = time.Now().UTC().Add(time.Minute).Format(heldSaleTimeLayout)
	if applied, err := ct.primaryHeld.UpsertIfNewer(ctx, newer); err != nil || !applied {
		t.Fatalf("precondition: till A's newer write must apply: applied=%v err=%v", applied, err)
	}

	stale := local
	stale.UpdatedAt = time.Now().UTC().Format(heldSaleTimeLayout) // older than newer's +1min
	outcome, err := heldSaleWriteThrough(ctx, ct.dp, data.NewHeldSalesRepo(ct.dp.Db), stale)
	if err != nil || outcome != heldSaleSyncRefused {
		t.Fatalf("precondition: this write must be refused by the primary's guard: outcome=%v err=%v", outcome, err)
	}

	row, ok := heldSaleRowOnLocal(t, ct.dp, local.ID)
	if !ok {
		t.Fatal("the refused write must still land locally")
	}
	if !row.PrimarySynced {
		t.Fatalf("F11: a refusal proves the primary holds this id, so the local row must be marked primary_synced; got %+v", row)
	}

	// And that marking must actually do its job: once the primary resolves
	// the order elsewhere, this till's mirror must not survive as a ghost.
	if err := ct.primaryHeld.Delete(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	if _, found := heldSaleForResume(ctx, ct.dp, data.NewHeldSalesRepo(ct.dp.Db), local.ID); found {
		t.Fatal("F11: once the primary has resolved this id, the marked mirror must not still resume")
	}
}
