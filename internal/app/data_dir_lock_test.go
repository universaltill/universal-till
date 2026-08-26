package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Run must refuse to start a second time against a data directory another
// process already owns (ut-docs#1097) — the actual acceptance criterion:
// "Two unitill-pos processes can never hold the same data directory; the
// second refuses to start and says why." Pre-acquires the lock exactly the
// way a first, already-running instance would, then proves Run (a) returns
// promptly rather than proceeding to bind a listener, and (b) returns an
// error a human can actually act on — not a bare, unexplained failure.
func TestRun_RefusesSecondInstanceAgainstSameDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")

	dbPath := filepath.Join(dataDir, "unitill-pos.db")
	held, err := db.AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("simulate a first running instance, AcquireDataDirLock: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded despite the data directory already being locked by another instance")
		}
		if !errors.Is(err, db.ErrDataDirLocked) {
			t.Fatalf("Run error = %v, want it to wrap db.ErrDataDirLocked", err)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Fatalf("Run took %s to refuse a locked data directory — it should fail fast, "+
				"before any of the rest of boot (migration, DB open, server bind) even starts", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s against an already-locked data directory")
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	// Give it a moment to reach a real listen bind, then let it shut down
	// cleanly — this is exercising normal boot-and-shutdown, not any
	// particular internal milestone.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on a normal, single-instance boot: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of ctx cancellation")
	}
}
