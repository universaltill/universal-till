package cloudsync

import (
	"context"
	"database/sql"
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

// pullCloudCatalog runs the cloud catalogue pull (ADR-0162 §4,
// ut-docs#4463; internal/catalogpull) when the cached entitlement block
// says this store's catalogue is cloud-mastered, and reports whether the
// catalogue changed. The caller has already checked this is the main till
// and that this tick's check-in POST reached the cloud. Errors are logged,
// never returned: the cursor stays and the next trigger retries, so a pull
// failure can never turn a successful check-in into a failed one.
func pullCloudCatalog(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) bool {
	if entitlement.CatalogMaster(ctx, data.NewSettingsRepo(db)) != entitlement.CatalogMasterCloud {
		return false
	}
	m := enroll.Effective(cfg).Marketplace
	fetch := catalogpull.NewHTTPFetcher(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken)
	res, err := catalogpull.Run(ctx, db, fetch, catalogpull.Options{
		ItemInLiveBasket:  hooks.CatalogItemInLiveBasket,
		TaxRateRefused:    hooks.CatalogTaxRateRefused,
		ItemPhotoReplaced: hooks.CatalogItemPhotoReplaced,
	})
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
	}
	if res.Applied > 0 || res.Refused > 0 || res.Pruned > 0 {
		logging.L().Infof("cloudsync: catalogue pull: %d applied, %d refused, %d skipped, %d pruned", res.Applied, res.Refused, res.Skipped, res.Pruned)
	}
	return res.Changed
}
