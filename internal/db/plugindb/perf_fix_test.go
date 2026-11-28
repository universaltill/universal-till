package plugindb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func pragmaInt(t *testing.T, s *Store, name string) int {
	t.Helper()
	var v int
	if err := s.conn.QueryRowContext(ctx, `PRAGMA `+name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// synchronous=NORMAL (1) and temp_store=file (0), still WAL. Mutation: drop
// either pragma from the DSN -> red.
func TestStoreDurabilityAndTempPragmas_3157(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "p", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if v := pragmaInt(t, s, "synchronous"); v != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", v)
	}
	if v := pragmaInt(t, s, "temp_store"); v != 0 {
		t.Fatalf("temp_store = %d, want 0 (file)", v)
	}
	var mode string
	if err := s.conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}

// The hot path (store open, same quota, NoMigrate) takes neither the per-id
// lock nor the connection: it works while an upgrade holds the id lock AND
// while a long statement holds the connection. Mutation: remove the fast
// path -> red (ErrBusy / blocked).
func TestOpenFastPathNeedsNeitherIDLockNorConnection_3157(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	opts := Options{NoMigrate: true, BusyWait: 50 * time.Millisecond}
	if _, err := m.Open(ctx, "p", opts); err != nil { // sets the quota once
		t.Fatal(err)
	}
	unlock := m.lockID("p")
	s := m.open["p"]
	if !s.claim() { // the connection is "in use" by a long statement
		t.Fatal("could not hold the connection")
	}
	done := make(chan error, 1)
	go func() { _, err := m.Open(ctx, "p", opts); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fast path Open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fast-path Open blocked")
	}
	<-s.sem
	unlock()
}

// An Open that must take the id lock gives up when its context ends instead
// of queueing forever behind a slow holder. Mutation: non-ctx lock -> hangs.
func TestOpenHonoursContextWhileWaitingForIDLock_3157(t *testing.T) {
	m, _ := newMgr(t)
	unlock := m.lockID("p")
	defer unlock()
	c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Open(c, "p", Options{Migrations: migs()}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open ignored its context while waiting for the id lock")
	}
	if len(m.locks) != 1 || m.locks["p"].refs != 1 {
		t.Fatalf("ref count leaked after the abandoned wait: %+v", m.locks["p"])
	}
}

// A changed quota is still applied (the fast path must not freeze it).
func TestOpenReappliesAChangedQuota_3157(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "p", Options{NoMigrate: true, QuotaBytes: 1 << 20})
	ps := pragmaInt(t, s, "page_size")
	if got := pragmaInt(t, s, "max_page_count"); got != (1<<20)/ps {
		t.Fatalf("max_page_count = %d", got)
	}
	if _, err := m.Open(ctx, "p", Options{NoMigrate: true, QuotaBytes: 2 << 20}); err != nil {
		t.Fatal(err)
	}
	if got := pragmaInt(t, s, "max_page_count"); got != (2<<20)/ps {
		t.Fatalf("quota not re-applied: max_page_count = %d", got)
	}
}

// CloseIdle closes (and unlinks) idle stores; the next Open reopens the data.
// Mutation: forget to delete from the map -> reopen returns a closed store.
func TestCloseIdleUnlinksAndReopens_3157(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "p", Options{Migrations: migs()})
	_, _ = s.Exec(ctx, `INSERT INTO log(note) VALUES('x')`)
	time.Sleep(5 * time.Millisecond)
	m.CloseIdle(time.Millisecond)
	if len(m.open) != 0 || !s.closed() {
		t.Fatalf("idle store not closed: open=%d", len(m.open))
	}
	s2, err := m.Open(ctx, "p", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Query(ctx, `SELECT note FROM log`); err != nil {
		t.Fatal(err)
	}
}

// LRU eviction beyond maxOpenStores closes the victims (outside m.mu).
func TestEvictionClosesVictims_3157(t *testing.T) {
	m, _ := newMgr(t)
	var first *Store
	for i := 0; i <= maxOpenStores; i++ {
		id := "p" + string(rune('a'+i))
		s, err := m.Open(ctx, id, Options{NoMigrate: true})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = s
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(m.open) != maxOpenStores || !first.closed() {
		t.Fatalf("open=%d first closed=%v", len(m.open), first.closed())
	}
}
