package data

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/db"
)

func TestSettingsRepo_SetAndGet(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := NewSettingsRepo(db)
	ctx := context.Background()

	// Missing key returns ok=false
	if val, ok, err := repo.Get(ctx, "missing"); err != nil || ok || val != "" {
		t.Fatalf("expected missing key, got val=%q ok=%v err=%v", val, ok, err)
	}

	if err := repo.Set(ctx, "site.name", "Unitill"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, ok, err := repo.Get(ctx, "site.name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || val != "Unitill" {
		t.Fatalf("unexpected get result val=%q ok=%v", val, ok)
	}

	// Ensure updated_at is written
	var updatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM settings WHERE key='site.name'`).Scan(&updatedAt); err != nil {
		t.Fatalf("scan updated_at: %v", err)
	}
	if updatedAt == "" {
		t.Fatal("expected updated_at to be set")
	}
}

// TestSettingsRepo_GetByPrefix is the ut-docs#1323 regression: the sale
// screen used to call Get once per active payment method's
// "payments.fee.<id>" key. GetByPrefix replaces that with a single
// prefix-scan query.
func TestSettingsRepo_GetByPrefix(t *testing.T) {
	db := newSettingsTestDB(t)
	repo := NewSettingsRepo(db)
	ctx := context.Background()

	for k, v := range map[string]string{
		"payments.fee.cash":       `{"bp":0,"fixed":0}`,
		"payments.fee.card":       `{"bp":150,"fixed":10}`,
		"payments.default_method": "cash", // must NOT be picked up by the "payments.fee." prefix
		"display.mode":            "self_order",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	got, err := repo.GetByPrefix(ctx, "payments.fee.")
	if err != nil {
		t.Fatalf("GetByPrefix: %v", err)
	}
	want := map[string]string{
		"payments.fee.cash": `{"bp":0,"fixed":0}`,
		"payments.fee.card": `{"bp":150,"fixed":10}`,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %d: %+v", len(want), len(got), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %s: got %q, want %q (full result %+v)", k, got[k], v, got)
		}
	}

	// A prefix matching nothing returns an empty, non-nil map.
	none, err := repo.GetByPrefix(ctx, "no.such.prefix.")
	if err != nil {
		t.Fatalf("GetByPrefix (no match): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("expected an empty non-nil map, got %#v", none)
	}
}

// A literal '%'/'_' in the prefix must match itself, not act as a LIKE
// wildcard — same discipline as PluginRepo.ListStorageByPrefix.
func TestSettingsRepo_GetByPrefix_EscapesLikeWildcards(t *testing.T) {
	db := newSettingsTestDB(t)
	repo := NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.Set(ctx, "weird.100%.done", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, "weird.100Xdone.other", "no"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByPrefix(ctx, "weird.100%.")
	if err != nil {
		t.Fatalf("GetByPrefix: %v", err)
	}
	if len(got) != 1 || got["weird.100%.done"] != "yes" {
		t.Fatalf("literal '%%' in prefix must not act as a wildcard, got %+v", got)
	}
}

func newSettingsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	return db
}

func TestSettingsRepo_SetMany(t *testing.T) {
	db := newSettingsTestDB(t)
	repo := NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.Set(ctx, "a.existing", "old"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := repo.SetMany(ctx, map[string]string{
		"a.existing": "new",
		"b.fresh":    "1",
		"c.fresh":    "2",
	}); err != nil {
		t.Fatalf("SetMany: %v", err)
	}

	for key, want := range map[string]string{"a.existing": "new", "b.fresh": "1", "c.fresh": "2"} {
		val, ok, err := repo.Get(ctx, key)
		if err != nil || !ok || val != want {
			t.Fatalf("Get(%s) = %q ok=%v err=%v, want %q", key, val, ok, err, want)
		}
	}

	// Upsert, not duplicate: a.existing must still be a single row.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='a.existing'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("a.existing has %d rows, want 1", n)
	}
}

// ut-docs#12: SetMany must be all-or-nothing. A trigger aborts the write of
// one key that sorts mid-batch; keys sorted before it must be rolled back,
// not left behind as a partial write.
func TestSettingsRepo_SetManyAtomic(t *testing.T) {
	db := newSettingsTestDB(t)
	if _, err := db.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'm.boom'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	repo := NewSettingsRepo(db)
	ctx := context.Background()

	err := repo.SetMany(ctx, map[string]string{
		"a.first": "1", // sorts before m.boom — written, then must roll back
		"m.boom":  "x",
		"z.last":  "3",
	})
	if err == nil {
		t.Fatal("SetMany with an aborting trigger must return an error")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("settings has %d rows after failed SetMany, want 0 (partial write not rolled back)", n)
	}
}

func TestSettingsRepo_GetOrCreate_SeedsOnFirstCall(t *testing.T) {
	sqlDB := newSettingsTestDB(t)
	repo := NewSettingsRepo(sqlDB)
	ctx := context.Background()

	got, err := repo.GetOrCreate(ctx, "till.id", "seed-value")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got != "seed-value" {
		t.Fatalf("got %q, want the seed value on first call", got)
	}

	stored, ok, err := repo.Get(ctx, "till.id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || stored != "seed-value" {
		t.Fatalf("expected the seed persisted, got %q ok=%v", stored, ok)
	}
}

func TestSettingsRepo_GetOrCreate_ReturnsExistingWithoutOverwriting(t *testing.T) {
	sqlDB := newSettingsTestDB(t)
	repo := NewSettingsRepo(sqlDB)
	ctx := context.Background()

	if err := repo.Set(ctx, "till.id", "already-here"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := repo.GetOrCreate(ctx, "till.id", "would-be-a-different-value")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got != "already-here" {
		t.Fatalf("got %q, want the pre-existing value preserved", got)
	}
}

// ut-docs#271: a plain Get-then-Set get-or-create lets two concurrent
// callers on a fresh key each miss the Get, each pass a different ifAbsent
// value, and last-Set-wins — some callers then hold a value nobody else
// agrees on. GetOrCreate must make every concurrent caller converge on the
// SAME persisted value regardless of interleaving.
//
// Deliberately a REAL migrated file-backed database via db.Open, not
// newSettingsTestDB's ":memory:" DSN — a ":memory:" DB gives every pooled
// connection its OWN isolated database (see
// TestAddBarcodeConcurrentRace's comment, ut-docs#304), so it can't
// exercise real multi-connection contention at all.
func TestSettingsRepo_GetOrCreate_ConcurrentCallersConverge(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbh.Close()
	repo := NewSettingsRepo(dbh.DB)
	ctx := context.Background()

	// A single round can miss the race window by chance (goroutines can
	// serialize through pure luck) — run it repeatedly on a fresh key each
	// time and assert the invariant every round, same approach as
	// TestAddBarcodeConcurrentRace.
	const rounds = 30
	const callersPerRound = 8
	for round := 0; round < rounds; round++ {
		key := fmt.Sprintf("race.key.%d", round)
		results := make([]string, callersPerRound)
		errs := make([]error, callersPerRound)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(callersPerRound)
		for i := 0; i < callersPerRound; i++ {
			go func(i int) {
				defer wg.Done()
				<-start // released together to maximise the race window
				results[i], errs[i] = repo.GetOrCreate(ctx, key, fmt.Sprintf("candidate-%d", i))
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: GetOrCreate[%d]: %v", round, i, err)
			}
		}
		want := results[0]
		for i, got := range results {
			if got != want {
				t.Fatalf("round %d: caller %d got %q, caller 0 got %q — concurrent callers disagree on the winning value", round, i, got, want)
			}
		}

		stored, ok, err := repo.Get(ctx, key)
		if err != nil {
			t.Fatalf("round %d: Get: %v", round, err)
		}
		if !ok || stored != want {
			t.Fatalf("round %d: persisted value %q (ok=%v) doesn't match what every caller agreed on (%q)", round, stored, ok, want)
		}
	}
}
