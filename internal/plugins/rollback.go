package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ErrVersionSourceMissing is returned by StoreVersion when a non-empty,
// non-versionDir sourcePath doesn't exist or isn't a directory (ut-docs#2799)
// — the live per-version install dir a snapshot would be taken from can go
// missing (disk cleanup, a partial delete, anything short of the happy path)
// without that meaning there was never a good version to protect. Callers
// distinguish this from "no rollback target at all" via HasVersion.
var ErrVersionSourceMissing = errors.New("plugin rollback: version source directory missing")

// RollbackManager handles plugin version rollback operations
type RollbackManager struct {
	db            *sql.DB
	pluginBaseDir string
	maxVersions   int // maximum versions to keep
}

// NewRollbackManager creates a new rollback manager
func NewRollbackManager(db *sql.DB, pluginBaseDir string) *RollbackManager {
	return &RollbackManager{
		db:            db,
		pluginBaseDir: pluginBaseDir,
		maxVersions:   3, // keep last 3 versions
	}
}

// VersionInfo contains information about a plugin version. Path is
// deliberately excluded from JSON (json:"-") — it's a local on-disk
// snapshot location, never client-facing data (ut-docs#2239).
type VersionInfo struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
	Path        string    `json:"-"`
	IsActive    bool      `json:"is_active"`
}

// GetVersionHistory retrieves version history for a plugin: every snapshot
// under pluginBaseDir/pluginID/versions/ (the tree StoreVersion writes and
// Rollback reads), flagged with which one is currently active.
//
// Its only caller used to be the sync path's own install-status record
// (ut-docs#1566) — Rollback itself was live via POST /api/plugins/{id}/rollback
// but nothing under web/ui could discover a target version to roll back to.
// GET /api/plugins/{id}/versions (internal/pages/plugin_api.go,
// handleListPluginVersions) now wires this in as that discovery step, and
// web/ui/pages/plugins.html's "Versions" control surfaces it to an operator
// (ut-docs#2239).
func (rm *RollbackManager) GetVersionHistory(ctx context.Context, pluginID string) ([]VersionInfo, error) {
	// The id reaches filepath.Join below; the HTTP handler validates it too,
	// this is the defence-in-depth copy (ut-docs#2891 review M2).
	if err := validatePluginID(pluginID); err != nil {
		return nil, err
	}
	// Check plugin directory
	pluginDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions")
	repo := data.NewPluginRepo(rm.db)

	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return []VersionInfo{}, nil
	}

	// Read version directories
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin versions: %w", err)
	}

	// Get current version from database
	currentVersion, _, err := repo.GetActivePluginVersion(ctx, pluginID)
	if err != nil {
		return nil, fmt.Errorf("failed to get current version: %w", err)
	}

	var versions []VersionInfo
	for _, entry := range entries {
		if entry.IsDir() {
			// A `.store-*` crash leftover from an interrupted StoreVersion
			// (ut-docs#3016) is not a version snapshot — never list it as
			// one. cleanupOldVersions applies the same filter.
			if isTempVersionDirName(entry.Name()) {
				continue
			}
			versionPath := filepath.Join(pluginDir, entry.Name())
			info, err := os.Stat(versionPath)
			if err != nil {
				continue
			}

			versions = append(versions, VersionInfo{
				Version:     entry.Name(),
				InstalledAt: info.ModTime(),
				Path:        versionPath,
				IsActive:    entry.Name() == currentVersion,
			})
		}
	}

	return versions, nil
}

// pluginLocks serializes StoreVersion, Rollback (ut-docs#3035 gap 1),
// UninstallPluginTree and RemoveVersionDir (ut-docs#3082), and the install
// paths — Importer.Import and installBundleFile (ut-docs#3273) — and the
// core-shipped builtin salon layout install (ut-docs#3278) per plugin
// tree. sync.Mutex is not reentrant: none of these may call another.
// Callers build a fresh RollbackManager per call (internal/pages), so the
// lock can't live on the manager: it is keyed by the cleaned plugin base
// dir plus the plugin id. Entries are never removed —
// one small mutex per plugin id ever touched, a bounded set on a till.
var pluginLocks sync.Map // string -> *sync.Mutex

func (rm *RollbackManager) lockPlugin(pluginID string) func() {
	return lockPluginTree(rm.pluginBaseDir, pluginID)
}

// lockPluginTree takes the pluginLocks mutex for pluginBaseDir/pluginID and
// returns its unlock. UninstallPluginTree and RemoveVersionDir (ut-docs#3082)
// and the install paths (ut-docs#3273) take it too.
func lockPluginTree(pluginBaseDir, pluginID string) func() {
	key := filepath.Clean(pluginBaseDir) + "\x00" + pluginID
	mu, _ := pluginLocks.LoadOrStore(key, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// LockPluginTree is lockPluginTree for callers outside this package — the
// core-shipped builtin layout install (ut-docs#3278). Not reentrant: never
// call another locking function of this package (UninstallPluginTree,
// RemoveVersionDir, Rollback, StoreVersion, Import, installBundleFile) while
// holding it.
func LockPluginTree(pluginBaseDir, pluginID string) func() {
	return lockPluginTree(pluginBaseDir, pluginID)
}

// Rollback rolls back a plugin to a previous version, snapshotting the
// version it leaves so the shop can roll forward again.
func (rm *RollbackManager) Rollback(ctx context.Context, pluginID, targetVersion, actorID string) error {
	return rm.rollback(ctx, pluginID, targetVersion, actorID, true)
}

// RollbackDiscardingCurrent is Rollback for a caller about to delete the
// version it is leaving — the cloudsync auto-rollback after a pinned install
// came back with the wrong version (ut-docs#3035 gap 3). It does not snapshot
// that version: a known-bad snapshot would take one of the maxVersions slots
// and evict a good one.
func (rm *RollbackManager) RollbackDiscardingCurrent(ctx context.Context, pluginID, targetVersion, actorID string) error {
	return rm.rollback(ctx, pluginID, targetVersion, actorID, false)
}

func (rm *RollbackManager) rollback(ctx context.Context, pluginID, targetVersion, actorID string, snapshotLeft bool) error {
	log := logging.L()
	repo := data.NewPluginRepo(rm.db)

	// Both reach filepath.Join and targetVersion is then persisted as the
	// active version — a "../../<other plugin>/<v>" would activate another
	// plugin's tree under this id (ut-docs#2891 review M2).
	if err := validatePluginID(pluginID); err != nil {
		return err
	}
	if err := validatePluginVersion(targetVersion); err != nil {
		return err
	}

	// Held until return: no StoreVersion for this plugin (update, sync) can
	// evict the target between the stat below and the manifest open, or
	// interleave with the post-commit snapshot (ut-docs#3035 gap 1).
	defer rm.lockPlugin(pluginID)()

	// Verify target version exists
	targetPath := filepath.Join(rm.pluginBaseDir, pluginID, "versions", targetVersion)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return fmt.Errorf("target version %s not found for plugin %s", targetVersion, pluginID)
	}
	if rollbackAfterTargetStat != nil {
		rollbackAfterTargetStat()
	}

	// Decide now whether the live per-version install dir needs restoring
	// from the snapshot (ut-docs#2799 review M1). Every consumer of an
	// installed plugin's files — wasm_runtime.go, plugins.go's locale
	// loader, plugin_page.go, themes.go, plugin_icons.go — reads from
	// pluginBaseDir/id/version/, never from versions/version/. The
	// HasVersion branch in cloudInstallPluginVersion means Rollback can be
	// reached with a real target whose live dir has gone missing (that's
	// exactly what StoreVersion's ErrVersionSourceMissing + HasVersion
	// combination is for) — without eventually restoring, such a rollback
	// would mark the plugin "installed" in the DB while leaving it with
	// zero files on disk. If the live dir already exists, it's left alone
	// either way, so the stat here — read-only, and a non-NotExist error is
	// still a real problem worth failing on immediately — is all that
	// happens now.
	//
	// The actual restore is deferred until immediately before tx.Commit()
	// (ut-docs#3016), well after this point: it used to run right here,
	// before the manifest was even opened, so a rollback later refused by
	// ParseManifest or any validate* check (a bad manifest, a colliding
	// payment/page/route key, an exclusive-preset conflict, ...) still left
	// a freshly-restored live dir behind as an orphan — disk state for a
	// rollback that never took effect. See the restore call further down
	// for the failure handling that placement requires.
	liveTargetDir := filepath.Join(rm.pluginBaseDir, pluginID, targetVersion)
	needRestore := false
	if _, err := os.Stat(liveTargetDir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check live install dir for %s: %w", targetVersion, err)
		}
		needRestore = true
	}

	// Get current version
	currentVersion, ok, err := repo.GetActivePluginVersion(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}
	if !ok {
		return fmt.Errorf("plugin %s has no active version", pluginID)
	}

	if currentVersion == targetVersion {
		return fmt.Errorf("plugin is already at version %s", targetVersion)
	}

	// Load manifest from target version
	manifestPath := filepath.Join(targetPath, "manifest.json")
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to open manifest: %w", err)
	}
	defer manifestFile.Close()

	manifest, err := ParseManifest(manifestFile)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Begin transaction
	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// A legacy on-disk manifest can carry payment keys that predate
	// ADR-0031's validation — rolling back must not restore a colliding
	// tender key that a fresh install would reject.
	if err := validatePaymentEntryKeys(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for page-entry keys (ut-docs#472) — a legacy on-disk
	// manifest can carry a page key that predates this validation and now
	// collides with another currently-installed plugin.
	if err := validatePageEntryKeys(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for page-entry routes (ut-docs#499) — a legacy
	// on-disk manifest can carry a route that predates this validation and
	// now collides with another currently-installed plugin's page entry.
	if err := validatePageEntryRoutes(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for a page entry's own declared icon_name (ut-docs#1734)
	// — a legacy on-disk manifest can carry an icon_name that predates this
	// validation, or one that was valid at the time but has since been
	// retired from the closed set.
	if err := validatePageEntryIcon(manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for a role:"preset" layout entry (ADR-0106 C) — the
	// rollback target may have been a preset while the current version is
	// not, and another plugin's preset may have become active since; a
	// fresh install would refuse a second active preset, so must this.
	if err := validateLayoutPresetExclusivity(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for layout amendments (ADR-0088) — a rolled-back
	// manifest may hide a protected key or restructure a key another
	// plugin has since taken; a fresh install would refuse it, so must this.
	if err := validateLayoutEntries(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Update plugins table
	if err := repo.UpdatePluginVersion(ctx, tx, pluginID, targetVersion, manifest.Entrypoint, "installed"); err != nil {
		return err
	}

	// Re-insert entries from rolled-back manifest (delete+bulk insert)
	var entryRows []data.PluginEntryRow
	for _, e := range manifest.Entries {
		configJSON := entryConfigJSON(e)
		entryRows = append(entryRows, data.PluginEntryRow{
			ID:            uuid.NewString(),
			Type:          e.Type,
			Key:           e.Key,
			Label:         e.Label,
			IconPath:      entryIconColumn(e),
			SortOrder:     e.SortOrder,
			ParentPageKey: e.ParentPageKey,
			MenuGroup:     e.MenuGroup,
			Route:         e.Route,
			TargetAction:  e.TargetAction,
			TriggerEvent:  e.TriggerEvent,
			ConfigJSON:    configJSON,
		})
	}
	if err := repo.ReplacePluginEntries(ctx, tx, pluginID, entryRows); err != nil {
		return err
	}

	// Create audit log entry
	auditData := map[string]interface{}{
		"plugin_id":       pluginID,
		"from_version":    currentVersion,
		"to_version":      targetVersion,
		"rollback_reason": "user_requested",
	}

	if actorID == "" {
		actorID = "system"
	}

	if err := repo.InsertAudit(ctx, tx, "plugin_rollback", pluginID, auditData, time.Now()); err != nil {
		return err
	}

	// Restore the live per-version install dir from the snapshot now, only
	// once every validator above and the DB writes just above have already
	// succeeded (ut-docs#3016; see needRestore's computation for why this
	// moved here). If this fails, the deferred tx.Rollback() above undoes
	// the DB writes, so nothing is left half-applied.
	if needRestore {
		if err := restoreLiveDirFromSnapshot(targetPath, liveTargetDir); err != nil {
			return fmt.Errorf("failed to restore live install dir for %s from snapshot: %w", targetVersion, err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		if needRestore {
			// The DB half didn't take, so don't leave the just-restored live
			// dir behind either — best-effort, matching every other cleanup
			// in this file (ut-docs#3016).
			_ = os.RemoveAll(liveTargetDir)
		}
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Snapshot the version we're leaving, same as an update does (ut-docs#2239
	// review) — without this, a rollback silently sheds the ability to roll
	// forward again: the version being left was never necessarily stored
	// (e.g. it was itself the very first install, never previously rolled
	// away from), so if this call is skipped a shop that rolls back and then
	// decides the earlier version was wrong too has nowhere to go back to.
	//
	// Deliberately AFTER tx.Commit() has already succeeded (ut-docs#3032): it
	// used to run before the target manifest was even opened, which meant its
	// cleanupOldVersions side effect (evict the oldest of maxVersions
	// snapshots by mtime) could evict the rollback TARGET itself — a
	// rollback to the oldest snapshot on disk failed with "failed to open
	// manifest: .../versions/<target>/manifest.json: no such file or
	// directory" even though the target existed when Rollback was called.
	// It also meant a rollback later refused by any validate* check still
	// mutated versions/ on disk for a rollback that never took effect. The
	// currentVersion's live per-version dir (pluginBaseDir/id/currentVersion)
	// is never deleted by Rollback, so it's still there to snapshot from
	// here, after commit. cleanupOldVersions is passed targetVersion as a
	// must-keep name so this snapshot pass can never evict the version the
	// rollback just landed on. Only attempt it when the live per-version
	// install directory actually exists — StoreVersion unconditionally
	// clears any existing snapshot before copying, so calling it against a
	// missing source would destroy an already-good prior snapshot instead of
	// leaving it alone. Warn-only on failure either way, same as the update
	// handler's own StoreVersion call — a snapshot failure must never
	// retroactively fail a rollback that has already committed.
	currentSourcePath := filepath.Join(rm.pluginBaseDir, pluginID, currentVersion)
	if !snapshotLeft {
		log.Infof("[Rollback] Not snapshotting %s of plugin %s: the caller discards it", currentVersion, pluginID)
	} else if _, statErr := os.Stat(currentSourcePath); statErr == nil {
		if err := rm.storeVersion(pluginID, currentVersion, currentSourcePath, targetVersion); err != nil {
			log.Warnf("[Rollback] Failed to store version %s for plugin %s after rolling back to %s: %v", currentVersion, pluginID, targetVersion, err)
		}
	}

	log.Infof("[Rollback] Plugin %s rolled back from %s to %s", pluginID, currentVersion, targetVersion)
	return nil
}

// rollbackAfterTargetStat is a test hook, nil in production: it runs inside
// Rollback right after the target snapshot's os.Stat, the window a concurrent
// StoreVersion used to evict the target in (ut-docs#3035 gap 1).
var rollbackAfterTargetStat func()

// StoreVersion saves a plugin version for potential rollback: snapshots the
// live per-version install directory (pluginBaseDir/pluginID/version/, the
// layout installer_marketplace.go's installBundleFile leaves behind) into
// this manager's own versions/ tree (pluginBaseDir/pluginID/versions/
// version/), which Rollback reads from later. sourcePath == "" keeps the
// pre-ut-docs#495 no-op-copy behavior (directory created, nothing to copy —
// some callers store a version marker with no known source location yet).
//
// Delegates to storeVersion with no must-keep names — this public signature
// must not change, since internal/pages calls it directly (ut-docs#3032).
//
// Serialized with Rollback per plugin (ut-docs#3035 gap 1).
func (rm *RollbackManager) StoreVersion(pluginID, version, sourcePath string) error {
	// Validate before locking, so a bad id never gets a lock entry.
	if err := validatePluginID(pluginID); err != nil {
		return err
	}
	defer rm.lockPlugin(pluginID)()
	return rm.storeVersion(pluginID, version, sourcePath)
}

// storeVersion is StoreVersion's implementation (caller holds the plugin
// lock), plus a must-keep set passed
// straight through to cleanupOldVersions (ut-docs#3032) — Rollback's
// post-commit "snapshot the version we're leaving" call uses this to protect
// the rollback target from the eviction that snapshot write can trigger.
func (rm *RollbackManager) storeVersion(pluginID, version, sourcePath string, keep ...string) error {
	log := logging.L()

	// StoreVersion RemoveAll()s versionDir before copying — never let an id
	// or version steer that outside the plugin tree (ut-docs#2891 M2).
	if err := validatePluginID(pluginID); err != nil {
		return err
	}
	if err := validatePluginVersion(version); err != nil {
		return err
	}
	versionDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions", version)

	if sourcePath != "" && filepath.Clean(sourcePath) != filepath.Clean(versionDir) {
		// Check the source exists and is a directory BEFORE touching
		// anything below (ut-docs#2799). The old code went straight to
		// os.RemoveAll(versionDir) — clearing out any existing, good
		// snapshot — and only then discovered the copy had nothing to read
		// from, leaving neither the old snapshot nor a new one. Rollback()
		// above already guards its own post-commit storeVersion call with
		// exactly this os.Stat, for exactly this reason; this makes every
		// caller safe, not just that one.
		//
		// Only "doesn't exist" or "exists but isn't a directory" maps to
		// the sentinel (ut-docs#2799 review M-minor 3) — any other os.Stat
		// error (permissions, a malformed path, ...) is a real problem, so
		// it stays a plain wrapped error and keeps callers' Warn logging.
		srcInfo, statErr := os.Stat(sourcePath)
		switch {
		case statErr == nil && srcInfo.IsDir():
			// Source is good; fall through to the copy below.
		case statErr == nil, errors.Is(statErr, fs.ErrNotExist):
			return fmt.Errorf("plugin %s version %s: source %s: %w", pluginID, version, sourcePath, ErrVersionSourceMissing)
		default:
			return fmt.Errorf("plugin %s version %s: failed to check source %s: %w", pluginID, version, sourcePath, statErr)
		}
		// Copy into a temp sibling under versions/ first and only swap it
		// in on success (ut-docs#2799 review M-minor 2). The old code
		// RemoveAll'd versionDir BEFORE copying, so any copy failure
		// (a symlinked source root, an unreadable file, a full disk, ...)
		// destroyed an already-good existing snapshot and left nothing in
		// its place. An interrupted previous StoreVersion call (partial
		// copy) must never be trusted as complete either way, so the old
		// versionDir is still cleared — just only once the replacement is
		// known-good.
		versionsParent := filepath.Join(rm.pluginBaseDir, pluginID, "versions")
		if err := os.MkdirAll(versionsParent, 0755); err != nil {
			return fmt.Errorf("failed to create versions directory: %w", err)
		}
		tmpDir, err := os.MkdirTemp(versionsParent, ".store-*")
		if err != nil {
			return fmt.Errorf("failed to create temp version directory: %w", err)
		}
		if err := copyVersionFiles(sourcePath, tmpDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return fmt.Errorf("failed to copy plugin files from %s: %w", sourcePath, err)
		}
		if err := os.RemoveAll(versionDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return fmt.Errorf("failed to clear stale version directory: %w", err)
		}
		if err := os.Rename(tmpDir, versionDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return fmt.Errorf("failed to move new version directory into place: %w", err)
		}
	} else if err := os.MkdirAll(versionDir, 0755); err != nil {
		return fmt.Errorf("failed to create version directory: %w", err)
	}

	log.Infof("[Rollback] Stored version %s for plugin %s", version, pluginID)

	// Clean up old versions
	if err := rm.cleanupOldVersions(pluginID, keep...); err != nil {
		log.Warnf("[Rollback] Failed to cleanup old versions: %v", err)
	}

	return nil
}

// HasVersion reports whether a rollback snapshot for pluginID/version
// already exists under this manager's versions/ tree — the same directory
// StoreVersion writes into and Rollback reads from. Callers use it to tell
// "StoreVersion just failed to refresh the snapshot" apart from "there was
// never a snapshot to roll back to" (ut-docs#2799): a live per-version
// install dir can go missing without the snapshot itself being lost.
//
// Requires versions/<version>/manifest.json specifically, as a regular
// file, not just the directory (ut-docs#2799 review M-minor 4) — a
// crash-partial StoreVersion (interrupted mid-copy) or an empty dir left by
// the sourcePath == "" marker-only path is not a real rollback target;
// Rollback itself would fail to open that manifest.
func (rm *RollbackManager) HasVersion(pluginID, version string) bool {
	if err := validatePluginID(pluginID); err != nil {
		return false
	}
	if err := validatePluginVersion(version); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(rm.pluginBaseDir, pluginID, "versions", version, "manifest.json"))
	return err == nil && info.Mode().IsRegular()
}

// isTempVersionDirName reports whether name is one of the temp working dirs
// StoreVersion (".store-*") or restoreLiveDirFromSnapshot (".restore-*")
// create via os.MkdirTemp — a real version or snapshot directory name never
// starts with a dot. GetVersionHistory and cleanupOldVersions both skip
// these: a crash between MkdirTemp and the final Rename leaves one behind,
// and it must never be listed as a version or counted toward maxVersions
// (ut-docs#3016).
func isTempVersionDirName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// staleTempDirAge is the ModTime age sweepStaleTempDirs requires before it
// will remove a leftover temp dir (ut-docs#3016). An hour comfortably
// exceeds how long a real StoreVersion copy or restoreLiveDirFromSnapshot
// takes, so anything still that old is a crash leftover, not a call still
// in flight.
const staleTempDirAge = time.Hour

// sweepStaleTempDirs best-effort removes entries directly under dir whose
// name starts with prefix and whose ModTime is older than olderThan — the
// `.store-*` (StoreVersion) and `.restore-*` (restoreLiveDirFromSnapshot)
// temp dirs a crash between os.MkdirTemp and the matching os.Rename leaves
// behind (ut-docs#3016). Only ever touches entries under dir itself whose
// name starts with the exact prefix; a failure to list dir or remove an
// entry is ignored — sweeping stale leftovers must never fail the call it's
// cleaning up after.
func sweepStaleTempDirs(dir, prefix string, olderThan time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// restoreLiveDirFromSnapshot recreates the live per-version install dir at
// liveDir by copying snapshotDir's contents into a temp sibling directory
// and atomically renaming it into place (ut-docs#2799 review M1) — never
// copying (or partially copying) straight into liveDir itself, which would
// leave a half-restored directory behind if the copy failed partway through.
func restoreLiveDirFromSnapshot(snapshotDir, liveDir string) error {
	parent := filepath.Dir(liveDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("failed to create plugin directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, ".restore-*")
	if err != nil {
		return fmt.Errorf("failed to create temp restore directory: %w", err)
	}
	if err := copyVersionFiles(snapshotDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to copy snapshot files: %w", err)
	}
	if err := os.Rename(tmpDir, liveDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to move restored files into place: %w", err)
	}
	return nil
}

// copyVersionFiles recursively copies sourceDir's tree into destDir,
// preserving relative structure and regular-file permissions.
func copyVersionFiles(sourceDir, destDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(sourceDir, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(destDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// cleanupOldVersions removes old plugin versions, keeping only maxVersions.
// Any name in keep is never deleted, regardless of age or how many real
// snapshots exist — Rollback's post-commit snapshot pass (ut-docs#3032)
// passes the rollback target here so its own eviction can never remove the
// version the rollback just landed on. Non-kept candidates are still
// deleted, oldest first, until the real-snapshot count is at most
// maxVersions or there are no more non-kept candidates left to delete (a
// keep set larger than maxVersions is honored in full, not truncated).
//
// The plugin's installed version (from the DB, active or disabled) is always
// kept too (ut-docs#3035 gap 2): after a rollback to the oldest snapshot it
// is also the oldest by mtime, and its snapshot is the only copy left if the
// live dir goes missing. If that version can't be read, nothing is evicted —
// an extra snapshot on disk is cheaper than losing the active one.
func (rm *RollbackManager) cleanupOldVersions(pluginID string, keep ...string) error {
	versionsDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions")
	kept := make(map[string]bool, len(keep)+1)
	for _, k := range keep {
		kept[k] = true
	}
	installed, ok, err := data.NewPluginRepo(rm.db).GetInstalledPluginVersion(context.Background(), pluginID)
	if err != nil {
		return fmt.Errorf("installed version of %s unknown, keeping every snapshot: %w", pluginID, err)
	}
	if ok {
		kept[installed] = true
	}

	// Best-effort sweep of crash leftovers before counting anything
	// (ut-docs#3016): a `.store-*` dir under versions/ (StoreVersion,
	// interrupted between its os.MkdirTemp and final os.Rename) or a
	// `.restore-*` dir directly under the plugin's own dir
	// (restoreLiveDirFromSnapshot, same failure shape) from an earlier
	// crash must eventually be reclaimed. Age-gated at staleTempDirAge so a
	// concurrent, still-running StoreVersion/restore call is never swept
	// out from under itself. Sweep failures are ignored — this is cleanup
	// riding along on a version store, never something that should fail it.
	sweepStaleTempDirs(versionsDir, ".store-", staleTempDirAge)
	sweepStaleTempDirs(filepath.Join(rm.pluginBaseDir, pluginID), ".restore-", staleTempDirAge)

	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return err
	}

	// A `.store-*` leftover is never a real snapshot, however fresh, and a
	// stray file is not a snapshot at all — drop both before the
	// maxVersions count below, so it can neither occupy one
	// of the "kept" slots nor push a real snapshot out of them
	// (ut-docs#3016). Filtering only inside the loop that builds the sort
	// list would still leave len(entries) itself (the check just below)
	// counting leftovers toward maxVersions.
	var realEntries []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() || isTempVersionDirName(entry.Name()) {
			continue
		}
		realEntries = append(realEntries, entry)
	}

	// If we have more than maxVersions, delete oldest — but never a kept one.
	if len(realEntries) > rm.maxVersions {
		// Get modification times, excluding kept names: a kept snapshot must
		// never become a deletion candidate, however old it is.
		type versionEntry struct {
			name    string
			modTime time.Time
		}

		var candidates []versionEntry
		for _, entry := range realEntries {
			if kept[entry.Name()] {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			candidates = append(candidates, versionEntry{
				name:    entry.Name(),
				modTime: info.ModTime(),
			})
		}

		// Sort by modification time (oldest first)
		for i := 0; i < len(candidates)-1; i++ {
			for j := i + 1; j < len(candidates); j++ {
				if candidates[i].modTime.After(candidates[j].modTime) {
					candidates[i], candidates[j] = candidates[j], candidates[i]
				}
			}
		}

		// Delete oldest candidates until the real count is back at
		// maxVersions, or until there are no more non-kept candidates left
		// — a keep set that itself exceeds maxVersions is honored in full,
		// not truncated.
		toDelete := len(realEntries) - rm.maxVersions
		if toDelete > len(candidates) {
			toDelete = len(candidates)
		}
		for i := 0; i < toDelete; i++ {
			dirPath := filepath.Join(versionsDir, candidates[i].name)
			if err := os.RemoveAll(dirPath); err != nil {
				return fmt.Errorf("failed to remove old version %s: %w", candidates[i].name, err)
			}
		}
	}

	return nil
}
