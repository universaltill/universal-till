package cloudsync

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/stockupload"
)

// Stock upload scheduling (ADR-0169 §8, ut-docs#4763). internal/stockupload
// does the upload; this file decides WHEN and WHO:
//
//   - who: only the main till (no sync.primary_url) of a cloud-master store
//     (the cached entitlement catalog_master is "cloud"; a till-master or
//     adopting store mirrors nothing) that holds a complete cloud enrolment.
//     A replica never uploads — it holds marks (the triggers run on every
//     till) but nothing here sends them; the cloud refuses a non-designated
//     device too (403 not_main_till), so this is defence in depth.
//   - when, any tier: at the end of every check-in that reached the cloud,
//     after the catalogue pull/upload (so a just-created item's cloud link
//     exists). "Otherwise at the next check-in" in the card.
//   - when, realtime tier (ADR-0117): StockUploadLoop wakes every
//     stockUploadInterval (5 s, the card's bound), and uploads whatever has
//     changed since: a burst of sales inside one interval is one request
//     carrying each row's latest quantity. It reads the cached tier, never
//     the socket, so it works with the socket down.
//
// Neither path can block a sale: the sale only writes marks locally, in its
// own transaction, and both paths run on their own goroutines. Failures are
// logged, never returned: the marks stay and the next trigger retries.

// stockUploadInterval is the realtime loop's period and so the upload's
// latency bound (a package var so a test can shorten it).
var stockUploadInterval = 5 * time.Second

// stockUploadRunTimeout bounds one upload run (a var so a test can shorten it).
var stockUploadRunTimeout = 60 * time.Second

const (
	// stockUploadBackoffStart / Max bound the loop's retry spacing after a
	// failure or refusal, so an unreachable cloud is not dialled every 5 s.
	stockUploadBackoffStart = 30 * time.Second
	stockUploadBackoffMax   = 5 * time.Minute
)

// stockUploadWarned is set once a refusal has been logged at Warn; reset by
// the next accepted upload.
var stockUploadWarned atomic.Bool

type stockGate string

const (
	stockGateOpen           stockGate = ""
	stockGateUnenrolled     stockGate = "unenrolled"
	stockGateReplica        stockGate = "replica"
	stockGateNotCloudMaster stockGate = "not_cloud_master"
)

// stockUploadGate decides whether this till may upload stock now. It reads
// only local settings. The order is: enrolment, role, catalogue mode.
func stockUploadGate(ctx context.Context, cfg *config.Config, r entitlement.Reader) (config.MarketplaceConfig, stockGate) {
	m := enroll.Effective(cfg).Marketplace
	if !enroll.CredentialsComplete(m) {
		return m, stockGateUnenrolled
	}
	if v, _, _ := r.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return m, stockGateReplica
	}
	if entitlement.CatalogMaster(ctx, r) != entitlement.CatalogMasterCloud {
		return m, stockGateNotCloudMaster
	}
	return m, stockGateOpen
}

// uploadCloudStock uploads the owed rows when the gate is open and reports
// whether it failed (a transport error or a refusal), so the loop can back
// off. It never returns an error: see the file comment.
func uploadCloudStock(ctx context.Context, cfg *config.Config, db *sql.DB) (failed bool) {
	settings := data.NewSettingsRepo(db)
	m, gate := stockUploadGate(ctx, cfg, settings)
	if gate != stockGateOpen {
		return false
	}
	has, err := data.NewStockUploadRepo(db).HasSendable(ctx, time.Now())
	if err != nil {
		logging.L().Warnf("cloudsync: stock upload: %v", err)
		return true
	}
	if !has {
		return false
	}
	// One Run is bounded (up to 20 requests of up to 30 s each would add
	// 10 minutes to a check-in): the rest waits for the next trigger.
	ctx, cancel := context.WithTimeout(ctx, stockUploadRunTimeout)
	defer cancel()
	res, err := stockupload.Run(ctx, db, stockupload.NewHTTPPoster(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken), buildinfo.Version)
	switch {
	case res.Busy:
		return false
	case errors.Is(err, stockupload.ErrStreamConflict):
		// The cloud holds a newer stream of this store than this till's (a
		// restored backup, a cloned disk, a clock that jumped) and a rotation
		// did not settle it, or was not allowed yet. The marks wait; stable
		// code for support.
		if stockUploadWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: %s: the cloud holds a newer stock stream for this store; the changes wait and the upload retries later (check this till's clock and that no other till shares its credential)", err)
		} else {
			logging.L().Infof("cloudsync: %s (still)", err)
		}
		return true
	case err != nil:
		if stockUploadWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: stock upload failed (the changes are kept and re-sent on the next trigger): %v", err)
		} else {
			logging.L().Infof("cloudsync: stock upload still failing: %v", err)
		}
		return true
	case res.Stopped != "":
		if stockUploadWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: stock upload not accepted (%d %s); the changes wait", res.StopStatus, res.Stopped)
		} else {
			logging.L().Infof("cloudsync: stock upload still not accepted (%d %s)", res.StopStatus, res.Stopped)
		}
		return true
	}
	stockUploadWarned.Store(false)
	if res.Sent > 0 || res.Dropped > 0 || res.Deferred > 0 {
		logging.L().Infof("cloudsync: stock upload: %d sent (%d applied, %d already known, %d rejected), %d kept for a retry, %d dropped %v, stream rotated: %t",
			res.Sent, res.Applied, res.Stale, res.Rejected, res.Deferred, res.Dropped, res.DropReasons, res.Rotated)
	}
	return false
}

// StockUploadLoop is the realtime tier's upload (see the file comment). It
// returns when ctx ends. On any other tier it does nothing but wake and look
// at the cached tier, so a plan change to realtime takes effect without a
// restart.
func StockUploadLoop(ctx context.Context, cfg *config.Config, db *sql.DB) {
	settings := data.NewSettingsRepo(db)
	backoff := time.Duration(0)
	var holdUntil time.Time
	t := time.NewTimer(stockUploadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(stockUploadInterval)
		now := time.Now()
		if now.Before(holdUntil) {
			continue
		}
		if tier, _ := entitlement.CloudLink(ctx, settings, now); tier != "realtime" {
			backoff = 0
			continue
		}
		if uploadCloudStock(ctx, cfg, db) {
			if backoff == 0 {
				backoff = stockUploadBackoffStart
			} else {
				backoff = min(backoff*2, stockUploadBackoffMax)
			}
			holdUntil = now.Add(backoff)
			continue
		}
		backoff = 0
	}
}

// StartStockUpload runs StockUploadLoop until ctx ends, joined by app.Run's
// drain through wg.
func StartStockUpload(ctx context.Context, cfg *config.Config, db *sql.DB, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("cloudsync.stockUpload")
		defer wg.Done()
		StockUploadLoop(ctx, cfg, db)
	}()
}
