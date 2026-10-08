package data

import (
	"context"
	"reflect"
	"testing"
)

// ut-docs#3161 (ADR-0121 §8): a plugin's `schedules[]` are persisted at
// install, replaced on every update, and removed with the plugin. This runs
// against the real migrated schema, so migration 068's table, its primary
// key and its ON DELETE CASCADE are part of what is under test.
func TestPluginRepo_PluginSchedulesRoundTripReplaceAndCascade(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "schedules.db")
	repo := NewPluginRepo(d.DB)

	for _, id := range []string{"com.test.a", "com.test.b"} {
		if _, err := d.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
			VALUES (?, '1.0.0', ?, 'wasm', './plugin.wasm', 'file://x', 'x', '0.0.1', '1.0', datetime('now'))`, id, id); err != nil {
			t.Fatalf("seed catalog %s: %v", id, err)
		}
		if _, err := d.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, ?, '1.0.0', './plugin.wasm', 'wasm', 1)`, id, id); err != nil {
			t.Fatalf("seed plugin %s: %v", id, err)
		}
	}

	replace := func(pluginID string, rows []PluginScheduleRow) {
		t.Helper()
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := repo.ReplacePluginSchedules(ctx, tx, pluginID, rows); err != nil {
			_ = tx.Rollback()
			t.Fatalf("ReplacePluginSchedules(%s): %v", pluginID, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	list := func() []PluginScheduleRow {
		t.Helper()
		got, err := repo.ListPluginSchedules(ctx)
		if err != nil {
			t.Fatalf("ListPluginSchedules: %v", err)
		}
		return got
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("empty DB listed %v", got)
	}

	replace("com.test.a", []PluginScheduleRow{
		{Event: "com.test.a.sync.tick", EveryS: 300, JitterS: 60},
		{Event: "com.test.a.retry.tick", EveryS: 30},
	})
	replace("com.test.b", []PluginScheduleRow{{Event: "com.test.b.poll", EveryS: 45, JitterS: 45}})

	want := []PluginScheduleRow{
		{PluginID: "com.test.a", Event: "com.test.a.retry.tick", EveryS: 30, JitterS: 0},
		{PluginID: "com.test.a", Event: "com.test.a.sync.tick", EveryS: 300, JitterS: 60},
		{PluginID: "com.test.b", Event: "com.test.b.poll", EveryS: 45, JitterS: 45},
	}
	if got := list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after install:\n got %+v\nwant %+v", got, want)
	}

	// An update replaces the set: a schedule the new version drops is gone,
	// a changed interval is the new one, and the other plugin is untouched.
	replace("com.test.a", []PluginScheduleRow{{Event: "com.test.a.sync.tick", EveryS: 600, JitterS: 0}})
	want = []PluginScheduleRow{
		{PluginID: "com.test.a", Event: "com.test.a.sync.tick", EveryS: 600, JitterS: 0},
		{PluginID: "com.test.b", Event: "com.test.b.poll", EveryS: 45, JitterS: 45},
	}
	if got := list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after update:\n got %+v\nwant %+v", got, want)
	}

	// Uninstall (DeletePlugin) removes the plugin's schedules via the FK
	// cascade — the production DB runs with foreign_keys=ON.
	if err := repo.DeletePlugin(ctx, nil, "com.test.b"); err != nil {
		t.Fatalf("DeletePlugin: %v", err)
	}
	want = want[:1]
	if got := list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after uninstall:\n got %+v\nwant %+v", got, want)
	}

	// An empty set clears them.
	replace("com.test.a", nil)
	if got := list(); len(got) != 0 {
		t.Fatalf("after clearing listed %v", got)
	}
}
