package cloudsync

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/config"
)

// Tick runs one full sync round for tests; Start drives tick on the loop.
// Test-only since #2824 (the loop needs tick's contacted flag), so the
// whole-program deadcode gate doesn't see an unreachable exported func.
func Tick(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) error {
	_, err := tick(ctx, cfg, db, hooks)
	return err
}
