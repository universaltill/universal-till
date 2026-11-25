package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4338 (ADR-0159 §1): GET /api/tables/{id}/qr is the staff-facing
// surface that shows the code a guest scans at that table. It opens the
// cloud's /t/<token> page, not the till (the LAN /self-order?table= link is
// gone). Gated by the order_tracking capability (Shop and above), a
// registered till and a usable cloud address; manager-only like every other
// /api/tables/* route.

const tableQRTestEndpoint = "https://cloud.example.test/api"

// newTableQRHarness is a tables mux on a Shop plan with a registered till
// and a cloud endpoint. tableQRRegistered is a package seam over the
// process-global enrol status, restored on cleanup (no t.Parallel here).
func newTableQRHarness(t *testing.T) (*http.ServeMux, *common.Deps, *data.POSRepo) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, d := newTablesTestMux(t)
	seedTrackingPlan(t, d, entitlement.PlanShop)
	prev := tableQRRegistered
	tableQRRegistered = func() bool { return true }
	t.Cleanup(func() { tableQRRegistered = prev })
	if d.Cfg == nil {
		d.Cfg = &config.Config{}
	}
	d.Cfg.Marketplace.EndpointURL = tableQRTestEndpoint
	return mux, d, data.NewPOSRepo(d.Db)
}

func getTableQR(mux *http.ServeMux, id, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/tables/"+id+"/qr", nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func tableToken(t *testing.T, repo *data.POSRepo, id string) string {
	t.Helper()
	tok, err := repo.EnsureTableGuestToken(t.Context(), id)
	if err != nil || tok == "" {
		t.Fatalf("EnsureTableGuestToken(%s) = %q, %v", id, tok, err)
	}
	return tok
}

func TestTablesQR_RendersCloudGuestURL(t *testing.T) {
	mux, _, repo := newTableQRHarness(t)
	id, err := repo.CreateTable(t.Context(), "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	rec := getTableQR(mux, id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := "https://cloud.example.test/t/" + tableToken(t, repo, id)
	if !strings.Contains(body, want) {
		t.Fatalf("want cloud URL %q in body: %s", want, body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("expected an embedded QR image: %s", body)
	}
	if !strings.Contains(body, "no longer work") {
		t.Fatalf("expected the reprint hint: %s", body)
	}
}

func TestTablesQR_NeverLeaksTableIDHostOrSelfOrder(t *testing.T) {
	mux, _, repo := newTableQRHarness(t)
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	body := getTableQR(mux, id, "192.168.1.50:8080").Body.String()
	for _, bad := range []string{id, "?table=", "/self-order", "192.168.1.50"} {
		if strings.Contains(body, bad) {
			t.Fatalf("body leaks %q: %s", bad, body)
		}
	}
}

func TestTablesQR_StableTokenAcrossCalls(t *testing.T) {
	mux, _, repo := newTableQRHarness(t)
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	a := getTableQR(mux, id, "").Body.String()
	b := getTableQR(mux, id, "").Body.String()
	tok := tableToken(t, repo, id)
	if !strings.Contains(a, "/t/"+tok) || !strings.Contains(b, "/t/"+tok) {
		t.Fatalf("token not stable across calls (%s)", tok)
	}
}

func TestTablesQR_MintsForUntokenedTable(t *testing.T) {
	mux, d, repo := newTableQRHarness(t)
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	if _, err := d.Db.Exec(`UPDATE tables SET guest_token = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	body := getTableQR(mux, id, "").Body.String()
	var tok string
	if err := d.Db.QueryRow(`SELECT COALESCE(guest_token,'') FROM tables WHERE id = ?`, id).Scan(&tok); err != nil || tok == "" {
		t.Fatalf("no token minted: %q, %v", tok, err)
	}
	if !strings.Contains(body, "https://cloud.example.test/t/"+tok) {
		t.Fatalf("minted token not in the URL: %s", body)
	}
}

func TestTablesQR_PlanGate(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, d *common.Deps)
	}{
		{"no plan", func(t *testing.T, d *common.Deps) {
			if _, err := d.Db.Exec(`DELETE FROM settings WHERE key LIKE 'entitlement.%'`); err != nil {
				t.Fatal(err)
			}
		}},
		{"local", func(t *testing.T, d *common.Deps) { seedTrackingPlan(t, d, entitlement.PlanLocal) }},
		{"local plus", func(t *testing.T, d *common.Deps) { seedTrackingPlan(t, d, entitlement.PlanLocalPlus) }},
		{"lapsed past grace", func(t *testing.T, d *common.Deps) {
			seedRetentionEntitlement(t, d, string(entitlement.PlanShop), entitlement.StatusActive,
				time.Now().Add(-entitlement.Grace-24*time.Hour))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, d, repo := newTableQRHarness(t)
			id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
			c.seed(t, d)
			rec := getTableQR(mux, id, "")
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, "Shop plan or above") {
				t.Fatalf("want the plan message (200), got %d: %s", rec.Code, body)
			}
			if strings.Contains(body, "<img") || strings.Contains(body, "/t/") {
				t.Fatalf("no code may be shown without the plan: %s", body)
			}
		})
	}
}

func TestTablesQR_NotRegistered(t *testing.T) {
	mux, _, repo := newTableQRHarness(t)
	tableQRRegistered = func() bool { return false }
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	body := getTableQR(mux, id, "").Body.String()
	if !strings.Contains(body, "Connect this till to the cloud") || strings.Contains(body, "<img") {
		t.Fatalf("want the connect-to-cloud message and no image: %s", body)
	}
}

func TestTablesQR_DisabledTable(t *testing.T) {
	mux, _, repo := newTableQRHarness(t)
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	if err := repo.SetTableEnabled(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	body := getTableQR(mux, id, "").Body.String()
	if !strings.Contains(body, "This table is deactivated") || strings.Contains(body, "<img") {
		t.Fatalf("want the deactivated message and no image: %s", body)
	}
}

func TestTablesQR_ReplicaNeverMintsOrShows(t *testing.T) {
	mux, d, repo := newTableQRHarness(t)
	id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
	if _, err := d.Db.Exec(`UPDATE tables SET guest_token = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main.local"); err != nil {
		t.Fatal(err)
	}
	body := getTableQR(mux, id, "").Body.String()
	if !strings.Contains(body, "manage tables on the main till") || strings.Contains(body, "<img") {
		t.Fatalf("want the main-till message and no image: %s", body)
	}
	var n int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM tables WHERE id = ? AND guest_token IS NOT NULL`, id).Scan(&n)
	if n != 0 {
		t.Fatal("a replica minted a token")
	}
}

func TestTablesQR_EmptyOrBadEndpoint(t *testing.T) {
	for _, ep := range []string{"", "   ", "not a url", "ftp://cloud.example.test"} {
		mux, d, repo := newTableQRHarness(t)
		d.Cfg.Marketplace.EndpointURL = ep
		id, _ := repo.CreateTable(t.Context(), "T5", "", 4, "rect", 100, 100)
		rec := getTableQR(mux, id, "")
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "no valid cloud address") || strings.Contains(body, "<img") {
			t.Fatalf("endpoint %q: want 200 + no_cloud_address, got %d: %s", ep, rec.Code, body)
		}
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
