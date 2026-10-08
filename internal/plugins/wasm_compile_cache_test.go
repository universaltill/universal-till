package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Persistent wazero compilation cache (ut-docs#3912): a till restart must not
// recompile every WASM plugin (10.5 s per plugin on a Pi 4), the cache must
// stay bounded as plugins are removed or updated, and a bad cache must never
// break a plugin.

// compileCacheFixture is one till data dir: a plugin install root, a cache
// dir and a database, shared by the runtimes a test builds over it.
type compileCacheFixture struct {
	db       *sql.DB
	base     string
	cacheDir string
	guest    []byte
}

func newCompileCacheFixture(t *testing.T) *compileCacheFixture {
	t.Helper()
	raw, err := os.ReadFile(buildHostfnGuest(t))
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	return &compileCacheFixture{
		db:       managerTestDB(t),
		base:     t.TempDir(),
		cacheDir: filepath.Join(t.TempDir(), "wasm-cache"),
		guest:    raw,
	}
}

// install lays module out as an active wasm plugin at version, with the
// storage permission the guest's handlers use.
func (f *compileCacheFixture) install(t *testing.T, id, version string, module []byte) {
	t.Helper()
	seedInstalledPlugin(t, f.db, id, id, version, "wasm", true)
	if _, err := f.db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, id); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	grantPerm(t, f.db, id, "storage")
	f.writeModule(t, id, version, module)
}

// bump moves an installed plugin to a new version with a new module.
func (f *compileCacheFixture) bump(t *testing.T, id, version string, module []byte) {
	t.Helper()
	seedCatalogRow(t, f.db, id, id, version, "")
	if _, err := f.db.Exec(`UPDATE plugins SET version = ? WHERE id = ?`, version, id); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	f.writeModule(t, id, version, module)
}

func (f *compileCacheFixture) writeModule(t *testing.T, id, version string, module []byte) {
	t.Helper()
	if err := writeFileWithParents(filepath.Join(f.base, id, version, "plugin.wasm"), module); err != nil {
		t.Fatalf("write module: %v", err)
	}
}

// runtime builds a cache-backed runtime over the fixture, as a till start does.
func (f *compileCacheFixture) runtime(t *testing.T) *WasmRuntime {
	t.Helper()
	w := NewWasmRuntimeWithCache(f.base, f.cacheDir)
	t.Cleanup(func() { w.Close(context.Background()) })
	return w
}

func (f *compileCacheFixture) sync(t *testing.T, w *WasmRuntime) {
	t.Helper()
	w.Sync(context.Background(), f.db)
	t.Cleanup(SharedBus(f.db).ResetSubscribers)
}

// variant returns the guest with a custom section appended: the same code,
// different bytes, so a different cache key — a second plugin or version.
func variant(module []byte, tag string) []byte {
	name := "ut-test"
	body := append([]byte{byte(len(name))}, name...)
	body = append(body, tag...)
	out := append([]byte{}, module...)
	out = append(out, 0x00) // custom section id
	out = appendULEB(out, uint32(len(body)))
	return append(out, body...)
}

func appendULEB(b []byte, v uint32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

// entries lists the cache entry files (no temp files) in the runtime's
// wazero version dir.
func cacheEntries(t *testing.T, w *WasmRuntime) []string {
	t.Helper()
	if w.cache == nil {
		t.Fatal("runtime has no compile cache")
	}
	des, err := os.ReadDir(w.cache.versionedDir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	var names []string
	for _, de := range des {
		if de.Type().IsRegular() && !strings.HasSuffix(de.Name(), ".tmp") {
			names = append(names, de.Name())
		}
	}
	sort.Strings(names)
	return names
}

// openCache opens the runtime's compile cache now, as its first compile would.
func openCache(t *testing.T, w *WasmRuntime) *wasmCompileCache {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensureCompileCache()
	if w.cache == nil {
		t.Fatal("compile cache did not open")
	}
	return w.cache
}

func lastLoad(w *WasmRuntime, id string) wasmLoadOutcome {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastLoad[id]
}

func readCacheIndex(t *testing.T, cacheDir string) map[string]compileCacheRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cacheDir, "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var idx compileCacheIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("parse index %s: %v", raw, err)
	}
	return idx.Plugins
}

func TestWasmCompileCache_RestartHitsCache(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id = "com.test.cachehit"
	f.install(t, id, "1.0.0", f.guest)

	w1 := f.runtime(t)
	f.sync(t, w1)
	if got := lastLoad(w1, id); got != wasmLoadCompiled {
		t.Fatalf("first start: load outcome %q, want %q", got, wasmLoadCompiled)
	}
	first := cacheEntries(t, w1)
	if len(first) != 1 {
		t.Fatalf("first start: %d cache entries %v, want 1", len(first), first)
	}
	if rec := readCacheIndex(t, f.cacheDir)[id]; rec.Version != "1.0.0" || rec.Entry != first[0] {
		t.Fatalf("index record %+v, want version 1.0.0 entry %s", rec, first[0])
	}

	// A till restart: a fresh runtime on the same cache dir.
	w2 := f.runtime(t)
	f.sync(t, w2)
	if got := lastLoad(w2, id); got != wasmLoadCacheHit {
		t.Fatalf("restart: load outcome %q, want %q", got, wasmLoadCacheHit)
	}
	if again := cacheEntries(t, w2); strings.Join(again, ",") != strings.Join(first, ",") {
		t.Fatalf("restart wrote cache entries: before %v after %v", first, again)
	}
	w2.mu.Lock()
	_, loaded := w2.modules[id]
	w2.mu.Unlock()
	if !loaded {
		t.Fatal("module not loaded from cache")
	}
	// The hit module must instantiate in the runtime that loaded it: an
	// event after the restart has to run (review finding, ut-docs#3912).
	body, _ := json.Marshal(map[string]string{"mode": "clock"})
	ev := Event{ID: "ev1", Type: "test.event", Timestamp: time.Now(), Payload: body}
	if _, err := w2.HandleEvent(context.Background(), id, ev); err != nil {
		t.Fatalf("HandleEvent after cache hit: %v", err)
	}
	if raw, err := data.NewPluginRepo(f.db).StorageGet(context.Background(), id, "results"); err != nil || len(raw) == 0 {
		t.Fatalf("guest left no results (err %v)", err)
	}
}

// A module that compiles nowhere is a broken plugin, not a bad cache: the
// healthy cache stays on and keeps its entries.
func TestWasmCompileCache_InvalidModuleKeepsCache(t *testing.T) {
	f := newCompileCacheFixture(t)
	const good, bad = "com.test.good", "com.test.bad"
	f.install(t, good, "1.0.0", f.guest)
	w := f.runtime(t)
	f.sync(t, w)
	entries := cacheEntries(t, w)
	if len(entries) != 1 {
		t.Fatalf("entries %v, want 1", entries)
	}

	f.install(t, bad, "1.0.0", []byte("\x00asm\x01\x00\x00\x00garbage"))
	f.sync(t, w)
	if got := cacheEntries(t, w); len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("healthy cache touched by an invalid module: before %v after %v", entries, got)
	}
	w.mu.Lock()
	isBad := w.cache == nil || w.cache.bad
	w.mu.Unlock()
	if isBad {
		t.Fatal("cache switched off by an invalid module")
	}
	rows, err := data.NewPluginRepo(f.db).ListInstalledPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == bad && row.InstallState != data.PluginStateBroken {
			t.Fatalf("invalid module not marked broken: %s", row.InstallState)
		}
		if row.ID == good && row.InstallState == data.PluginStateBroken {
			t.Fatal("good plugin marked broken")
		}
	}
}

func TestWasmCompileCache_UninstalledPluginEntryPruned(t *testing.T) {
	f := newCompileCacheFixture(t)
	const keep, drop = "com.test.keep", "com.test.drop"
	f.install(t, keep, "1.0.0", f.guest)
	f.install(t, drop, "1.0.0", variant(f.guest, "drop"))

	w := f.runtime(t)
	f.sync(t, w)
	if n := len(cacheEntries(t, w)); n != 2 {
		t.Fatalf("%d cache entries, want 2", n)
	}
	keepEntry := readCacheIndex(t, f.cacheDir)[keep].Entry

	if _, err := f.db.Exec(`DELETE FROM plugins WHERE id = ?`, drop); err != nil {
		t.Fatal(err)
	}
	f.sync(t, w)

	if got := cacheEntries(t, w); len(got) != 1 || got[0] != keepEntry {
		t.Fatalf("after uninstalling %s: entries %v, want only %s", drop, got, keepEntry)
	}
	idx := readCacheIndex(t, f.cacheDir)
	if _, ok := idx[drop]; ok {
		t.Fatalf("index still has a row for uninstalled %s: %+v", drop, idx)
	}
	if _, ok := idx[keep]; !ok {
		t.Fatalf("index lost active %s: %+v", keep, idx)
	}
}

// Disabling a plugin keeps its entry: re-enabling it on a Pi 4 must not
// cost another 10 s compile. The bound stays one entry per installed plugin.
func TestWasmCompileCache_DisabledPluginKeepsEntry(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id = "com.test.toggle"
	f.install(t, id, "1.0.0", f.guest)

	w := f.runtime(t)
	f.sync(t, w)
	entries := cacheEntries(t, w)
	if len(entries) != 1 {
		t.Fatalf("%d cache entries, want 1", len(entries))
	}
	setActive := func(on int) {
		if _, err := f.db.Exec(`UPDATE plugins SET is_active = ? WHERE id = ?`, on, id); err != nil {
			t.Fatal(err)
		}
	}
	setActive(0)
	f.sync(t, w)
	if got := cacheEntries(t, w); strings.Join(got, ",") != strings.Join(entries, ",") {
		t.Fatalf("disabling pruned the entry: before %v after %v", entries, got)
	}
	if _, ok := readCacheIndex(t, f.cacheDir)[id]; !ok {
		t.Fatal("disabling dropped the index row")
	}

	setActive(1)
	f.sync(t, w)
	if got := lastLoad(w, id); got != wasmLoadCacheHit {
		t.Fatalf("re-enable: load outcome %q, want %q", got, wasmLoadCacheHit)
	}
}

func TestWasmCompileCache_VersionBumpPrunesOldEntry(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id = "com.test.bump"
	f.install(t, id, "1.0.0", f.guest)

	w := f.runtime(t)
	f.sync(t, w)
	old := cacheEntries(t, w)
	if len(old) != 1 {
		t.Fatalf("v1: entries %v, want 1", old)
	}

	f.bump(t, id, "1.1.0", variant(f.guest, "1.1.0"))
	f.sync(t, w)
	if got := lastLoad(w, id); got != wasmLoadCompiled {
		t.Fatalf("v1.1.0: load outcome %q, want %q", got, wasmLoadCompiled)
	}
	got := cacheEntries(t, w)
	if len(got) != 1 || got[0] == old[0] {
		t.Fatalf("after bump: entries %v (old %v), want one new entry", got, old)
	}
	if rec := readCacheIndex(t, f.cacheDir)[id]; rec.Version != "1.1.0" || rec.Entry != got[0] {
		t.Fatalf("index record %+v, want 1.1.0 → %s", rec, got[0])
	}
}

func TestWasmCompileCache_CorruptEntryFallsBackAndWipes(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id, second = "com.test.corrupt", "com.test.second"
	f.install(t, id, "1.0.0", f.guest)

	w1 := f.runtime(t)
	f.sync(t, w1)
	entries := cacheEntries(t, w1)
	if len(entries) != 1 {
		t.Fatalf("entries %v, want 1", entries)
	}
	if err := os.WriteFile(filepath.Join(w1.cache.versionedDir, entries[0]), []byte("garbage, not a compiled module"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.install(t, second, "1.0.0", variant(f.guest, "second"))
	w2 := f.runtime(t)
	f.sync(t, w2)

	for _, pid := range []string{id, second} {
		if got := lastLoad(w2, pid); got != wasmLoadNoCache {
			t.Fatalf("%s: load outcome %q, want %q", pid, got, wasmLoadNoCache)
		}
	}
	rows, err := data.NewPluginRepo(f.db).ListInstalledPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.InstallState == data.PluginStateBroken {
			t.Fatalf("%s marked broken by a corrupt cache", row.ID)
		}
	}
	if got := cacheEntries(t, w2); len(got) != 0 {
		t.Fatalf("corrupt cache not wiped: %v", got)
	}

	// The plugin still runs: its module lives in the uncached runtime.
	body, _ := json.Marshal(map[string]string{"mode": "clock"})
	ev := Event{ID: "ev1", Type: "test.event", Timestamp: time.Now(), Payload: body}
	if _, err := w2.HandleEvent(context.Background(), id, ev); err != nil {
		t.Fatalf("HandleEvent after fallback: %v", err)
	}
	if raw, err := data.NewPluginRepo(f.db).StorageGet(context.Background(), id, "results"); err != nil || len(raw) == 0 {
		t.Fatalf("guest left no results (err %v)", err)
	}
}

func TestWasmCompileCache_UnwritableCacheDirStillLoads(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user writing")
	}
	f := newCompileCacheFixture(t)
	const id = "com.test.readonly"
	f.install(t, id, "1.0.0", f.guest)
	if err := os.MkdirAll(f.cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.cacheDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.cacheDir, 0o700) })

	w := f.runtime(t)
	f.sync(t, w)
	if w.cache != nil {
		t.Fatal("cache enabled on an unwritable dir")
	}
	if got := lastLoad(w, id); got != wasmLoadNoCache {
		t.Fatalf("load outcome %q, want %q", got, wasmLoadNoCache)
	}
}

func TestWasmCompileCache_UnwritableVersionDirFallsBack(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not stop this user writing")
	}
	f := newCompileCacheFixture(t)
	const id = "com.test.fullcache"
	f.install(t, id, "1.0.0", f.guest)

	w := f.runtime(t)
	c := openCache(t, w)
	// The version dir exists but can take no new entry (disk full, read-only
	// mount): wazero fails CompileModule itself.
	if err := os.Chmod(c.versionedDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(c.versionedDir, 0o700) })
	f.sync(t, w)
	if got := lastLoad(w, id); got != wasmLoadNoCache {
		t.Fatalf("load outcome %q, want %q", got, wasmLoadNoCache)
	}
}

func TestWasmCompileCache_StaleVersionDirsAndTempFilesRemoved(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id = "com.test.stale"
	f.install(t, id, "1.0.0", f.guest)

	stale := filepath.Join(f.cacheDir, "wazero-v0.0.1-"+goruntime.GOARCH+"-"+goruntime.GOOS)
	if err := writeFileWithParents(filepath.Join(stale, "deadbeef"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(f.cacheDir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("not ours to delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(f.cacheDir), "wazero-v0.0.1-outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	w := f.runtime(t)
	leftover := filepath.Join(openCache(t, w).versionedDir, "abcd.1234.tmp")
	if err := os.WriteFile(leftover, []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.sync(t, w)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale version dir survived: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("temp leftover survived: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("non-cache file deleted: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("dir outside the cache dir deleted: %v", err)
	}
	if n := len(cacheEntries(t, w)); n != 1 {
		t.Fatalf("active plugin's entry: %d entries, want 1", n)
	}
}

// A cache hit the index cannot attribute (index lost) must disable the
// orphan prune, or the active plugin's own entry could be deleted.
func TestWasmCompileCache_UnattributedHitSkipsPrune(t *testing.T) {
	f := newCompileCacheFixture(t)
	const id = "com.test.noindex"
	f.install(t, id, "1.0.0", f.guest)

	w1 := f.runtime(t)
	f.sync(t, w1)
	entries := cacheEntries(t, w1)
	if err := os.Remove(filepath.Join(f.cacheDir, "index.json")); err != nil {
		t.Fatal(err)
	}

	w2 := f.runtime(t)
	f.sync(t, w2)
	if got := lastLoad(w2, id); got != wasmLoadCacheHit {
		t.Fatalf("load outcome %q, want %q", got, wasmLoadCacheHit)
	}
	if got := cacheEntries(t, w2); strings.Join(got, ",") != strings.Join(entries, ",") {
		t.Fatalf("unattributed entry pruned: before %v after %v", entries, got)
	}
}

// Two plugins shipping identical module bytes share one cache entry; the
// second is attributed by content hash, so the prune stays on.
func TestWasmCompileCache_IdenticalModulesShareEntry(t *testing.T) {
	f := newCompileCacheFixture(t)
	f.install(t, "com.test.twin.a", "1.0.0", f.guest)
	f.install(t, "com.test.twin.b", "1.0.0", f.guest)

	w := f.runtime(t)
	f.sync(t, w)
	idx := readCacheIndex(t, f.cacheDir)
	a, b := idx["com.test.twin.a"], idx["com.test.twin.b"]
	if a.Entry == "" || a.Entry != b.Entry {
		t.Fatalf("twin records %+v / %+v, want one shared entry", a, b)
	}
	w.mu.Lock()
	unattributed := len(w.cache.unattributed)
	w.mu.Unlock()
	if unattributed != 0 {
		t.Fatalf("%d unattributed plugins, want 0", unattributed)
	}
}

func TestWasmCompileCache_OffOnMobile(t *testing.T) {
	for goos, want := range map[string]bool{
		"android": false, "ios": false,
		"linux": true, "darwin": true, "windows": true,
	} {
		if got := compileCacheSupported(goos); got != want {
			t.Errorf("compileCacheSupported(%q) = %v, want %v", goos, got, want)
		}
	}

	for _, goos := range []string{"android", "ios"} {
		dir := filepath.Join(t.TempDir(), "wasm-cache")
		w := newWasmRuntimeWithCacheFor(t.TempDir(), dir, goos)
		w.mu.Lock()
		w.ensureCompileCache()
		w.mu.Unlock()
		if w.cache != nil {
			t.Errorf("%s: compile cache enabled", goos)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s: cache dir created (%v)", goos, err)
		}
	}
}

// The cache opens on the first compile: a till (or a test calling
// plugins.Init) with no WASM plugin never creates the cache dir.
func TestWasmCompileCache_NotCreatedWithoutWasmPlugins(t *testing.T) {
	db := managerTestDB(t)
	cacheDir := filepath.Join(t.TempDir(), "wasm-cache")
	w := NewWasmRuntimeWithCache(t.TempDir(), cacheDir)
	w.Sync(context.Background(), db)
	t.Cleanup(SharedBus(db).ResetSubscribers)
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache dir created with no WASM plugin (%v)", err)
	}
}

// The cache-free constructor stays cache-free (existing callers and tests).
func TestNewWasmRuntime_NoCache(t *testing.T) {
	if NewWasmRuntime(t.TempDir()).cache != nil {
		t.Fatal("NewWasmRuntime enabled a compile cache")
	}
}
