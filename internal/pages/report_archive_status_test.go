package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#574 (ADR-0147): the Report retention card enables cloud/both only
// with a cloud_backup entitlement, and on the main till shows the pending
// upload count and a refused-upload warning; the status bar carries a chip
// for the refusal.

func getSettingsBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// retentionSelect returns the retention card's <select name="mode"> markup.
func retentionSelect(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `hx-post="/api/settings/report-retention"`)
	if i < 0 {
		t.Fatal("retention form missing")
	}
	rest := body[i:]
	j := strings.Index(rest, "</select>")
	if j < 0 {
		t.Fatal("retention select missing")
	}
	return rest[:j]
}

func setKV(t *testing.T, d *common.Deps, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func activeCloudBackup(t *testing.T, d *common.Deps) {
	t.Helper()
	setKV(t, d, map[string]string{
		entitlement.KeyPlan:               "shop",
		entitlement.KeySubscriptionStatus: "active",
		entitlement.KeyLastConfirmedAt:    time.Now().UTC().Format(time.RFC3339),
	})
}

func seedTwoArchives(t *testing.T, d *common.Deps) {
	t.Helper()
	repo := data.NewPOSRepo(d.Db)
	for _, p := range []string{"2026-09-01", "2026-09-02"} {
		if _, err := repo.ArchiveReport(t.Context(), "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsRetention_CloudOptionsNeedSubscription(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	sel := retentionSelect(t, getSettingsBody(t, mux))
	for _, v := range []string{`value="cloud" disabled`, `value="both" disabled`} {
		if !strings.Contains(sel, v) {
			t.Errorf("without a subscription the select must contain %q:\n%s", v, sel)
		}
	}
	if !strings.Contains(sel, "needs an active subscription") {
		t.Errorf("disabled options must say why:\n%s", sel)
	}
	if strings.Contains(sel, "coming soon") {
		t.Errorf("the old coming-soon hint is still rendered:\n%s", sel)
	}
}

func TestSettingsRetention_CloudOptionsEnabledWithCloudBackup(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	activeCloudBackup(t, d)
	sel := retentionSelect(t, getSettingsBody(t, mux))
	if strings.Contains(sel, "disabled") || strings.Contains(sel, "needs an active subscription") {
		t.Fatalf("with cloud_backup every mode must be selectable:\n%s", sel)
	}
}

func TestSettingsRetention_MainTillShowsPendingAndRefusal(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedTwoArchives(t, d)
	setKV(t, d, map[string]string{
		common.KeyReportRetentionMode:       "cloud",
		data.ReportArchiveCloudRefusedAtKey: "2026-10-01T00:00:00Z",
	})
	body := getSettingsBody(t, mux)
	if !strings.Contains(body, "Reports waiting to upload to the cloud: 2") {
		t.Errorf("pending upload count missing")
	}
	if !strings.Contains(body, "Cloud upload refused") {
		t.Errorf("refusal warning missing")
	}
}

func TestSettingsRetention_TillModeOrReplicaShowsNoCloudStatus(t *testing.T) {
	cases := map[string]map[string]string{
		"till mode": {common.KeyReportRetentionMode: "till", data.ReportArchiveCloudRefusedAtKey: "2026-10-01T00:00:00Z"},
		"replica":   {common.KeyReportRetentionMode: "cloud", data.ReportArchiveCloudRefusedAtKey: "2026-10-01T00:00:00Z", "sync.primary_url": "http://10.0.0.2:8080"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			seedTwoArchives(t, d)
			setKV(t, d, kv)
			body := getSettingsBody(t, mux)
			if strings.Contains(body, "Reports waiting to upload") || strings.Contains(body, "Cloud upload refused") {
				t.Fatalf("%s must not show the cloud upload status", name)
			}
		})
	}
}

func getReportArchiveChip(t *testing.T, d *common.Deps, u auth.User) string {
	t.Helper()
	mux := http.NewServeMux()
	registerReportArchiveChip(mux, d)
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/report-archive-chip", nil), u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/report-archive-chip = %d", rec.Code)
	}
	return strings.TrimSpace(rec.Body.String())
}

func TestReportArchiveChip(t *testing.T) {
	refused := map[string]string{common.KeyReportRetentionMode: "cloud", data.ReportArchiveCloudRefusedAtKey: "2026-10-01T00:00:00Z"}

	t.Run("no refusal: empty", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		setKV(t, d, map[string]string{common.KeyReportRetentionMode: "cloud"})
		if got := getReportArchiveChip(t, d, mgrUser); got != "" {
			t.Fatalf("chip = %q, want empty", got)
		}
	})
	t.Run("refused, cloud mode, main till: chip linking to the retention card", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		setKV(t, d, refused)
		got := getReportArchiveChip(t, d, mgrUser)
		if !strings.Contains(got, `data-testid="sb-report-archive"`) || !strings.Contains(got, "Report upload refused") {
			t.Fatalf("chip = %q", got)
		}
		if !strings.Contains(got, `href="/settings#settings-retention"`) {
			t.Fatalf("a manager's chip must link to the retention card: %q", got)
		}
	})
	t.Run("cashier: words, no link", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		setKV(t, d, refused)
		got := getReportArchiveChip(t, d, auth.User{ID: "c1", Role: "cashier"})
		if !strings.Contains(got, "Report upload refused") || strings.Contains(got, "href=") {
			t.Fatalf("cashier chip = %q", got)
		}
	})
	t.Run("till mode: empty", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		setKV(t, d, refused)
		setKV(t, d, map[string]string{common.KeyReportRetentionMode: "till"})
		if got := getReportArchiveChip(t, d, mgrUser); got != "" {
			t.Fatalf("chip = %q, want empty in till mode", got)
		}
	})
	t.Run("replica: empty", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		setKV(t, d, refused)
		setKV(t, d, map[string]string{"sync.primary_url": "http://10.0.0.2:8080"})
		if got := getReportArchiveChip(t, d, mgrUser); got != "" {
			t.Fatalf("chip = %q, want empty on a replica", got)
		}
	})
}

// The refusal state is per-till (never admin-synced): a replica must not
// inherit the main till's refusal, and the cloudsync writer and the page
// reader must agree on one key.
func TestReportArchiveRefusalKeyIsPerTill(t *testing.T) {
	if data.SettingScope(data.ReportArchiveCloudRefusedAtKey) != data.SettingPerTill {
		t.Fatalf("%s must be a per-till setting", data.ReportArchiveCloudRefusedAtKey)
	}
	if common.KeyReportRetentionMode != "store.report_retention_mode" {
		t.Fatalf("retention mode key moved: %q", common.KeyReportRetentionMode)
	}
}

// Tester finding B1 (ut-docs#574): the generic settings upsert must not be
// a side door around POST /api/settings/report-retention's validation,
// cloud_backup gate, elevation and audit (ADR-0147 §1) — even for an admin.
func TestSettingsUpsert_RefusesReportRetentionMode(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	registerSettings(mux, dp)
	for _, v := range []string{"both", "cloud", "nonsense", "till"} {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/upsert", strings.NewReader("key="+common.KeyReportRetentionMode+"&value="+v))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("upsert %s=%q: expected 403, got %d: %s", common.KeyReportRetentionMode, v, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Report retention card") {
			t.Fatalf("body = %q, want the translated pointer to the retention card", rec.Body.String())
		}
		if got, ok, _ := dp.Settings.Get(t.Context(), common.KeyReportRetentionMode); ok {
			t.Fatalf("upsert stored the mode %q", got)
		}
	}
}
