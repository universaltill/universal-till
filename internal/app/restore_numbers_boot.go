package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// jumpNumbersAfterRestore moves receipt and Z numbers ahead when a backup
// restore left its marker file (ut-docs#5068): the backup lacks what the lost
// till issued after it, and those numbers must never be issued twice. The
// marker is a file, written before the restore's swap, so a crash in between
// still jumps on the next boot. On any error the file stays and the next boot
// retries; boot is never blocked.
func jumpNumbersAfterRestore(ctx context.Context, sqlDB *sql.DB, dbPath string, now time.Time, log bootLogger) {
	if !db.NumberJumpPending(dbPath) {
		return
	}
	jump, err := data.NewPOSRepo(sqlDB).JumpNumbersAfterRestore(ctx, now, db.NumberJumpToken(dbPath))
	if err != nil {
		log.Errorf("restore: jump receipt and Z numbers: %v", err)
		return
	}
	db.ClearNumberJumpPending(dbPath)
	if jump.AlreadyApplied {
		log.Infof("backup restored: number jump was already recorded")
		return
	}
	log.Infof("backup restored: receipts continue at %s, Z numbers at %d", jump.Marker.NextReceipt, jump.Marker.NextZ)
}
