package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#574 (ADR-0040 card 4, ADR-0147): what the Report retention card
// and the status bar show about the cloud upload of archived reports.

// reportArchiveCloudStatus is the retention card's cloud view.
type reportArchiveCloudStatus struct {
	// CloudAllowed: cloud/both may be chosen now (ADR-0147 §1 gate).
	CloudAllowed bool
	// Show: this is the main till and the mode uploads, so the upload
	// state below means something. A replica never uploads (§2).
	Show bool
	// Pending: rows not yet acknowledged by the cloud.
	Pending int
	// Refused: the cloud's last answer was 402 subscription_inactive.
	Refused bool
}

// loadReportArchiveCloudStatus reads the card's cloud view. Best-effort: a
// read error just leaves a field at its zero value (logged), like the
// card's other reads.
func loadReportArchiveCloudStatus(ctx context.Context, d *common.Deps, mode string, now time.Time) reportArchiveCloudStatus {
	st := reportArchiveCloudStatus{CloudAllowed: reportRetentionCloudAllowed(ctx, d, now)}
	if !data.UploadsReportArchives(strings.TrimSpace(mode)) || !reportArchiveOnMainTill(ctx, d) {
		return st
	}
	st.Show = true
	n, err := data.NewPOSRepo(d.Db).CountUnackedReportArchives(ctx)
	if err != nil {
		logging.L().Errorf("report archive pending count: %v", err)
	}
	st.Pending = n
	st.Refused = reportArchiveUploadRefused(ctx, d)
	return st
}

// reportArchiveUploadRefused reports whether cloudsync recorded a 402 that
// no later accepted upload has cleared.
func reportArchiveUploadRefused(ctx context.Context, d *common.Deps) bool {
	v, _, _ := d.Settings.Get(ctx, data.ReportArchiveCloudRefusedAtKey)
	return strings.TrimSpace(v) != ""
}

// reportArchiveChipVisible: the chip shows only on the main till, in mode
// cloud or both, while a refusal stands (ADR-0147 §1/§2).
func reportArchiveChipVisible(ctx context.Context, d *common.Deps) bool {
	mode, _, _ := d.Settings.Get(ctx, common.KeyReportRetentionMode)
	if !data.UploadsReportArchives(strings.TrimSpace(mode)) {
		return false
	}
	return reportArchiveOnMainTill(ctx, d) && reportArchiveUploadRefused(ctx, d)
}

// registerReportArchiveChip: GET /ui/report-archive-chip, polled from the
// status bar on every page (base.html), same shape as /ui/cloud-auth-chip.
// Empty 200 unless the cloud refused the report upload; then a status chip
// — never a modal, the till keeps every report locally and retries —
// linking to Settings → Report retention for a viewer who may open
// Settings.
func registerReportArchiveChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/report-archive-chip", func(w http.ResponseWriter, r *http.Request) {
		if !reportArchiveChipVisible(r.Context(), d) {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/report_archive_chip.html", map[string]any{
			"canManage": canPerform(d, r, "settings"),
		})(w, r)
	})
}
