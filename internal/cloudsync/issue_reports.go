package cloudsync

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
)

// uploadPendingIssueReports pushes any locally-saved, not-yet-uploaded
// bug-report bundles (ADR-0022) to the cloud. Best-effort: a bundle that
// fails to upload stays in the local pending queue and is retried on the
// next tick — the same "never blocks, always retries" contract as the rest
// of this file. The cloud-side receiving endpoint is spec 012 Phase 2; until
// it exists, uploads fail harmlessly and bundles simply wait.
func uploadPendingIssueReports(ctx context.Context, cfg *config.Config) {
	bundles, err := issuereport.Pending()
	if err != nil {
		logging.L().Warnf("cloudsync: listing pending issue reports: %v", err)
		return
	}
	for _, b := range bundles {
		if err := uploadIssueReport(ctx, cfg, b); err != nil {
			logging.L().Warnf("cloudsync: issue report %s not uploaded (will retry): %v", b.Meta.ID, err)
			continue
		}
		if err := issuereport.Discard(b.Meta.ID); err != nil {
			logging.L().Warnf("cloudsync: issue report %s uploaded but not cleared locally: %v", b.Meta.ID, err)
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
	_ = w.WriteField("created_at", b.Meta.CreatedAt.Format("2006-01-02T15:04:05.000000000Z07:00"))
	if err := attachFile(w, "audio", "audio.webm", b.AudioPath); err != nil {
		return err
	}
	if b.VideoPath != "" {
		if err := attachFile(w, "video", "video.webm", b.VideoPath); err != nil {
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
