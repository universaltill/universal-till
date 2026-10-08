package plugins

// Persistent wazero compilation cache (ut-docs#3912). Compiling a WASM
// plugin takes ~10 s per plugin on a Pi 4; without a cache every till start
// paid that again. wazero's own file cache (NewCompilationCacheWithDir)
// stores one entry per module but never prunes and cannot be keyed by us, so
// this file adds the bookkeeping around it:
//
//   - index.json maps plugin id → {version, entry, sha256}; an entry is
//     attributed by diffing the version dir listing around the compile
//     (compiles are serial under w.mu).
//   - The end of each Sync prunes entries no installed plugin references, temp
//     leftovers, and version dirs of other wazero builds — bounded on disk.
//   - A cache that makes wazero fail (corrupt entry, disk full, read-only)
//     never breaks a plugin: the module is compiled again in an uncached
//     runtime, the cache is wiped and switched off until restart.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/universaltill/universal-till/internal/logging"
)

// wasmLoadOutcome says how a module load got its compiled code; it is the
// value in the "wasm load" log line and what the cache tests assert on.
type wasmLoadOutcome string

const (
	wasmLoadCompiled wasmLoadOutcome = "compiled"               // compiled, entry written to the cache
	wasmLoadCacheHit wasmLoadOutcome = "cache hit"              // code read from the cache
	wasmLoadNoCache  wasmLoadOutcome = "compiled without cache" // no cache, or the cache is off
)

// compileCacheIndexFile is the manifest inside the cache dir.
const compileCacheIndexFile = "index.json"

// compileCacheRecord is one plugin's row in the manifest.
type compileCacheRecord struct {
	Version string `json:"version"`
	Entry   string `json:"entry"`  // file name in the wazero version dir
	SHA256  string `json:"sha256"` // of the module bytes
}

// compileCacheIndex is the manifest file's shape.
type compileCacheIndex struct {
	Plugins map[string]compileCacheRecord `json:"plugins"`
}

// wasmCompileCache is the bookkeeping for one cache dir. Every method is
// called with the owning WasmRuntime's mu held.
type wasmCompileCache struct {
	dir          string // the cache dir handed to the runtime
	versionedDir string // dir/wazero-<version>-<arch>-<os>, where wazero writes
	index        map[string]compileCacheRecord
	// unattributed holds active plugins whose module came from the cache but
	// whose entry the index cannot name; while any is set the orphan prune
	// is skipped, or it could delete that plugin's entry.
	unattributed map[string]bool
	// bad: wazero failed on this cache; it is wiped and unused until restart.
	bad bool
}

// compileCacheSupported reports whether goos runs the wazero compiler, the
// only engine that uses the cache. Android and iOS run the interpreter.
func compileCacheSupported(goos string) bool {
	return goos != "android" && goos != "ios"
}

// NewWasmRuntimeWithCache is NewWasmRuntime with a persistent compilation
// cache in cacheDir (plugins.Init passes paths.Data("wasm-cache")). The
// cache is opened on the first compile, so a till (or a test) with no WASM
// plugin never touches the disk; one that cannot be set up is logged once
// and the runtime runs without it.
func NewWasmRuntimeWithCache(baseDir, cacheDir string) *WasmRuntime {
	return newWasmRuntimeWithCacheFor(baseDir, cacheDir, goruntime.GOOS)
}

func newWasmRuntimeWithCacheFor(baseDir, cacheDir, goos string) *WasmRuntime {
	w := NewWasmRuntime(baseDir)
	if !compileCacheSupported(goos) {
		logging.L().Infof("wasm compile cache: off (%s runs the interpreter, which has no compiled code to cache)", goos)
		return w
	}
	w.cacheDir = cacheDir
	return w
}

// ensureCompileCache opens the cache on first use. Caller holds w.mu.
func (w *WasmRuntime) ensureCompileCache() {
	if w.cacheDir == "" || w.cacheOpened {
		return
	}
	w.cacheOpened = true
	c, cc, err := openWasmCompileCache(w.cacheDir)
	if err != nil {
		logging.L().Infof("wasm compile cache: off (%v)", err)
		return
	}
	logging.L().Infof("wasm compile cache: on at %s (%d plugin entries indexed)", c.versionedDir, len(c.index))
	w.cache = c
	w.cachedRT = newPluginRuntime(cc)
}

// openWasmCompileCache creates the wazero file cache in dir and finds the
// version dir it writes to.
func openWasmCompileCache(dir string) (*wasmCompileCache, wazero.CompilationCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	before := wazeroVersionDirs(dir)
	cc, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return nil, nil, err
	}
	versioned := resolveWazeroVersionDir(dir, before)
	if versioned == "" {
		_ = cc.Close(context.Background())
		return nil, nil, fmt.Errorf("cannot tell which wazero version dir in %s is this build's", dir)
	}
	c := &wasmCompileCache{
		dir:          dir,
		versionedDir: versioned,
		index:        readCompileCacheIndex(dir),
		unattributed: map[string]bool{},
	}
	return c, cc, nil
}

// wazeroVersionDirSuffix is the "-<arch>-<os>" end wazero gives its dir.
func wazeroVersionDirSuffix() string {
	return "-" + goruntime.GOARCH + "-" + goruntime.GOOS
}

// wazeroVersionDirs lists dir's wazero-*-<arch>-<os> subdirectories.
func wazeroVersionDirs(dir string) map[string]bool {
	out := map[string]bool{}
	des, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, de := range des {
		if de.IsDir() && strings.HasPrefix(de.Name(), "wazero-") && strings.HasSuffix(de.Name(), wazeroVersionDirSuffix()) {
			out[de.Name()] = true
		}
	}
	return out
}

// resolveWazeroVersionDir finds the dir wazero just set up in dir: the name
// derived as wazero derives it, else the one that appeared, else the only
// one there. "" when it cannot be told apart.
func resolveWazeroVersionDir(dir string, before map[string]bool) string {
	derived := "wazero-" + wazeroModuleVersion() + wazeroVersionDirSuffix()
	if st, err := os.Stat(filepath.Join(dir, derived)); err == nil && st.IsDir() {
		return filepath.Join(dir, derived)
	}
	after := wazeroVersionDirs(dir)
	var fresh []string
	for name := range after {
		if !before[name] {
			fresh = append(fresh, name)
		}
	}
	if len(fresh) == 1 {
		return filepath.Join(dir, fresh[0])
	}
	if len(after) == 1 {
		for name := range after {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

// wazeroModuleVersion mirrors wazero's internal/version.GetWazeroVersion,
// which names the cache's version dir.
func wazeroModuleVersion() string {
	ret := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if strings.Contains(dep.Path, "github.com/tetratelabs/wazero") {
				ret = dep.Version
			}
		}
		if ret == "" || ret == "(devel)" {
			ret = info.Main.Version
		}
	}
	if ret == "" || ret == "(devel)" {
		return "dev"
	}
	return ret
}

func readCompileCacheIndex(dir string) map[string]compileCacheRecord {
	raw, err := os.ReadFile(filepath.Join(dir, compileCacheIndexFile))
	if err != nil {
		return map[string]compileCacheRecord{}
	}
	var idx compileCacheIndex
	if err := json.Unmarshal(raw, &idx); err != nil || idx.Plugins == nil {
		// A broken index only costs attribution: hits go unattributed and
		// the orphan prune waits until the plugins are recompiled.
		return map[string]compileCacheRecord{}
	}
	return idx.Plugins
}

// save writes the index atomically (temp file + rename).
func (c *wasmCompileCache) save() {
	raw, err := json.Marshal(compileCacheIndex{Plugins: c.index})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.dir, ".index-*.tmp")
	if err != nil {
		logging.L().Errorf("wasm compile cache: write index: %v", err)
		return
	}
	_, werr := tmp.Write(raw)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(c.dir, compileCacheIndexFile))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		logging.L().Errorf("wasm compile cache: write index: %v", werr)
	}
}

// snapshot is the version dir's entry files with their modification times.
func (c *wasmCompileCache) snapshot() map[string]time.Time {
	out := map[string]time.Time{}
	des, err := os.ReadDir(c.versionedDir)
	if err != nil {
		return out
	}
	for _, de := range des {
		if !de.Type().IsRegular() || strings.HasSuffix(de.Name(), ".tmp") {
			continue
		}
		if info, err := de.Info(); err == nil {
			out[de.Name()] = info.ModTime()
		}
	}
	return out
}

func (c *wasmCompileCache) entryExists(name string) bool {
	if name == "" || name != filepath.Base(name) {
		return false
	}
	st, err := os.Stat(filepath.Join(c.versionedDir, name))
	return err == nil && st.Mode().IsRegular()
}

// attribute records which entry a successful cached compile of raw used,
// given the version dir before it, and says whether it compiled or hit.
func (c *wasmCompileCache) attribute(pluginID, version string, raw []byte, before map[string]time.Time) wasmLoadOutcome {
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var written []string
	for name, mod := range c.snapshot() {
		if prev, ok := before[name]; !ok || !prev.Equal(mod) {
			written = append(written, name)
		}
	}
	switch {
	case len(written) == 1:
		c.index[pluginID] = compileCacheRecord{Version: version, Entry: written[0], SHA256: hash}
		delete(c.unattributed, pluginID)
		c.save()
		return wasmLoadCompiled
	case len(written) > 1: // compiles are serial; nothing else should write here
		c.unattributed[pluginID] = true
		return wasmLoadCompiled
	}
	if rec, ok := c.index[pluginID]; ok && rec.Version == version && rec.SHA256 == hash && c.entryExists(rec.Entry) {
		delete(c.unattributed, pluginID)
		return wasmLoadCacheHit
	}
	// Same bytes as another plugin's indexed module → same entry.
	for _, rec := range c.index {
		if rec.SHA256 == hash && c.entryExists(rec.Entry) {
			c.index[pluginID] = compileCacheRecord{Version: version, Entry: rec.Entry, SHA256: hash}
			delete(c.unattributed, pluginID)
			c.save()
			return wasmLoadCacheHit
		}
	}
	c.unattributed[pluginID] = true
	return wasmLoadCacheHit
}

// forget drops a plugin that is no longer loaded.
func (c *wasmCompileCache) forget(pluginID string) {
	delete(c.unattributed, pluginID)
}

// wipe deletes every entry and the index: wazero failed on this cache.
func (c *wasmCompileCache) wipe() {
	des, err := os.ReadDir(c.versionedDir)
	if err == nil {
		for _, de := range des {
			if de.Type().IsRegular() {
				if err := os.Remove(filepath.Join(c.versionedDir, de.Name())); err != nil {
					logging.L().Errorf("wasm compile cache: wipe %s: %v", de.Name(), err)
				}
			}
		}
	}
	c.index = map[string]compileCacheRecord{}
	c.unattributed = map[string]bool{}
	if err := os.Remove(filepath.Join(c.dir, compileCacheIndexFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.L().Errorf("wasm compile cache: wipe index: %v", err)
	}
}

// prune keeps the cache bounded to the installed plugins' entries (a
// disabled plugin keeps its entry, so re-enabling it does not recompile):
// it drops index rows of uninstalled plugins, entries no installed plugin
// references (skipped while any loaded plugin's entry is unattributed),
// temp leftovers and the version dirs of other wazero builds. It only ever
// deletes children of c.dir and c.versionedDir.
func (c *wasmCompileCache) prune(installed map[string]bool) {
	if c.bad {
		return
	}
	referenced := map[string]bool{}
	for id, rec := range c.index {
		if !installed[id] {
			delete(c.index, id)
			continue
		}
		referenced[rec.Entry] = true
	}
	unsafe := false
	for id := range c.unattributed {
		if !installed[id] {
			delete(c.unattributed, id)
			continue
		}
		unsafe = true
	}
	remove := func(path string) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logging.L().Errorf("wasm compile cache: prune %s: %v", path, err)
		}
	}
	if des, err := os.ReadDir(c.versionedDir); err == nil {
		for _, de := range des {
			if !de.Type().IsRegular() {
				continue
			}
			name := de.Name()
			if strings.HasSuffix(name, ".tmp") || (!unsafe && !referenced[name]) {
				remove(filepath.Join(c.versionedDir, name))
			}
		}
	}
	if des, err := os.ReadDir(c.dir); err == nil {
		current := filepath.Base(c.versionedDir)
		for _, de := range des {
			name := de.Name()
			switch {
			case de.IsDir() && strings.HasPrefix(name, "wazero-") && name != current:
				if err := os.RemoveAll(filepath.Join(c.dir, name)); err != nil {
					logging.L().Errorf("wasm compile cache: prune %s: %v", name, err)
				}
			case de.Type().IsRegular() && strings.HasPrefix(name, ".index-") && strings.HasSuffix(name, ".tmp"):
				remove(filepath.Join(c.dir, name))
			}
		}
	}
	c.save()
}

// newPluginRuntime builds a wazero runtime with the plugin host's config,
// WASI and the "ut" host module; cache may be nil.
func newPluginRuntime(cache wazero.CompilationCache) wazero.Runtime {
	ctx := context.Background()
	// WithCloseOnContextDone(true) (ut-docs#504 review finding): wazero
	// disables this by default, meaning HandleEvent's per-call
	// context.WithTimeout (timeoutFor) was computed but never actually
	// enforced — a guest stuck in a CPU-bound loop (buggy or malicious
	// plugin.wasm) ran forever regardless of the deadline. That was always
	// latent, but ut-docs#504's fix (EventBus.publish holds eb.mu.RLock
	// across a Blocking handler's call, closing the shutdown/Reload
	// channel-close race) turned "one wedged handler hangs one publish
	// call" into "one wedged handler wedges the entire bus" — including
	// HasSubscribers/Generation on the checkout tax-rate-ask path
	// (internal/pages/tax_hook.go) and every future ResetSubscribers
	// (Manager.Reload/Close), since Go's RWMutex blocks new readers once a
	// writer is pending. Enabling this makes the timeout this code already
	// computes and applies actually terminate the guest module, bounding
	// that hold — matching wazero's own documented guidance for untrusted
	// guests.
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(wasmMemoryLimitPages)
	if cache != nil {
		cfg = cfg.WithCompilationCache(cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if err := instantiateHostModule(ctx, rt); err != nil {
		// Modules that import "ut" will fail to instantiate; log, don't crash.
		logging.L().Errorf("wasm host module: %v", err)
	}
	return rt
}

// compile compiles raw for pluginID@version and returns the module, the
// runtime that owns it (instances must be made there) and how it was
// obtained. A cache failure never fails the plugin. Caller holds w.mu.
func (w *WasmRuntime) compile(pluginID, version string, raw []byte) (wazero.CompiledModule, wazero.Runtime, wasmLoadOutcome, error) {
	ctx := context.Background()
	w.ensureCompileCache()
	c := w.cache
	if c == nil || c.bad {
		m, err := w.rt.CompileModule(ctx, raw)
		return m, w.rt, wasmLoadNoCache, err
	}
	before := c.snapshot()
	m, err := w.cachedRT.CompileModule(ctx, raw)
	if err == nil {
		return m, w.cachedRT, c.attribute(pluginID, version, raw, before), nil
	}
	m, plainErr := w.rt.CompileModule(ctx, raw)
	if plainErr != nil {
		return nil, nil, "", plainErr // the module itself does not compile
	}
	logging.L().Errorf("wasm compile cache: %s failed only with the cache (%v) — wiping %s; plugins compile without the cache until restart",
		pluginID, err, c.versionedDir)
	c.wipe()
	c.bad = true
	return m, w.rt, wasmLoadNoCache, nil
}
