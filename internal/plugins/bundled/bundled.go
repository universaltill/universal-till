package bundled

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Available reports whether this binary embeds the bundled set at all (the
// bundledplugins build tag). A default build is empty.
func Available() bool {
	if v := availableOverride.Load(); v != nil {
		return *v
	}
	return embeddedFS() != nil
}

var availableOverride atomic.Pointer[bool]

// OverrideAvailableForTest makes Available report v for a test and returns
// the restore func. Test-only: scripts/ci/guard-appstore-no-code-fetch.sh
// fails on a caller outside a _test.go file.
func OverrideAvailableForTest(v bool) (restore func()) {
	prev := availableOverride.Swap(&v)
	return func() { availableOverride.Store(prev) }
}

// IDs are the ids of the bundled first-party plugins in the compiled-in lock
// (the Settings > Plugins "Built in" group), whether or not embedded in this
// build.
func IDs() []string {
	l, err := compiledLock()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(l.Plugins))
	for _, e := range l.Plugins {
		ids = append(ids, e.ID)
	}
	return ids
}

// EnabledByDefaultFor returns the bundled plugin that is a legal necessity
// for a shop in country (the lock's enable_for_countries), e.g. the UK VAT
// plugin for GB, from the compiled-in lock. Callers gate on the appstore
// channel (the only one that embeds the set).
func EnabledByDefaultFor(country string) (id string, ok bool) {
	l, err := compiledLock()
	if err != nil {
		return "", false
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	for _, e := range l.Plugins {
		for _, c := range e.EnableForCountries {
			if c == country {
				return e.ID, true
			}
		}
	}
	return "", false
}

// LockedVersion is the version of id pinned in the compiled-in lock.
func LockedVersion(id string) (string, bool) {
	l, err := compiledLock()
	if err != nil {
		return "", false
	}
	for _, e := range l.Plugins {
		if e.ID == id {
			return e.Version, true
		}
	}
	return "", false
}

// IsBundled reports whether id is a bundled first-party plugin.
func IsBundled(id string) bool {
	for _, b := range IDs() {
		if b == id {
			return true
		}
	}
	return false
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readVerified reads a file of the embedded bundle and checks it against its
// pinned sha256.
func readVerified(fsys fs.FS, dir, rel, want string) ([]byte, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, rel))
	if err != nil {
		return nil, err
	}
	if got := sum(raw); got != want {
		return nil, fmt.Errorf("%s: sha256 %s does not match the lock (%s)", rel, got, want)
	}
	return raw, nil
}

var syncMu sync.Mutex

// afterPersist is a test seam: it runs right after the manifest is persisted,
// i.e. at the point an app kill would leave whatever was committed.
var afterPersist func()

// Sync installs or upgrades every bundled plugin the binary embeds. Per
// plugin: the embedded bytes are verified against the compiled-in lock, the
// files land under paths.Plugins() and the manifest is persisted through the
// same plugins.PersistManifest every install uses (TrustLevel "system", the
// lock-verified flag set). A first install is DISABLED: the shop enables it
// after entering its keys. An upgrade (embedded newer than installed, i.e. an
// app update) keeps the shop's enabled state and settings. A plugin whose
// bytes do not match the lock is refused and logged; Sync never panics and
// one bad plugin never blocks another. changed reports whether anything was
// installed or upgraded, so the caller reloads the plugin manager.
func Sync(ctx context.Context, db *sql.DB) (changed bool, err error) {
	fsys := embeddedFS()
	if fsys == nil {
		return false, nil
	}
	lock, err := compiledLock()
	if err != nil {
		return false, err
	}
	return syncFrom(ctx, db, fsys, lock)
}

func syncFrom(ctx context.Context, db *sql.DB, fsys fs.FS, lock *Lock) (changed bool, err error) {
	syncMu.Lock()
	defer syncMu.Unlock()
	var errs []string
	for _, e := range lock.Plugins {
		c, perr := syncOne(ctx, db, fsys, e)
		if perr != nil {
			logging.L().Errorf("bundled plugin %s refused: %v", e.ID, perr)
			errs = append(errs, e.ID+": "+perr.Error())
			continue
		}
		changed = changed || c
	}
	if len(errs) > 0 {
		return changed, fmt.Errorf("bundled plugins: %s", strings.Join(errs, "; "))
	}
	return changed, nil
}

func syncOne(ctx context.Context, db *sql.DB, fsys fs.FS, e Entry) (bool, error) {
	repo := data.NewPluginRepo(db)
	manifestRaw, err := readVerified(fsys, e.ID, "manifest.json", e.ManifestSHA256)
	if err != nil {
		return false, err
	}
	wasmRaw, err := readVerified(fsys, e.ID, e.WasmPath, e.WasmSHA256)
	if err != nil {
		return false, err
	}
	extra := map[string][]byte{}
	for rel, want := range e.Files {
		raw, err := readVerified(fsys, e.ID, rel, want)
		if err != nil {
			return false, err
		}
		extra[rel] = raw
	}
	m, err := plugins.ParseManifest(bytes.NewReader(manifestRaw))
	if err != nil {
		return false, fmt.Errorf("parse embedded manifest: %w", err)
	}
	if m.ID != e.ID || m.Version != e.Version || m.Runtime != "wasm" {
		return false, fmt.Errorf("embedded manifest %s@%s (%s) does not match the lock %s@%s", m.ID, m.Version, m.Runtime, e.ID, e.Version)
	}
	if filepath.ToSlash(strings.TrimPrefix(m.Entrypoint, "./")) != e.WasmPath {
		return false, fmt.Errorf("manifest entrypoint %q is not the locked wasm path %q", m.Entrypoint, e.WasmPath)
	}

	installed, found, err := repo.GetInstalledPluginVersion(ctx, e.ID)
	if err != nil {
		return false, fmt.Errorf("check installed: %w", err)
	}
	if found && installed == e.Version {
		if verifyOnDisk(e, paths.Plugins(e.ID, e.Version)) && trustIsSystem(ctx, repo, e.ID) {
			return false, nil
		}
		// Files missing or altered, or the row lost its system trust: rewrite
		// from the verified embed.
	}
	// installed != embedded (older: an app update; NEWER: an app downgrade, a
	// marketplace copy restored from a backup or joined from another till):
	// on this channel the loader only runs the locked bytes, so a row at any
	// other version is dead. The embedded version replaces it, keeping the
	// shop's enabled state and settings (ut-docs#6565, version skew).
	wasActive := false
	if found {
		if wasActive, err = repo.PluginActive(ctx, e.ID); err != nil {
			return false, fmt.Errorf("read enabled state: %w", err)
		}
	}

	previouslyDeclared, prevErr := plugins.DeclaredPermissionNames(ctx, db, e.ID)
	defer plugins.LockPluginTree(paths.Plugins(), e.ID)()
	dest := paths.Plugins(e.ID, e.Version)
	files := map[string][]byte{"manifest.json": manifestRaw, e.WasmPath: wasmRaw}
	for rel, raw := range extra {
		files[rel] = raw
	}
	for rel, raw := range files {
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return false, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", target, err)
		}
	}
	if err := plugins.PersistManifest(ctx, db, m, plugins.InstallOptions{
		InstalledFromURL:    "bundled://" + e.ID + "@" + e.Version,
		SHA256:              e.WasmSHA256,
		TrustLevel:          "system",
		BundledLockVerified: true,
		// First install: off until the shop turns it on. Upgrade: keep its
		// choice. In the persist transaction, never a separate later write.
		InstallInactive: !wasActive,
	}); err != nil {
		return false, fmt.Errorf("persist: %w", err)
	}
	// The lock vouches for these bytes as the marketplace signature does for
	// a download: grant the declared permissions by the marketplace rule
	// (first declaration only), or a net: call never reaches the provider.
	plugins.GrantDeclaredPermissions(ctx, db, m, previouslyDeclared, prevErr)
	if afterPersist != nil {
		afterPersist()
	}
	if found && installed != e.Version {
		_ = os.RemoveAll(paths.Plugins(e.ID, installed)) // best effort; the DB is the truth
	}
	return true, nil
}

// verifyOnDisk reports whether every locked file of e (manifest, wasm and
// the extra files) is on disk under dir with its pinned hash.
func verifyOnDisk(e Entry, dir string) bool {
	want := map[string]string{"manifest.json": e.ManifestSHA256, e.WasmPath: e.WasmSHA256}
	for rel, h := range e.Files {
		want[rel] = h
	}
	for rel, h := range want {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || sum(raw) != h {
			return false
		}
	}
	return true
}

// Verify is the plugins.BundledCodeVerifier: wasm, the bytes the loader is
// about to compile from wasmPath, is the bundled first-party plugin
// id@version, byte for byte as pinned in the compiled-in lock. False in a
// build that embeds nothing. The bytes are hashed on every call: no memo
// keyed on path, size or modification time, which a same-size swap defeats.
func Verify(id, version, wasmPath string, wasm []byte) bool {
	if !Available() {
		return false
	}
	lock, err := compiledLock()
	if err != nil {
		return false
	}
	return verifyAgainst(lock, id, version, wasmPath, wasm)
}

func verifyAgainst(lock *Lock, id, version, wasmPath string, wasm []byte) bool {
	for _, e := range lock.Plugins {
		if e.ID != id || e.Version != version {
			continue
		}
		if !strings.HasSuffix(filepath.ToSlash(wasmPath), "/"+id+"/"+version+"/"+e.WasmPath) {
			return false
		}
		return len(wasm) > 0 && sum(wasm) == e.WasmSHA256
	}
	return false
}

// Register wires Verify into the wasm loader. Called once at boot, before the
// first plugin Reload.
func Register() { plugins.SetBundledCodeVerifier(Verify) }

func trustIsSystem(ctx context.Context, repo *data.PluginRepo, id string) bool {
	trust, ok, err := repo.PluginTrustLevel(ctx, id)
	return err == nil && ok && trust == "system"
}
