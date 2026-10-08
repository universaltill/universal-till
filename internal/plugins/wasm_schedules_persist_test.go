package plugins

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3161 (ADR-0121 §8): the ticker reads schedules from the DB, so a
// manifest's schedules[] must be persisted at install and replaced — not
// accumulated — on every update and rollback.

func listSchedulesFor(t *testing.T, repo *data.PluginRepo, pluginID string) []data.PluginScheduleRow {
	t.Helper()
	all, err := repo.ListPluginSchedules(context.Background())
	if err != nil {
		t.Fatalf("ListPluginSchedules: %v", err)
	}
	var out []data.PluginScheduleRow
	for _, s := range all {
		if s.PluginID == pluginID {
			out = append(out, s)
		}
	}
	return out
}

func TestPersistManifest_PersistsAndReplacesSchedules(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)

	m := &Manifest{
		ID: "com.test.sched", Name: "Sched", Version: "1.0.0", Runtime: "wasm", Entrypoint: "./plugin.wasm",
		Permissions: []string{"schedule"},
		Schedules: []ManifestSchedule{
			{Event: "com.test.sched.sync.tick", EveryS: 300, JitterS: 60},
			{Event: "com.test.sched.retry.tick", EveryS: 30},
		},
	}
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := []data.PluginScheduleRow{
		{PluginID: m.ID, Event: "com.test.sched.retry.tick", EveryS: 30},
		{PluginID: m.ID, Event: "com.test.sched.sync.tick", EveryS: 300, JitterS: 60},
	}
	if got := listSchedulesFor(t, repo, m.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("after install:\n got %+v\nwant %+v", got, want)
	}

	// Update: one schedule dropped, one changed.
	m.Version = "1.1.0"
	m.Schedules = []ManifestSchedule{{Event: "com.test.sched.sync.tick", EveryS: 120, JitterS: 0}}
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	want = []data.PluginScheduleRow{{PluginID: m.ID, Event: "com.test.sched.sync.tick", EveryS: 120}}
	if got := listSchedulesFor(t, repo, m.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("after update:\n got %+v\nwant %+v", got, want)
	}

	// Uninstall removes them (FK cascade on the real schema).
	if err := UninstallPlugin(ctx, d.DB, m.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := listSchedulesFor(t, repo, m.ID); len(got) != 0 {
		t.Fatalf("after uninstall listed %+v", got)
	}
}

// Rollback writes plugin rows without PersistManifest, so it must rewrite
// the schedules from the target version's manifest too.
func TestRollback_RewritesSchedules(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()
	repo := data.NewPluginRepo(d.DB)

	v2 := &Manifest{
		ID: "com.rb.sched", Name: "RB", Version: "2.0.0", Runtime: "none",
		Schedules: []ManifestSchedule{{Event: "com.rb.sched.new.tick", EveryS: 60}},
	}
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.sched', '1.0.0', 'RB', 'none', '', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-09-30T00:00:00Z')`)
	seedRollbackTarget(t, base, "com.rb.sched", "1.0.0",
		`{"id":"com.rb.sched","name":"RB","version":"1.0.0","runtime":"none","schedules":[{"event":"com.rb.sched.old.tick","every_s":45,"jitter_s":5}]}`)

	if err := NewRollbackManager(d.DB, base).Rollback(ctx, "com.rb.sched", "1.0.0", "tester"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	want := []data.PluginScheduleRow{{PluginID: "com.rb.sched", Event: "com.rb.sched.old.tick", EveryS: 45, JitterS: 5}}
	if got := listSchedulesFor(t, repo, "com.rb.sched"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after rollback:\n got %+v\nwant %+v", got, want)
	}
}
