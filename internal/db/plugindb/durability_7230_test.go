package plugindb

import (
	"context"
	"testing"
)

// ut-docs#7230 (R9): keep_years > 0 implies synchronous=FULL (2); every other
// plugin stays NORMAL (1).
func syncOf(t *testing.T, s *Store) int {
	t.Helper()
	v, err := s.Synchronous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSynchronousIsNormalWithoutRetention_7230(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "plain", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", got)
	}
}

func TestSynchronousIsFullWithRetention_7230(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "fiscal", Options{KeepYears: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL)", got)
	}
}

func TestMigrateForInstallOpensFullForRetention_7230(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.MigrateForInstall(ctx, "fisc2", "", "1.0.0", Options{Migrations: migs(), KeepYears: 6}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Open(ctx, "fisc2", Options{NoMigrate: true, KeepYears: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 2 {
		t.Fatalf("synchronous = %d, want 2", got)
	}
}

// A store opened before the manifest asked for retention (an upgrade adds
// keep_years) is raised to FULL on the next Open, and stays FULL.
func TestOpenStoreIsRaisedToFullWhenRetentionAppears_7230(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "grow", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 1 {
		t.Fatalf("before: %d, want 1", got)
	}
	s, err = m.Open(ctx, "grow", Options{NoMigrate: true, KeepYears: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 2 {
		t.Fatalf("after: %d, want 2", got)
	}
	s, err = m.Open(ctx, "grow", Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := syncOf(t, s); got != 2 {
		t.Fatalf("a retention store must never be lowered while open: %d", got)
	}
}

// Synchronous (test-only: the deadcode guard rejects an unused production method) reports the connection's PRAGMA synchronous (1 NORMAL, 2 FULL).
func (s *Store) Synchronous(ctx context.Context) (int, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var v int
	err = s.conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&v)
	return v, err
}
