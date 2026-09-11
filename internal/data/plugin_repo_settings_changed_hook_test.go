package data

import (
	"context"
	"testing"
)

// ut-docs#1941: the three plugin-settings writers used to rely on EVERY
// caller remembering a separate plugins.SharedBus(db).BumpGeneration() after
// the write, enforced only by a grep guard (scripts/ci/guard-plugin-settings-
// bump.sh, now deleted). That convention was broken twice for real
// (ut-docs#222, ut-docs#1351 — a live VAT over-collection in the Germany
// café pilot). internal/data cannot import internal/plugins (import cycle),
// so the invalidation is now structural via PluginRepo.OnSettingsChanged: a
// caller that can see the bus attaches the bump ONCE at construction and
// every write through that repo fires it, gated inside the writer on "did
// this write actually change anything" so the hook can never be forgotten
// and never fires for a no-op.

func TestPluginRepo_OnSettingsChanged_FiresOnUpsertPluginSetting(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	fired := 0
	if got := repo.OnSettingsChanged(func() { fired++ }); got != repo {
		t.Fatalf("OnSettingsChanged must return the same *PluginRepo for chaining")
	}

	// First write: INSERT path (no row yet) — must fire.
	if err := repo.UpsertPluginSetting(ctx, "com.example.tax", "rate", `"700"`); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("after first upsert (insert): hook fired %d times, want 1", fired)
	}

	// Second write with a DIFFERENT value: UPDATE path — must fire.
	if err := repo.UpsertPluginSetting(ctx, "com.example.tax", "rate", `"1900"`); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("after second upsert (changed value): hook fired %d times, want 2", fired)
	}

	// Third write with the SAME value: nothing an .ask asker reads changed —
	// mirrors the caller-side `string(raw) == row.ValueJSON → continue` gate
	// plugin_settings_page.go used to rely on, now enforced inside the writer.
	if err := repo.UpsertPluginSetting(ctx, "com.example.tax", "rate", `"1900"`); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("after third upsert (identical value): hook fired %d times, want still 2", fired)
	}
}

func TestPluginRepo_OnSettingsChanged_FiresOnUpsertPluginSettingScoped(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	fired := 0
	repo.OnSettingsChanged(func() { fired++ })

	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "reader_id", `"abc"`, "register", false); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("after scoped insert: hook fired %d times, want 1", fired)
	}
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "reader_id", `"def"`, "register", false); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("after scoped update (changed value): hook fired %d times, want 2", fired)
	}
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "reader_id", `"def"`, "register", false); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("after scoped update (identical value): hook fired %d times, want still 2", fired)
	}

	// A declared secret is sealed with a fresh nonce on every write, so the
	// stored bytes always differ — a secret write always counts as a change
	// (the caller-side diff guard in plugin_settings_page.go still filters
	// blank "keep current" submissions before it ever reaches the writer).
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "api_token", `"s3cret"`, "global", true); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "api_token", `"s3cret"`, "global", true); err != nil {
		t.Fatal(err)
	}
	if fired != 4 {
		t.Fatalf("after two secret writes: hook fired %d times, want 4", fired)
	}
}

func TestPluginRepo_OnSettingsChanged_MergeAdditiveFiresOnlyWhenAdded(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	fired := 0
	repo.OnSettingsChanged(func() { fired++ })

	added, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"tc1": 700})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || fired != 1 {
		t.Fatalf("first merge: added=%d fired=%d, want 1/1", added, fired)
	}

	// Same entry again: nothing new lands (added == 0) — mirrors the
	// `if added > 0` gate import_page.go's mergeTakeawayOverrides used to
	// carry; the hook must NOT fire.
	added, err = repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"tc1": 1900})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || fired != 1 {
		t.Fatalf("merge with no new entries: added=%d fired=%d, want 0/1", added, fired)
	}

	// Empty input short-circuits before the transaction — no fire.
	added, err = repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || fired != 1 {
		t.Fatalf("merge with empty input: added=%d fired=%d, want 0/1", added, fired)
	}

	// A genuinely new entry alongside an existing one: fires once.
	added, err = repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"tc1": 1, "tc2": 700})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || fired != 2 {
		t.Fatalf("merge with one new entry: added=%d fired=%d, want 1/2", added, fired)
	}
}

// Regression safety for every other constructor of PluginRepo (fiscal_repo,
// the WASM host functions, sync, tests): a repo with no hook attached must
// keep working exactly as before — no nil-func panic on any writer.
func TestPluginRepo_OnSettingsChanged_NilHookDoesNotPanic(t *testing.T) {
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	seedCatalogEntry(t, d, "com.example.tax", "1.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.tax"); err != nil {
		t.Fatal(err)
	}

	if err := repo.UpsertPluginSetting(ctx, "com.example.tax", "rate", `"700"`); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.tax", "rate", `"1900"`, "global", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "takeaway_rate_overrides", map[string]int{"tc1": 700}); err != nil {
		t.Fatal(err)
	}
}
