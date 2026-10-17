package cloudsync

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/catalogpull"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
)

// catalogAuthWarned is set once a 401/403 from the catalogue feed has been
// logged at Warn; reset once the feed accepts the credential again.
var catalogAuthWarned atomic.Bool

// catalogReportWarned is set once a failed apply-failures report has been
// logged at Warn; reset once a report is accepted.
var catalogReportWarned atomic.Bool

// catalogReportRejectedWarned is set once a 400 from the apply-failures
// report has been logged at Warn; reset once a report is accepted.
var catalogReportRejectedWarned atomic.Bool

// pullCloudCatalog uploads the till's own catalogue edits (ADR-0162 §5,
// ut-docs#4464) and then runs the cloud catalogue pull (§4, ut-docs#4463;
// internal/catalogpull) when the cached entitlement block says this store's
// catalogue is cloud-mastered, and reports whether the catalogue changed.
// The caller has already checked this is the main till
// and that this tick's check-in POST reached the cloud. Errors are logged,
// never returned: the cursor stays and the next trigger retries, so a pull
// failure can never turn a successful check-in into a failed one.
func pullCloudCatalog(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) bool {
	if entitlement.CatalogMaster(ctx, data.NewSettingsRepo(db)) != entitlement.CatalogMasterCloud {
		return false
	}
	m := enroll.Effective(cfg).Marketplace
	opt := catalogpull.Options{
		ItemInLiveBasket:  hooks.CatalogItemInLiveBasket,
		TaxRateRefused:    hooks.CatalogTaxRateRefused,
		ItemPhotoReplaced: hooks.CatalogItemPhotoReplaced,
	}
	// Upload first, then pull (ADR-0162 §4/§5, ut-docs#4464): the till's
	// own edits reach the cloud's conflict test before the feed could bring
	// a newer cloud value over them (the pull also leaves a pending field
	// alone when the upload could not run).
	uploaded := pushCloudCatalog(ctx, db, catalogpull.NewHTTPPoster(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken), opt)
	fetch := catalogpull.NewHTTPFetcher(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken)
	res, err := catalogpull.Run(ctx, db, fetch, opt)
	if err != nil {
		logging.L().Warnf("cloudsync: catalogue pull stopped (kept what was applied; retried on the next check-in): %v", err)
	}
	switch {
	case res.StopStatus == http.StatusUnauthorized || res.StopStatus == http.StatusForbidden:
		// Every tick would answer the same until the device is re-enrolled:
		// warn once per process, then quietly.
		if catalogAuthWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: catalogue pull refused (%d %s): this till's cloud credential is not accepted; the till's copy stands until it is re-enrolled", res.StopStatus, res.Stopped)
		} else {
			logging.L().Infof("cloudsync: catalogue pull still refused (%d %s)", res.StopStatus, res.Stopped)
		}
	case res.Stopped != "":
		catalogAuthWarned.Store(false)
		logging.L().Infof("cloudsync: catalogue pull not served now (%s); the till's copy stands", res.Stopped)
	}
	if res.Stopped == "" && err == nil {
		catalogAuthWarned.Store(false) // accepted again: warn on the next refusal
		// ut-docs#4493: tell the cloud which rows the till refused, when
		// that set changed — only after a pull the feed served, so a
		// stopped or failing feed adds no extra request.
		scope := m.StoreID + "\n" + m.EndpointURL
		reportCatalogApplyFailures(ctx, db, scope, catalogpull.NewHTTPReporter(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken))
	}
	if res.Applied > 0 || res.Refused > 0 || res.Pruned > 0 {
		logging.L().Infof("cloudsync: catalogue pull: %d applied, %d refused, %d skipped, %d pruned", res.Applied, res.Refused, res.Skipped, res.Pruned)
	}
	return res.Changed || uploaded
}

// pushCloudCatalog uploads the outbox and reports whether an answer changed
// the local catalogue. Errors are logged, never returned: the outbox stays
// and the next trigger re-sends it.
func pushCloudCatalog(ctx context.Context, db *sql.DB, post catalogpull.Poster, opt catalogpull.Options) bool {
	res, err := catalogpull.Push(ctx, db, post, opt)
	if err != nil {
		logging.L().Warnf("cloudsync: catalogue upload stopped (kept the rest; re-sent on the next check-in): %v", err)
	}
	if res.Stopped != "" {
		logging.L().Infof("cloudsync: catalogue upload not accepted now (%d %s); %d changes wait", res.StopStatus, res.Stopped, res.Pending)
	}
	if res.Sent > 0 {
		logging.L().Infof("cloudsync: catalogue upload: %d sent, %d accepted, %d replaced by the cloud's row, %d re-minted, %d waiting", res.Sent, res.Accepted, res.Conflicts, res.Rejected, res.Pending)
	}
	return res.Changed
}

// reportCatalogApplyFailures sends the till's refused cloud rows
// (catalogpull.ReportFailures). Failures are logged, never returned: the
// stored fingerprint stays, so the next served pull retries.
func reportCatalogApplyFailures(ctx context.Context, db *sql.DB, scope string, post catalogpull.Reporter) {
	sent, err := catalogpull.ReportFailures(ctx, db, scope, post)
	var stop *catalogpull.ReportStopError
	var rej *catalogpull.ReportRejectedError
	switch {
	case errors.As(err, &rej):
		// The same body cannot succeed; ReportFailures holds it back until
		// the set changes. Warn once per process.
		if catalogReportRejectedWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: catalogue apply-failures report refused (%d %s); not re-sent until the set changes", rej.Status, rej.Code)
		} else {
			logging.L().Infof("cloudsync: catalogue apply-failures report refused again (%d %s)", rej.Status, rej.Code)
		}
	case errors.As(err, &stop):
		// The credential or the store's state; the pull's own refusal
		// handling warns about that.
		logging.L().Infof("cloudsync: catalogue apply-failures report not accepted (%d %s)", stop.Status, stop.Code)
	case err != nil:
		if catalogReportWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("cloudsync: catalogue apply-failures report failed (retried on the next check-in): %v", err)
		} else {
			logging.L().Infof("cloudsync: catalogue apply-failures report still failing: %v", err)
		}
	case sent:
		catalogReportWarned.Store(false)
		catalogReportRejectedWarned.Store(false)
		logging.L().Infof("cloudsync: catalogue apply-failures reported to the cloud")
	}
}
