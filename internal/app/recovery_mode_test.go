package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// freeAddr returns a loopback address for a test to bind Run's HTTP layer
// to — a fixed, known address rather than UT_LISTEN_ADDR=127.0.0.1:0 (what
// every other Run test in this package uses), because this test needs to
// poll and POST against recovery mode's own listener while Run is still
// inside its boot loop, before cfg.ListenAddr would ever be echoed back
// anywhere a test could read it.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitListening(t *testing.T, addr string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s within %s", addr, within)
}

// The end-to-end acceptance criterion this card exists for (ut-docs#1436):
// "Inject a failing migration ... → server stays up, serves recovery page
// instead of exiting, /healthz stays unhealthy, Retry (after injected
// failure cleared) boots normally." Simulates the failure by corrupting an
// already-fully-migrated database's file header (fails at db.Open's own
// PRAGMA exec, before Ping — a genuine, reproducible db.Open-class failure,
// not a mock) rather than a synthetic error, then restores the original
// bytes before clicking Retry — proving the SAME process recovers with no
// restart, per ADR-0075.
func TestRun_BootFailureServesRecoveryModeThenRecoversOnRetry(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "unitill-pos.db")

	// A real, fully-migrated database to corrupt — not a hand-built fixture.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}
	good, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read seeded db: %v", err)
	}
	corrupt := make([]byte, len(good))
	copy(corrupt, good)
	copy(corrupt[:16], []byte("not-a-sqlite-hdr")) // SQLite's real magic header is 16 bytes
	if err := os.WriteFile(dbPath, corrupt, 0o644); err != nil {
		t.Fatalf("corrupt seeded db: %v", err)
	}

	addr := freeAddr(t)
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", addr)

	bootReachedPagesInit := observePagesInit(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	waitListening(t, addr, 15*time.Second)

	// Recovery mode is up: /healthz must not report healthy.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("/healthz reported healthy while the corrupted database should still be failing to open")
	}

	// Run must NOT have exited/returned yet — it stays up serving recovery
	// mode, per ADR-0075, not the old exit-on-error behavior.
	select {
	case err := <-done:
		t.Fatalf("Run returned (err=%v) instead of staying up serving recovery mode", err)
	default:
	}

	// Fix the actual cause, then click Retry.
	if err := os.WriteFile(dbPath, good, 0o644); err != nil {
		t.Fatalf("restore good db: %v", err)
	}
	retryResp, err := http.Post("http://"+addr+"/api/recovery/retry", "", nil)
	if err != nil {
		t.Fatalf("POST /api/recovery/retry: %v", err)
	}
	_ = retryResp.Body.Close()

	// Retry re-attempts in the SAME process (no restart) and this time
	// succeeds — boot proceeds all the way to pages.Init, same milestone
	// TestRun_AcquiresLockAndStillBootsNormally already uses.
	select {
	case <-bootReachedPagesInit:
	case err := <-done:
		t.Fatalf("Run exited (err=%v) instead of recovering after Retry", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not reach pages.Init within 15s of a successful Retry")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on shutdown after recovering: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return within 15s of ctx cancellation after recovering")
	}
}
