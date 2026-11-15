package app

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/data"
)

// rotateStockStreamAfterRestore starts a new stock-upload stream and owes
// every inventory row again, once a staged backup restore has been applied
// (ut-docs#4763). A restored database carries the stream id, marks and
// sequence of the backup's day; without this the cloud's mirror would show the
// pre-restore levels until each row happened to change (the cloud only learns
// of the restart from a later sale's stale answer). Harmless on a till that
// does not upload stock: the marks just wait. Best-effort, never blocks boot.
func rotateStockStreamAfterRestore(ctx context.Context, sqlDB *sql.DB, log bootLogger) {
	if _, err := data.NewStockUploadRepo(sqlDB).RotateStream(ctx); err != nil {
		log.Errorf("stock upload: new stream after a restore: %v", err)
		return
	}
	log.Infof("stock upload: backup restored; every stock level will be re-sent to the cloud")
}
