package plugins

// Parallel first compile (ut-docs#3921). The compile cache (ut-docs#3912)
// makes later starts fast, but the first start after an install, an update
// or a wazero bump still compiles every plugin — ~10.5 s each on a Pi 4, one
// after another under w.mu. Before Sync's serial load loop, warmCompileCache
// compiles the cache misses concurrently — at most GOMAXPROCS at a time, and
// fewer when their estimated working set would pass half the available
// memory; the loop then reads each module back from the cache.
//
// Attribution stays exact without the dir-listing diff the serial path uses
// (two compiles writing at once can't be told apart that way): each miss
// compiles in its own runtime over its own staging cache dir under
// <cache>/staging, so the one entry that appears there is that module's.
// The entry is renamed into the shared version dir and indexed under w.mu,
// the lock the serial path holds across its own diff, so the two never see
// each other's writes. Anything that goes wrong here only skips the
// parallel compile: the serial path then compiles that plugin as before.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// wasmLoadCompiledParallel: the module was compiled by the parallel pass and
// the load read it back from the cache.
const wasmLoadCompiledParallel wasmLoadOutcome = "compiled in parallel"

// compileStagingDir holds the per-compile cache dirs, inside the cache dir so
// the rename into the version dir stays on one filesystem.
const compileStagingDir = "staging"

// onWarmCompile, when set (tests only), runs in each parallel compile's
// goroutine just before it compiles.
var onWarmCompile func(pluginID string)

// warmTarget is one plugin whose module a warm job compiles.
type warmTarget struct{ id, version string }

// warmJob is one distinct module (plugins with identical bytes share it).
type warmJob struct {
	hash    string
	raw     []byte
	targets []warmTarget
	// filled by the worker
	staging string // per-compile cache dir; removed afterwards
	entry   string // the entry file wazero wrote under staging
	took    time.Duration
	err     error
}

// compileWarmWorkers is how many compiles run at once: configured (0 =
// GOMAXPROCS, so a Pi 4 compiles four at a time), never more than jobs.
func compileWarmWorkers(jobs, configured int) int {
	n := configured
	if n <= 0 {
		n = goruntime.GOMAXPROCS(0)
	}
	if n > jobs {
		n = jobs
	}
	if n < 1 {
		n = 1
	}
	return n
}

// compileWorkingSetFactor estimates one compile's peak memory as this many
// times the module's size: tax-uk (5.1 MiB) added ~110 MB of peak RSS per
// extra concurrent compile on linux/amd64 (~21×, ut-docs#3921); 32 leaves
// headroom for arm64 and other builds until the Pi is measured.
const compileWorkingSetFactor = 32

// memoryBoundWorkers lowers workers so the largest modules compiling at once
// fit in half of the available memory (till memory must stay bounded, and
// the till keeps selling meanwhile). It may return 1: no parallel pass.
func memoryBoundWorkers(workers int, jobs []*warmJob, available uint64) int {
	sizes := make([]uint64, 0, len(jobs))
	for _, j := range jobs {
		sizes = append(sizes, uint64(len(j.raw))*compileWorkingSetFactor)
	}
	sort.Slice(sizes, func(a, b int) bool { return sizes[a] > sizes[b] })
	budget := available / 2
	var sum uint64
	n := 0
	for n < workers && n < len(sizes) && sum+sizes[n] <= budget {
		sum += sizes[n]
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}

// memAvailable reads MemAvailable from /proc/meminfo (Linux, the Pi tills);
// ok is false elsewhere, where only the GOMAXPROCS bound applies. Inside a
// memory-limited container it reads the host's value, not the cgroup limit:
// fine for the Pi target, a known gap for containerised tills.
func memAvailable() (uint64, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, found := strings.CutPrefix(line, "MemAvailable:"); found {
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return 0, false
			}
			kb, err := strconv.ParseUint(fields[0], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb * 1024, true
		}
	}
	return 0, false
}

// warmCompileCache compiles the cache misses among rows' active wasm plugins
// concurrently and indexes their entries, so the serial load that follows
// hits the cache. It does nothing unless at least two distinct modules miss.
// Called by Sync without w.mu held.
func (w *WasmRuntime) warmCompileCache(rows []data.InstalledPluginRow) {
	if w == nil {
		return
	}
	type candidate struct {
		id, version, path string
	}
	w.mu.Lock()
	if w.cacheDir == "" {
		w.mu.Unlock()
		return
	}
	var cands []candidate
	for _, row := range rows {
		if row.Runtime != "wasm" || !row.IsActive {
			continue
		}
		if _, ok := w.modules[row.ID]; ok && w.versions[row.ID] == row.Version {
			continue
		}
		cands = append(cands, candidate{row.ID, row.Version,
			filepath.Join(w.baseDir, row.ID, row.Version, strings.TrimPrefix(row.Entrypoint, "./"))})
	}
	w.mu.Unlock()
	if len(cands) < 2 {
		return
	}

	type module struct {
		candidate
		raw  []byte
		hash string
	}
	var mods []module
	for _, c := range cands {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			continue // the serial load reports it
		}
		sum := sha256.Sum256(raw)
		mods = append(mods, module{c, raw, hex.EncodeToString(sum[:])})
	}
	if len(mods) < 2 {
		return
	}

	w.mu.Lock()
	w.ensureCompileCache()
	c := w.cache
	if c == nil || c.bad {
		w.mu.Unlock()
		return
	}
	byHash := map[string]*warmJob{}
	var jobs []*warmJob
	for _, m := range mods {
		if c.wouldHit(m.id, m.version, m.hash) {
			continue
		}
		j := byHash[m.hash]
		if j == nil {
			j = &warmJob{hash: m.hash, raw: m.raw}
			byHash[m.hash] = j
			jobs = append(jobs, j)
		}
		j.targets = append(j.targets, warmTarget{m.id, m.version})
	}
	cacheDir, versionBase := c.dir, filepath.Base(c.versionedDir)
	workers := compileWarmWorkers(len(jobs), w.compileWorkers)
	w.mu.Unlock()
	if len(jobs) < 2 {
		return
	}
	// The module bytes of plugins that will hit are not needed any more.
	mods = nil
	if avail, ok := memAvailable(); ok {
		if bounded := memoryBoundWorkers(workers, jobs, avail); bounded < workers {
			logging.L().Infof("wasm compile cache: %d modules to compile, %d at a time for memory (%d MiB available)",
				len(jobs), bounded, avail>>20)
			workers = bounded
		}
	}
	if workers < 2 {
		return // one at a time is what the serial path already does
	}

	start := time.Now()
	stagingRoot := filepath.Join(cacheDir, compileStagingDir)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j *warmJob) {
			defer logging.RecoverAndLog("plugins.wasmCompileWarm")
			defer wg.Done()
			defer func() { <-sem }()
			j.compile(stagingRoot, versionBase)
		}(j)
	}
	wg.Wait()

	compiled := 0
	w.mu.Lock()
	// A cache that went bad (or was replaced) meanwhile gets nothing.
	if w.cache == c && !c.bad {
		for _, j := range jobs {
			if j.err == nil && j.entry == "" {
				j.err = fmt.Errorf("compile did not finish") // recovered panic
			}
			if j.err != nil {
				continue
			}
			name := filepath.Base(j.entry)
			if err := os.Rename(j.entry, filepath.Join(c.versionedDir, name)); err != nil {
				j.err = err
				continue
			}
			for _, t := range j.targets {
				c.index[t.id] = compileCacheRecord{Version: t.version, Entry: name, SHA256: j.hash}
				delete(c.unattributed, t.id)
				w.warmed[t.id] = true
			}
			compiled++
		}
		if compiled > 0 {
			c.save()
		}
	}
	w.mu.Unlock()

	for _, j := range jobs {
		if j.staging != "" {
			if err := os.RemoveAll(j.staging); err != nil {
				logging.L().Errorf("wasm compile cache: remove %s: %v", j.staging, err)
			}
		}
		for _, t := range j.targets {
			if j.err != nil {
				logging.L().Infof("wasm compile %s@%s: parallel compile failed after %s (%v); it loads serially",
					t.id, t.version, j.took.Round(time.Millisecond), j.err)
			} else {
				logging.L().Infof("wasm compile %s@%s: compiled in %s", t.id, t.version, j.took.Round(time.Millisecond))
			}
		}
	}
	logging.L().Infof("wasm compile cache: %d of %d modules compiled in parallel (%d at a time) in %s",
		compiled, len(jobs), workers, time.Since(start).Round(time.Millisecond))
}

// wouldHit reports whether loading pluginID@version with these module bytes
// would read an entry the index already names. Caller holds w.mu.
func (c *wasmCompileCache) wouldHit(pluginID, version, hash string) bool {
	if rec, ok := c.index[pluginID]; ok && rec.Version == version && rec.SHA256 == hash && c.entryExists(rec.Entry) {
		return true
	}
	for _, rec := range c.index {
		if rec.SHA256 == hash && c.entryExists(rec.Entry) {
			return true
		}
	}
	return false
}

// compile compiles j.raw in a runtime of its own over a fresh staging cache
// dir and sets j.entry to the one entry file wazero wrote there.
func (j *warmJob) compile(stagingRoot, versionBase string) {
	start := time.Now()
	defer func() { j.took = time.Since(start) }()
	if onWarmCompile != nil {
		onWarmCompile(j.targets[0].id)
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		j.err = err
		return
	}
	dir, err := os.MkdirTemp(stagingRoot, "c-*")
	if err != nil {
		j.err = err
		return
	}
	j.staging = dir
	ctx := context.Background()
	cc, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		j.err = err
		return
	}
	rt := newPluginRuntime(cc)
	m, err := rt.CompileModule(ctx, j.raw)
	if err == nil {
		_ = m.Close(ctx)
	}
	_ = rt.Close(ctx)
	_ = cc.Close(ctx)
	if err != nil {
		j.err = err
		return
	}
	vdir := filepath.Join(dir, versionBase)
	des, err := os.ReadDir(vdir)
	if err != nil {
		j.err = fmt.Errorf("no cache entry written: %w", err)
		return
	}
	var entries []string
	for _, de := range des {
		if de.Type().IsRegular() && !strings.HasSuffix(de.Name(), ".tmp") {
			entries = append(entries, de.Name())
		}
	}
	if len(entries) != 1 {
		j.err = fmt.Errorf("%d cache entries written, want 1", len(entries))
		return
	}
	j.entry = filepath.Join(vdir, entries[0])
}
