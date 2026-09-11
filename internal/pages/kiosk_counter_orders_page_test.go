package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func setupKioskCounterOrdersDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "counter-orders-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbase.Close() })
	dp := &common.Deps{
		Db:       dbase.DB,
		Settings: settings.NewStore(dbase.DB),
		Menu:     []common.MenuItem{},
	}
	return dp, dbase
}

// The staff board must list an open counter order and remove it from the
// list the instant it's marked collected — HTTP-level, list + collect flow.
func TestKioskCounterOrdersPage_ListAndMarkCollected(t *testing.T) {
	dp, dbase := setupKioskCounterOrdersDeps(t)
	repo := data.NewKioskCounterOrdersRepo(dbase.DB)
	created, err := repo.Create(context.Background(), data.KioskCounterOrder{
		OrderType: "takeaway",
		Lines: []data.KioskCounterOrderLine{
			{Name: "Flat White", Qty: "2"},
			{Name: "Croissant", Qty: "1"},
		},
	})
	if err != nil {
		t.Fatalf("seed counter order: %v", err)
	}

	mux := http.NewServeMux()
	registerKioskCounterOrdersPage(mux, dp)

	// Page shell renders.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kiosk-counter-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /kiosk-counter-orders: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Fragment lists the open order with its ref and items.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/ui/kiosk-counter-orders", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /ui/kiosk-counter-orders: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()
	if !strings.Contains(body, created.DisplayNo) {
		t.Fatalf("list fragment missing display_no %q: %s", created.DisplayNo, body)
	}
	if !strings.Contains(body, "Flat White") || !strings.Contains(body, "Croissant") {
		t.Fatalf("list fragment missing ordered items: %s", body)
	}

	// Mark collected removes the row via an OOB delete and updates the DB.
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/api/kiosk-counter-orders/"+created.ID+"/collect", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("POST collect: want 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), `hx-swap-oob="delete"`) {
		t.Fatalf("collect response missing the OOB row-delete marker: %s", rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), "counter-order-row-"+created.ID) {
		t.Fatalf("collect response OOB marker doesn't target this order's row: %s", rec3.Body.String())
	}

	var status string
	if err := dbase.DB.QueryRow(`SELECT status FROM kiosk_counter_orders WHERE id = ?`, created.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != data.KioskCounterOrderStatusCollected {
		t.Fatalf("status = %q, want %q", status, data.KioskCounterOrderStatusCollected)
	}

	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/ui/kiosk-counter-orders", nil))
	if strings.Contains(rec4.Body.String(), created.DisplayNo) {
		t.Fatalf("collected order must no longer appear in the open list: %s", rec4.Body.String())
	}
}

// An empty board shows the plain "no open counter orders" state, never a
// bare/broken table.
func TestKioskCounterOrdersPage_EmptyState(t *testing.T) {
	dp, _ := setupKioskCounterOrdersDeps(t)
	mux := http.NewServeMux()
	registerKioskCounterOrdersPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/kiosk-counter-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/kiosk-counter-orders: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<table") {
		t.Fatalf("empty board must not render a table: %s", rec.Body.String())
	}
}

// Collecting an unknown id must not error (same silent-no-op convention as
// a re-tapped button) and must still return the OOB delete marker so a
// stale client-side row disappears either way.
func TestKioskCounterOrdersPage_CollectUnknownIDIsNoop(t *testing.T) {
	dp, _ := setupKioskCounterOrdersDeps(t)
	mux := http.NewServeMux()
	registerKioskCounterOrdersPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/kiosk-counter-orders/does-not-exist/collect", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST collect (unknown id): want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
