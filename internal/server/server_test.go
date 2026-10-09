package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/housekeeping"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/stagedupload"
	"github.com/universaltill/universal-till/internal/testsupport"
)

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

// Revocation checking only exists when a marketplace endpoint is configured —
// without one there is nothing to poll.
func TestNewBackgroundJobs_RevocationCheckerGatedOnEndpoint(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	noEndpoint := NewBackgroundJobs(nil, nil, &config.Config{}, logger)
	if noEndpoint.revocationChecker != nil {
		t.Fatal("revocation checker created without a marketplace endpoint")
	}
	if noEndpoint.telemetryClient == nil {
		t.Fatal("telemetry client should always be constructed")
	}

	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = "http://127.0.0.1:1"
	withEndpoint := NewBackgroundJobs(nil, nil, cfg, logger)
	if withEndpoint.revocationChecker == nil {
		t.Fatal("revocation checker missing despite a configured endpoint")
	}
}

// Context cancellation stops the scheduler loops: with a permanently failing
// marketplace the revocation poll keeps firing, until cancel.
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

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	bj := &BackgroundJobs{
		revocationChecker:  plugins.NewRevocationChecker(nil, srv.URL, nil),
		cfg:                &config.Config{},
		logger:             log.New(io.Discard, "", 0),
		telemetryInterval:  time.Hour,
		revocationInterval: 20 * time.Millisecond,
	}
	bj.Start(ctx, &wg)

	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never reached the marketplace")
	}

	// Before cancel: wg must NOT already be at zero (a missing wg.Add on any
	// of the 2 job goroutines would let Wait return instantly, vacuously
	// "passing" the check below without ever tracking them).
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — job goroutines not tracked")
	}

	cancel()

	// Deterministic proof the 2 job goroutines actually exited (not just a
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
		telemetryClient: plugins.NewTelemetryClient(sqlDB, func() plugins.TelemetryIdentity {
			return plugins.TelemetryIdentity{EndpointURL: "http://127.0.0.1:1", DeviceID: "dev", MerchantID: "merchant", StoreID: "store", Token: "tok"}
		}),
		cfg:                &config.Config{},
		logger:             log.New(logBuf, "", 0),
		telemetryInterval:  20 * time.Millisecond,
		revocationInterval: time.Hour,
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
		revocationChecker:  plugins.NewRevocationChecker(nil, srv.URL, nil),
		cfg:                &config.Config{},
		logger:             log.New(logBuf, "", 0),
		telemetryInterval:  time.Hour,
		revocationInterval: 20 * time.Millisecond,
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
// operator's browser at the address actually bound once accepting, leaves the
// shared cfg untouched (ut-docs#2990), and shuts down cleanly (returning nil)
// when the context is cancelled.
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
	go func() { errCh <- Start(ctx, cfg, handler, nil, nil, nil, &wg) }()

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
	// The browser must have been sent to the port actually bound (an
	// ephemeral one), not the :0 we asked for.
	if url == "http://127.0.0.1:0" || !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("browser opened %q, want the bound 127.0.0.1:<port> address", url)
	}
	// ut-docs#2990: Start must not write the bound address back into the
	// shared config. Background goroutines started before Start (the cloud
	// link, via enroll.Effective) copy *cfg concurrently, so a write here is
	// a data race on every boot.
	if cfg.ListenAddr != "127.0.0.1:0" {
		t.Fatalf("Start rewrote cfg.ListenAddr to %q; the shared config must stay read-only", cfg.ListenAddr)
	}
}

// ut-docs#2990: goroutines started before Start read the shared config while
// Start binds. Under -race this fails if Start writes any field of *cfg.
func TestStart_DoesNotWriteSharedConfigWhileOthersReadIt(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "false")
	cfg := &config.Config{ListenAddr: "127.0.0.1:0"}
	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				c := *cfg // what enroll.Effective does per call
				_ = c
				runtime.Gosched()
			}
		}
	}()
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	go func() {
		errCh <- Start(ctx, cfg, http.NotFoundHandler(), nil, nil, nil, &wg)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Start: %v", err)
	}
	close(stop)
	<-readerDone
	wg.Wait()
	if cfg.ListenAddr != "127.0.0.1:0" {
		t.Fatalf("Start rewrote cfg.ListenAddr to %q", cfg.ListenAddr)
	}
}

// An unbindable address surfaces as an error, not a hang.
func TestStart_ReturnsErrorOnBadAddr(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "false")
	cfg := &config.Config{ListenAddr: "not-a-listen-addr"}
	err := Start(context.Background(), cfg, http.NewServeMux(), nil, nil, nil, &sync.WaitGroup{})
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
	go func() { errCh <- Start(ctx, cfg, http.NewServeMux(), nil, d.DB, nil, &wg) }()

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

// openDailyNoPhotosProblems returns the still-open Problems keyed
// ProblemKeyDailyBackupNoPhotos (other ring entries are ignored).
func openDailyNoPhotosProblems() []logging.Problem {
	var out []logging.Problem
	for _, p := range logging.OpenProblems(time.Now(), time.Hour) {
		if p.Key == ProblemKeyDailyBackupNoPhotos {
			out = append(out, p)
		}
	}
	return out
}

// A daily snapshot that was written but could not include the photos
// (ut-docs#3991) is raised as a keyed Problem so it reaches the problems
// feed, the snapshot stays listed, and a later fully successful daily backup
// resolves it.
func TestRunDailyBackup_NoPhotosRaisesThenResolvesProblem(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	oldSeam := snapshotWithAssets
	t.Cleanup(func() { snapshotWithAssets = oldSeam })

	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	snapshotWithAssets = func(conn *sql.DB, p, _ string) (string, error) {
		path, serr := appdb.Snapshot(conn, p)
		if serr != nil {
			return "", serr
		}
		return path, errors.New("add photos to backup: boom")
	}
	runDailyBackup(d.DB, dbPath)

	list, err := appdb.ListBackups(dbPath)
	if err != nil || len(list) != 1 {
		t.Fatalf("photo-less snapshot must stay listed: len=%d err=%v", len(list), err)
	}
	if got := openDailyNoPhotosProblems(); len(got) != 1 {
		t.Fatalf("want 1 open %q problem after a photo-less daily backup, got %v", ProblemKeyDailyBackupNoPhotos, got)
	}

	msg := openDailyNoPhotosProblems()[0].Msg
	if !strings.Contains(msg, "boom") || !strings.Contains(msg, list[0].Name) || strings.Contains(msg, filepath.Dir(dbPath)) {
		t.Fatalf("problem must carry the cause and the snapshot's file name, not its directory: %q", msg)
	}

	// A second photo-less day must not stack a second open entry: the
	// back-office panel shows only a handful of problems (review finding).
	ageNewestDailyBackup(t, dbPath, 72*time.Hour)
	runDailyBackup(d.DB, dbPath)
	if list, err = appdb.ListBackups(dbPath); err != nil || len(list) != 2 {
		t.Fatalf("second photo-less run must add a snapshot: len=%d err=%v", len(list), err)
	}
	if got := openDailyNoPhotosProblems(); len(got) != 1 {
		t.Fatalf("want exactly 1 open %q problem after two photo-less days, got %v", ProblemKeyDailyBackupNoPhotos, got)
	}

	ageNewestDailyBackup(t, dbPath, 48*time.Hour)
	snapshotWithAssets = oldSeam // real seam: photos included, no error
	runDailyBackup(d.DB, dbPath)

	list, err = appdb.ListBackups(dbPath)
	if err != nil || len(list) != 3 {
		t.Fatalf("third run must add a snapshot: len=%d err=%v", len(list), err)
	}
	if got := openDailyNoPhotosProblems(); len(got) != 0 {
		t.Fatalf("a fully successful daily backup must resolve the problem, still open: %v", got)
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

// ut-docs#3092: the hourly backup loop runs housekeeping at most once per
// housekeeping.Interval — a pre-restore copy past its age limit goes on the
// first call; one added later survives until the next interval. With no
// database the statutory floor falls back to GlobalArchiveMinDays (10
// years, ut-docs#3365), so the fixtures are named (and aged) from 2010.
// isolateStagedUploads points the staged-upload sweep at root/tmp, so a
// housekeeping run never deletes from the real system temp dir
// (ut-docs#3955 review).
func isolateStagedUploads(t *testing.T, root string) {
	t.Helper()
	orig := stagedupload.Dir
	stagedupload.Dir = func() string { return filepath.Join(root, "tmp") }
	t.Cleanup(func() { stagedupload.Dir = orig })
}

func TestRunHousekeeping_OncePerInterval(t *testing.T) {
	root := t.TempDir()
	origData, origPending := paths.DataDir(), issuereport.PendingDir
	paths.Init(root)
	issuereport.PendingDir = filepath.Join(root, "issue-reports", "pending")
	t.Cleanup(func() { paths.Init(origData); issuereport.PendingDir = origPending })
	isolateStagedUploads(t, root)
	dbPath := filepath.Join(root, "unitill-pos.db")
	dir, err := appdb.BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-60 * 24 * time.Hour)
	aged := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var s housekeeping.Schedule
	first := aged("pre-restore-20100101-000000.db")
	runHousekeeping(&s, nil, dbPath, now)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("first run kept an expired pre-restore copy")
	}
	second := aged("pre-restore-20100102-000000.db")
	runHousekeeping(&s, nil, dbPath, now.Add(time.Hour))
	if _, err := os.Stat(second); err != nil {
		t.Fatal("ran again within the interval")
	}
	runHousekeeping(&s, nil, dbPath, now.Add(housekeeping.Interval))
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("did not run after a full interval")
	}
}

// ut-docs#3365: a pre-restore copy is a full former database (sales,
// payments, Z reports, audit log). runHousekeeping keeps every copy inside
// the shop country's archive_min_days floor, resolved from the live DB, even
// past housekeeping.PreRestoreMaxAge; a copy past the floor still goes.
func TestRunHousekeeping_KeepsPreRestoreCopiesInsideStatutoryFloor(t *testing.T) {
	root := t.TempDir()
	origData, origPending := paths.DataDir(), issuereport.PendingDir
	paths.Init(root)
	issuereport.PendingDir = filepath.Join(root, "issue-reports", "pending")
	t.Cleanup(func() { paths.Init(origData); issuereport.PendingDir = origPending })
	isolateStagedUploads(t, root)
	dbPath := filepath.Join(root, "unitill-pos.db")
	d, err := appdb.Open(testsupport.MigratedDBFile(t, "hk_floor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// A 60-day floor: between PreRestoreMaxAge (30) and the 3650-day
	// fallback, so the test proves the resolved value is the one applied.
	for _, q := range []string{
		`UPDATE country_settings SET archive_min_days = 60 WHERE code = 'GB'`,
		`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	dir, err := appdb.BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inside := write("pre-restore-20260819-120000.db")  // 40 days: past maxAge, inside the floor
	outside := write("pre-restore-20260630-120000.db") // 90 days: past both

	var s housekeeping.Schedule
	runHousekeeping(&s, d.DB, dbPath, now)
	if _, err := os.Stat(inside); err != nil {
		t.Error("pre-restore copy inside the statutory floor was removed")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("pre-restore copy past the statutory floor was kept")
	}
}

// ut-docs#3365 review finding: archive_min_days has no upper bound (only
// validateCountrySetting's floor), so a shop configuring an absurdly large
// "never purge" value must not overflow time.Duration (int64 nanoseconds,
// ~106,751 days) to a negative minRetain -- which would flip the floor OFF
// and delete a copy it was meant to protect, exactly backwards from
// "fail safe toward retaining, never toward deleting".
func TestRunHousekeeping_ClampsAbsurdArchiveRetentionInsteadOfOverflowing(t *testing.T) {
	root := t.TempDir()
	origData, origPending := paths.DataDir(), issuereport.PendingDir
	paths.Init(root)
	issuereport.PendingDir = filepath.Join(root, "issue-reports", "pending")
	t.Cleanup(func() { paths.Init(origData); issuereport.PendingDir = origPending })
	isolateStagedUploads(t, root)
	dbPath := filepath.Join(root, "unitill-pos.db")
	d, err := appdb.Open(testsupport.MigratedDBFile(t, "hk_floor_overflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, q := range []string{
		`UPDATE country_settings SET archive_min_days = 999999999 WHERE code = 'GB'`,
		`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	dir, err := appdb.BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	inside := filepath.Join(dir, "pre-restore-20260819-120000.db") // 40 days old
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var s housekeeping.Schedule
	runHousekeeping(&s, d.DB, dbPath, now)
	if _, err := os.Stat(inside); err != nil {
		t.Error("an absurd archive_min_days overflowed minRetain negative and deleted a protected copy")
	}
}

// Before enrolment there is no live device id, so the telemetry identity
// falls back to the configured one, and carries the live store/credential
// read through enroll.Effective (ut-docs#3561).
func TestTelemetryIdentityFallsBackToConfiguredDevice(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://cloud.test/api", DeviceID: "till-x", StoreID: "store-x", MerchantToken: "tok-x"}}
	id := telemetryIdentity(cfg)
	if id.EndpointURL != "http://cloud.test/api" || id.DeviceID != "till-x" || id.StoreID != "store-x" || id.Token != "tok-x" {
		t.Fatalf("telemetryIdentity = %+v", id)
	}
}

// ageNewestDailyBackup renames and backdates the newest backup so the next
// runDailyBackup no longer sees a fresh (<24h) one and snapshots again.
func ageNewestDailyBackup(t *testing.T, dbPath string, age time.Duration) {
	t.Helper()
	list, err := appdb.ListBackups(dbPath)
	if err != nil || len(list) == 0 {
		t.Fatalf("list backups: len=%d err=%v", len(list), err)
	}
	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	when := time.Now().Add(-age)
	name := "unitill-pos-" + when.UTC().Format("20060102-150405") + ".db"
	if err := os.Rename(filepath.Join(backupDir, list[0].Name), filepath.Join(backupDir, name)); err != nil {
		t.Fatalf("rename backup: %v", err)
	}
	if err := os.Chtimes(filepath.Join(backupDir, name), when, when); err != nil {
		t.Fatalf("backdate backup: %v", err)
	}
}
