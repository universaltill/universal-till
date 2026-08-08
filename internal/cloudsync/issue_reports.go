package cloudsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
)

// uploadPendingIssueReports pushes any locally-saved, not-yet-uploaded
// bug-report bundles (ADR-0022) to the cloud. Best-effort: a bundle that
// fails to upload stays in the local pending queue and is retried on the
// next tick — the same "never blocks, always retries" contract as the rest
// of this file. Uploads go to the live cloud endpoint
// (POST /v1/stores/issue-reports, see uploadIssueReport below).
//
// After a successful upload the local record survives (ut-docs#348): a
// SentReport row is persisted BEFORE the bundle's media is discarded, so
// /my-reports can show what was reported and what became of it. Only the
// bulky blobs (audio/video/screenshots + logs) are thrown away. If the
// record can't be persisted, the bundle is deliberately NOT discarded —
// re-uploading the whole thing next tick beats losing the report with no
// trace anywhere (SaveSent upserts by id, so the retry never duplicates).
func uploadPendingIssueReports(ctx context.Context, cfg *config.Config, db *sql.DB) {
	bundles, err := issuereport.Pending()
	if err != nil {
		logging.L().Warnf("cloudsync: listing pending issue reports: %v", err)
		return
	}
	repo := data.NewIssueReportsRepo(db)
	for _, b := range bundles {
		if err := uploadIssueReport(ctx, cfg, b); err != nil {
			logging.L().Warnf("cloudsync: issue report %s not uploaded (will retry): %v", b.Meta.ID, err)
			continue
		}
		if err := repo.SaveSent(ctx, data.SentReport{
			ID:         b.Meta.ID,
			Note:       b.Meta.Note,
			CapturedAt: b.Meta.CreatedAt,
			HadAudio:   b.AudioPath != "",
			HadVideo:   b.VideoPath != "",
			ImageCount: len(b.ImagePaths),
			Status:     "sent",
		}); err != nil {
			logging.L().Warnf("cloudsync: issue report %s uploaded but its retained record failed to save — keeping the bundle for retry: %v", b.Meta.ID, err)
			continue
		}
		if err := issuereport.Discard(b.Meta.ID); err != nil {
			logging.L().Warnf("cloudsync: issue report %s uploaded but not cleared locally: %v", b.Meta.ID, err)
		}
	}
}

// pullIssueReportStatuses pulls the cloud's per-report statuses down
// (GET /v1/stores/issue-reports, ut-docs#348) and applies them to the
// retained issue_reports_sent rows, matched by the till's own report id —
// the correlation key the cloud echoes back verbatim. Best-effort like
// everything else in this file: any failure logs a warning and returns, and
// /my-reports simply keeps showing the last-known statuses (offline-first —
// the page itself never touches the network).
func pullIssueReportStatuses(ctx context.Context, cfg *config.Config, db *sql.DB) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return // not registered — nothing to pull
	}

	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/issue-reports?store_id=" + m.StoreID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logging.L().Warnf("cloudsync: issue-report status pull: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		logging.L().Warnf("cloudsync: issue-report status pull: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Warnf("cloudsync: issue-report status pull returned %d", resp.StatusCode)
		return
	}
	var out struct {
		Data struct {
			Reports []struct {
				ID             string `json:"id"`
				Status         string `json:"status"`
				GithubIssueURL string `json:"github_issue_url"`
			} `json:"reports"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Warnf("cloudsync: decode issue-report statuses: %v", err)
		return
	}
	repo := data.NewIssueReportsRepo(db)
	for _, item := range out.Data.Reports {
		if item.ID == "" {
			continue
		}
		// An id the till never retained is a silent no-op inside UpdateStatus.
		if err := repo.UpdateStatus(ctx, item.ID, item.Status, item.GithubIssueURL); err != nil {
			logging.L().Warnf("cloudsync: issue report %s status not applied: %v", item.ID, err)
		}
	}
}

func uploadIssueReport(ctx context.Context, cfg *config.Config, b issuereport.Bundle) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return fmt.Errorf("not registered")
	}

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	_ = w.WriteField("store_id", m.StoreID)
	_ = w.WriteField("device_id", enroll.CurrentStatus().DeviceID)
	_ = w.WriteField("report_id", b.Meta.ID)
	_ = w.WriteField("note", b.Meta.Note)
	// Capture-time UI locale (ut-docs#397) so cloud-side transcription can
	// hand it to Whisper. Always sent — an empty string means "no locale
	// known, auto-detect" on the cloud side, so don't skip the field.
	_ = w.WriteField("locale", b.Meta.Locale)
	_ = w.WriteField("created_at", b.Meta.CreatedAt.Format("2006-01-02T15:04:05.000000000Z07:00"))
	if b.AudioPath != "" {
		if err := attachFile(w, "audio", "audio.webm", b.AudioPath); err != nil {
			return err
		}
	}
	if b.VideoPath != "" {
		if err := attachFile(w, "video", "video.webm", b.VideoPath); err != nil {
			return err
		}
	}
	// Screenshots (ut-docs#347): one repeated "image" field per captured
	// screenshot, in capture order (ImagePaths is already sorted by
	// issuereport.Pending()). A repeated field name is valid multipart and
	// is how the cloud side reads them back as a slice.
	for _, p := range b.ImagePaths {
		if err := attachFile(w, "image", filepath.Base(p), p); err != nil {
			return err
		}
	}
	for _, p := range b.Meta.Logs {
		fw, err := w.CreateFormField("logs")
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte(p.At.Format("2006-01-02T15:04:05Z07:00") + "\t" + p.Level + "\t" + p.Msg)); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}

	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/issue-reports"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("issue-reports upload returned %d", resp.StatusCode)
	}
	return nil
}

func attachFile(w *multipart.Writer, field, filename, path string) error {
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}
