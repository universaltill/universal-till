package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2997: the last three Settings handlers that wrote a shop-wide key
// locally on an additional till -- the EOD card's business-day start, the
// report retention mode and the auto-update schedule -- write through to
// the main till like every other shop-wide setting (#2791, #2979). One
// batch per save; a failure writes nothing locally, not even the per-till
// keys saved alongside, and is not audited.

func newEODUpdateWriteThroughReplica(t *testing.T, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newSettingsSyncReplica(t, mainURL)
	registerEODAPI(mux, dp)
	registerReportArchiveAPI(mux, dp)
	registerUpdateAPI(mux, dp)
	return mux, dp
}

var eodSettingsForm = url.Values{
	"enabled": {"on"}, "time": {"23:30"}, "business_day_start": {"04:00"},
	"article_print_mode": {eodArticlePrintCapped}, "article_print_cap": {"12"},
}

var eodPerTillWant = map[string]string{
	keyEODEnabled: "true", keyEODTime: "23:30",
	keyEODArticlePrintMode: eodArticlePrintCapped, keyEODArticlePrintCap: "12",
}

func TestSettingsWriteThrough_EODBusinessDayOnMainPerTillLocal(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newEODUpdateWriteThroughReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/eod", eodSettingsForm, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica settings/eod = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (one batch)", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, keyReportsBusinessDayStart); got != "04:00" {
		t.Fatalf("main till %s = %q, want 04:00", keyReportsBusinessDayStart, got)
	}
	if got := mustSetting(t, dp, keyReportsBusinessDayStart); got != "04:00" {
		t.Fatalf("replica %s = %q, want the mirrored 04:00", keyReportsBusinessDayStart, got)
	}
	assertSettingSyncAudit(t, main.dp, "m1", keyReportsBusinessDayStart, "04:00", "Till 2")
	for k, want := range eodPerTillWant {
		if got := mustSetting(t, dp, k); got != want {
			t.Errorf("replica %s = %q, want %q", k, got, want)
		}
		if got := mustSetting(t, main.dp, k); got != "" {
			t.Errorf("main till %s = %q — a per-till key must never be sent", k, got)
		}
	}
	if !hasAudit(t, dp, "report", "-", "eod_settings_changed") {
		t.Fatal("a successful save must still be audited on this till")
	}
}

func TestSettingsWriteThrough_EODUnreachableWritesNothing(t *testing.T) {
	mux, dp := newEODUpdateWriteThroughReplica(t, deadPrimaryURL())

	rec := postForm(mux, "/api/settings/eod", eodSettingsForm, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("settings/eod = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	for k := range eodPerTillWant {
		if got := mustSetting(t, dp, k); got != "" {
			t.Errorf("refused change wrote a per-till key locally: %s = %q", k, got)
		}
	}
	if got := mustSetting(t, dp, keyReportsBusinessDayStart); got != "" {
		t.Fatalf("refused change wrote locally: %s = %q", keyReportsBusinessDayStart, got)
	}
	if hasAudit(t, dp, "report", "-", "eod_settings_changed") {
		t.Fatal("a refused save must not be audited")
	}
}

func TestSettingsWriteThrough_ReportRetentionOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newEODUpdateWriteThroughReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/report-retention", url.Values{"mode": {common.ReportRetentionModeTill}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica settings/report-retention = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.KeyReportRetentionMode); got != common.ReportRetentionModeTill {
		t.Fatalf("main till %s = %q, want till", common.KeyReportRetentionMode, got)
	}
	if got := mustSetting(t, dp, common.KeyReportRetentionMode); got != common.ReportRetentionModeTill {
		t.Fatalf("replica %s = %q, want the mirrored till", common.KeyReportRetentionMode, got)
	}
	if !hasAudit(t, dp, "report", "-", "report_retention_mode_changed") {
		t.Fatal("a successful save must still be audited on this till")
	}
}

func TestSettingsWriteThrough_ReportRetentionUnreachableWritesNothing(t *testing.T) {
	mux, dp := newEODUpdateWriteThroughReplica(t, deadPrimaryURL())

	rec := postForm(mux, "/api/settings/report-retention", url.Values{"mode": {common.ReportRetentionModeTill}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("settings/report-retention = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, common.KeyReportRetentionMode); got != "" {
		t.Fatalf("refused change wrote locally: %q", got)
	}
	if hasAudit(t, dp, "report", "-", "report_retention_mode_changed") {
		t.Fatal("a refused save must not be audited")
	}
}

// "On" is sent as the shop-wide default (unset), exactly what the main till
// stores for itself (#2726): never an explicit "true", which would read as
// on for every till. "Off" travels as "false".
func TestSettingsWriteThrough_UpdateScheduleOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	if err := main.dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	mux, dp := newEODUpdateWriteThroughReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/update-schedule", url.Values{"enabled": {"on"}, "time": {"02:30"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica update-schedule on = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (one batch)", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, keyAutoUpdateEnabled); got != "" {
		t.Fatalf("main till %s = %q, want the default (unset) — never an explicit true", keyAutoUpdateEnabled, got)
	}
	if got := mustSetting(t, main.dp, keyAutoUpdateTime); got != "02:30" {
		t.Fatalf("main till %s = %q, want 02:30", keyAutoUpdateTime, got)
	}
	if got := mustSetting(t, dp, keyAutoUpdateTime); got != "02:30" {
		t.Fatalf("replica %s = %q, want the mirrored 02:30", keyAutoUpdateTime, got)
	}

	rec = postForm(mux, "/api/settings/update-schedule", url.Values{"time": {"02:30"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replica update-schedule off = %d %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, main.dp, keyAutoUpdateEnabled); got != "false" {
		t.Fatalf("main till %s = %q, want false", keyAutoUpdateEnabled, got)
	}
	if got := mustSetting(t, dp, keyAutoUpdateEnabled); got != "false" {
		t.Fatalf("replica %s = %q, want the mirrored false", keyAutoUpdateEnabled, got)
	}
}

func TestSettingsWriteThrough_UpdateScheduleUnreachableWritesNothing(t *testing.T) {
	mux, dp := newEODUpdateWriteThroughReplica(t, deadPrimaryURL())

	rec := postForm(mux, "/api/settings/update-schedule", url.Values{"enabled": {"on"}, "time": {"02:30"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("update-schedule = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	for _, k := range []string{keyAutoUpdateEnabled, keyAutoUpdateTime} {
		if got := mustSetting(t, dp, k); got != "" {
			t.Fatalf("refused change wrote locally: %s = %q", k, got)
		}
	}
}

// On a main till nothing changes: saved locally, nothing is sent.
func TestSettingsWriteThrough_EODUpdateOnMainTillStayLocal(t *testing.T) {
	mux, _, dp := newFullAuthDeps(t)
	registerEODAPI(mux, dp)
	registerReportArchiveAPI(mux, dp)
	registerUpdateAPI(mux, dp)

	if rec := postForm(mux, "/api/settings/eod", eodSettingsForm, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("settings/eod = %d %q", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/report-retention", url.Values{"mode": {common.ReportRetentionModeTill}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("settings/report-retention = %d %q", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/settings/update-schedule", url.Values{"enabled": {"on"}, "time": {"02:30"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("update-schedule = %d %q", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]string{
		keyReportsBusinessDayStart: "04:00", keyEODTime: "23:30",
		common.KeyReportRetentionMode: common.ReportRetentionModeTill,
		keyAutoUpdateEnabled:          "", keyAutoUpdateTime: "02:30",
	} {
		if got := mustSetting(t, dp, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
