package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#815: GET /api/tables/{id}/qr is the staff-facing surface that
// lets a manager view/print the code a guest scans at that table to reach
// /self-order?table=<id> on their own phone — same data:image/png URI
// pattern as orderTrackingQRView (order_tracking.go) / the enrol-token QR
// (sync_api.go), gated the same manager-only way as every other
// /api/tables/* route on this page.
func TestTablesQR_RendersImageAndURLForEnabledTable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	posRepo := data.NewPOSRepo(d.Db)
	tableID, err := posRepo.CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tables/"+tableID+"/qr", nil)
	req.Host = "192.168.1.50:8080"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tables/%s/qr: want 200, got %d: %s", tableID, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("expected an embedded QR image: %s", body)
	}
	wantURL := "http://192.168.1.50:8080/self-order?table=" + tableID
	if !strings.Contains(body, wantURL) {
		t.Fatalf("expected the self-order URL %q in the response: %s", wantURL, body)
	}
}

// A non-manager must not reach this staff surface, same gate as every
// other /api/tables/* route on this page.
func TestTablesQR_RequiresManager(t *testing.T) {
	mux, d := newTablesTestMux(t)
	posRepo := data.NewPOSRepo(d.Db)
	tableID, err := posRepo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/tables/"+tableID+"/qr", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET qr = %d, want 403", rec.Code)
	}
}

// An unknown table id must render the error partial, never a 500.
func TestTablesQR_UnknownTableRendersError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTablesTestMux(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tables/does-not-exist/qr", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET qr for unknown table: want 200 (htmx-swappable error partial), got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data:image/png;base64,") {
		t.Fatalf("unknown table must never render a QR image: %s", rec.Body.String())
	}
}
