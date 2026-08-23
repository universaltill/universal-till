package cloudsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// errNotRegistered is uploadIssueReport's sentinel for "this till isn't
// enrolled in the marketplace yet" — a sentinel (not just a string) so the
// upload-failure classification below (ut-docs#637) can tell it apart from
// every other failure reliably, via errors.Is, rather than string-matching
// an error message that's free to change wording later.
var errNotRegistered = errors.New("not registered")

// pullStatusMaxBytes bounds a single status-pull response page (ut-docs#445)
// — the decoder must not buffer an unbounded body just because a
// misbehaving or compromised endpoint sent one. 4MB comfortably covers a
// full page (pullStatusPageLimit rows of id/status/url/timestamp strings)
// with headroom.
const pullStatusMaxBytes = 4 << 20

// pullStatusPageLimit / pullStatusMaxPages bound pullIssueReportStatuses'
// page loop (ut-docs#445): the cloud's own GET /v1/stores/issue-reports now
// caps a single response (ut-cloud's issueReportsDefaultLimit), so a store
// with more pending statuses than one page needs more than one round trip
// to stay fresh. pullStatusPageLimit matches that cloud-side default so a
// single round trip suffices for realistic single-store volume.
// pullStatusMaxPages bounds the worst case per tick — this whole function
// is best-effort and retried every cloudsync tick, so a store that needs
// more than pullStatusMaxPages pages simply catches up over a few ticks
// rather than this one tick doing unbounded work.
const (
	pullStatusPageLimit = 200
	pullStatusMaxPages  = 10
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
// record can't be persisted, the bundle is NOT immediately discarded —
// re-uploading the whole thing next tick beats losing the report with no
// trace anywhere (SaveSent upserts by id, so the retry never duplicates) —
// but that retry is capped (ut-docs#446): a persistently-failing local write
// (disk full, DB briefly read-only) would otherwise re-POST the bundle's
// full multipart body forever. After issuereport.MaxSentFailCount SaveSent
// failures (see that constant and RecordSentFailure's own docs for exactly
// what counts and why it still engages even when the disk itself is what's
// failing), the till gives up on local retention and discards the bundle —
// the report itself is not lost, it already reached the cloud (verified
// idempotent server-side), only this till's own local trace of it is. This
// cap applies ONLY to the SaveSent step: the cloud upload itself failing
// (offline/network-down) keeps retrying unboundedly, per offline-first
// (ADR-0003) — see the "not uploaded" branch below, which never touches the
// failure counter.
func uploadPendingIssueReports(ctx context.Context, cfg *config.Config, db *sql.DB) {
	bundles, err := issuereport.Pending()
	if err != nil {
		logging.L().Warnf("cloudsync: listing pending issue reports: %v", err)
		return
	}
	repo := data.NewIssueReportsRepo(db)
	for _, b := range bundles {
		if err := uploadIssueReport(ctx, cfg, b); err != nil {
			// ut-docs#637: classify and persist the failure so /my-reports can
			// eventually present this bundle as failing rather than merely
			// pending, without changing the retry itself — still unbounded,
			// per offline-first (ADR-0003). A failure to record the count is
			// only logged, never treated as reason to skip the retry.
			reason := issuereport.UploadFailReasonOther
			if errors.Is(err, errNotRegistered) {
				reason = issuereport.UploadFailReasonNotRegistered
			}
			// ut-docs#642: once /my-reports is already presenting this bundle
			// as failing — not_registered flags it from the very first
			// failure, other only once UploadFailCount reaches
			// issuereport.UploadFailingThreshold — and the reason hasn't
			// changed since the last recorded failure, another tick would
			// just fsync an identical meta.json rewrite that changes nothing
			// the operator sees, forever, while UploadFailCount grows
			// unbounded for no display benefit. Skip the write in that case.
			// Still record it the moment the reason changes (e.g.
			// not_registered -> other once enrolment finishes and a fresh
			// failure happens) — that IS a real state change.
			alreadyPresentedAsFailing := b.Meta.UploadFailReason == reason &&
				(reason == issuereport.UploadFailReasonNotRegistered || b.Meta.UploadFailCount >= issuereport.UploadFailingThreshold)
			if !alreadyPresentedAsFailing {
				if _, rerr := issuereport.RecordUploadFailure(b.Meta.ID, reason); rerr != nil {
					logging.L().Warnf("cloudsync: issue report %s upload-fail count not recorded: %v", b.Meta.ID, rerr)
				}
			}
			logging.L().Warnf("cloudsync: issue report %s not uploaded (will retry): %v", b.Meta.ID, err)
			continue
		}
		// ut-docs#637 review: clear any upload-fail state recorded by an
		// earlier tick BEFORE deciding what happens to the bundle next — if
		// SaveSent below fails and the bundle survives for retry, it must
		// not keep showing a stale "failing" reason for a report that has,
		// in fact, already reached the cloud.
		if cerr := issuereport.ClearUploadFailure(b.Meta.ID); cerr != nil {
			logging.L().Warnf("cloudsync: issue report %s upload-fail state not cleared: %v", b.Meta.ID, cerr)
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
			count, cerr := issuereport.RecordSentFailure(b.Meta.ID)
			if cerr != nil {
				logging.L().Warnf("cloudsync: issue report %s uploaded but its retained record failed to save, and its failure count couldn't be updated either — keeping the bundle for retry: %v (count error: %v)", b.Meta.ID, err, cerr)
				continue
			}
			if count >= issuereport.MaxSentFailCount {
				logging.L().Warnf("cloudsync: issue report %s uploaded to the cloud successfully but its local retained record failed to save %d times — giving up on local retention and discarding the bundle (the report itself was NOT lost; it is already on the cloud): %v", b.Meta.ID, count, err)
				if derr := issuereport.Discard(b.Meta.ID); derr != nil {
					logging.L().Warnf("cloudsync: issue report %s also failed to discard after giving up on local retention: %v", b.Meta.ID, derr)
				}
				continue
			}
			logging.L().Warnf("cloudsync: issue report %s uploaded but its retained record failed to save (attempt %d/%d) — keeping the bundle for retry: %v", b.Meta.ID, count, issuereport.MaxSentFailCount, err)
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

	// The cloud endpoint now caps a single response and reports the store's
	// true total row count alongside it (ut-docs#445), so a store with more
	// pending statuses than one page needs more than one round trip to stay
	// fully fresh. Page while offset+fetched hasn't yet covered total — the
	// precise "is there more" signal, not just "did the last page come back
	// full" (which mis-loops against a pre-#445 cloud that ignores
	// limit/offset and returns everything unbounded: that cloud never sends
	// total, so it decodes to its zero value, offset+fetched > 0 = total
	// immediately, and this stops after one page — correct, since that one
	// page already contained every row) — up to the safety cap; a store that
	// needs more pages than that catches up over the next few ticks (this
	// whole function is best-effort/retried already).
	offset := 0
	for page := 0; page < pullStatusMaxPages; page++ {
		fetched, total, err := pullIssueReportStatusesPage(ctx, cfg, db, pullStatusPageLimit, offset)
		if err != nil {
			return // already logged inside pullIssueReportStatusesPage
		}
		offset += fetched
		if offset >= total {
			return // covered every row the cloud reported — no more pages
		}
		if page == pullStatusMaxPages-1 {
			logging.L().Warnf("cloudsync: issue-report status pull hit the %d-page safety cap (%d/%d reports applied this tick) — status refresh incomplete, will continue next tick", pullStatusMaxPages, offset, total)
		}
	}
}

// pullIssueReportStatusesPage fetches and applies ONE page of the cloud's
// per-report statuses (GET /v1/stores/issue-reports?store_id=...&limit=...
// &offset=...), returning how many report rows this page contained and the
// store's true total row count (ut-docs#445's "total" field) so the caller
// can tell precisely whether there may be more. A cloud predating ut-docs#445
// sends no "total" field, which decodes to 0 — see pullIssueReportStatuses's
// own comment for why that's the correct degradation, not a bug. Best-effort
// like the rest of this file: any failure logs a warning and returns a
// non-nil error so the caller stops paging for this tick — /my-reports
// simply keeps showing the last-known statuses (offline-first — the page
// itself never touches the network).
func pullIssueReportStatusesPage(ctx context.Context, cfg *config.Config, db *sql.DB, limit, offset int) (fetched, total int, err error) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return 0, 0, errNotRegistered
	}

	url := fmt.Sprintf("%s/v1/stores/issue-reports?store_id=%s&limit=%d&offset=%d",
		strings.TrimRight(m.EndpointURL, "/"), m.StoreID, limit, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logging.L().Warnf("cloudsync: issue-report status pull: %v", err)
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		logging.L().Warnf("cloudsync: issue-report status pull: %v", err)
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Warnf("cloudsync: issue-report status pull returned %d", resp.StatusCode)
		return 0, 0, fmt.Errorf("issue-report status pull returned %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Reports []struct {
				ID             string `json:"id"`
				Status         string `json:"status"`
				GithubIssueURL string `json:"github_issue_url"`
			} `json:"reports"`
			Total int `json:"total"`
		} `json:"data"`
	}
	// Bounded so a misbehaving/compromised endpoint can't make this decode
	// buffer an unbounded body (ut-docs#445).
	if err := json.NewDecoder(io.LimitReader(resp.Body, pullStatusMaxBytes)).Decode(&out); err != nil {
		logging.L().Warnf("cloudsync: decode issue-report statuses: %v", err)
		return 0, 0, err
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
	return len(out.Data.Reports), out.Data.Total, nil
}

func uploadIssueReport(ctx context.Context, cfg *config.Config, b issuereport.Bundle) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return errNotRegistered
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
