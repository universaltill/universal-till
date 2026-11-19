package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/universaltill/universal-till/internal/data"
)

// ErrRevokedRelease is returned when a release the marketplace revoked is
// enabled, persisted (install/update/import) or rolled back to
// (ut-docs#5658). Local read only; the feed is recorded by SyncRevocations.
var ErrRevokedRelease = errors.New("plugin release revoked by the marketplace")

// CheckNotRevoked refuses a revoked (id, version). A DB error fails closed.
// A nil tx reads through the repo's own handle.
func CheckNotRevoked(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, id, version string) error {
	rr, revoked, err := repo.RevokedRelease(ctx, tx, id, version)
	if err != nil {
		return fmt.Errorf("check revoked release: %w", err)
	}
	if revoked {
		return fmt.Errorf("%s v%s: %w (reason: %s)", id, version, ErrRevokedRelease, rr.Reason)
	}
	return nil
}
