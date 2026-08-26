package app

// Tests for the ut-docs#1040 install-time provisioner: on a fresh .deb
// install on a Pi running a DESKTOP OS, postinstall.sh runs
// `unitill-pos provision-desktop-kiosk-defaults` (as the pos service user,
// against the real service DB) to seed WindowMode=kiosk +
// LaunchOnStartup=true and record a system-actor audit entry, so the
// decision is visible to the owner on /audit — not just in installer
// stdout. TDD-first per the pipeline's standing convention: these were
// written (and watched fail: "undefined: provisionDesktopKioskDefaults")
// before provision.go existed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func openProvisionTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func auditRows(t *testing.T, database *db.DB, action string) []string {
	t.Helper()
	rows, err := database.Query(
		`SELECT COALESCE(actor_id, ''), COALESCE(data_json, '') FROM audit_log WHERE action = ?`, action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var actor, payload string
		if err := rows.Scan(&actor, &payload); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		if actor != "system" {
			t.Errorf("audit actor = %q, want %q (an install-time action has no session user)", actor, "system")
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func TestProvisionDesktopKioskDefaults_SeedsSettingsAndAudits(t *testing.T) {
	database := openProvisionTestDB(t)
	ctx := context.Background()

	did, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true)
	if err != nil {
		t.Fatalf("provisionDesktopKioskDefaults: %v", err)
	}
	if !did {
		t.Fatal("first run reported nothing to do on a fresh DB")
	}

	settingsRepo := data.NewSettingsRepo(database.DB)
	if v, ok, _ := settingsRepo.Get(ctx, common.KeyWindowMode); !ok || v != "kiosk" {
		t.Errorf("%s = %q (present=%v), want %q", common.KeyWindowMode, v, ok, "kiosk")
	}
	if v, ok, _ := settingsRepo.Get(ctx, common.KeyLaunchOnStartup); !ok || v != "true" {
		t.Errorf("%s = %q (present=%v), want %q", common.KeyLaunchOnStartup, v, ok, "true")
	}
	if _, ok, _ := settingsRepo.Get(ctx, keyDesktopKioskOverlayProvisioned); !ok {
		t.Errorf("completion marker %s not written", keyDesktopKioskOverlayProvisioned)
	}

	payloads := auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)
	if len(payloads) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &payload); err != nil {
		t.Fatalf("audit payload is not JSON: %v (%q)", err, payloads[0])
	}
	if payload["trigger"] != "deb-postinstall" {
		t.Errorf("audit payload trigger = %v, want %q — the entry must record what triggered the provisioning", payload["trigger"], "deb-postinstall")
	}
	if payload["autostart_staged"] != true {
		t.Errorf("audit payload autostart_staged = %v, want true", payload["autostart_staged"])
	}
	if payload["window_mode"] != "kiosk" {
		t.Errorf("audit payload window_mode = %v, want %q", payload["window_mode"], "kiosk")
	}
}

func TestProvisionDesktopKioskDefaults_IsIdempotent(t *testing.T) {
	database := openProvisionTestDB(t)
	ctx := context.Background()

	if _, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// An owner later changes their mind in Settings…
	settingsRepo := data.NewSettingsRepo(database.DB)
	if err := settingsRepo.Set(ctx, common.KeyWindowMode, "normal"); err != nil {
		t.Fatalf("simulate owner change: %v", err)
	}

	// …and a re-run (postinstall is idempotent by contract, same as the
	// headless path's --auto) must neither clobber that choice nor write a
	// second audit row.
	did, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if did {
		t.Error("second run reported work done — must be a no-op once the marker exists")
	}
	if v, _, _ := settingsRepo.Get(ctx, common.KeyWindowMode); v != "normal" {
		t.Errorf("re-run clobbered the owner's window mode: got %q, want %q", v, "normal")
	}
	if n := len(auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)); n != 1 {
		t.Errorf("audit rows after re-run = %d, want exactly 1", n)
	}
}

func TestProvisionDesktopKioskDefaults_RecordsAutostartNotStaged(t *testing.T) {
	// postinstall can fail to stage the autostart entry (e.g. the desktop
	// shell binary can't exec because the WebKit libs were skipped with
	// --no-install-recommends) — the audit entry must say so honestly.
	database := openProvisionTestDB(t)
	if _, err := provisionDesktopKioskDefaults(context.Background(), database.DB, "deb-postinstall", false); err != nil {
		t.Fatalf("provisionDesktopKioskDefaults: %v", err)
	}
	payloads := auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)
	if len(payloads) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &payload); err != nil {
		t.Fatalf("audit payload is not JSON: %v", err)
	}
	if payload["autostart_staged"] != false {
		t.Errorf("audit payload autostart_staged = %v, want false", payload["autostart_staged"])
	}
}

// retryOnError is the general-purpose retry/backoff (ut-docs#1094) that
// openWithRetry wraps db.Open in — tested directly against a fake fn, with
// delay=0, so it exercises the real retry LOGIC (attempt count, which
// error surfaces, no-sleep-after-the-last-try) without depending on
// SQLite's actual lock-contention behavior or real wall-clock time.

func TestRetryOnError_SucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err := retryOnError(5, 0, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryOnError: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (stop retrying the moment fn succeeds)", calls)
	}
}

func TestRetryOnError_GivesUpAfterMaxAttemptsAndReturnsTheLastError(t *testing.T) {
	calls := 0
	err := retryOnError(4, 0, func() error {
		calls++
		return fmt.Errorf("attempt %d failed", calls)
	})
	if calls != 4 {
		t.Errorf("calls = %d, want exactly 4 (never exceed the attempt budget)", calls)
	}
	if err == nil || err.Error() != "attempt 4 failed" {
		t.Errorf("err = %v, want the LAST attempt's error (\"attempt 4 failed\"), not the first — a caller needs the real, current failure reason", err)
	}
}

// retryOnErrorWithin runs retryOnError(attempts, delay, fn) on its own
// goroutine and fails the test if it hasn't returned within timeout — used
// by the two sleep-boundary tests below with a large delay (time.Hour) so
// a regression that sleeps when it shouldn't fails FAST with a clear
// message, instead of the test genuinely blocking for that same hour (a
// real risk with a plain "assert on elapsed time after the call returns"
// — independent review of ut-docs#1094 caught this in an earlier draft:
// a mutated retryOnError that slept unconditionally hung until go test's
// own timeout killed the whole package, dumping a goroutine trace instead
// of a normal failure).
func retryOnErrorWithin(t *testing.T, timeout time.Duration, attempts int, delay time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- retryOnError(attempts, delay, fn) }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("retryOnError(attempts=%d, delay=%v) did not return within %v — it slept when it should not have", attempts, delay, timeout)
		return nil // unreachable: t.Fatalf stops the goroutine
	}
}

func TestRetryOnError_FirstTryAloneNeverSleeps(t *testing.T) {
	// A single successful first attempt must return immediately — this
	// guards against a future refactor accidentally sleeping BEFORE the
	// first call (which would slow down the overwhelmingly common case:
	// the database is ready and provisioning succeeds first try). delay is
	// deliberately huge (time.Hour): if a regression sleeps even once, the
	// 2s budget below fails the test fast rather than the goroutine
	// blocking for a real hour.
	if err := retryOnErrorWithin(t, 2*time.Second, 5, time.Hour, func() error { return nil }); err != nil {
		t.Fatalf("retryOnError: %v", err)
	}
}

func TestRetryOnError_NeverSleepsAfterTheFinalAttempt(t *testing.T) {
	// Every attempt fails, so retryOnError gives up after the last one —
	// it must not sleep AFTER that final failed attempt (nothing left to
	// wait for), only BETWEEN attempts. With attempts=1 there is no
	// "between" at all, so the same huge-delay/short-timeout technique as
	// above catches an over-sleeping regression fast rather than hanging.
	err := retryOnErrorWithin(t, 2*time.Second, 1, time.Hour, func() error { return errors.New("boom") })
	if err == nil {
		t.Fatal("expected the single attempt's error to surface")
	}
}

// TestOpenWithRetry_SucceedsFirstTryAgainstAnAlreadyMigratedDatabase is a
// real-database sanity check for openWithRetry's happy path (the
// overwhelmingly common case — no race, nothing to retry): a second
// db.Open against a database unitill-pos.service (simulated here by a
// first, real db.Open) already fully migrated must succeed on the very
// first attempt, migrate() applying nothing. The actual ut-docs#1094
// migration RACE this retry exists for (two connections both reading
// migrate()'s unprotected "current version" before either commits, so the
// second re-applies a migration the first just did — a genuine SQL error,
// not a lock busy_timeout could wait out) is a real multi-connection
// timing race against SQLite's own file locking;
// reproducing it deterministically here would mean a flaky test, which
// this repo's own testing standard rules out. retryOnError's tests above
// cover the actual retry/backoff logic (attempt count, which error
// surfaces, sleep timing) generically and deterministically instead —
// openWithRetry itself is a thin, direct wrapper around it plus db.Open.
func TestOpenWithRetry_SucceedsFirstTryAgainstAnAlreadyMigratedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")

	first, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("simulate the service's own already-completed migration: %v", err)
	}
	first.Close()

	second, err := openWithRetry(dbPath, provisionOpenRetryAttempts, 0)
	if err != nil {
		t.Fatalf("openWithRetry against an already-migrated database: %v", err)
	}
	second.Close()
}
