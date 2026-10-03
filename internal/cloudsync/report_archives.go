package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// Till-side uploader for the cloud report archive (ut-docs#574, ADR-0040
// card 4, ADR-0147 §2/§3): the main till POSTs each report_archive row the
// cloud has not acknowledged yet to {EndpointURL}/v1/stores/report-archives
// and sets cloud_acked_at on a 2xx. Only in retention mode cloud or both;
// the un-acked rows are the queue, so a failed round simply leaves them for
// a later one. Best-effort and off the sale path like the rest of this
// package (ADR-0003). Same shape as pushSalesAggregates.

const (
	reportArchivePath = "/v1/stores/report-archives"
	// reportArchiveBatch bounds one round: a till catching up after a long
	// offline spell or a fresh switch to cloud sends at most this many rows
	// per round, oldest first, instead of its whole history in one tick.
	reportArchiveBatch = 50
)

var (
	// reportArchiveIntervalNS throttles pushReportArchives to one round per
	// interval (default 10 minutes, ADR-0147 §2); atomic so tests can
	// override it while Start's goroutine reads it.
	reportArchiveIntervalNS atomic.Int64
	// reportArchiveLastNS is the UnixNano start of the last round (0 = never).
	reportArchiveLastNS atomic.Int64
)

func init() {
	reportArchiveIntervalNS.Store(int64(10 * time.Minute))
}

// reportArchiveIdentifier mirrors ut-cloud's reportarchive.ValidIdentifier:
// the cloud keys the blob on period, so only this alphabet is accepted.
var reportArchiveIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// reportArchiveWirePeriod maps a stored period to the form the cloud accepts
// (ADR-0147 §3): ':' → '-', '+' → 'p', '.' → '_', everything else unchanged.
// "2026-09-30T23:15:00+02:00" becomes "2026-09-30T23-15-00p02-00" — the
// local business date stays the prefix the owner read API (#3271) matches
// on, and the mapping is one-to-one on RFC3339 input, so two closes on one
// day never collide. A pre-ADR-0066 "2026-09-30" is unchanged. ok=false
// when the mapped value still fails the identifier check: never sent.
func reportArchiveWirePeriod(period string) (string, bool) {
	wire := strings.NewReplacer(":", "-", "+", "p", ".", "_").Replace(period)
	if !reportArchiveIdentifier.MatchString(wire) {
		return "", false
	}
	return wire, true
}

// reportArchiveUpload is the wire body; field names mirror ut-cloud's
// report-archive upload handler. Content is the archived report itself
// (content_json), sent as JSON, not as a string.
type reportArchiveUpload struct {
	StoreID string          `json:"store_id"`
	Kind    string          `json:"kind"`
	Period  string          `json:"period"`
	Content json.RawMessage `json:"content"`
}

// pushReportArchives runs one upload round. Called from Tick's main-till-
// only block; never returns an error — every failure is logged and a later
// round retries.
//
// Outcomes per row (ADR-0147 §2):
//   - 2xx: mark acked, clear the refusal state;
//   - 402 subscription_inactive: record the refusal (status chip), stop;
//   - a content rejection (rejectedRollup: 400/413/422…): skip the row, it
//     stays local and un-acked, the next row is tried;
//   - anything else (transport, auth, 5xx): stop, the next round retries.
func pushReportArchives(ctx context.Context, cfg *config.Config, db *sql.DB) {
	now := time.Now()
	last := reportArchiveLastNS.Load()
	if last != 0 && now.UnixNano()-last < reportArchiveIntervalNS.Load() {
		return
	}
	if !reportArchiveLastNS.CompareAndSwap(last, now.UnixNano()) {
		return
	}

	settings := data.NewSettingsRepo(db)
	mode, _, err := settings.Get(ctx, data.ReportRetentionModeKey)
	if err != nil {
		logging.L().Warnf("cloudsync: report archives: read mode: %v", err)
		return
	}
	if !data.UploadsReportArchives(strings.TrimSpace(mode)) {
		// Back on till (or never left it): a past refusal no longer means
		// anything, so the chip and the settings warning go.
		clearReportArchiveRefusal(ctx, settings)
		return
	}

	repo := data.NewPOSRepo(db)
	rows, err := repo.ListUnackedReportArchives(ctx, reportArchiveBatch)
	if err != nil {
		logging.L().Warnf("cloudsync: report archives: list: %v", err)
		return
	}
	storeID := enroll.Effective(cfg).Marketplace.StoreID
	sent := 0
	for _, row := range rows {
		period, ok := reportArchiveWirePeriod(row.Period)
		if !ok {
			logging.L().Warnf("cloudsync: report archive %s/%q has no valid wire period, kept local", row.Kind, row.Period)
			continue
		}
		payload, err := json.Marshal(reportArchiveUpload{
			StoreID: storeID, Kind: row.Kind, Period: period, Content: json.RawMessage(row.Content),
		})
		if err != nil {
			// content_json that is not valid JSON: the cloud could never
			// accept it either. Kept local, like a content rejection.
			logging.L().Warnf("cloudsync: report archive %s/%s: encode: %v", row.Kind, period, err)
			continue
		}
		if _, err := post(ctx, cfg, reportArchivePath, payload); err != nil {
			var se *statusError
			if errors.As(err, &se) && se.StatusCode == http.StatusPaymentRequired {
				recordReportArchiveRefusal(ctx, settings, now)
				return
			}
			if errors.As(err, &se) && rejectedRollup(se.StatusCode) {
				logging.L().Warnf("cloudsync: report archive %s/%s rejected (%d), kept local: %v", row.Kind, period, se.StatusCode, err)
				continue
			}
			logging.L().Warnf("cloudsync: report archive %s/%s upload failed (will retry): %v", row.Kind, period, err)
			return
		}
		if _, err := repo.MarkReportArchiveAcked(ctx, row.ID, time.Now()); err != nil {
			logging.L().Warnf("cloudsync: report archives: mark acked: %v", err)
			return
		}
		clearReportArchiveRefusal(ctx, settings)
		sent++
	}
	if sent > 0 {
		logging.L().Infof("cloudsync: report archives uploaded (%d)", sent)
	}
}

// recordReportArchiveRefusal stores the 402 time for the chip, logging only
// on the first refusal (the key is already set on every later one).
func recordReportArchiveRefusal(ctx context.Context, settings *data.SettingsRepo, now time.Time) {
	if prev, _, _ := settings.Get(ctx, data.ReportArchiveCloudRefusedAtKey); strings.TrimSpace(prev) != "" {
		return
	}
	logging.L().Infof("cloudsync: report archives not uploaded: cloud subscription inactive (402); kept on this till")
	if err := settings.Set(ctx, data.ReportArchiveCloudRefusedAtKey, now.UTC().Format(time.RFC3339)); err != nil {
		logging.L().Warnf("cloudsync: report archives: record refusal: %v", err)
	}
}

// clearReportArchiveRefusal removes the refusal state, writing only when it
// is set so an ordinary round costs no settings write.
func clearReportArchiveRefusal(ctx context.Context, settings *data.SettingsRepo) {
	if prev, ok, _ := settings.Get(ctx, data.ReportArchiveCloudRefusedAtKey); !ok || prev == "" {
		return
	}
	if err := settings.Delete(ctx, data.ReportArchiveCloudRefusedAtKey); err != nil {
		logging.L().Warnf("cloudsync: report archives: clear refusal: %v", err)
	}
}
