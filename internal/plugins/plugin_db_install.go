package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/logging"
)

// errMigrationsNotCovered is returned when a plugin declares db.migrations
// but the SQL did not arrive inside a digest-verified artifact: running
// unsigned SQL against a shop's data is never acceptable.
var errMigrationsNotCovered = errors.New("plugin database migrations are not covered by a verified artifact digest")

// migrateDataForInstall applies the plugin's own-database migrations from the
// freshly extracted (already verified) code tree, in one transaction, before
// the new version is activated (ADR-0121 4, ut-docs#3157). It returns nil, nil
// when the manifest declares no db block (such a plugin never gets a DB file).
// The returned Upgrade's Undo must be called if any later install step fails.
func migrateDataForInstall(ctx context.Context, m *Manifest, codeDir, fromVersion string) (*plugindb.Upgrade, error) {
	return migrateDataWith(ctx, DataManager(), m, codeDir, fromVersion)
}

func migrateDataWith(ctx context.Context, mgr *plugindb.Manager, m *Manifest, codeDir, fromVersion string) (*plugindb.Upgrade, error) {
	if m == nil {
		return nil, nil
	}
	if err := validatePluginID(m.ID); err != nil {
		return nil, err
	}
	if err := restoreRemovedDataForInstall(mgr, m.ID, fromVersion); err != nil {
		return nil, fmt.Errorf("plugin %s: restore kept data: %w", m.ID, err)
	}
	if m.DB == nil {
		return nil, nil
	}
	if err := validateMigrationsDir(m.DB.Migrations); err != nil {
		return nil, err
	}
	if err := refuseSymlinkedMigrationsPath(codeDir, m.DB.Migrations); err != nil {
		return nil, err
	}
	migs, err := plugindb.LoadMigrations(os.DirFS(codeDir), m.DB.Migrations)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: load database migrations: %w", m.ID, err)
	}
	up, err := mgr.MigrateForInstall(ctx, m.ID, fromVersion, m.Version, plugindb.Options{
		QuotaBytes: int64(m.EffectiveLimits(runtime.GOOS).StorageMB) << 20,
		Migrations: migs,
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %s: database migration failed, previous version kept: %w", m.ID, err)
	}
	return up, nil
}

// restoreRemovedDataForInstall moves a plugin's soft-deleted data (database
// and blobs, ADR-0121 §8: "a reinstall restores it") back before a fresh
// install; fromVersion is "" only when the plugin is not installed. Live data
// is never overwritten (ErrExists) and nothing kept is not an error. It must
// run before the migration step, which would otherwise create an empty
// database and block the restore for good. If the install then fails the data
// simply stays live: nothing is deleted, the next install finds it.
func restoreRemovedDataForInstall(mgr *plugindb.Manager, id, fromVersion string) error {
	if fromVersion != "" {
		return nil
	}
	err := mgr.Restore(id)
	if err == nil {
		logging.L().Infof("[plugins] restored kept data of %s on reinstall", id)
		return nil
	}
	if errors.Is(err, plugindb.ErrNotRemoved) || errors.Is(err, plugindb.ErrExists) {
		return nil
	}
	return err
}

// refuseSymlinkedMigrationsPath fails if any component of the migrations
// directory inside codeDir is a symlink or not a directory.
func refuseSymlinkedMigrationsPath(codeDir, rel string) error {
	cur := codeDir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("db.migrations %q: %w", rel, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("db.migrations %q must be a real directory inside the plugin", rel)
		}
	}
	return nil
}

// undoDataMigration reverts a successful migration after a later install step
// failed; a nil upgrade (plugin without a db block) is a no-op.
func undoDataMigration(up *plugindb.Upgrade, pluginID string) {
	if up == nil || up.Undo == nil {
		return
	}
	if err := up.Undo(); err != nil {
		logging.L().Warnf("[plugins] undo database migration of %s: %v", pluginID, err)
	}
}

// requireDigestCoveredMigrations refuses a manifest that declares a database
// unless covered is true (the archive hash matched a signed value).
func requireDigestCoveredMigrations(m *Manifest, covered bool) error {
	if m != nil && m.DB != nil && !covered {
		return errMigrationsNotCovered
	}
	return nil
}
