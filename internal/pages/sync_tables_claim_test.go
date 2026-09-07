package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table-claim write-through, primary side (ut-docs#1703): POST
// /api/sync/tables/claim and /release are the bearer-authed endpoints a
// replica's claimTableWriteThrough/releaseTableClaimWriteThrough
// (tables_claim_proxy.go) proxy to, so two tills can never both claim one
// table. Same shape as sync_tables_test.go / sync_orders_test.go: syncTill
// auth, JSON envelope, snake_case.

func newSyncTablesClaimTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_tables_claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTablesClaim(mux, dp)
	return mux, dp, dbase
}

// postSyncReleaseAll posts to /api/sync/tables/release-all with zero or more
// repeated keep_table_id form values — the caller's own currently-held-order
// table ids that must survive the release (ut-docs#1712).
func postSyncReleaseAll(mux *http.ServeMux, bearer string, keepTableIDs ...string) *httptest.ResponseRecorder {
	form := url.Values{}
	for _, id := range keepTableIDs {
		form.Add("keep_table_id", id)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tables/release-all", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postSyncTableClaim(mux *http.ServeMux, action, tableID, bearer string) *httptest.ResponseRecorder {
	form := url.Values{"table_id": {tableID}}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tables/"+action, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncTableClaimResp struct {
	Data struct {
		Claimed  bool `json:"claimed"`
		Released bool `json:"released"`
	} `json:"data"`
	Error any `json:"error"`
}

func decodeSyncTableClaim(t *testing.T, rec *httptest.ResponseRecorder) syncTableClaimResp {
	t.Helper()
	var resp syncTableClaimResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	return resp
}

func TestSyncTablesClaim_RequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	for _, action := range []string{"claim", "release"} {
		if rec := postSyncTableClaim(mux, action, "some-table", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s no bearer: status = %d, want 401", action, rec.Code)
		}
		if rec := postSyncTableClaim(mux, action, "some-table", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s bad bearer: status = %d, want 401", action, rec.Code)
		}
	}
}

func TestSyncTablesClaim_EmptyTableIDIs400(t *testing.T) {
	mux, dp, _ := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	for _, action := range []string{"claim", "release"} {
		rec := postSyncTableClaim(mux, action, "  ", "bearer-t2")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s empty table_id: status = %d, want 400 (body %q)", action, rec.Code, rec.Body.String())
		}
	}
}

// Happy path: till 2 claims a free table (recorded against ITS till id on
// the primary); till 3 is then refused the same table; till 2's release
// frees it and till 3 can take it.
func TestSyncTablesClaim_ClaimThenRefuseOtherTillThenRelease(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")

	repo := data.NewPOSRepo(dbase.DB)
	id, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	rec := postSyncTableClaim(mux, "claim", id, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); !resp.Data.Claimed || resp.Error != nil {
		t.Fatalf("claim: want claimed=true error=null, got %+v", resp)
	}
	var owner string
	if err := dbase.DB.QueryRow(`SELECT c.till_id FROM table_claims c WHERE c.table_id = ?`, id).Scan(&owner); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	var till2 string
	if err := dbase.DB.QueryRow(`SELECT id FROM tills WHERE name = 'Till 2'`).Scan(&till2); err != nil {
		t.Fatal(err)
	}
	if owner != till2 {
		t.Fatalf("claim must be recorded against the calling till (%q), got %q", till2, owner)
	}
	// The primary's own occupancy view sees it too — this is what its floor
	// plan / picker and the read-only GET /api/sync/tables serve.
	if free, err := repo.IsTableFree(context.Background(), id, ""); err != nil || free {
		t.Fatalf("a till's claim must occupy the table on the primary, free=%v err=%v", free, err)
	}

	rec = postSyncTableClaim(mux, "claim", id, "bearer-t3")
	if rec.Code != http.StatusOK {
		t.Fatalf("other till's claim: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); resp.Data.Claimed {
		t.Fatalf("a second till must be refused a table a fresh till holds, got %+v", resp)
	}

	rec = postSyncTableClaim(mux, "release", id, "bearer-t3")
	if rec.Code != http.StatusOK {
		t.Fatalf("other till's release: status = %d, want 200", rec.Code)
	}
	if free, err := repo.IsTableFree(context.Background(), id, ""); err != nil || free {
		t.Fatalf("another till's release must not free the owner's claim, free=%v err=%v", free, err)
	}

	rec = postSyncTableClaim(mux, "release", id, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("release: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); !resp.Data.Released || resp.Error != nil {
		t.Fatalf("release: want released=true error=null, got %+v", resp)
	}
	if free, err := repo.IsTableFree(context.Background(), id, ""); err != nil || !free {
		t.Fatalf("owner's release must free the table, free=%v err=%v", free, err)
	}
	// Release is idempotent — a second release is still 200/released.
	if rec := postSyncTableClaim(mux, "release", id, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("repeat release: status = %d, want 200", rec.Code)
	}

	rec = postSyncTableClaim(mux, "claim", id, "bearer-t3")
	if resp := decodeSyncTableClaim(t, rec); rec.Code != http.StatusOK || !resp.Data.Claimed {
		t.Fatalf("after release the other till must be able to claim: status=%d resp=%+v", rec.Code, resp)
	}
}

// Stale takeover through the full HTTP handler: till 2 claimed, then went
// silent (its last_seen_at is older than tillClaimTTL — the same 2-minute
// bound sync_admin.go's status chip uses to call a till offline). Till 3's
// claim must expire the orphaned claim and succeed.
func TestSyncTablesClaim_StaleTillClaimIsTakenOver(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")

	repo := data.NewPOSRepo(dbase.DB)
	id, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if rec := postSyncTableClaim(mux, "claim", id, "bearer-t2"); rec.Code != http.StatusOK || !decodeSyncTableClaim(t, rec).Data.Claimed {
		t.Fatalf("till 2 claim: status=%d body=%q", rec.Code, rec.Body.String())
	}
	// Still fresh: refused.
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", id, "bearer-t3")); resp.Data.Claimed {
		t.Fatal("till 3 must be refused while till 2 is fresh")
	}
	// Till 2 goes silent: its last_seen_at ages past the TTL.
	stale := time.Now().UTC().Add(-(tillClaimTTL + time.Minute)).Format(time.RFC3339)
	if _, err := dbase.DB.Exec(`UPDATE tills SET last_seen_at = ? WHERE name = 'Till 2'`, stale); err != nil {
		t.Fatal(err)
	}
	rec := postSyncTableClaim(mux, "claim", id, "bearer-t3")
	if rec.Code != http.StatusOK {
		t.Fatalf("takeover: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); !resp.Data.Claimed {
		t.Fatalf("a stale till's claim must be taken over, got %+v", resp)
	}
	var owner, till3 string
	if err := dbase.DB.QueryRow(`SELECT till_id FROM table_claims WHERE table_id = ?`, id).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := dbase.DB.QueryRow(`SELECT id FROM tills WHERE name = 'Till 3'`).Scan(&till3); err != nil {
		t.Fatal(err)
	}
	if owner != till3 {
		t.Fatalf("after takeover the claim must belong to till 3 (%q), got %q", till3, owner)
	}
}

// TestSyncTablesClaim_ReleaseAllRequiresBearer mirrors
// TestSyncTablesClaim_RequiresBearer for the release-all endpoint, which
// takes no table_id form field at all.
func TestSyncTablesClaim_ReleaseAllRequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	if rec := postSyncReleaseAll(mux, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}
	if rec := postSyncReleaseAll(mux, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
}

// TestSyncTablesClaim_ReleaseAllDropsEveryTableTheCallingTillHolds
// (ut-docs#1712): a replica calls this once at boot, before re-claiming its
// held orders, to clear whatever a crashed live basket left behind on a
// table it may never revisit again — the residual left by ut-docs#1703's
// own review (finding 7). Unlike /release (one table_id), this takes none:
// every table the calling till holds must go in one call, and it must never
// touch another till's claims or the primary's own local (”) claim.
func TestSyncTablesClaim_ReleaseAllDropsEveryTableTheCallingTillHolds(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")

	repo := data.NewPOSRepo(dbase.DB)
	t1, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	t2, err := repo.CreateTable(context.Background(), "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	t3, err := repo.CreateTable(context.Background(), "T3", "", 4, "rect", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable T3: %v", err)
	}

	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", t1, "bearer-t2")); !resp.Data.Claimed {
		t.Fatalf("till 2 claim T1: %+v", resp)
	}
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", t2, "bearer-t2")); !resp.Data.Claimed {
		t.Fatalf("till 2 claim T2: %+v", resp)
	}
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", t3, "bearer-t3")); !resp.Data.Claimed {
		t.Fatalf("till 3 claim T3: %+v", resp)
	}

	// Till 2 "reboots": still enrolled and about to become "online" again
	// (this very call refreshes its last_seen_at), but it never revisits T1
	// or T2 to re-claim them — the exact gap ClaimTableForTill's per-table
	// staleness check cannot close on its own.
	rec := postSyncReleaseAll(mux, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("release-all: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); !resp.Data.Released || resp.Error != nil {
		t.Fatalf("release-all: want released=true error=null, got %+v", resp)
	}

	if free, err := repo.IsTableFree(context.Background(), t1, ""); err != nil || !free {
		t.Fatalf("till 2's claim on T1 must be gone after its release-all, free=%v err=%v", free, err)
	}
	if free, err := repo.IsTableFree(context.Background(), t2, ""); err != nil || !free {
		t.Fatalf("till 2's claim on T2 must be gone after its release-all, free=%v err=%v", free, err)
	}
	if free, err := repo.IsTableFree(context.Background(), t3, ""); err != nil || free {
		t.Fatalf("till 3's claim on T3 must survive till 2's release-all, free=%v err=%v", free, err)
	}

	// Idempotent — nothing left to release, still 200/released.
	if rec := postSyncReleaseAll(mux, "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("repeat release-all: status = %d, want 200", rec.Code)
	}
}

// TestSyncTablesClaim_ReleaseAllKeepsHeldOrdersTable (ut-docs#1712,
// independent review 2026-09-07, blocker 1): a held order's table_claims row
// survives a restart BY DESIGN (ut-docs#1704) and must never be dropped by
// this endpoint just because it can't otherwise tell a legitimate held-order
// claim apart from a genuinely orphaned live-basket one. keep_table_id is
// the caller's declaration of which of its own tables are NOT orphans.
func TestSyncTablesClaim_ReleaseAllKeepsHeldOrdersTable(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	repo := data.NewPOSRepo(dbase.DB)
	held, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	orphan, err := repo.CreateTable(context.Background(), "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", held, "bearer-t2")); !resp.Data.Claimed {
		t.Fatalf("claim held table: %+v", resp)
	}
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", orphan, "bearer-t2")); !resp.Data.Claimed {
		t.Fatalf("claim orphan table: %+v", resp)
	}

	rec := postSyncReleaseAll(mux, "bearer-t2", held)
	if rec.Code != http.StatusOK {
		t.Fatalf("release-all: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	if free, err := repo.IsTableFree(context.Background(), held, ""); err != nil || free {
		t.Fatalf("the held order's table must survive release-all, free=%v err=%v", free, err)
	}
	if free, err := repo.IsTableFree(context.Background(), orphan, ""); err != nil || !free {
		t.Fatalf("a table NOT in the keep list must still be released, free=%v err=%v", free, err)
	}
}

// The primary's OWN live basket claims locally with till_id=” (ClaimTable,
// unchanged). A replica's claim must be refused against it — and must never
// expire it, however long ago the primary was "seen" (it has no tills row).
func TestSyncTablesClaim_PrimaryOwnLocalClaimBlocksReplicaAndIsNeverExpired(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	repo := data.NewPOSRepo(dbase.DB)
	id, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if claimed, err := repo.ClaimTable(context.Background(), id); err != nil || !claimed {
		t.Fatalf("primary's own ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", id, "bearer-t2")); resp.Data.Claimed {
		t.Fatal("a replica must be refused a table the primary's own basket holds")
	}
	var owner string
	if err := dbase.DB.QueryRow(`SELECT till_id FROM table_claims WHERE table_id = ?`, id).Scan(&owner); err != nil || owner != "" {
		t.Fatalf("the primary's own '' claim must survive, got %q (err %v)", owner, err)
	}
}

// A till re-claiming a table it already owns must SUCCEED — the blocking bug
// found in independent review, 2026-09-07, pinned here through the full HTTP
// surface (which also proves the owner is derived from the BEARER, never from
// anything the caller can supply).
//
// The TTL only expires a claim whose owning till has gone quiet. A till that
// crashes mid-basket and reboots is loud again within seconds — every sync
// call touches tills.last_seen_at — and its boot sweep clears only its own
// LOCAL rows, so its orphan on the primary was never expirable and never
// releasable. Re-picking that table answered "occupied" forever, to the one
// till that could have cleared it. Same shape whenever a release
// write-through fails: the local row goes regardless (offline-first, by
// design), the primary's does not.
func TestSyncTablesClaim_OwnOrphanedClaimIsRetakenAfterRestart(t *testing.T) {
	mux, dp, dbase := newSyncTablesClaimTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedSyncOrdersTill(t, dp, "Till 3", "bearer-t3")

	repo := data.NewPOSRepo(dbase.DB)
	id, err := repo.CreateTable(context.Background(), "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", id, "bearer-t2")); !resp.Data.Claimed {
		t.Fatalf("initial claim: %+v", resp)
	}
	// Till 2 crashes and reboots: still enrolled, still online (this very
	// call refreshes its last_seen_at), its local claim gone with the dead
	// process, and the operator re-picks the same table.
	rec := postSyncTableClaim(mux, "claim", id, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("re-claim: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if resp := decodeSyncTableClaim(t, rec); !resp.Data.Claimed || resp.Error != nil {
		t.Fatalf("a till must be able to re-take its OWN orphaned claim, got %+v", resp)
	}

	var owner, till2 string
	if err := dbase.DB.QueryRow(`SELECT till_id FROM table_claims WHERE table_id = ?`, id).Scan(&owner); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if err := dbase.DB.QueryRow(`SELECT id FROM tills WHERE name = 'Till 2'`).Scan(&till2); err != nil {
		t.Fatal(err)
	}
	if owner != till2 {
		t.Fatalf("the re-take must keep the row owned by the calling till (%q), got %q", till2, owner)
	}

	// And it must not have widened into "anyone may take it": till 3, with
	// till 2 live and holding the table, is still refused.
	if resp := decodeSyncTableClaim(t, postSyncTableClaim(mux, "claim", id, "bearer-t3")); resp.Data.Claimed {
		t.Fatalf("a DIFFERENT live till must still be refused, got %+v", resp)
	}
}
