package pages

import (
	"context"
	"errors"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// customerPhoneBackfillTimeout bounds one country-change back-fill run.
const customerPhoneBackfillTimeout = 2 * time.Minute

// startCustomerPhoneE164BackfillForCountryChange re-normalises
// customers.phone_e164 for the shop country that was just saved
// (ut-docs#3992, ut-docs#3200, ADR-0131 §3). The boot back-fill only sees
// the country at boot; every writer of store.country (and the setup wizard,
// which runs after first boot) calls this right after persisting it. Runs
// in a background goroutine on d.AsyncWork, never on the request path;
// best-effort: a failure is logged and never fails the save — rows left
// NULL are normalised by the caller lookup itself and by the next run. A
// run overtaken by a newer country change stops quietly
// (data.ErrPhoneE164RegionChanged).
func startCustomerPhoneE164BackfillForCountryChange(d *common.Deps) {
	if d == nil || d.Db == nil {
		return
	}
	d.AsyncWork.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.customerPhoneE164Backfill")
		defer d.AsyncWork.Done()
		ctx, cancel := context.WithTimeout(context.Background(), customerPhoneBackfillTimeout)
		defer cancel()
		n, err := data.NewPOSRepo(d.Db).BackfillCustomerPhoneE164(ctx, data.DefaultPhoneE164BackfillBatch)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, data.ErrPhoneE164RegionChanged) {
				return
			}
			logging.L().Errorf("back-fill customers phone_e164 after country change: %v", err)
			return
		}
		if n > 0 {
			logging.L().Infof("re-normalised the phone number of %d customer(s) for the new shop country (ut-docs#3992)", n)
		}
	}()
}
