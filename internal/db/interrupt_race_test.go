package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#1718: a cancelled query must never kill an UNRELATED query running
// on a different goroutine.
//
// Reported twice in one day on a real tablet: the till reached a state where
// every SQLite call returned SQLITE_INTERRUPT ("interrupted (9)") until the
// Android app was force-stopped. The operator saw a blank page reading
// "auth unavailable" and had no way back — /login itself was dead, because
// auth_page.go's NeedsFirstBoot could not read the users table.
//
// The captured device log is what pins the mechanism down. 715 interrupt
// failures, and the victims were overwhelmingly BACKGROUND goroutines —
// `lan discovery`, and the `cloudsync` tick, which passes the long-lived
// server context straight through (cloudsync.Start) and cancels nothing per
// tick. So the goroutine whose context was cancelled was not the goroutine
// that died: the cancellation's watcher fired sqlite3_interrupt against a
// connection that had already gone back to database/sql's pool, and whoever
// picked that connection up next took the hit.
//
// modernc.org/sqlite v1.29.10's interruptOnDone checked its "done" flag and
// called interrupt as two separate steps, so the watcher could win the check,
// be descheduled, and fire the interrupt after its own statement had long
// finished. Upstream fixed exactly this by putting both under one mutex,
// with the comment "donemu prevents a TOCTOU logical race between checking
// the done flag and calling interrupt".
//
// This test drives that race directly rather than trusting a version number:
// one pool, a crowd of goroutines whose contexts are cancelled mid-query, and
// one victim goroutine whose context is NEVER cancelled. The victim must
// never see an interrupt.
func TestCancelledQueryNeverInterruptsAnotherGoroutine(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "interrupt.db")
	d, err := Open(dbFile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// Force real contention over a small, shared set of connections: the bug
	// is cross-CONNECTION-REUSE contamination, so a pool big enough to give
	// every goroutine its own connection would hide it.
	d.SetMaxOpenConns(2)
	d.SetMaxIdleConns(2)

	if _, err := d.Exec(`CREATE TABLE interrupt_probe (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 200; i++ {
		if _, err := d.Exec(`INSERT INTO interrupt_probe (v) VALUES (?)`, "row"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var (
		wg       sync.WaitGroup
		stop     atomic.Bool
		victimNs atomic.Int64 // how many uncancellable queries actually ran
		bad      atomic.Value // first cross-goroutine interrupt seen
	)

	// The cancellers: each starts a query and cancels its context almost
	// immediately, so cancellation lands while the statement is in flight or
	// just after it completes — the window the race lives in.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					time.Sleep(time.Duration(50+len("x")) * time.Microsecond)
					cancel()
				}()
				rows, err := d.QueryContext(ctx, `SELECT id, v FROM interrupt_probe ORDER BY id`)
				if err == nil {
					for rows.Next() {
					}
					_ = rows.Close()
				}
				cancel()
			}
		}()
	}

	// The victim: a background worker, exactly like cloudsync's tick. Its
	// context is never cancelled, so NOTHING it does can legitimately return
	// SQLITE_INTERRUPT. Any interrupt here came from another goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			var n int
			err := d.QueryRowContext(context.Background(),
				`SELECT count(*) FROM interrupt_probe`).Scan(&n)
			victimNs.Add(1)
			if err == nil {
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if strings.Contains(err.Error(), "interrupted") {
				bad.CompareAndSwap(nil, err.Error())
				stop.Store(true)
				return
			}
		}
	}()

	time.Sleep(3 * time.Second)
	stop.Store(true)
	wg.Wait()

	if got := bad.Load(); got != nil {
		t.Fatalf("a cancelled query on another goroutine interrupted an uncancellable one: %v\n"+
			"This is ut-docs#1718 — the till dies with \"auth unavailable\" in exactly this way.\n"+
			"Check modernc.org/sqlite is new enough to carry the donemu fix in interruptOnDone.", got)
	}
	// Guard against a vacuous pass: if the victim barely ran, the absence of
	// an interrupt proves nothing about the race.
	if n := victimNs.Load(); n < 100 {
		t.Fatalf("victim goroutine only completed %d queries — too few to have exercised the race", n)
	}
}
