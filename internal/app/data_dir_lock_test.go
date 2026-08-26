package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// observePagesInit swaps the pagesInit seam (see app.go) for a wrapper that
// closes the returned channel the moment Run reaches pages.Init, restoring
// the original on cleanup. That milestone is the deterministic "boot got
// past config, the data-directory lock, the legacy-data migration, the
// pending restore and db.Open" signal the tests below need — the same seam
// and the same reasoning as TestRun_WaitsForAsyncWorkBeforeClosingDatabase
// in app_test.go, which deliberately uses it instead of sleeping for a
// guessed number of milliseconds and hoping boot got far enough on a loaded
// CI runner.
func observePagesInit(t *testing.T) <-chan struct{} {
	t.Helper()
	orig := pagesInit
	t.Cleanup(func() { pagesInit = orig })

	reached := make(chan struct{})
	pagesInit = func(ctx, bgCtx context.Context, cfg *config.Config, pm *plugins.Manager, dbConn *sql.DB, catalogRepo *marketplace.CatalogRepository, wg *sync.WaitGroup) (http.Handler, *common.Deps) {
		handler, deps := orig(ctx, bgCtx, cfg, pm, dbConn, catalogRepo, wg)
		close(reached)
		return handler, deps
	}
	return reached
}

// Run must refuse to start a second time against a data directory another
// process already owns (ut-docs#1097) — the actual acceptance criterion:
// "Two unitill-pos processes can never hold the same data directory; the
// second refuses to start and says why." Pre-acquires the lock exactly the
// way a first, already-running instance would, then proves Run (a) returns
// an error a human can actually act on, not a bare unexplained failure, and
// (b) bailed out BEFORE the rest of boot — asserted by the pages.Init seam
// never firing, which is a direct observation of where Run stopped rather
// than a wall-clock guess about how fast it should have got there.
func TestRun_RefusesSecondInstanceAgainstSameDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")

	bootReachedPagesInit := observePagesInit(t)

	dbPath := filepath.Join(dataDir, "unitill-pos.db")
	held, err := db.AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("simulate a first running instance, AcquireDataDirLock: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	done := make(chan error, 1)
	go func() { done <- Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded despite the data directory already being locked by another instance")
		}
		if !errors.Is(err, db.ErrDataDirLocked) {
			t.Fatalf("Run error = %v, want it to wrap db.ErrDataDirLocked", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s against an already-locked data directory")
	}

	select {
	case <-bootReachedPagesInit:
		t.Fatal("Run reached pages.Init against an already-locked data directory — " +
			"it must refuse before the rest of boot (legacy-data migration, pending " +
			"restore, db.Open, server bind) touches the data directory at all")
	default:
	}
}

// The happy path this whole mechanism must not break: a normal, single
// instance still boots and binds successfully — the lock is acquired and
// held for Run's lifetime, not accidentally left locked against itself or
// released too early.
func TestRun_AcquiresLockAndStillBootsNormally(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")

	bootReachedPagesInit := observePagesInit(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	// Wait for boot to genuinely get past the lock, db.Open and route
	// registration before asking it to shut down — never a fixed sleep, so a
	// slow runner makes this test slower rather than flaky.
	select {
	case <-bootReachedPagesInit:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not reach pages.Init within 30s on a normal, single-instance boot")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on a normal, single-instance boot: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of ctx cancellation")
	}

	// And the lock it held is genuinely gone once Run returned, so the next
	// start (a restart, the overwhelmingly common case) is not refused.
	next, err := db.AcquireDataDirLock(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		t.Fatalf("data directory still locked after Run returned: %v — Run's deferred "+
			"Release must run on every exit path, or every restart would refuse to start", err)
	}
	_ = next.Release()
}
