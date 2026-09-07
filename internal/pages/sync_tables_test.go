package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table occupancy (ut-docs#1392): the primary-side READ-ONLY sync
// surface. GET /api/sync/tables serves the same rows the local floor-plan
// tiles and table picker render — bearer-authed via syncTill, JSON envelope,
// same shape as sync_orders_test.go.

func newSyncTablesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_tables.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTables(mux, dp)
	return mux, dp, dbase
}

func TestSyncTablesGet_RequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncTablesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	req := httptest.NewRequest(http.MethodGet, "/api/sync/tables", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sync/tables", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
}

func TestSyncTablesGet_ReturnsOccupancy(t *testing.T) {
	mux, dp, dbase := newSyncTablesTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	repo := data.NewPOSRepo(dbase.DB)
	ctx := context.Background()
	free, err := repo.CreateTable(ctx, "T1", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	busy, err := repo.CreateTable(ctx, "T2", "", 2, "round", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, busy); err != nil || !claimed {
		t.Fatalf("ClaimTable T2: claimed=%v err=%v", claimed, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sync/tables", nil)
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data  []syncTableRow `json:"data"`
		Error any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("error = %v, want null", resp.Error)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("want 2 tables, got %d (%q)", len(resp.Data), rec.Body.String())
	}
	byID := map[string]syncTableRow{}
	for _, row := range resp.Data {
		byID[row.ID] = row
	}
	if row, ok := byID[free]; !ok || row.Occupied {
		t.Fatalf("free table must list as unoccupied, got %+v (ok=%v)", row, ok)
	}
	if row, ok := byID[busy]; !ok || !row.Occupied || row.OccupiedSince == "" {
		t.Fatalf("claimed table must list as occupied with a timestamp, got %+v (ok=%v)", row, ok)
	}
	if byID[free].Label != "T1" || byID[free].AreaZone != "Terrace" {
		t.Fatalf("expected T1/Terrace to round-trip, got %+v", byID[free])
	}
}
