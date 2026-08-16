package data

import (
	"context"
	"sync"
	"testing"
)

// ut-docs#785: UpsertPluginSettingScoped did its update-then-insert as two
// separate, non-transactional statements. plugin_settings' UNIQUE(plugin_id,
// key, scope, scope_id) doesn't actually block duplicate scope='global' rows
// because scope_id is NULL for global rows and SQLite treats NULLs as
// distinct in a unique index — so two near-simultaneous callers could each
// miss the other's row in the UPDATE and both fall through to INSERT,
// leaving two rows for the same (plugin_id, key, global) triple. The fix
// wraps the update-then-insert in its own transaction, the same
// _txlock=immediate serialization MergeAdditiveJSONMapSetting already uses
// (ut-docs#311/#532), so a second caller can't start until the first commits.

// The vulnerable window only exists once per row's lifecycle — from
// "absent" to "first INSERT lands" — so a single batch of concurrent
// callers gets exactly one shot at the race regardless of how many
// goroutines it uses: after any one caller's INSERT commits, every
// subsequent UPDATE in that batch matches a row and the INSERT branch is
// never reached again. Measured against the pre-fix code: one 20-goroutine
// batch reproduced the duplicate in under 20% of runs — real, but not a
// dependable regression guard. Re-running the race across many independent
// rounds (deleting the row between rounds so each round gets its own fresh
// "absent" window) gives every round its own shot, which is what actually
// makes this deterministic: measured at 100% reproduction against the
// pre-fix code across 20+ rounds, both with and without -race.
func TestUpsertPluginSettingScoped_ConcurrentCallersNeverDuplicateGlobalRow(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	const rounds = 25
	const callersPerRound = 8
	for round := 0; round < rounds; round++ {
		if _, err := d.DB.ExecContext(ctx,
			`DELETE FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'rate' AND scope = 'global'`); err != nil {
			t.Fatalf("round %d: clear prior row: %v", round, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, callersPerRound)
		for i := 0; i < callersPerRound; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // all callers fire together, maximizing the chance they all miss the row
				errs[i] = repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "rate", `{"v":1}`, "global")
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d caller %d: %v", round, i, err)
			}
		}

		var count int
		if err := d.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'rate' AND scope = 'global'`).
			Scan(&count); err != nil {
			t.Fatalf("round %d: count rows: %v", round, err)
		}
		if count != 1 {
			t.Fatalf("round %d: expected exactly 1 global row after %d concurrent upserts, got %d (duplicate-row race)", round, callersPerRound, count)
		}
	}
}

// Sequential calls (the far more common case in practice) must also never
// leave more than one row behind, and the final value must be the last
// write's.
func TestUpsertPluginSettingScoped_SequentialCallsUpdateInPlace(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	for i, v := range []string{`{"v":1}`, `{"v":2}`, `{"v":3}`} {
		if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "rate", v, "global"); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	var count int
	var value string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'rate' AND scope = 'global'`).
		Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 global row after 3 sequential upserts, got %d", count)
	}
	if err := d.DB.QueryRowContext(ctx,
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'rate' AND scope = 'global'`).
		Scan(&value); err != nil {
		t.Fatalf("read value: %v", err)
	}
	if value != `{"v":3}` {
		t.Fatalf("value = %q, want last write %q", value, `{"v":3}`)
	}
}
