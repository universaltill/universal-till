package data

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// ut-docs#532: mergeTakeawayOverrides used to do an unguarded
// Get-then-Upsert read-modify-write on a plugin setting — two near-
// simultaneous callers could both read the same starting JSON, both compute
// a merge, and the second write clobbers the first's addition.
// MergeAdditiveJSONMapSetting closes that race by running the read, merge
// and write inside one transaction (the DSN's _txlock=immediate takes the
// write lock at BEGIN, so the second caller cannot start its own read until
// the first has committed).

func TestMergeAdditiveJSONMapSetting_ConcurrentCallersBothLand(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "tax_code_" + string(rune('a'+i))
			_, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{key: 700 + i})
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}

	var raw string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatalf("read merged value: %v", err)
	}
	var got map[string]int
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("merged value not valid JSON %q: %v", raw, err)
	}
	if len(got) != n {
		t.Fatalf("expected all %d concurrent entries to land, got %d: %v", n, len(got), got)
	}
	for i := 0; i < n; i++ {
		key := "tax_code_" + string(rune('a'+i))
		if got[key] != 700+i {
			t.Fatalf("entry %q = %d, want %d (entry lost to a lost-update race)", key, got[key], 700+i)
		}
	}
}

// An entry already present (e.g. a merchant's hand-set override) must never
// be overwritten by a newly discovered value for the same key.
func TestMergeAdditiveJSONMapSetting_PreservesExistingEntry(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"a": 100}); err != nil {
		t.Fatal(err)
	}

	added, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"a": 999, "b": 200})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1 (only %q is new)", added, "b")
	}

	var raw string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["a"] != 100 {
		t.Fatalf("existing entry clobbered: a=%d, want 100", got["a"])
	}
	if got["b"] != 200 {
		t.Fatalf("new entry not merged: b=%d, want 200", got["b"])
	}
}

// An existing value that isn't valid JSON (a hand-edit gone wrong) must be
// left completely untouched, with an error returned so the caller can warn.
func TestMergeAdditiveJSONMapSetting_InvalidExistingJSONLeftUntouched(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()

	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPluginSetting(ctx, "com.example.tax", "takeaway_rate_overrides", `{not json`); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"a": 100}); err == nil {
		t.Fatal("expected an error for invalid existing JSON")
	}

	var raw string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.example.tax' AND key = 'takeaway_rate_overrides'`).
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{not json` {
		t.Fatalf("invalid existing JSON must be left untouched, got %q", raw)
	}
}
