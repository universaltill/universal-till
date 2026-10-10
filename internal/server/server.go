package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	dbpkg "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/housekeeping"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// BackgroundJobs manages periodic marketplace tasks. There is deliberately no
// catalog-sync job: the marketplace catalog refreshes only when an operator
// opens a page that shows it (CatalogRepository.GetOrFetch) — ADR-0148 audit
// item 1, ut-docs#3625. A 15-minute ticker used to re-fetch it forever for any
// till that had browsed the marketplace once, paid or not.
type BackgroundJobs struct {
	revocationChecker  *plugins.RevocationChecker
	telemetryClient    *plugins.TelemetryClient
	telemetryInterval  time.Duration
	revocationInterval time.Duration
	logger             *log.Logger
	cfg                *config.Config
}

// NewBackgroundJobs creates a background job scheduler
func NewBackgroundJobs(db *sql.DB, supervisor *plugins.Supervisor, cfg *config.Config, logger *log.Logger) *BackgroundJobs {
	var revocationChecker *plugins.RevocationChecker
	if cfg.Marketplace.EndpointURL != "" {
		revocationChecker = plugins.NewRevocationChecker(db, cfg.Marketplace.EndpointURL, supervisor)
	}

	telemetryClient := plugins.NewTelemetryClient(db, func() plugins.TelemetryIdentity {
		return telemetryIdentity(cfg)
	})

	return &BackgroundJobs{
		revocationChecker:  revocationChecker,
		telemetryClient:    telemetryClient,
		telemetryInterval:  5 * time.Minute,
		revocationInterval: 30 * time.Minute,
		logger:             logger,
		cfg:                cfg,
	}
}

// telemetryIdentity is who the telemetry tick reports as, read per tick
// through enroll.Effective: the startup cfg has no store or credential on a
// till that enrols or pairs after boot. The device id is the one the
// credential was minted for (enroll's live id, as cloudsync sends), so an
// env-pinned UT_MARKETPLACE_DEVICE_ID can't cause a 403 device_mismatch.
// MerchantID is wire compatibility only; the cloud takes it from the
// credential.
func telemetryIdentity(cfg *config.Config) plugins.TelemetryIdentity {
	m := enroll.Effective(cfg).Marketplace
	deviceID := enroll.CurrentStatus().DeviceID
	if deviceID == "" {
		deviceID = marketplace.DeviceIDFromConfig(&m)
	}
	return plugins.TelemetryIdentity{
		EndpointURL: m.EndpointURL,
		DeviceID:    deviceID,
		MerchantID:  m.ClientID,
		StoreID:     m.StoreID,
		Token:       m.MerchantToken,
	}
}

// Start begins all background jobs. wg is marked Done once every job's
// goroutine has fully exited (ctx cancelled), so a caller waiting on wg
// never returns while one of these could still be mid-sync/mid-write.
func (bj *BackgroundJobs) Start(ctx context.Context, wg *sync.WaitGroup) {
	// Telemetry reporting job
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("server.telemetry")
		defer wg.Done()
		ticker := time.NewTicker(bj.telemetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := bj.telemetryClient.ReportNow(ctx); err != nil {
					bj.logger.Printf("[Scheduler] telemetry report failed: %v", err)
				}
			}
		}
	}()

	// Revocation check job (T030)
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("server.revocation")
		defer wg.Done()
		ticker := time.NewTicker(bj.revocationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if bj.revocationChecker != nil {
					bj.logger.Println("[Scheduler] checking for revoked plugins")
					count, err := bj.revocationChecker.SyncRevocations(ctx)
					if err != nil {
						bj.logger.Printf("[Scheduler] revocation sync failed: %v", err)
					} else if count > 0 {
						bj.logger.Printf("[Scheduler] disabled %d revoked plugins", count)
					}
				}
			}
		}
	}()
}

// Start boots the HTTP server and every background job it owns. wg is marked
// Done, per job/goroutine, once each has fully exited (ctx cancelled) —
// including this function's own graceful-shutdown goroutine — so a caller
// that both waits on wg AND waits for Start to return (as app.Run does) can
// safely assume nothing Start ever spawned is still touching db/supervisor
// once both have happened. wg must not be nil.
func Start(ctx context.Context, cfg *config.Config, handler http.Handler, catalogRepo *marketplace.CatalogRepository, db *sql.DB, supervisor *plugins.Supervisor, wg *sync.WaitGroup) error {
	// Start background jobs if a marketplace (catalog repository) is
	// configured. Start() itself only launches goroutines and returns
	// immediately, so it doesn't need its own wrapping goroutine — jobs' own 2
	// goroutines register with wg directly.
	if catalogRepo != nil {
		logger := log.New(log.Writer(), "[BackgroundJobs] ", log.LstdFlags)
		jobs := NewBackgroundJobs(db, supervisor, cfg, logger)
		jobs.Start(ctx, wg)
	}

	// Daily local DB backup (docs: architecture/local-backup.md) and device
	// housekeeping (ut-docs#3092) — runs regardless of marketplace config. Checks hourly; snapshots when the
	// newest backup is older than 24h; first check shortly after boot so a
	// till powered off nightly still gets one.
	if db != nil {
		wg.Add(1)
		go func() {
			defer logging.RecoverAndLog("server.housekeeping")
			defer wg.Done()
			var hk housekeeping.Schedule
			run := func() {
				runDailyBackup(db, cfg.DBPath, paths.Data("public", "assets"))
				runHousekeeping(&hk, db, cfg.DBPath, time.Now())
			}
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
				run()
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					run()
				}
			}
		}()
	}

	// Related-items rebuild (docs: architecture/ai-integration.md §h):
	// market-basket stats from the shop's own sales history feed the
	// "customers also buy" strip. Runs at startup (tills reboot daily) and
	// every 24h for always-on installs. Local SQL only — no network.
	if db != nil {
		wg.Add(1)
		go func() {
			defer logging.RecoverAndLog("server.relatedItems")
			defer wg.Done()
			repo := data.NewRelatedItemsRepo(db)
			rebuild := func() {
				jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				if n, err := repo.Rebuild(jobCtx); err != nil {
					log.Printf("[RelatedItems] rebuild failed: %v", err)
				} else {
					log.Printf("[RelatedItems] rebuilt %d co-occurrence pairs", n)
				}
			}
			rebuild()
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					rebuild()
				}
			}
		}()
	}

	srv := &http.Server{
		Handler: handler,
	}

	// Bind the configured port, or the next free one if it's already taken (a
	// second till on the same machine, or another app on :8080) so a busy port
	// never blocks startup. The address actually bound stays local: cfg is
	// shared with background goroutines pages.Init already started (the cloud
	// link copies *cfg via enroll.Effective), so writing it back here was a
	// data race on every boot (ut-docs#2990). The log line, the browser-open
	// and plugins.SetTillListenAddr below all take actualAddr instead.
	ln, actualAddr, err := bindListener(cfg.ListenAddr, cfg.Demo)
	if err != nil {
		return err
	}
	if movedOffConfiguredAddr(cfg.ListenAddr, actualAddr) {
		log.Printf("port %s was busy — listening on %s instead", cfg.ListenAddr, actualAddr)
	}
	reportBoundAddr(ctx, cfg.ListenAddr, actualAddr)
	// A fallback bind may have moved the port; plugin egress must refuse
	// the one really serving (ut-docs#2891).
	plugins.SetTillListenAddr(actualAddr)
	// Same port, TLS as well as plain HTTP (ADR-0114 §7, ut-docs#2736).
	ln = withLANTLS(ln, paths.Data("tls"), cfg.Demo)

	// Graceful shutdown when context is cancelled. Registered with wg because
	// net/http's Shutdown contract only guarantees Serve returns once
	// listeners are closed — supervisor.Shutdown below can still be running
	// after Serve (and so this function) has already returned; without this,
	// a caller waiting on wg alone could race their DB writes (audit_log)
	// against database.Close().
	//
	// Deliberately NOT where the wasm runtime's event-channel drainers are
	// joined (ut-docs#503, a regression from ut-docs#380/PR#268's original
	// wiring here): this goroutine fires on the SAME ctx.Done() signal that
	// independently triggers every other background service on the caller's
	// wg (cloudsync's ticker included), and closing the wasm runtime's
	// subscriber channels while one of those is still mid-publish is a real
	// "send on closed channel" panic (EventBus.publish releases its lock
	// before the channel send — reproduced directly against this exact call
	// path). That join now happens in app.Run's own deferred cleanup,
	// strictly after every wg member — this goroutine included — has
	// actually exited, not merely been asked to.
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("server.shutdown")
		defer wg.Done()
		<-ctx.Done()
		log.Printf("shutting down HTTP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if supervisor != nil {
			_ = supervisor.Shutdown(shutdownCtx)
		}
	}()

	log.Printf("listening on %s", actualAddr)

	// Convenience: open the setup/sale page in the operator's browser once the
	// server accepts connections. Skipped on kiosk tills (they launch their own
	// browser) and when UT_OPEN_BROWSER is set falsy.
	if openBrowserFor(cfg.Demo) {
		go func(addr string) {
			defer logging.RecoverAndLog("server.openSetupPage")
			openSetupPage(addr)
		}(actualAddr)
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ProblemKeyDailyBackupNoPhotos tags the Problems entry (back-office Problems
// panel + cloud heartbeat, ADR-0018) for "the daily auto-backup was written
// but could not include the uploaded photos" (ut-docs#3991). It stays open
// until a later daily backup completes with photos and resolves it.
const ProblemKeyDailyBackupNoPhotos = "backup.daily_no_photos"

// ProblemKeyDailyBackupFailed tags the Problems entry for "the daily
// auto-backup could not write any snapshot at all" (ut-docs#3993). It stays
// open until a later daily snapshot is written or a fresh backup exists.
const ProblemKeyDailyBackupFailed = "backup.daily_failed"

// snapshotWithAssets is a seam so tests can simulate a photo failure.
var snapshotWithAssets = dbpkg.SnapshotWithAssets

// runDailyBackup snapshots the local DB unless a backup newer than 24h
// already exists, then prunes old snapshots to the newest DefaultBackupKeep.
func runDailyBackup(db *sql.DB, dbPath, assetsRoot string) {
	list, err := dbpkg.ListBackups(dbPath)
	if err == nil && len(list) > 0 && time.Since(list[0].ModTime) < 24*time.Hour {
		checkFreshBackup(dbPath, assetsRoot, list[0].Name)
		return
	}
	// Uploaded photos ride inside the snapshot (ut-docs#2724). A photo
	// failure still leaves a usable DB backup, so it is not fatal — but it
	// is raised as a keyed Problem (ut-docs#3991) so the shop is told the
	// backup lacks its photos, and resolved by the next complete backup.
	path, err := snapshotWithAssets(db, dbPath, assetsRoot)
	if err != nil && path == "" {
		log.Printf("[Backup] snapshot failed: %v", err)
		raiseOnce(ProblemKeyDailyBackupFailed, "[Backup] daily backup failed: %v", err)
		return
	}
	// A snapshot was written, so the "no snapshot at all" condition is over.
	logging.ResolveProblems(ProblemKeyDailyBackupFailed)
	if err != nil {
		// One open entry per condition, not one per failing day: the
		// back-office panel shows only a few problems. Cause first, file
		// name only — the heartbeat cuts a problem at 200 characters.
		logging.ResolveProblems(ProblemKeyDailyBackupNoPhotos)
		logging.L().WarnProblemf(ProblemKeyDailyBackupNoPhotos, "[Backup] daily backup has no photos (%v): %s", err, filepath.Base(path))
	} else {
		logging.ResolveProblems(ProblemKeyDailyBackupNoPhotos)
	}
	log.Printf("[Backup] daily snapshot: %s", path)
	_ = dbpkg.PruneBackups(dbPath, dbpkg.DefaultBackupKeep)
}

// raiseOnce logs a keyed Problem unless one with that key is already open, so
// the hourly loop never stacks entries (ut-docs#3993).
func raiseOnce(key, format string, args ...any) {
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		if p.Key == key {
			return
		}
	}
	logging.L().WarnProblemf(key, format, args...)
}

// checkFreshBackup runs when the newest backup is under 24h old. The
// Problems ring is in-memory, so after a restart a "no photos" Problem
// (ut-docs#3991) would vanish while the photo-less backup is still the
// newest: re-derive it from the backup itself (ut-docs#3993). A fresh backup
// also means the "no snapshot at all" failure is over.
func checkFreshBackup(dbPath, assetsRoot, name string) {
	logging.ResolveProblems(ProblemKeyDailyBackupFailed)
	dir, err := dbpkg.BackupDir(dbPath)
	lacks := false
	if err == nil {
		lacks, err = dbpkg.BackupLacksPhotos(filepath.Join(dir, name), assetsRoot)
	}
	switch {
	case lacks && err != nil:
		raiseOnce(ProblemKeyDailyBackupNoPhotos, "[Backup] daily backup has no photos (%v): %s", err, name)
	case lacks:
		raiseOnce(ProblemKeyDailyBackupNoPhotos, "[Backup] daily backup has no photos: %s", name)
	case err != nil:
		// Can't tell (e.g. a hot journal left by a power cut mid-backup):
		// change nothing, and log a repeated cause only once, not hourly.
		if msg := err.Error(); msg != lastFreshCheckErr {
			lastFreshCheckErr = msg
			log.Printf("[Backup] check newest backup for photos: %v", err)
		}
		return
	default:
		logging.ResolveProblems(ProblemKeyDailyBackupNoPhotos)
	}
	lastFreshCheckErr = ""
}

// lastFreshCheckErr is the last cause checkFreshBackup logged; only the
// housekeeping goroutine touches it.
var lastFreshCheckErr string

// maxArchiveRetainDays is the largest archive_min_days that still converts
// to a positive time.Duration (int64 nanoseconds): math.MaxInt64 / one day,
// floored. A shop-configured value above this is clamped rather than
// overflowed (ut-docs#3365 review finding).
const maxArchiveRetainDays = math.MaxInt64 / int64(24*time.Hour)

// ProblemKeyDiskLow tags the Problems entry (back-office Problems panel +
// cloud heartbeat, ADR-0018) for "free space on this till's disk is below
// the low-disk floor" (ut-docs#3121). It stays open until a later check
// finds the disk back above the floor.
const ProblemKeyDiskLow = "disk.low"

// diskFree probes the disk holding the data directory; a var so tests can
// inject a full or an empty disk.
var diskFree = diskspace.Probe

// diskLowOpen reports whether a disk.low Problem is still open in the
// Problems ring. It reads the ring rather than remembering its own flag, so
// an entry evicted by 50 later warnings (a full disk produces many) is
// raised again on the next check instead of going quiet (review of
// ut-docs#3121).
func diskLowOpen(now time.Time) bool {
	for _, p := range logging.OpenProblems(now, 0) {
		if p.Key == ProblemKeyDiskLow {
			return true
		}
	}
	return false
}

// runHousekeeping runs the daily device clean-up when due — or at once,
// with each kind pruned to its minimum, when free space is below the
// low-disk floor (ut-docs#3121) — then caps the backups' share of the free
// space and reports a disk still below the floor. Called hourly by the
// background loop. Failures are logged and skipped; nothing here blocks
// selling.
func runHousekeeping(s *housekeeping.Schedule, db *sql.DB, dbPath string, now time.Time) {
	dataDir := filepath.Dir(dbPath)
	usage, probeErr := diskFree(dataDir)
	if probeErr != nil && !errors.Is(probeErr, diskspace.ErrUnsupported) {
		log.Printf("[Housekeeping] free-space probe: %v", probeErr)
	}
	low := probeErr == nil && diskspace.Low(usage)
	if s.Due(now) || low {
		sweepHousekeeping(s, db, dbPath, now, low, usage)
	}
	if probeErr != nil {
		// Unknown free space: never cap or warn on a guess, and don't
		// keep claiming a low disk nobody can measure any more.
		logging.ResolveProblems(ProblemKeyDiskLow)
		return
	}
	after, err := diskFree(dataDir)
	if err != nil {
		return
	}
	if r := housekeeping.CapBackups(dbPath, after.Free); r.Err != nil {
		log.Printf("[Housekeeping] backup cap: %v", r.Err)
	} else if r.Removed > 0 {
		log.Printf("[Housekeeping] backups over %d%% of free space: removed %d, freed %d bytes", housekeeping.BackupSharePercent, r.Removed, r.Freed)
		if u, err := diskFree(dataDir); err == nil {
			after = u
		}
	}
	reportDiskLow(after, now)
}

// reportDiskLow raises the disk.low Problem when the disk is below the
// floor after the clean-up and none is open yet (one entry per episode),
// and resolves it once the disk is back above.
func reportDiskLow(u diskspace.Usage, now time.Time) {
	if !diskspace.Low(u) {
		logging.ResolveProblems(ProblemKeyDiskLow)
		return
	}
	if diskLowOpen(now) {
		return
	}
	logging.L().WarnProblemf(ProblemKeyDiskLow, "[Housekeeping] disk almost full after clean-up: %d MB free of %d MB (floor %d MB)", u.Free>>20, u.Total>>20, diskspace.Floor(u.Total)>>20)
}

// sweepHousekeeping is one housekeeping run: the Normal limits, or LowDisk
// ones when low.
func sweepHousekeeping(s *housekeeping.Schedule, db *sql.DB, dbPath string, now time.Time, low bool, usage diskspace.Usage) {
	s.Ran(now)
	// Pre-restore copies are full databases (sales, payments, Z reports,
	// audit log), so they are kept for the shop's statutory archive floor
	// (ADR-0040, ut-docs#3365). On any failure to resolve it, fall back to
	// the global floor: fail safe toward retaining, never toward deleting.
	minDays := data.GlobalArchiveMinDays
	if db != nil {
		if d, err := data.ResolveArchiveMinDays(context.Background(), db); err == nil {
			minDays = d
		} else {
			log.Printf("[Housekeeping] resolve archive retention: %v (keeping pre-restore copies for %d days)", err, minDays)
		}
	}
	// archive_min_days has no upper bound (only validateCountrySetting's
	// floor) -- a shop configuring an enormous "never purge" value would
	// overflow time.Duration (int64 nanoseconds, ~106,751 days) to negative,
	// which would flip the floor OFF and fail toward deleting instead of
	// retaining. Clamp instead.
	if minDays > maxArchiveRetainDays {
		minDays = maxArchiveRetainDays
	}
	minRetain := time.Duration(minDays) * 24 * time.Hour
	policy := map[string]string{}
	for _, rule := range housekeeping.Retention() {
		policy[rule.Kind] = rule.Policy
	}
	limits := housekeeping.Normal
	if low {
		limits = housekeeping.LowDisk
	}
	if low && !diskLowOpen(now) {
		log.Printf("[Housekeeping] low disk: %d MB free of %d MB (floor %d MB) — pruning each kind to its minimum", usage.Free>>20, usage.Total>>20, diskspace.Floor(usage.Total)>>20)
	}
	for _, r := range housekeeping.RunWith(dbPath, now, minRetain, limits) {
		if r.Err != nil {
			log.Printf("[Housekeeping] %s: %v", r.Kind, r.Err)
			continue
		}
		if r.Removed > 0 {
			log.Printf("[Housekeeping] %s: removed %d, freed %d bytes (policy: %s)", r.Kind, r.Removed, r.Freed, policy[r.Kind])
		}
	}
}

// bindListener is Start's bind: listenWithFallback normally, but a demo
// till (ADR-0113 §1.8, ut-docs#2687) binds exactly addr or fails — the demo
// broker proxies to the address it chose, so a till quietly listening on a
// nearby port would receive (or leave another process receiving) the wrong
// visitor's traffic.
func bindListener(addr string, demo bool) (net.Listener, string, error) {
	if !demo {
		return listenWithFallback(addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("demo mode binds exactly %s: %w", addr, err)
	}
	return ln, ln.Addr().String(), nil
}

// withLANTLS serves ln over TLS as well as plain HTTP, on the same port
// (ADR-0114 §7, ut-docs#2736): a connection opening with a TLS handshake
// gets the till's pinned self-signed certificate from dir, anything else
// plain HTTP as before. TLS is never the cause of a failed request — if the
// key can't be loaded or created, ln is served plain-only and the reason is
// logged. A demo till (ADR-0113) sits behind the demo broker, which talks
// plain HTTP to it, so it gets no LAN key.
func withLANTLS(ln net.Listener, dir string, demo bool) net.Listener {
	if demo {
		return ln
	}
	c, err := lantls.LoadOrCreate(dir)
	if err != nil {
		log.Printf("[LAN TLS] serving plain HTTP only: %v", err)
		return ln
	}
	log.Printf("[LAN TLS] serving TLS on the same port")
	return lantls.Listen(ln, c.TLSConfig())
}

// boundAddrKey carries a WithBoundAddr reporter on Start's context.
type boundAddrKey struct{}

// WithBoundAddr returns a ctx whose Start reports the address it really
// bound, and whether that moved off cfg.ListenAddr (movedOffConfiguredAddr),
// right after the bind and before serving (ut-docs#3290). mobile.Start drives
// app.Run in-process and polls the port it asked for; a fallback bind used
// to leave it polling that port for 30s. Every other caller sets nothing.
func WithBoundAddr(ctx context.Context, report func(actual string, moved bool)) context.Context {
	return context.WithValue(ctx, boundAddrKey{}, report)
}

// reportBoundAddr calls ctx's WithBoundAddr reporter, if it has one.
func reportBoundAddr(ctx context.Context, configured, actual string) {
	if report, ok := ctx.Value(boundAddrKey{}).(func(string, bool)); ok && report != nil {
		report(actual, movedOffConfiguredAddr(configured, actual))
	}
}

// openBrowserFor is shouldOpenBrowser, except a demo till never opens a
// browser on its host (ADR-0113).
func openBrowserFor(demo bool) bool {
	return !demo && shouldOpenBrowser()
}

// listenWithFallback binds addr, or the next free port when addr's port is
// already in use, so a busy port doesn't stop the till from starting. It tries
// the configured port, then the next 20, then lets the OS pick any free port.
// Returns the listener and the address actually bound. If the configured port
// binds cleanly the behaviour is unchanged.
//
// ut-docs#1169: a wildcard configured host (the default ":8080" included) is
// reachable off-device by design — self-order kiosk clients and till-to-till
// pairing need that. But needing a FALLBACK means something else already
// holds the configured port, almost always a second, unconfigured instance
// racing this one at boot — and repeating the wildcard bind on the fallback
// port turned that race into a real incident: an empty, un-provisioned till
// silently reachable, and pairable, from the whole LAN. So any fallback bind
// degrades a wildcard host to loopback-only instead — still reachable for
// local diagnosis, never off-device — rather than silently re-exposing
// whatever lost the race. A caller that genuinely wants a second
// LAN-reachable instance on the same box sets UT_LISTEN_ADDR to its own
// dedicated port instead of relying on this fallback.
func listenWithFallback(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, ln.Addr().String(), nil
	}
	host, portStr, serr := net.SplitHostPort(addr)
	base, aerr := strconv.Atoi(portStr)
	if serr != nil || aerr != nil {
		return nil, "", err // unparseable addr — surface the original bind error
	}
	fallbackHost := host
	if isWildcardHost(host) {
		fallbackHost = "127.0.0.1"
	}
	for p := base + 1; p <= base+20; p++ {
		cand := net.JoinHostPort(fallbackHost, strconv.Itoa(p))
		if l, e := net.Listen("tcp", cand); e == nil {
			return l, l.Addr().String(), nil
		}
	}
	// Last resort: port 0 → the OS hands us any free port, still loopback-only
	// when the configured host was wildcard, for the same reason as above.
	if l, e := net.Listen("tcp", net.JoinHostPort(fallbackHost, "0")); e == nil {
		return l, l.Addr().String(), nil
	}
	return nil, "", err // nothing free — report the original failure
}

// movedOffConfiguredAddr reports whether listenWithFallback really had to
// move: a different port, or a wildcard host degraded to loopback. A plain
// string compare (the pre-#2722 check) also fired on a clean first-try bind,
// because the configured "0.0.0.0:34029" comes back from the listener as
// "[::]:34029" — logging a false "port was busy" that misled the #2722
// investigation. A configured port of 0 ("any") never counts as a move.
func movedOffConfiguredAddr(configured, actual string) bool {
	ch, cp, err1 := net.SplitHostPort(configured)
	ah, ap, err2 := net.SplitHostPort(actual)
	if err1 != nil || err2 != nil {
		return configured != actual
	}
	if cp != "0" && cp != ap {
		return true
	}
	if isWildcardHost(ch) {
		return !isWildcardHost(ah)
	}
	return ch != ah
}

// isWildcardHost reports whether host means "every interface": Go's own
// all-interfaces shorthand (the empty host, as in ":8080"), or any IP literal
// net.Listen also treats as all-interfaces. Delegates to net.IP.IsUnspecified
// rather than hand-enumerating spellings — an independent review of this
// change (ut-docs#1169) proved by direct probe that a string-literal switch
// covering only "0.0.0.0"/"::" still leaks a wildcard bind for "::0",
// "0:0:0:0:0:0:0:0" and "::ffff:0.0.0.0", all of which net.Listen also binds
// to every interface. IsUnspecified (with ParseIP's built-in 4-in-6 handling)
// covers every spelling net.Listen itself accepts as wildcard, so this can't
// drift out of sync with what net.Listen actually does.
func isWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// shouldOpenBrowser decides whether to auto-open the browser. UT_OPEN_BROWSER
// wins when set; otherwise default on, except on kiosk tills.
func shouldOpenBrowser() bool {
	if v := os.Getenv("UT_OPEN_BROWSER"); v != "" {
		on, err := strconv.ParseBool(v)
		return err == nil && on
	}
	if k, _ := strconv.ParseBool(os.Getenv("UT_KIOSK")); k {
		return false
	}
	return true
}

// openSetupPage waits for the listener to accept, then opens the local URL in
// the default browser. Entirely best-effort — any failure is silently ignored
// (headless boxes, no browser, SSH sessions).
func openSetupPage(listenAddr string) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	// Wait up to ~5s for the server to start accepting connections.
	addr := net.JoinHostPort(host, port)
	for range 50 {
		conn, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond) // netaccess:allow loopback probe of this till's own listener before opening the local browser
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	startBrowser(fmt.Sprintf("http://%s:%s", host, port))
}

// startBrowser launches the OS default browser at url. A package var so tests
// can stub it — the real body spawns an actual browser process, which is
// untestable exec glue (documented, not faked).
var startBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
