package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// stubTokenProvider satisfies oauth.TokenProvider. Auth is optional on the
// catalog endpoint, so returning an error means "proceed unauthenticated".
type stubTokenProvider struct{}

func (stubTokenProvider) GetToken(context.Context) (string, error) {
	return "", fmt.Errorf("no token in tests")
}

func (stubTokenProvider) ClearCache() error { return nil }

// newTestCatalogRepo builds a real CatalogRepository against a local httptest
// marketplace. Hermetic: loopback only, cache under t.TempDir.
func newTestCatalogRepo(t *testing.T, serverURL string) *marketplace.CatalogRepository {
	t.Helper()
	cfg := &config.MarketplaceConfig{
		EndpointURL:       serverURL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
	}
	client := marketplace.NewClient(cfg, stubTokenProvider{})
	repo, err := marketplace.NewCatalogRepository(client, t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	return repo
}

// catalogServer is an httptest marketplace catalog endpoint that records the
// `arch` query param of every request it serves.
func catalogServer(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	archSeen := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archSeen <- r.URL.Query().Get("arch")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"plugins": []any{}})
	}))
	t.Cleanup(srv.Close)
	return srv, archSeen
}

// syncBuffer is a mutex-guarded buffer so a background goroutine's logger can
// be read by the test without a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The background catalog sync must request the catalog for the architecture
// the till is actually running on — the same runtime.GOOS/runtime.GOARCH every
// interactive path (plugins_store_page, cloudsync_wire, plugin_api) sends. A
// hardcoded arch means an arm64 till's scheduler overwrites the cache with an
// amd64-filtered catalog.
func TestSyncCatalog_SendsRealDeviceArch(t *testing.T) {
	// Pass-through half, sentinel-pinned: on linux/amd64 CI the real runtime
	// arch equals the historical hardcoded "linux/amd64", so asserting the
	// runtime value alone would be blind to a re-hardcoding there. The
	// sentinel proves syncCatalog sends exactly what deviceArchOf returns,
	// on every platform.
	oldArch := deviceArchOf
	deviceArchOf = func() string { return "sentinelos/sentinelarch" }
	t.Cleanup(func() { deviceArchOf = oldArch })

	srv, archSeen := catalogServer(t)
	bj := &BackgroundJobs{
		catalogRepo: newTestCatalogRepo(t, srv.URL),
		cfg:         &config.Config{DefaultLocale: "en-US"},
		logger:      log.New(io.Discard, "", 0),
	}

	bj.syncCatalog(context.Background())

	select {
	case got := <-archSeen:
		if got != "sentinelos/sentinelarch" {
			t.Fatalf("background sync requested arch %q, want the deviceArchOf value to pass through verbatim", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no catalog request reached the marketplace")
	}
}

// The default deviceArchOf computes from the actual runtime, matching what
// every interactive path sends. (Red on any non-amd64-linux box if someone
// re-hardcodes it; the sentinel test above covers the pass-through on CI.)
func TestDeviceArchOf_ReportsRuntime(t *testing.T) {
	want := runtime.GOOS + "/" + runtime.GOARCH
	if got := deviceArchOf(); got != want {
		t.Fatalf("deviceArchOf() = %q, want %q", got, want)
	}
}

// Revocation checking only exists when a marketplace endpoint is configured —
// without one there is nothing to poll.
func TestNewBackgroundJobs_RevocationCheckerGatedOnEndpoint(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	noEndpoint := NewBackgroundJobs(nil, nil, nil, &config.Config{}, logger)
	if noEndpoint.revocationChecker != nil {
		t.Fatal("revocation checker created without a marketplace endpoint")
	}
	if noEndpoint.telemetryClient == nil {
		t.Fatal("telemetry client should always be constructed")
	}
	if noEndpoint.retryBaseDelay <= 0 {
		t.Fatalf("retryBaseDelay not initialised: %v", noEndpoint.retryBaseDelay)
	}

	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = "http://127.0.0.1:1"
	withEndpoint := NewBackgroundJobs(nil, nil, nil, cfg, logger)
	if withEndpoint.revocationChecker == nil {
		t.Fatal("revocation checker missing despite a configured endpoint")
	}
}

// A till with no marketplace configured has a nil catalog repo — the sync must
// be a quiet no-op, not a panic.
func TestSyncCatalog_NilRepoIsNoop(t *testing.T) {
	bj := &BackgroundJobs{
		cfg:    &config.Config{},
		logger: log.New(io.Discard, "", 0),
	}
	bj.syncCatalog(context.Background()) // must not panic
}

// A failing marketplace is retried exactly 3 times with backoff, then given up
// on with a logged failure — never an infinite loop, never a crash.
func TestSyncCatalog_RetriesWithBackoffThenGivesUp(t *testing.T) {
	requests := make(chan struct{}, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	logBuf := &syncBuffer{}
	bj := &BackgroundJobs{
		catalogRepo:    newTestCatalogRepo(t, srv.URL),
		cfg:            &config.Config{DefaultLocale: "en-US"},
		logger:         log.New(logBuf, "", 0),
		retryBaseDelay: time.Millisecond,
	}

	bj.syncCatalog(context.Background())

	if got := len(requests); got != 3 {
		t.Fatalf("marketplace saw %d requests, want exactly 3 attempts", got)
	}
	if !strings.Contains(logBuf.String(), "catalog sync failed after 3 attempts") {
		t.Fatalf("final failure not logged; log was:\n%s", logBuf.String())
	}
}

// A transient failure recovers on retry: fail once, succeed on attempt 2, stop.
func TestSyncCatalog_SucceedsAfterRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"plugins": []any{}})
	}))
	t.Cleanup(srv.Close)

	logBuf := &syncBuffer{}
	bj := &BackgroundJobs{
		catalogRepo:    newTestCatalogRepo(t, srv.URL),
		cfg:            &config.Config{DefaultLocale: "en-US"},
		logger:         log.New(logBuf, "", 0),
		retryBaseDelay: time.Millisecond,
	}

	bj.syncCatalog(context.Background())

	if got := calls.Load(); got != 2 {
		t.Fatalf("marketplace saw %d requests, want 2 (fail, then success)", got)
	}
	if !strings.Contains(logBuf.String(), "catalog sync successful") {
		t.Fatalf("success not logged; log was:\n%s", logBuf.String())
	}
}

// Shutdown during the retry backoff must abort promptly instead of sleeping
// through the remaining delay.
func TestSyncCatalog_AbortsBackoffOnShutdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	bj := &BackgroundJobs{
		catalogRepo:    newTestCatalogRepo(t, srv.URL),
		cfg:            &config.Config{DefaultLocale: "en-US"},
		logger:         log.New(io.Discard, "", 0),
		retryBaseDelay: time.Hour, // without the ctx-aware backoff this would hang
	}

	done := make(chan struct{})
	go func() {
		bj.syncCatalog(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let attempt 1 fail and enter backoff
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncCatalog did not abort its backoff on context cancel")
	}
}

// plantSnapshot writes a catalog snapshot file directly into the repo's cache
// dir with the given age, so tests control staleness without network calls.
func plantSnapshot(t *testing.T, cacheDir string, fetchedAt time.Time) {
	t.Helper()
	snap := map[string]any{
		"plugins":          []any{},
		"snapshot_version": 1,
		"fetched_at":       fetchedAt,
		"locale":           "en-US",
		"device_arch":      runtime.GOOS + "/" + runtime.GOARCH,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "catalog-snapshot.json"), raw, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func newTestCatalogRepoAt(t *testing.T, serverURL, cacheDir string) *marketplace.CatalogRepository {
	t.Helper()
	cfg := &config.MarketplaceConfig{
		EndpointURL:       serverURL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
	}
	client := marketplace.NewClient(cfg, stubTokenProvider{})
	repo, err := marketplace.NewCatalogRepository(client, cacheDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	return repo
}

// LOCAL-FIRST: the scheduler only re-fetches when a cached catalog exists AND
// has gone stale — and once refreshed, the now-fresh cache stops further
// fetches until it ages again.
func TestBackgroundJobsStart_SyncsWhenStaleThenStops(t *testing.T) {
	srv, archSeen := catalogServer(t)
	cacheDir := t.TempDir()
	plantSnapshot(t, cacheDir, time.Now().Add(-time.Hour)) // stale (>15m)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bj := &BackgroundJobs{
		catalogRepo:         newTestCatalogRepoAt(t, srv.URL, cacheDir),
		cfg:                 &config.Config{DefaultLocale: "en-US"},
		logger:              log.New(io.Discard, "", 0),
		catalogSyncInterval: 20 * time.Millisecond,
		telemetryInterval:   time.Hour,
		revocationInterval:  time.Hour,
		retryBaseDelay:      time.Millisecond,
	}
	bj.Start(ctx, &sync.WaitGroup{})

	select {
	case <-archSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("stale cache never triggered a catalog sync")
	}
	// The sync refreshed the cache; further ticks must NOT fetch again.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-archSeen:
		t.Fatal("scheduler kept fetching after the cache was refreshed — local-first violated")
	default:
	}
}

// A fresh cache is left alone entirely.
func TestBackgroundJobsStart_SkipsWhenFresh(t *testing.T) {
	srv, archSeen := catalogServer(t)
	cacheDir := t.TempDir()
	plantSnapshot(t, cacheDir, time.Now()) // fresh

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bj := &BackgroundJobs{
		catalogRepo:         newTestCatalogRepoAt(t, srv.URL, cacheDir),
		cfg:                 &config.Config{DefaultLocale: "en-US"},
		logger:              log.New(io.Discard, "", 0),
		catalogSyncInterval: 20 * time.Millisecond,
		telemetryInterval:   time.Hour,
		revocationInterval:  time.Hour,
		retryBaseDelay:      time.Millisecond,
	}
	bj.Start(ctx, &sync.WaitGroup{})

	time.Sleep(200 * time.Millisecond)
	select {
	case <-archSeen:
		t.Fatal("fresh cache triggered a catalog sync")
	default:
	}
}

// Context cancellation stops the scheduler loops: with a permanently failing
// marketplace the cache stays stale (requests keep flowing), until cancel.
func TestBackgroundJobsStart_CancelStopsLoops(t *testing.T) {
	requests := make(chan struct{}, 256)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- struct{}{}:
		default:
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cacheDir := t.TempDir()
	plantSnapshot(t, cacheDir, time.Now().Add(-time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	bj := &BackgroundJobs{
		catalogRepo:         newTestCatalogRepoAt(t, srv.URL, cacheDir),
		cfg:                 &config.Config{DefaultLocale: "en-US"},
		logger:              log.New(io.Discard, "", 0),
		catalogSyncInterval: 20 * time.Millisecond,
		telemetryInterval:   time.Hour,
		revocationInterval:  time.Hour,
		retryBaseDelay:      time.Millisecond,
	}
	bj.Start(ctx, &wg)

	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never reached the marketplace")
	}

	// Before cancel: wg must NOT already be at zero (a missing wg.Add on any
	// of the 3 job goroutines would let Wait return instantly, vacuously
	// "passing" the check below without ever tracking them).
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — job goroutines not tracked")
	}

	cancel()

	// Deterministic proof the 3 job goroutines actually exited (not just a
	// timing-based inference from silence on the requests channel below).
	if !waitWithin(&wg, 5*time.Second) {
		t.Fatal("BackgroundJobs goroutines did not join wg within 5s of context cancel")
	}

	time.Sleep(100 * time.Millisecond) // let in-flight attempts finish
	for {                              // drain everything that raced the cancel
		select {
		case <-requests:
			continue
		default:
		}
		break
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case <-requests:
		t.Fatal("scheduler kept polling after context cancel")
	default:
	}
}

// The telemetry tick logs failures instead of crashing the scheduler — proven
// with a real TelemetryClient over a closed DB.
func TestBackgroundJobsStart_TelemetryFailureIsLoggedNotFatal(t *testing.T) {
	d, err := appdb.Open(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB := d.DB
	d.Close() // every query now fails

	logBuf := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bj := &BackgroundJobs{
		telemetryClient:     plugins.NewTelemetryClient(sqlDB, "http://127.0.0.1:1", "dev", "merchant", "store"),
		cfg:                 &config.Config{},
		logger:              log.New(logBuf, "", 0),
		catalogSyncInterval: time.Hour,
		telemetryInterval:   20 * time.Millisecond,
		revocationInterval:  time.Hour,
	}
	bj.Start(ctx, &sync.WaitGroup{})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "telemetry report failed") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("telemetry failure never logged; log was:\n%s", logBuf.String())
}

// The revocation tick polls the marketplace's revocation feed when configured.
func TestBackgroundJobsStart_RevocationTickPollsFeed(t *testing.T) {
	requests := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"revocations":[]}`))
	}))
	t.Cleanup(srv.Close)

	logBuf := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bj := &BackgroundJobs{
		revocationChecker:   plugins.NewRevocationChecker(nil, srv.URL, nil),
		cfg:                 &config.Config{},
		logger:              log.New(logBuf, "", 0),
		catalogSyncInterval: time.Hour,
		telemetryInterval:   time.Hour,
		revocationInterval:  20 * time.Millisecond,
	}
	bj.Start(ctx, &sync.WaitGroup{})

	select {
	case path := <-requests:
		if path != "/v1/revocations" {
			t.Fatalf("revocation poll hit %q, want /v1/revocations", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation feed never polled")
	}
	if !strings.Contains(logBuf.String(), "checking for revoked plugins") {
		t.Fatalf("revocation check not logged; log was:\n%s", logBuf.String())
	}
}

// stubBrowser swaps startBrowser for a channel-reporting stub for the duration
// of the test — a real browser must never be spawned from a test run.
func stubBrowser(t *testing.T) chan string {
	t.Helper()
	urls := make(chan string, 4)
	old := startBrowser
	startBrowser = func(u string) {
		select {
		case urls <- u:
		default:
		}
	}
	t.Cleanup(func() { startBrowser = old })
	return urls
}

// Start serves the handler on the configured (here: ephemeral) port, opens the
// operator's browser at the bound address once accepting, records the actual
// address into cfg.ListenAddr, and shuts down cleanly (returning nil) when the
// context is cancelled.
func TestStart_ServesOpensBrowserAndShutsDown(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "true")
	urls := stubBrowser(t)

	cfg := &config.Config{ListenAddr: "127.0.0.1:0"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "till-ok")
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	go func() { errCh <- Start(ctx, cfg, handler, nil, nil, nil, nil, &wg) }()

	var url string
	select {
	case url = <-urls:
	case <-time.After(10 * time.Second):
		t.Fatal("browser open never triggered")
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "till-ok" {
		t.Fatalf("GET %s -> %d %q, want 200 till-ok", url, resp.StatusCode, body)
	}

	// Before cancel: wg must NOT already be at zero — the shutdown goroutine
	// is alive but parked on <-ctx.Done(), so a missing wg.Add there would
	// let this vacuously "pass" without ever tracking it.
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — shutdown goroutine not tracked")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start returned %v on graceful shutdown, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}
	// Start's own graceful-shutdown goroutine (srv.Shutdown + supervisor.Shutdown)
	// must have fully finished by now too, not just be "probably done" —
	// net/http's Shutdown contract only guarantees Serve returns once listeners
	// close, so without wg tracking that goroutine, this could otherwise race.
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("wg did not reach zero within 2s of Start returning — shutdown goroutine not joined")
	}
	// Safe to read cfg now that Start has returned: it must hold the address
	// actually bound (an ephemeral port, not the :0 we asked for).
	if cfg.ListenAddr == "127.0.0.1:0" {
		t.Fatal("cfg.ListenAddr was not updated to the bound address")
	}
	if url != "http://"+cfg.ListenAddr {
		t.Fatalf("browser opened %q but the server bound %q", url, cfg.ListenAddr)
	}
}

// An unbindable address surfaces as an error, not a hang.
func TestStart_ReturnsErrorOnBadAddr(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "false")
	cfg := &config.Config{ListenAddr: "not-a-listen-addr"}
	err := Start(context.Background(), cfg, http.NewServeMux(), nil, nil, nil, nil, &sync.WaitGroup{})
	if err == nil {
		t.Fatal("Start succeeded on an unbindable address")
	}
}

// waitWithin reports whether wg.Wait() returns within d.
func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// With a DB configured, Start kicks off the related-items rebuild at startup
// (tills reboot daily — the strip must be fresh without waiting 24h).
func TestStart_WithDBRunsRelatedItemsRebuild(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "false")
	logBuf := &syncBuffer{}
	oldOut := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{ListenAddr: "127.0.0.1:0", DBPath: dbPath}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	go func() { errCh <- Start(ctx, cfg, http.NewServeMux(), nil, d.DB, nil, nil, &wg) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "[RelatedItems] rebuilt") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "[RelatedItems] rebuilt") {
		t.Fatalf("related-items rebuild never ran at startup; log:\n%s", logBuf.String())
	}

	// Before cancel: wg must NOT already be at zero — the daily-backup and
	// related-items goroutines are alive, parked on their own tickers, so a
	// missing wg.Add on either would let this vacuously "pass" below.
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — a background goroutine was not tracked")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	// The related-items ticker goroutine (which reads db) must have actually
	// exited by now — this test's own t.Cleanup closes d right after, and a
	// straggler reading a closed *sql.DB is exactly the bug class this
	// wg-joining fix exists to prevent.
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("wg did not reach zero within 2s of Start returning — a background goroutine was left unjoined")
	}
}

// The daily backup snapshots when no fresh backup exists, then skips while one
// is newer than 24h — and never duplicates.
func TestRunDailyBackup_SnapshotsOnceThenSkipsFresh(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	runDailyBackup(d.DB, dbPath)
	list, err := appdb.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("after first run: %d backups, want 1", len(list))
	}

	// Age the backup to 2h old — still fresh (<24h), so the next run must
	// skip. Rename it to an older timestamped name AND backdate its mtime:
	// Snapshot() reuses a same-second filename, so without the rename an
	// always-snapshot regression would be invisible to this test.
	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	oldName := "unitill-pos-" + time.Now().UTC().Add(-2*time.Hour).Format("20060102-150405") + ".db"
	if err := os.Rename(filepath.Join(backupDir, list[0].Name), filepath.Join(backupDir, oldName)); err != nil {
		t.Fatalf("rename backup: %v", err)
	}
	aged := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(backupDir, oldName), aged, aged); err != nil {
		t.Fatalf("backdate backup: %v", err)
	}

	runDailyBackup(d.DB, dbPath) // 2h-old backup exists → must skip
	list, err = appdb.ListBackups(dbPath)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("after second run: %d backups, want still 1 (fresh one skipped)", len(list))
	}
	if list[0].Name != oldName {
		t.Fatalf("second run replaced the backup (%q -> %q) instead of skipping", oldName, list[0].Name)
	}
}

// A failing snapshot is logged, not fatal.
func TestRunDailyBackup_SnapshotFailureLogged(t *testing.T) {
	logBuf := &syncBuffer{}
	oldOut := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	d.Close() // snapshot over a closed DB must fail

	runDailyBackup(d.DB, dbPath)
	if !strings.Contains(logBuf.String(), "[Backup] snapshot failed") {
		t.Fatalf("snapshot failure not logged; log:\n%s", logBuf.String())
	}
}

// An address that can't be parsed surfaces the original bind error.
func TestListenWithFallback_UnparseableAddr(t *testing.T) {
	ln, _, err := listenWithFallback("completely bogus")
	if err == nil {
		ln.Close()
		t.Fatal("bound a nonsense address")
	}
}

// When the requested port AND the next 20 are all busy, the OS picks any free
// port — startup still succeeds.
func TestListenWithFallback_LastResortAnyFreePort(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind base: %v", err)
	}
	defer base.Close()
	_, portStr, _ := net.SplitHostPort(base.Addr().String())
	basePort, _ := strconv.Atoi(portStr)

	// Occupy base+1..base+20 ourselves; any we can't bind are busy anyway.
	// (Tiny TOCTOU window: a port busy at hold time could free before the
	// call under test and be bound in-range — vanishingly rare on CI, where
	// these ports are almost always free for us to hold.)
	var held []net.Listener
	defer func() {
		for _, l := range held {
			l.Close()
		}
	}()
	for p := basePort + 1; p <= basePort+20; p++ {
		if l, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p))); e == nil {
			held = append(held, l)
		}
	}

	ln, addr, err := listenWithFallback(base.Addr().String())
	if err != nil {
		t.Fatalf("last-resort bind failed: %v", err)
	}
	defer ln.Close()
	_, gotPortStr, _ := net.SplitHostPort(addr)
	gotPort, _ := strconv.Atoi(gotPortStr)
	if gotPort >= basePort && gotPort <= basePort+20 {
		t.Fatalf("bound %d inside the busy range %d..%d", gotPort, basePort, basePort+20)
	}
}

func TestShouldOpenBrowser(t *testing.T) {
	cases := []struct {
		name     string
		openEnv  string
		kioskEnv string
		want     bool
	}{
		{"explicit on", "true", "", true},
		{"explicit off", "false", "", false},
		{"garbage value means off", "sideways", "", false},
		{"kiosk suppresses", "", "1", false},
		{"kiosk garbage ignored", "", "sideways", true},
		{"default on", "", "", true},
		{"explicit on wins over kiosk", "1", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_OPEN_BROWSER", tc.openEnv)
			t.Setenv("UT_KIOSK", tc.kioskEnv)
			if got := shouldOpenBrowser(); got != tc.want {
				t.Fatalf("shouldOpenBrowser() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Wildcard bind hosts are normalized to localhost so the opened URL is
// actually reachable from the operator's browser.
func TestOpenSetupPage_NormalizesWildcardHost(t *testing.T) {
	urls := stubBrowser(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	openSetupPage(net.JoinHostPort("0.0.0.0", port))

	select {
	case got := <-urls:
		want := "http://localhost:" + port
		if got != want {
			t.Fatalf("opened %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser never opened")
	}
}

// A malformed listen address is a silent no-op — never a crash, never a
// browser pointed at garbage.
func TestOpenSetupPage_BadAddrIsNoop(t *testing.T) {
	urls := stubBrowser(t)
	openSetupPage("garbage")
	select {
	case got := <-urls:
		t.Fatalf("browser opened %q for a garbage address", got)
	default:
	}
}

// The browser open waits for the server to start accepting: a listener that
// appears shortly after the call still gets the page opened at the right URL.
func TestOpenSetupPage_WaitsForListener(t *testing.T) {
	urls := stubBrowser(t)
	// Reserve a port, free it, then re-bind it after a delay.
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	addr := tmp.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	tmp.Close()

	lateCh := make(chan net.Listener, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		if l, e := net.Listen("tcp", addr); e == nil {
			lateCh <- l
		}
	}()
	t.Cleanup(func() {
		select {
		case l := <-lateCh:
			l.Close()
		default:
		}
	})

	start := time.Now()
	openSetupPage(addr)
	select {
	case got := <-urls:
		want := "http://127.0.0.1:" + port
		if got != want {
			t.Fatalf("opened %q, want %q", got, want)
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("open took %v — the accept-wait loop is not working", time.Since(start))
		}
	default:
		t.Fatal("browser never opened")
	}
}
