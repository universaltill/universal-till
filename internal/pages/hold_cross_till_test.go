package pages

import (
	"context"
	"encoding/json"
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
	"github.com/universaltill/universal-till/internal/settings"
)

// Cross-till HELD-order table occupancy, end to end (ut-docs#1704): a table
// held (parked) on till A must not read free on till B. Unlike the other
// hold_api_test.go coverage (which only ever runs a single till's own
// local DB), this file stands up a REAL primary — registerSyncTables +
// registerSyncTablesClaim on their own real, migrated database, exactly the
// shape production runs — and a REAL replica till (registerHoldAPI, also a
// real migrated database) pointed at it, so the assertion is the actual
// AC this card asked for: "a table held on till A is not falsely offered
// as free on till B", proven through the real HTTP surface both ends of
// the write-through/proxy pair use, not a mocked stand-in for either side.

// newHoldCrossTillPrimary boots a real primary: its own migrated DB, the
// bearer-authed sync surface a replica's write-through/proxy calls hit.
func newHoldCrossTillPrimary(t *testing.T, tillBearer string) (*httptest.Server, *data.POSRepo) {
	t.Helper()
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{Db: dbase}
	mux := http.NewServeMux()
	registerSyncTables(mux, dp)
	registerSyncTablesClaim(mux, dp)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, err := data.NewTillsRepo(dbase).InsertTill(context.Background(), "Replica", hashBearer(tillBearer)); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	return srv, data.NewPOSRepo(dbase)
}

// newHoldCrossTillReplica boots a real replica till: registerHoldAPI on its
// own real, migrated database, pointed at primaryURL/bearer.
func newHoldCrossTillReplica(t *testing.T, primaryURL, bearer string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Db:       dbase,
		Engine:   engine,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Settings: settings.NewStore(dbase),
	}
	setReplicaSettings(t, dp.Settings, primaryURL, bearer)
	mux := http.NewServeMux()
	registerHoldAPI(mux, dp)
	return mux, dp
}

// getSyncTables fetches GET /api/sync/tables straight from the primary --
// exactly the call tablesWithStateForDisplay/the read-proxy makes -- and
// reports whether tableID comes back Occupied.
func getSyncTablesOccupied(t *testing.T, primaryURL, bearer, tableID string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, primaryURL+"/api/sync/tables", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/sync/tables: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/sync/tables: status %d", resp.StatusCode)
	}
	var out struct {
		Data []syncTableRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, row := range out.Data {
		if row.ID == tableID {
			return row.Occupied
		}
	}
	t.Fatalf("table %s not in primary's response", tableID)
	return false
}

// TestHoldOnReplica_OccupiesTableOnPrimary is the core ut-docs#1704 outcome:
// a dine-in order parked (held) on a REPLICA till must show occupied when
// read from the PRIMARY -- and therefore on every other till, which all
// read cross-till occupancy through exactly this same primary endpoint
// (tables_sync_proxy.go's tablesWithStateForDisplay).
func TestHoldOnReplica_OccupiesTableOnPrimary(t *testing.T) {
	primary, primaryRepo := newHoldCrossTillPrimary(t, "b-123")
	tableID, err := primaryRepo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable on primary: %v", err)
	}

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")

	// The floor plan itself already synced this table's row to the replica
	// (ut-docs#1546, the admin bundle) -- mirror that here rather than
	// re-deriving admin sync, which is not what this test is about.
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		tableID, "T1", "", 4, "rect", 100, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}

	// Pick the table for the live basket -- this is what actually
	// write-throughs the claim to the primary (claimTableWriteThrough,
	// called from pos_api.go's /api/pos/table in production; called
	// directly here since this harness registers only the hold API).
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	posRepo := data.NewPOSRepo(dp.Db)
	claimed, err := claimTableWriteThrough(context.Background(), dp, posRepo, tableID, false)
	if err != nil || !claimed {
		t.Fatalf("seed table pick: claimed=%v err=%v", claimed, err)
	}
	dp.Engine.SetTable(tableID, "T1")

	// Documents the pre-hold state: the LIVE claim already occupies the
	// table cross-till (ut-docs#1703/#1392), so the assertion after hold
	// below is clearly about HOLD specifically continuing that, not about
	// the live pick this test seeds through.
	if !getSyncTablesOccupied(t, primary.URL, "b-123", tableID) {
		t.Fatal("the live claim itself must already occupy the table on the primary before hold (ut-docs#1703)")
	}

	// Park it.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The core assertion: the PRIMARY -- and therefore any OTHER till
	// reading through it -- must see this table occupied by the held
	// order, not free.
	if !getSyncTablesOccupied(t, primary.URL, "b-123", tableID) {
		t.Fatal("a table held (parked) on a replica till must read occupied on the primary — a table held on till A must not be falsely offered as free on till B")
	}
}

// TestHoldOnReplica_MoveMigratesOccupancyOnPrimary: moving a held order to a
// different table (POST /api/pos/held/table) must free the OLD table and
// occupy the NEW one, as seen cross-till from the primary.
func TestHoldOnReplica_MoveMigratesOccupancyOnPrimary(t *testing.T) {
	primary, primaryRepo := newHoldCrossTillPrimary(t, "b-123")
	t1, err := primaryRepo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	t2, err := primaryRepo.CreateTable(context.Background(), "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	for _, tbl := range []struct{ id, label string }{{t1, "T1"}, {t2, "T2"}} {
		if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
			tbl.id, tbl.label, "", 4, "rect", 100, 100); err != nil {
			t.Fatalf("mirror table %s onto replica: %v", tbl.label, err)
		}
	}

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	posRepo := data.NewPOSRepo(dp.Db)
	if claimed, err := claimTableWriteThrough(context.Background(), dp, posRepo, t1, false); err != nil || !claimed {
		t.Fatalf("seed table pick: claimed=%v err=%v", claimed, err)
	}
	dp.Engine.SetTable(t1, "T1")

	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	if !getSyncTablesOccupied(t, primary.URL, "b-123", t1) {
		t.Fatal("T1 must be occupied on the primary right after hold")
	}

	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}
	moveReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id="+t2))
	moveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	moveRec := httptest.NewRecorder()
	mux.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", moveRec.Code, moveRec.Body.String())
	}

	if getSyncTablesOccupied(t, primary.URL, "b-123", t1) {
		t.Fatal("T1 must read free on the primary after the held order moved off it")
	}
	if !getSyncTablesOccupied(t, primary.URL, "b-123", t2) {
		t.Fatal("T2 must read occupied on the primary after the held order moved onto it")
	}
}

// hotPathTestPrimaryDelay is the artificial per-request delay
// newSlowHoldCrossTillPrimary's primary answers with, below. It must sit
// strictly BETWEEN crossTillHotPathProxyTimeout (so the fix actually times
// out against it) and heldSaleProxyClient's/tableClaimProxyClient's shared
// 800ms Timeout (so a genuinely single-hop, unmarked caller elsewhere in
// the same test setup -- the seed Hold, for instance -- still succeeds
// against the primary rather than also falling back); each test below
// asserts this invariant explicitly rather than trusting the comment.
// Strictly speaking this simulates a primary that is reachable but SLOW to
// answer every call, not a true blackhole (dropped packets, no answer at
// all) -- client-side, the two are indistinguishable (both time out the
// same way), so this is a faithful enough stand-in for ut-docs#2270's own
// failure mode, and it's what lets these tests assert the LOCAL fallback
// actually landed (a true blackhole primary would too, just with no
// "primary answered anyway" log noise from the delayed handler underneath).
const hotPathTestPrimaryDelay = 700 * time.Millisecond

// newSlowHoldCrossTillPrimary boots a real primary exactly like
// newHoldCrossTillPrimary above (plus registerSyncHeldSales, which that one
// doesn't need for its own table-claim-only assertions), but every request
// is delayed by `delay` before being served -- see hotPathTestPrimaryDelay's
// own comment for why, and the exact invariant `delay` must satisfy.
func newSlowHoldCrossTillPrimary(t *testing.T, tillBearer string, delay time.Duration) (*httptest.Server, *data.POSRepo) {
	t.Helper()
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{Db: dbase}
	mux := http.NewServeMux()
	registerSyncTables(mux, dp)
	registerSyncTablesClaim(mux, dp)
	registerSyncHeldSales(mux, dp)
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(slow)
	t.Cleanup(srv.Close)

	if _, err := data.NewTillsRepo(dbase).InsertTill(context.Background(), "Replica", hashBearer(tillBearer)); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	return srv, data.NewPOSRepo(dbase)
}

// TestResumeHeldSale_HotPathBoundedWhenPrimaryIsSlow (ut-docs#2270): a
// busy-basket resume stacks up to THREE independent write-through hops
// (order claim, auto-park, table re-claim -- four before ADR-0093
// Amendment B folded the lookup and delete into the claim) on one request -- see
// crossTillHotPathProxyTimeout's own comment for why the old-table release
// is deliberately not a fifth hop on this specific branch. Against a
// primary that answers every call but takes hotPathTestPrimaryDelay --
// reachable, just slow, not a clean refusal, which fails fast -- the
// PRE-fix behaviour (each hop bounded only by its 800ms client Timeout,
// comfortably longer than the delay) would let EVERY hop actually succeed
// against the primary, ~700ms each, ~2.1s total. The fix bounds each hop
// specifically on this path to crossTillHotPathProxyTimeout (300ms,
// shorter than the delay), so every hop instead falls back to the local
// read/write within ~300ms, ~0.9s total -- and, the point this test exists
// to prove past the timing itself, that local fallback must still SUCCEED
// rather than erroring on an already-expired context (the bug this card's
// fix specifically had to avoid, see crossTillHotPathNetCtx's own
// comment).
func TestResumeHeldSale_HotPathBoundedWhenPrimaryIsSlow(t *testing.T) {
	if hotPathTestPrimaryDelay <= crossTillHotPathProxyTimeout || hotPathTestPrimaryDelay >= heldSaleProxyClient.Timeout {
		t.Fatalf("test setup: hotPathTestPrimaryDelay (%s) must sit strictly between crossTillHotPathProxyTimeout (%s) and the proxy clients' shared Timeout (%s)",
			hotPathTestPrimaryDelay, crossTillHotPathProxyTimeout, heldSaleProxyClient.Timeout)
	}
	const resumeHotPathHops = 3
	primary, primaryRepo := newSlowHoldCrossTillPrimary(t, "b-123", hotPathTestPrimaryDelay)
	t1, err := primaryRepo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	t2, err := primaryRepo.CreateTable(context.Background(), "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	for _, tbl := range []struct{ id, label string }{{t1, "T1"}, {t2, "T2"}} {
		if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
			tbl.id, tbl.label, "", 4, "rect", 100, 100); err != nil {
			t.Fatalf("mirror table %s onto replica: %v", tbl.label, err)
		}
	}

	// Order H1: parked on T1 -- this is the order the test resumes.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan (H1): %v", err)
	}
	dp.Engine.SetTable(t1, "T1")
	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold H1: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	var h1 string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&h1); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}

	// The CURRENT busy basket that resuming H1 must auto-park. Given a
	// table (T2) for realism -- a cashier who had picked a table before
	// ringing anything up -- but this does NOT add a release hop: the
	// auto-park's own d.Engine.Reset() always clears the engine's table id
	// before resumeHeldSale ever reads prevTable, so the release call a few
	// lines into resumeHeldSale always sees "" and short-circuits with no
	// network call, regardless of what was set here (see
	// crossTillHotPathProxyTimeout's own comment).
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan (busy basket): %v", err)
	}
	dp.Engine.SetTable(t2, "T2")
	if !dp.Engine.HasItems() {
		t.Fatal("test setup: live basket must be busy before resume")
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/pos/resume", strings.NewReader("id="+h1))
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeRec := httptest.NewRecorder()

	start := time.Now()
	mux.ServeHTTP(resumeRec, resumeReq)
	elapsed := time.Since(start)

	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", resumeRec.Code, resumeRec.Body.String())
	}
	// The regression this test guards: unbounded, resumeHotPathHops stacked
	// hotPathTestPrimaryDelay-long hops would take ~2.1s (3 x 700ms);
	// bounded to crossTillHotPathProxyTimeout each, ~0.9s (3 x 300ms). The
	// 2x factor is headroom for scheduling jitter, not a magic number --
	// it still leaves this assertion (1.8s) below the ~2.1s regression
	// figure while comfortably above the ~0.9s expected one.
	if maxElapsed := time.Duration(resumeHotPathHops) * crossTillHotPathProxyTimeout * 2; elapsed >= maxElapsed {
		t.Fatalf("resume took %s (want < %s) -- the resume hot path must bound each write-through hop to ~%s, not the full client Timeout (ut-docs#2270)",
			elapsed, maxElapsed, crossTillHotPathProxyTimeout)
	}

	// The local fallback must actually have succeeded, not merely returned
	// 200 with the resume silently failed -- this is the correctness half
	// of the fix, not just the timing half: a poisoned (already-expired)
	// context reaching a local repo fallback would surface here as the
	// resume having done nothing (the read half) or H1's row surviving the
	// resume it's meant to consume (the write/delete half, checked below --
	// resumeHeldSale swallows its local cleanup delete's own error, so
	// only the row's actual absence proves this half of the fallback
	// worked, not just a 200 response).
	snap := dp.Engine.Snapshot()
	if snap.TableID != t1 {
		t.Fatalf("resume must restore H1's table T1, got table_id=%q", snap.TableID)
	}
	if origin := dp.Engine.HeldOrigin(); origin.ID != h1 {
		t.Fatalf("resume must restore held order %s, engine's HeldOrigin is %q", h1, origin.ID)
	}
	var remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM held_sales WHERE id = ?`, h1).Scan(&remaining); err != nil {
		t.Fatalf("query held_sales count for %s: %v", h1, err)
	}
	if remaining != 0 {
		t.Fatalf("resume must delete H1's held_sales row locally even though the primary delete hop fell back -- %d row(s) for %s still present", remaining, h1)
	}
}

// TestHeldTableMove_HotPathBoundedWhenPrimaryIsSlow (ut-docs#2270): the
// held/table move handler stacks up to FIVE independent write-through/read
// hops on one request -- lookup, new-table claim, move commit, old-table
// release, and (this card's own review caught it missing its bound
// initially) the table-state fetch behind the strip re-render every exit
// path of this handler ends with. Same shape and same reasoning as
// TestResumeHeldSale_HotPathBoundedWhenPrimaryIsSlow above: a primary that
// answers but takes hotPathTestPrimaryDelay would, pre-fix, let all five
// hops actually succeed (~3.5s); bounded to crossTillHotPathProxyTimeout
// each, they instead fall back locally (~1.5s) -- and the move must still
// actually land locally, not merely return 200.
func TestHeldTableMove_HotPathBoundedWhenPrimaryIsSlow(t *testing.T) {
	if hotPathTestPrimaryDelay <= crossTillHotPathProxyTimeout || hotPathTestPrimaryDelay >= tableClaimProxyClient.Timeout {
		t.Fatalf("test setup: hotPathTestPrimaryDelay (%s) must sit strictly between crossTillHotPathProxyTimeout (%s) and the proxy clients' shared Timeout (%s)",
			hotPathTestPrimaryDelay, crossTillHotPathProxyTimeout, tableClaimProxyClient.Timeout)
	}
	const moveHotPathHops = 5
	primary, primaryRepo := newSlowHoldCrossTillPrimary(t, "b-123", hotPathTestPrimaryDelay)
	t1, err := primaryRepo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	t2, err := primaryRepo.CreateTable(context.Background(), "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}

	mux, dp := newHoldCrossTillReplica(t, primary.URL, "b-123")
	for _, tbl := range []struct{ id, label string }{{t1, "T1"}, {t2, "T2"}} {
		if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
			tbl.id, tbl.label, "", 4, "rect", 100, 100); err != nil {
			t.Fatalf("mirror table %s onto replica: %v", tbl.label, err)
		}
	}

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	dp.Engine.SetTable(t1, "T1")
	holdRec := httptest.NewRecorder()
	mux.ServeHTTP(holdRec, httptest.NewRequest(http.MethodPost, "/api/pos/hold", nil))
	if holdRec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", holdRec.Code, holdRec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales`).Scan(&id); err != nil {
		t.Fatalf("query held_sales id: %v", err)
	}

	moveReq := httptest.NewRequest(http.MethodPost, "/api/pos/held/table", strings.NewReader("id="+id+"&table_id="+t2))
	moveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	moveRec := httptest.NewRecorder()

	start := time.Now()
	mux.ServeHTTP(moveRec, moveReq)
	elapsed := time.Since(start)

	if moveRec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", moveRec.Code, moveRec.Body.String())
	}
	// Same headroom reasoning as the resume test above: ~3.5s unbounded
	// (5 x 700ms) vs. ~1.5s bounded (5 x 300ms), 2x factor for jitter.
	if maxElapsed := time.Duration(moveHotPathHops) * crossTillHotPathProxyTimeout * 2; elapsed >= maxElapsed {
		t.Fatalf("move took %s (want < %s) -- the held/table move hot path must bound each write-through hop to ~%s, not the full client Timeout (ut-docs#2270)",
			elapsed, maxElapsed, crossTillHotPathProxyTimeout)
	}

	var movedTable string
	if err := dp.Db.QueryRow(`SELECT table_id FROM held_sales WHERE id = ?`, id).Scan(&movedTable); err != nil {
		t.Fatalf("query held_sales table_id: %v", err)
	}
	if movedTable != t2 {
		t.Fatalf("local fallback must still land the move -- held_sales.table_id = %q, want %q", movedTable, t2)
	}
}
