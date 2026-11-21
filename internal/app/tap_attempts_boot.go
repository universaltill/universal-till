package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// markOrphanedTapAttempts: ut-docs#6795 — a tap attempt still pending at boot
// lost its collect with the last process (basket, lock and attempt id are
// memory only): it becomes unknown/till_restarted, a payment needing
// attention, and the tap gate refuses new taps on its provider until a
// manager settles it. Best effort: an error is logged and boot goes on; no
// broker call (an offline boot is normal). Must run before anything starts a
// collect, so it can never flip a row this process is collecting.
func markOrphanedTapAttempts(ctx context.Context, sqlDB *sql.DB, now time.Time, log bootLogger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := data.NewPaymentAttemptRepo(sqlDB).MarkPendingUnknownAtBoot(ctx, now)
	if err != nil {
		log.Errorf("tap: mark orphaned card payments: %v", err)
		return
	}
	if n > 0 {
		log.Infof("tap: %d card payment(s) left open by the last run need a manager's attention (ut-docs#6795)", n)
	}
}
