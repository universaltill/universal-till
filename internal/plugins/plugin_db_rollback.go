package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

// strictSemver is the only directory-name shape RollbackManager treats as a
// version snapshot: an optional "v", MAJOR.MINOR.PATCH, optional pre-release
// and build parts. validatePluginVersion is deliberately not used for this:
// it also accepts names like "notes" or "latest".
var strictSemver = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func parseStrictSemver(name string) bool { return strictSemver.MatchString(name) }

// prepareDataForRollback brings the plugin's own SQLite data to a state the
// rollback target can read (ADR-0121 §4, ut-docs#3157). A target without a
// db block touches nothing. A live database the target can read as it is (it
// has applied no migration the target lacks, checksums equal) is KEPT: the
// rows written since the upgrade are the shop's records and are never set
// aside without need. Only when the database is ahead of the target is the
// pre-upgrade copy taken when the target was upgraded away from restored
// (newer data is kept aside as .pre-upgrade/rolled-back-<ts>.db); with no
// such copy the rollback is refused. The returned undo reverses the data move
// and is called if the rollback's DB commit then fails; it is never nil on
// success.
func (rm *RollbackManager) prepareDataForRollback(ctx context.Context, pluginID, targetVersion, targetDir string, target *Manifest) (func() error, error) {
	noop := func() error { return nil }
	if target == nil || target.DB == nil {
		return noop, nil
	}
	dm := DataManager()
	if dm == nil {
		return noop, nil
	}
	ms, err := plugindb.LoadMigrations(os.DirFS(targetDir), target.DB.Migrations)
	if err != nil {
		return nil, fmt.Errorf("load target migrations: %w", err)
	}
	compatErr := dm.CheckCompatible(ctx, pluginID, ms)
	if compatErr == nil {
		return noop, nil
	}
	if !errors.Is(compatErr, plugindb.ErrMigrationBehind) && !errors.Is(compatErr, plugindb.ErrMigrationTampered) {
		return nil, compatErr
	}
	up, err := dm.RestorePreUpgrade(ctx, pluginID, targetVersion)
	if errors.Is(err, plugindb.ErrNoPreUpgrade) {
		return nil, compatErr
	}
	if err != nil {
		return nil, err
	}
	if up == nil || up.Undo == nil {
		return noop, nil
	}
	return up.Undo, nil
}
