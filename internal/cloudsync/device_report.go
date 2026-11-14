package cloudsync

import (
	"context"
	"crypto/sha256"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
)

// deviceReportTypes are the directive types that change this till's own
// device report (the /v1/stores/sync body): after one of them applies, the
// same tick re-sends the report (pushDeviceReportIfChanged; its hash gate
// keeps an unchanged report free). ut-docs#5614.
var deviceReportTypes = map[string]bool{
	"set_till_setting":        true, // till_settings
	"set_setting":             true, // theme, and till_settings keys it may touch
	"set_quick_button_layout": true, // quick_buttons
	"rename_till":             true, // name
}

// appliedKinds is what this tick's applied directives changed, for the
// same-tick re-reports (contract §3.6).
type appliedKinds struct{ catalog, deviceReport bool }

// pushDeviceReportIfChanged re-sends this till's device report after a
// directive changed it, so the cloud holds the applied state in this tick,
// not the next (ut-docs#5614; contract §3.6). postedSum is the state hash
// of the report this tick already POSTed (postedOK false: it could not be
// hashed). An unchanged report sends nothing. The answer's directives are
// dropped: anything pending stays pending on the cloud and is served again
// on the next check-in (a directive claimed by this answer keeps its
// delivered_at — it only means a cancel is refused until then).
func pushDeviceReportIfChanged(ctx context.Context, cfg *config.Config, settings *data.SettingsRepo, hooks Hooks, postedSum [sha256.Size]byte, postedOK bool) error {
	req := buildSyncRequest(ctx, cfg, settings, hooks)
	sum, err := stateHash(req.devices)
	if err == nil && postedOK && sum == postedSum {
		return nil
	}
	if _, err := pushSync(ctx, cfg, settings, req); err != nil {
		return err
	}
	checkinPlan{post: true, sum: sum}.done(cfg) // no version: records time + hash only
	return nil
}
