package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2557 (ADR-0111 §4): the manager-only toggle for per-staff figures
// in cloud reports. Off unless the stored value is exactly "true".

func TestCloudStaffBreakdown_ManagerTogglesSetting(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	const path = "/api/settings/cloud-staff-breakdown"

	if rec := postForm(mux, path, url.Values{"enabled": {"on"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager enable = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.CloudStaffBreakdownKey); v != "true" {
		t.Fatalf("%s = %q, want true", data.CloudStaffBreakdownKey, v)
	}
	// An unchecked box sends nothing and stores an explicit "false".
	if rec := postForm(mux, path, url.Values{}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager disable = %d, want 204", rec.Code)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), data.CloudStaffBreakdownKey); !ok || v != "false" {
		t.Fatalf("%s = %q (found=%v), want false", data.CloudStaffBreakdownKey, v, ok)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_staff_breakdown_changed' AND entity_id = ?`, data.CloudStaffBreakdownKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2 (one per successful save)", n)
	}
	var payload string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'cloud_staff_breakdown_changed' ORDER BY rowid DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"enabled":"false"`) {
		t.Fatalf("audit payload = %s, want enabled=false", payload)
	}
}

func TestCloudStaffBreakdown_CashierRefused403(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/cloud-staff-breakdown", url.Values{"enabled": {"on"}}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403 (no PIN-elevation path)", rec.Code)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), data.CloudStaffBreakdownKey); ok {
		t.Fatalf("refused write stored %q", v)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_staff_breakdown_changed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("audit rows = %d after a refused write, want 0", n)
	}
}

func TestCloudStaffBreakdown_CardManagerOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("manager GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	if !strings.Contains(body, `id="settings-cloud-staff"`) {
		t.Fatal("manager render is missing the Staff sales in cloud reports card")
	}
	if !strings.Contains(body, `data-key="settings-cloud-staff"`) {
		t.Error("manager sidebar is missing the settings-cloud-staff row")
	}
	box := checkboxTag(t, body, `data-testid="cloud-staff-enabled"`)
	if strings.Contains(box, "checked") {
		t.Errorf("checkbox is checked by default: %s", box)
	}
	for _, v := range []string{"false", "yes", "1"} {
		if err := d.Settings.Set(t.Context(), data.CloudStaffBreakdownKey, v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(checkboxTag(t, get(), `data-testid="cloud-staff-enabled"`), "checked") {
			t.Errorf("value %q must not render as checked (only exact \"true\" enables)", v)
		}
	}
	if err := d.Settings.Set(t.Context(), data.CloudStaffBreakdownKey, "true"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checkboxTag(t, get(), `data-testid="cloud-staff-enabled"`), "checked") {
		t.Error("checkbox must be checked when the stored value is true")
	}

	cash := settingsRefusedForCashier(t, mux)
	if strings.Contains(cash, `id="settings-cloud-staff"`) || strings.Contains(cash, `settings-cloud-staff`) {
		t.Error("cashier response carries the card or its nav row")
	}
}

// checkboxTag returns the <input ...> tag containing marker.
func checkboxTag(t *testing.T, body, marker string) string {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("marker %s not found in page", marker)
	}
	start := strings.LastIndex(body[:at], "<input")
	end := strings.Index(body[at:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("no <input> around %s", marker)
	}
	return body[start : at+end+1]
}
