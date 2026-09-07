package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
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
	dbase, err := db.Open(filepath.Join(t.TempDir(), "primary.db"))
	if err != nil {
		t.Fatalf("open primary db: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTables(mux, dp)
	registerSyncTablesClaim(mux, dp)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, err := data.NewTillsRepo(dbase.DB).InsertTill(context.Background(), "Replica", hashBearer(tillBearer)); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	return srv, data.NewPOSRepo(dbase.DB)
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

	dbase, err := db.Open(filepath.Join(t.TempDir(), "replica.db"))
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Db:       dbase.DB,
		Engine:   engine,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Settings: settings.NewStore(dbase.DB),
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
	claimed, err := claimTableWriteThrough(context.Background(), dp, posRepo, tableID)
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
	if claimed, err := claimTableWriteThrough(context.Background(), dp, posRepo, t1); err != nil || !claimed {
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
