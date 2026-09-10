package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fresh install extracts to a folder with no data/ directory. Opening the
// default ./data/unitill-pos.db path must create that directory rather than
// fail with SQLite CANTOPEN ("out of memory (14)") and crash on first run.
func TestOpenCreatesMissingDataDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "extracted")
	// Neither base/ nor base/data/ exists yet — mirrors a fresh download.
	path := filepath.Join(base, "data", "unitill-pos.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a fresh install must succeed, got: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
}

// ut-docs#311: the DSN must put a real file-backed database in WAL journal
// mode on every pooled connection — WAL's MVCC is the cushion that keeps a
// concurrent reader/writer pair from tripping over rollback-journal lock
// promotion. (":memory:" DBs report journal_mode=memory instead; that's fine
// and deliberately not special-cased in Open.)
func TestOpenUsesWALJournalMode(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "wal.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	var mode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// ut-docs#311 regression test for the actual lock-contention bug: with the
// old deferred-BEGIN DSN, a transaction that read first (taking SHARED) and
// then wrote — the check-then-insert shape almost every write path in this
// repo uses — failed INSTANTLY with SQLITE_BUSY (~20µs) on the
// SHARED→RESERVED promotion while another connection held RESERVED:
// SQLite's deadlock-avoidance skips the busy handler on exactly that
// promotion, so the configured 5s busy_timeout never applied. (Verified
// while writing this test: a write-first transaction DOES wait via the busy
// handler even pre-fix — only the read-then-write shape hit the instant
// failure, so B below must read before it writes to reproduce the bug.)
// With _txlock=immediate every BeginTx runs BEGIN IMMEDIATE, which acquires
// the write lock at BEGIN through the busy handler like a normal wait.
//
// So: while connection A holds an open write transaction, connection B's
// BeginTx+read+write must (1) genuinely WAIT on A (not return in
// microseconds) and (2) ultimately SUCCEED once A commits. Fast-and-error
// was the bug; fast-and-no-error would mean the lock isn't real;
// slow-and-error would mean busy_timeout expired. Only slow-and-success is
// right.
//
// A real file-backed DB is required — ":memory:" gives each pooled
// connection its own isolated database and cannot exercise multi-connection
// locking (same reasoning as TestAddBarcodeConcurrentRace in internal/data).
func TestConcurrentWriterWaitsInsteadOfInstantBusy(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "locking.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	// Throwaway table, migration-free: this test is about locking, not schema.
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS lock_probe (n INTEGER)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	const holdFor = 200 * time.Millisecond
	held := make(chan struct{})
	aDone := make(chan error, 1)

	// Connection A: open a write transaction, do one write, hold it open for
	// a deliberate delay, then commit.
	go func() {
		txA, err := d.BeginTx(ctx, nil)
		if err != nil {
			aDone <- err
			close(held)
			return
		}
		if _, err := txA.ExecContext(ctx, `INSERT INTO lock_probe (n) VALUES (1)`); err != nil {
			_ = txA.Rollback()
			aDone <- err
			close(held)
			return
		}
		close(held) // A now holds the write lock
		time.Sleep(holdFor)
		aDone <- txA.Commit()
	}()

	<-held
	// Connection B: while A is still holding, begin + read + write, and time
	// how long the calls themselves take — pre-fix, the deferred BEGIN and
	// the SELECT both succeed instantly and the INSERT's SHARED→RESERVED
	// promotion returned SQLITE_BUSY in ~20µs.
	start := time.Now()
	txB, err := d.BeginTx(ctx, nil)
	if err == nil {
		var n int
		err = txB.QueryRowContext(ctx, `SELECT COUNT(*) FROM lock_probe`).Scan(&n)
	}
	if err == nil {
		_, err = txB.ExecContext(ctx, `INSERT INTO lock_probe (n) VALUES (2)`)
	}
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("concurrent write failed after %v (want a wait then success; instant SQLITE_BUSY is the pre-fix bug, a slow error means busy_timeout expired): %v", elapsed, err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("concurrent write returned in %v — it did not wait for the holding transaction (lock not actually held at BEGIN?)", elapsed)
	}
	if err := txB.Commit(); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	if err := <-aDone; err != nil {
		t.Fatalf("connection A: %v", err)
	}
}

// ADR-0020: self-order kiosk sales attribute to a fixed, well-known "kiosk"
// user (not a session — the kiosk route is auth-exempt). sales.cashier_id
// has a real FK to users(id), so this row must exist on every till,
// low-privilege (role=cashier, never manager) so it can never pass an
// isManagerOrAuthOff check if ever probed.
func TestKioskUserSeeded(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "kiosk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var role string
	var active int
	if err := d.DB.QueryRow(`SELECT role, is_active FROM users WHERE id = 'kiosk'`).Scan(&role, &active); err != nil {
		t.Fatalf("kiosk user not seeded: %v", err)
	}
	if role != "cashier" || active != 1 {
		t.Fatalf("unexpected kiosk user: role=%q active=%d, want role=cashier active=1", role, active)
	}
}

// ut-docs#1239: migration 036 was the first to use CREATE TEMP TABLE, and
// with SQLite's default temp_store a temp table is backed by a file in the
// system temp directory. An unrooted Android app has no writable default
// temp dir (TMPDIR unset, /tmp absent), so the very first real-device boot
// died with SQLITE_IOERR_GETTEMPPATH (6410) inside migration 36 and the
// till showed a bare white screen. temp_store=MEMORY keeps temp tables and
// indices off the filesystem entirely, on every pooled connection, on every
// platform — a desktop till gains a little speed, the Android till gains
// the ability to boot. (mobile.Start additionally exports a writable TMPDIR
// as a backstop for non-temp-table spills such as VACUUM.)
func TestOpenTempStoreMemory(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "temp_store.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	var mode int
	if err := d.QueryRow(`PRAGMA temp_store`).Scan(&mode); err != nil {
		t.Fatalf("read temp_store: %v", err)
	}
	if mode != 2 { // 0=default, 1=file, 2=memory
		t.Fatalf("temp_store = %d, want 2 (memory)", mode)
	}
}

// ut-docs#2030: Open() interpolated the raw filesystem path into a "file:"
// DSN unescaped. SQLite's own URI-form filename parser treats '#' and '?'
// as ending the path component, so a data directory containing one of them
// silently truncated the path AND dropped every _pragma query param after
// it — including foreign_keys(1), _txlock=immediate (ut-docs#311) and
// journal_mode(WAL). This proves both halves of that: the database actually
// lands at the full intended (nested) path rather than being truncated at
// the special character, and the pragmas a dropped query string would
// silently disable actually took effect.
func TestOpenEscapesSpecialCharsInPath(t *testing.T) {
	base := t.TempDir()
	// One special character per path segment, so a truncation at any one
	// of them is independently detectable via "did this exact file exist".
	dir := filepath.Join(base, "hash#test", "q?mark", "percent%dir")
	path := filepath.Join(dir, "db.sqlite")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a path containing #/?/%% must succeed, got: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created at the full intended path %q (truncated at a special character?): %v", path, err)
	}

	var journalMode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal — the _pragma query string was silently dropped", journalMode)
	}

	var foreignKeys int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1 — the _pragma query string was silently dropped", foreignKeys)
	}
}
