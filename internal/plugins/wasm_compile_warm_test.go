package plugins

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Parallel first compile (ut-docs#3921): the first start after an install,
// update or wazero bump compiles every cache miss at once instead of one
// after another, and each entry is still attributed to its own plugin.

// withWarmHook sets onWarmCompile for one test.
func withWarmHook(t *testing.T, hook func(pluginID string)) {
	t.Helper()
	onWarmCompile = hook
	t.Cleanup(func() { onWarmCompile = nil })
}

func TestWasmCompileWarm_CompilesMissesConcurrently(t *testing.T) {
	f := newCompileCacheFixture(t)
	ids := []string{"com.test.par.a", "com.test.par.b", "com.test.par.c"}
	for _, id := range ids {
		f.install(t, id, "1.0.0", variant(f.guest, id))
	}

	// Each compile holds its slot until a second one is in flight (or a
	// few seconds pass): run one at a time, at most one is ever in flight
	// and the test fails.
	var mu sync.Mutex
	started, inFlight, maxInFlight := 0, 0, 0
	two, closed := make(chan struct{}), false
	withWarmHook(t, func(string) {
		mu.Lock()
		started++
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		if inFlight == 2 && !closed {
			closed = true
			close(two)
		}
		mu.Unlock()
		select {
		case <-two:
		case <-time.After(5 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
	})

	w := f.runtime(t)
	w.compileWorkers = len(ids)
	f.sync(t, w)

	mu.Lock()
	defer mu.Unlock()
	if started != len(ids) {
		t.Fatalf("%d parallel compiles started, want %d", started, len(ids))
	}
	if maxInFlight < 2 {
		t.Fatalf("at most %d compile in flight, want >= 2", maxInFlight)
	}
}

func TestWasmCompileWarm_AttributesEachEntry(t *testing.T) {
	f := newCompileCacheFixture(t)
	ids := []string{"com.test.attr.a", "com.test.attr.b", "com.test.attr.c"}
	for _, id := range ids {
		f.install(t, id, "1.0.0", variant(f.guest, id))
	}
	var warmed sync.Map
	withWarmHook(t, func(id string) { warmed.Store(id, true) })

	w1 := f.runtime(t)
	w1.compileWorkers = len(ids)
	f.sync(t, w1)

	entries := cacheEntries(t, w1)
	if len(entries) != len(ids) {
		t.Fatalf("%d cache entries %v, want %d", len(entries), entries, len(ids))
	}
	idx := readCacheIndex(t, f.cacheDir)
	seen := map[string]string{}
	for _, id := range ids {
		if _, ok := warmed.Load(id); !ok {
			t.Errorf("%s was not compiled in parallel", id)
		}
		rec := idx[id]
		if rec.Version != "1.0.0" || rec.Entry == "" {
			t.Fatalf("%s: index record %+v", id, rec)
		}
		if other, dup := seen[rec.Entry]; dup {
			t.Fatalf("%s and %s both attributed to entry %s", id, other, rec.Entry)
		}
		seen[rec.Entry] = id
		if got := lastLoad(w1, id); got != wasmLoadCompiledParallel {
			t.Errorf("%s: load outcome %q, want %q", id, got, wasmLoadCompiledParallel)
		}
	}
	// Staging dirs are gone once the entries moved in.
	if des, _ := os.ReadDir(filepath.Join(f.cacheDir, compileStagingDir)); len(des) != 0 {
		t.Fatalf("staging dir not empty: %d leftovers", len(des))
	}
	w1.mu.Lock()
	unattributed := len(w1.cache.unattributed)
	w1.mu.Unlock()
	if unattributed != 0 {
		t.Fatalf("%d unattributed plugins, want 0", unattributed)
	}

	// Each attribution is right, not just distinct: deleting one plugin's
	// recorded entry makes exactly that plugin miss on the next start.
	victim := ids[1]
	if err := os.Remove(filepath.Join(w1.cache.versionedDir, idx[victim].Entry)); err != nil {
		t.Fatal(err)
	}
	warmed = sync.Map{}
	w2 := f.runtime(t)
	f.sync(t, w2)
	for _, id := range ids {
		want := wasmLoadCacheHit
		if id == victim {
			want = wasmLoadCompiled // one miss compiles serially
		}
		if got := lastLoad(w2, id); got != want {
			t.Errorf("restart: %s load outcome %q, want %q", id, got, want)
		}
	}

	// Pruning still works on parallel-compiled entries: an uninstall drops
	// exactly that plugin's entry.
	gone := ids[0]
	goneEntry := readCacheIndex(t, f.cacheDir)[gone].Entry
	if _, err := f.db.Exec(`DELETE FROM plugins WHERE id = ?`, gone); err != nil {
		t.Fatal(err)
	}
	f.sync(t, w2)
	after := cacheEntries(t, w2)
	if len(after) != len(ids)-1 || strings.Contains(strings.Join(after, ","), goneEntry) {
		t.Fatalf("after uninstall: entries %v, want %d without %s", after, len(ids)-1, goneEntry)
	}
}

// Plugins shipping the same bytes are compiled once and share the entry.
func TestWasmCompileWarm_SharedBytesCompileOnce(t *testing.T) {
	f := newCompileCacheFixture(t)
	f.install(t, "com.test.share.a", "1.0.0", f.guest)
	f.install(t, "com.test.share.b", "1.0.0", f.guest)
	f.install(t, "com.test.share.c", "1.0.0", variant(f.guest, "c"))
	var mu sync.Mutex
	compiles := 0
	withWarmHook(t, func(string) { mu.Lock(); compiles++; mu.Unlock() })

	w := f.runtime(t)
	w.compileWorkers = 4
	f.sync(t, w)

	if compiles != 2 {
		t.Fatalf("%d parallel compiles, want 2 (one per distinct module)", compiles)
	}
	idx := readCacheIndex(t, f.cacheDir)
	a, b, c := idx["com.test.share.a"], idx["com.test.share.b"], idx["com.test.share.c"]
	if a.Entry == "" || a.Entry != b.Entry || c.Entry == "" || c.Entry == a.Entry {
		t.Fatalf("records a=%+v b=%+v c=%+v", a, b, c)
	}
	if n := len(cacheEntries(t, w)); n != 2 {
		t.Fatalf("%d cache entries, want 2", n)
	}
}

// A single miss, or misses next to cache hits, gains nothing from the
// parallel pass: it stays on the serial path.
func TestWasmCompileWarm_SkipsWhenFewerThanTwoMisses(t *testing.T) {
	f := newCompileCacheFixture(t)
	f.install(t, "com.test.one.a", "1.0.0", variant(f.guest, "a"))
	compiles := 0
	withWarmHook(t, func(string) { compiles++ })

	w1 := f.runtime(t)
	w1.compileWorkers = 4
	f.sync(t, w1)
	f.install(t, "com.test.one.b", "1.0.0", variant(f.guest, "b"))
	w2 := f.runtime(t)
	w2.compileWorkers = 4
	f.sync(t, w2)

	if compiles != 0 {
		t.Fatalf("%d parallel compiles, want 0", compiles)
	}
	if got := lastLoad(w2, "com.test.one.a"); got != wasmLoadCacheHit {
		t.Errorf("a: %q, want %q", got, wasmLoadCacheHit)
	}
	if got := lastLoad(w2, "com.test.one.b"); got != wasmLoadCompiled {
		t.Errorf("b: %q, want %q", got, wasmLoadCompiled)
	}
}

// A module that fails in the parallel pass is left to the serial path,
// which marks it broken as before; the others still load from the cache.
func TestWasmCompileWarm_BrokenModuleLeftToSerialPath(t *testing.T) {
	f := newCompileCacheFixture(t)
	f.install(t, "com.test.mix.a", "1.0.0", variant(f.guest, "a"))
	f.install(t, "com.test.mix.b", "1.0.0", variant(f.guest, "b"))
	f.install(t, "com.test.mix.bad", "1.0.0", []byte("\x00asm\x01\x00\x00\x00garbage"))

	w := f.runtime(t)
	w.compileWorkers = 3
	f.sync(t, w)

	for _, id := range []string{"com.test.mix.a", "com.test.mix.b"} {
		if got := lastLoad(w, id); got != wasmLoadCompiledParallel {
			t.Errorf("%s: %q, want %q", id, got, wasmLoadCompiledParallel)
		}
	}
	var state string
	if err := f.db.QueryRow(`SELECT install_state FROM plugins WHERE id = ?`, "com.test.mix.bad").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "broken" {
		t.Fatalf("bad module install_state %q, want broken", state)
	}
	w.mu.Lock()
	bad := w.cache.bad
	w.mu.Unlock()
	if bad {
		t.Fatal("a broken module turned the cache off")
	}
}

// Staging dirs a killed start left behind are removed when the cache opens.
func TestWasmCompileWarm_StagingLeftoversRemovedOnOpen(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "wasm-cache")
	left := filepath.Join(cacheDir, compileStagingDir, "c-123", "wazero-x", "entry")
	if err := writeFileWithParents(left, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	w := NewWasmRuntimeWithCache(t.TempDir(), cacheDir)
	t.Cleanup(func() { w.Close(context.Background()) })
	openCache(t, w)
	if _, err := os.Stat(filepath.Join(cacheDir, compileStagingDir)); !os.IsNotExist(err) {
		t.Fatalf("staging leftovers still there (%v)", err)
	}
}

func TestCompileWarmWorkers(t *testing.T) {
	procs := goruntime.GOMAXPROCS(0)
	for _, tc := range []struct{ jobs, configured, want int }{
		{jobs: 3, configured: 2, want: 2},
		{jobs: 2, configured: 8, want: 2},
		{jobs: 1000, configured: 0, want: procs}, // default: GOMAXPROCS
		{jobs: 1, configured: 0, want: 1},
	} {
		if got := compileWarmWorkers(tc.jobs, tc.configured); got != tc.want {
			t.Errorf("compileWarmWorkers(%d, %d) = %d, want %d", tc.jobs, tc.configured, got, tc.want)
		}
	}
}

func TestMemoryBoundWorkers(t *testing.T) {
	mod := func(mib int) *warmJob { return &warmJob{raw: make([]byte, mib<<20)} }
	jobs := []*warmJob{mod(1), mod(5), mod(2), mod(5)} // working sets 32/160/64/160 MiB
	const mib = 1 << 20
	for _, tc := range []struct {
		workers int
		avail   uint64
		want    int
	}{
		{workers: 4, avail: 4096 * mib, want: 4}, // plenty: GOMAXPROCS bound only
		{workers: 4, avail: 640 * mib, want: 2},  // budget 320: the two 5 MiB modules
		{workers: 4, avail: 300 * mib, want: 1},  // budget 150: not even one → serial
		{workers: 2, avail: 4096 * mib, want: 2}, // never above the CPU bound
	} {
		if got := memoryBoundWorkers(tc.workers, jobs, tc.avail); got != tc.want {
			t.Errorf("memoryBoundWorkers(%d, avail %d MiB) = %d, want %d", tc.workers, tc.avail/mib, got, tc.want)
		}
	}
}

// One worker gains nothing over the serial path, so the pass is skipped.
func TestWasmCompileWarm_SkipsWithOneWorker(t *testing.T) {
	f := newCompileCacheFixture(t)
	f.install(t, "com.test.w1.a", "1.0.0", variant(f.guest, "a"))
	f.install(t, "com.test.w1.b", "1.0.0", variant(f.guest, "b"))
	compiles := 0
	withWarmHook(t, func(string) { compiles++ })

	w := f.runtime(t)
	w.compileWorkers = 1
	f.sync(t, w)

	if compiles != 0 {
		t.Fatalf("%d parallel compiles with one worker, want 0", compiles)
	}
	if got := lastLoad(w, "com.test.w1.b"); got != wasmLoadCompiled {
		t.Errorf("b: %q, want %q", got, wasmLoadCompiled)
	}
}

// TestWasmCompileWarm_Measure is the Pi measurement harness, off by default:
//
//	UT_WASM_COMPILE_MEASURE=/path/plugin.wasm UT_WASM_COMPILE_N=3 \
//	  go test ./internal/plugins -run TestWasmCompileWarm_Measure -v -count=1
//
// (or build the test binary with GOARCH=arm64 go test -c and copy it to the
// Pi). It installs N copies of the module (distinct bytes, so N cache
// misses) and times a first start serially (1 worker: the serial path),
// then in parallel (GOMAXPROCS workers, fewer if memory bounds them), each
// on an empty cache, logging wall time and the
// process's peak RSS. Run serial and parallel in separate processes
// (UT_WASM_COMPILE_WORKERS=1 / =0) for a per-mode peak RSS: VmHWM never drops.
func TestWasmCompileWarm_Measure(t *testing.T) {
	path := os.Getenv("UT_WASM_COMPILE_MEASURE")
	if path == "" {
		t.Skip("set UT_WASM_COMPILE_MEASURE=<plugin.wasm> to measure")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(os.Getenv("UT_WASM_COMPILE_N"))
	if n < 2 {
		n = 3
	}
	modes := []int{1, 0}
	if v := os.Getenv("UT_WASM_COMPILE_WORKERS"); v != "" {
		k, _ := strconv.Atoi(v)
		modes = []int{k}
	}
	for _, workers := range modes {
		f := &compileCacheFixture{
			db:       managerTestDB(t),
			base:     t.TempDir(),
			cacheDir: filepath.Join(t.TempDir(), "wasm-cache"),
			guest:    raw,
		}
		for i := 0; i < n; i++ {
			id := "com.test.measure." + strconv.Itoa(i)
			f.install(t, id, "1.0.0", variant(raw, id))
		}
		w := f.runtime(t)
		w.compileWorkers = workers
		start := time.Now()
		f.sync(t, w)
		took := time.Since(start)
		t.Logf("workers=%d (0 = GOMAXPROCS %d), %d modules of %.1f MiB: first start %s, peak RSS so far %s",
			workers, goruntime.GOMAXPROCS(0), n, float64(len(raw))/(1<<20), took.Round(time.Millisecond), peakRSS())
	}
}

// peakRSS is VmHWM from /proc/self/status ("n/a" off Linux).
func peakRSS() string {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "n/a"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
		}
	}
	return "n/a"
}
