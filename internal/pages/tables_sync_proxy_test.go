package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// Cross-till table occupancy, replica side (ut-docs#1392): when this till is
// a replica (sync.primary_url set), the floor-plan tiles and the basket's
// table picker OR the primary's live occupancy into their own local view —
// see tablesWithStateForDisplay's own doc comment for why this is a MERGE,
// never a wholesale replace (round-2 review finding: table_claims/held_sales
// are local-only, so replacing the local view with the primary's answer made
// a replica's OWN occupied tables read as free). On ANY failure reaching the
// primary they fall back — silently — to the existing local-only path, so an
// offline station keeps working exactly as before (offline-first, ADR-0003).
// setReplicaSettings is shared with order_status_proxy_test.go.

func TestTablesState_OccupiedOnPrimaryShowsOccupiedLocally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	// Locally this table looks completely free — only the PRIMARY knows
	// about the claim/held order occupying it.
	id, err := repo.CreateTable(context.Background(), "T-Shared", "", 2, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var calls atomic.Int64
	var gotAuth atomic.Value
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/sync/tables" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":%q,"label":"T-Shared","area_zone":"","seat_count":2,"shape":"rect","pos_x":100,"pos_y":100,"enabled":true,"created_at":"","updated_at":"","occupied":true,"occupied_since":"2026-09-01T10:00:00Z"}],"error":null}`, id)
	}))
	defer primary.Close()
	setReplicaSettings(t, d.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("must call the primary exactly once, got %d calls", calls.Load())
	}
	if auth, _ := gotAuth.Load().(string); auth != "Bearer b-123" {
		t.Fatalf("primary must be called with the sync bearer, got %q", auth)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "T-Shared") {
		t.Fatalf("expected the table's (local) label to still render, got %q", body)
	}
	if !strings.Contains(body, "occupied") {
		t.Fatalf("primary-reported occupancy must be reflected, got %q", body)
	}
}

// The core round-2 regression: a table THIS till has occupied locally (a
// held order parked on it) must still render occupied even when the
// primary — which has never seen this till's local held_sales row — reports
// it free. Losing this would let a manager double-seat a table their own
// till already has a live parked order on, the moment the primary is
// reachable — worse than the display gap ut-docs#1392 exists to close.
func TestTablesState_OwnLocalOccupancyPreservedWhenPrimaryReportsFree(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	id, err := repo.CreateTable(context.Background(), "T-OwnHeld", "", 2, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`, id); err != nil {
		t.Fatalf("seed occupying held sale: %v", err)
	}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":%q,"label":"T-OwnHeld","area_zone":"","seat_count":2,"shape":"rect","pos_x":100,"pos_y":100,"enabled":true,"created_at":"","updated_at":"","occupied":false,"occupied_since":""}],"error":null}`, id)
	}))
	defer primary.Close()
	setReplicaSettings(t, d.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "occupied") {
		t.Fatalf("this till's own local occupancy must survive even though the primary reports the table free, got %q", body)
	}
}

func TestTablesState_ReplicaFallsBackToLocalWhenPrimaryUnreachable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.CreateTable(context.Background(), "T-Local2", "", 2, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := primary.URL
	primary.Close()
	setReplicaSettings(t, d.Settings, deadURL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "T-Local2") {
		t.Fatalf("unreachable primary must fall back to the local list, got %q", body)
	}
}

func TestTablesState_ReplicaFallsBackWhenPrimaryAnswersNon200(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.CreateTable(context.Background(), "T-Local3", "", 2, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, d.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "T-Local3") {
		t.Fatalf("a non-200 primary answer must fall back to the local list, got %q", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("the primary must actually have been attempted, got %d calls", calls.Load())
	}
}

func TestTablesState_ReplicaFallsBackWhenPrimaryAnswersMalformedBody(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.CreateTable(context.Background(), "T-Local4", "", 2, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer primary.Close()
	setReplicaSettings(t, d.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "T-Local4") {
		t.Fatalf("a malformed primary body must fall back to the local list, got %q", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("the primary must actually have been attempted, got %d calls", calls.Load())
	}
}

// A replica whose sync.bearer isn't set must not even attempt the primary —
// straight to the local path, no error.
func TestTablesState_ReplicaWithoutBearerStaysLocal(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	if _, err := repo.CreateTable(context.Background(), "T-Local5", "", 2, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer primary.Close()
	setReplicaSettings(t, d.Settings, primary.URL, "")

	req := httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "T-Local5") {
		t.Fatalf("must render the local list, got %q", body)
	}
	if calls.Load() != 0 {
		t.Fatalf("must not call the primary without a bearer, got %d calls", calls.Load())
	}
}

// The table picker (a cashier surface, not manager-gated) must also read
// through the same primary proxy — an occupied-on-primary table must not be
// offered as free just because this till's own local DB has never heard of
// the claim.
func TestTablePicker_ReplicaExcludesTableOccupiedOnPrimaryOnly(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()
	// Locally this table looks completely free.
	remoteFree, err := repo.CreateTable(ctx, "T-Remote", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":%q,"label":"T-Remote","area_zone":"","seat_count":4,"shape":"rect","pos_x":100,"pos_y":100,"enabled":true,"created_at":"","updated_at":"","occupied":true,"occupied_since":"2026-09-01T10:00:00Z"}],"error":null}`, remoteFree)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "T-Remote") {
		t.Fatalf("a table the PRIMARY reports occupied must not be offered, got %s", body)
	}
}

// The picker mirror of the round-2 regression: a table THIS till has
// occupied locally (a held order parked on it) must stay excluded from the
// picker even though the primary — which never saw the local held_sales
// row — reports it free. Losing this would offer a cashier a table their
// own till already has a live order on.
func TestTablePicker_OwnLocalHeldTableStaysExcludedWhenPrimaryReportsFree(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()
	id, err := repo.CreateTable(ctx, "T-OwnHeld", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`, id); err != nil {
		t.Fatalf("seed occupying held sale: %v", err)
	}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":%q,"label":"T-OwnHeld","area_zone":"","seat_count":4,"shape":"rect","pos_x":100,"pos_y":100,"enabled":true,"created_at":"","updated_at":"","occupied":false,"occupied_since":""}],"error":null}`, id)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "T-OwnHeld") {
		t.Fatalf("a table THIS till has locally held must stay excluded even though the primary reports it free, got %s", body)
	}
}
