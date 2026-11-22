package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
)

// bootRestoreChecks runs the restore detection and then the number jump, in
// that order, so a jump the check owes lands on the same boot (ut-docs#6294).
// The jump is owed by every applied backup restore (a file, so a crash after
// the swap still jumps) and by a data folder restored onto a new install, and
// is a no-op on every other boot (ut-docs#5068). Call after the settings store
// is ready and before settings.LoadRuntimeConfig and anything that sells.
// The journal lookup keeps a restored replica's sync.restored_since after the
// original's newest entry (ut-docs#5185).
func bootRestoreChecks(ctx context.Context, cfg *config.Config, sqlDB *sql.DB, kv enroll.Settings, now time.Time, log bootLogger) {
	repo := data.NewPOSRepo(sqlDB)
	// A carried marker whose token this DB already applied is spent; a lookup
	// error keeps the marker (a pending restore must still win).
	applied := func(token string) bool {
		done, err := repo.AppliedRestoreJumpToken(ctx)
		return err == nil && token != "" && done == token
	}
	enroll.CheckRestoredInstall(ctx, cfg, kv, repo.NewestLocalJournalEntryAt,
		func(token string) error { return db.OweNumberJump(cfg.DBPath, token, applied) })
	jumpNumbersAfterRestore(ctx, sqlDB, cfg.DBPath, now, log)
}

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
