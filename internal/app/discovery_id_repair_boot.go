package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// repairInheritedDiscoveryIDOnPrimary re-mints the LAN discovery id of a
// till that was promoted before ut-docs#5893 and still holds its former main
// till's id (ut-docs#5897, ADR-0033 "two till ids"). Runs once per boot on a
// main/standalone till, before the advertiser and HTTP server start; the
// repo method skips replicas and defers (writing nothing, retried next boot)
// while any till holds a credential for this till or a pairing is in flight.
// Best-effort: a failure is logged and never blocks startup.
func repairInheritedDiscoveryIDOnPrimary(ctx context.Context, sqlDB *sql.DB, log bootLogger) {
	mirrorMain := ""
	if st, err := data.NewSalesMirrorRepo(sqlDB).State(ctx); err != nil {
		log.Errorf("repair discovery id: read sales-copy state: %v", err)
	} else {
		mirrorMain = st.MainTillID
	}
	res, err := data.NewSettingsRepo(sqlDB).RepairInheritedDiscoveryID(ctx, mirrorMain, time.Now())
	if err != nil {
		log.Errorf("repair discovery id: %v", err)
		return
	}
	switch {
	case res.Repaired:
		log.Infof("re-minted this till's LAN discovery id %s -> %s (inherited from its former main till, signal %s; ut-docs#5897)", res.OldID, res.NewID, res.Signal)
	case res.Deferred != "":
		log.Infof("LAN discovery id repair deferred (%s); will retry on the next boot (ut-docs#5897)", res.Deferred)
	}
}
