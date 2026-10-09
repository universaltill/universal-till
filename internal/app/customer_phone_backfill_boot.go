package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// startCustomerPhoneE164Backfill computes customers.phone_e164 for every
// customer still without one (ut-docs#3200, ADR-0131 §3), in a background
// goroutine registered on wg so shutdown waits for it. It runs on EVERY
// till, main and replica alike: the column is derived from the till's
// rows and the shop country, and the caller-ID lookup reads it locally. A
// replica also receives the main's values through the admin pull, and its
// country changes arrive that way too — so a replica re-runs at its next
// boot, while a main re-runs on every country save (ut-docs#3992).
// Never on a request path; chunked (data.DefaultPhoneE164BackfillBatch
// rows per short write transaction) so a sale never waits long on the
// lock; stops between chunks when ctx ends. A failure is logged and never
// stops the till — the lookup normalises rows the back-fill missed itself.
func startCustomerPhoneE164Backfill(ctx context.Context, wg *sync.WaitGroup, sqlDB *sql.DB, log bootLogger) {
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("app.customerPhoneE164Backfill")
		defer wg.Done()
		n, err := data.NewPOSRepo(sqlDB).BackfillCustomerPhoneE164(ctx, data.DefaultPhoneE164BackfillBatch)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, data.ErrPhoneE164RegionChanged) {
				return // shutting down, or a newer run owns the new country; the next boot carries on
			}
			log.Errorf("back-fill customers phone_e164: %v", err)
			return
		}
		if n > 0 {
			log.Infof("normalised the phone number of %d customer(s) for caller ID (ut-docs#3200)", n)
		}
	}()
}
