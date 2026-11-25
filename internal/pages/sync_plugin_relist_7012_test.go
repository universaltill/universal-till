package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7012: a stale Active record for an old listing L1 of plugin P must
// not get P pruned when the main still lists P under a new listing L2 — P's
// settings survive one tick.
func TestConvergePluginSet_7012_RelistedPluginKeepsInstallAndSettings(t *testing.T) {
	dp := newDeps6666(t, "7012-relist.db")
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"L1": presetManifest6930("com.t.p", "/tables"),
		"L2": presetManifest6930("com.t.p", "/tables"),
	})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L1", "com.t.p", boolp6666(true))}) || !active6666(t, dp, "com.t.p") {
		t.Fatalf("setup: P not installed active: %+v", logging.Recent())
	}
	repo := data.NewPluginRepo(dp.Db)
	if err := repo.UpsertPluginSetting(t.Context(), "com.t.p", "greeting", `"hello"`); err != nil {
		t.Fatalf("setup: setting: %v", err)
	}
	// The main now lists P under L2 only; the till's L1 record is stale.
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))}) {
		t.Fatalf("relist tick did not converge: %+v", logging.Recent())
	}
	if len(removes) != 0 {
		t.Fatalf("removes = %v, want none (P is still listed, under L2)", removes)
	}
	if !active6666(t, dp, "com.t.p") {
		t.Fatal("P not installed/active after relist tick")
	}
	if v, ok, err := repo.GetPluginSetting(t.Context(), "com.t.p", "greeting"); err != nil || !ok || v != `"hello"` {
		t.Fatalf("setting = %q,%v,%v, want it to survive the tick", v, ok, err)
	}
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

// A second tick with the stale L1 record still around keeps P and its
// settings, and installs/removes nothing.
func TestConvergePluginSet_7012_RelistedPluginSecondTickIsQuiet(t *testing.T) {
	dp := newDeps6666(t, "7012-relist2.db")
	calls := stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"L1": presetManifest6930("com.t.p", "/tables"),
		"L2": presetManifest6930("com.t.p", "/tables"),
	})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L1", "com.t.p", boolp6666(true))}) {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	repo := data.NewPluginRepo(dp.Db)
	if err := repo.UpsertPluginSetting(t.Context(), "com.t.p", "greeting", `"hello"`); err != nil {
		t.Fatal(err)
	}
	rows := []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))}
	installsBefore := len(*calls)
	for i := 0; i < 2; i++ {
		if !convergePluginSet(t.Context(), dp, rows) {
			t.Fatalf("tick %d did not converge: %+v", i, logging.Recent())
		}
	}
	if len(removes) != 0 || len(*calls) != installsBefore {
		t.Fatalf("removes=%v installs=%d (before %d), want none/no change", removes, len(*calls), installsBefore)
	}
	if !active6666(t, dp, "com.t.p") {
		t.Fatal("P not active after two ticks")
	}
	if v, ok, err := repo.GetPluginSetting(t.Context(), "com.t.p", "greeting"); err != nil || !ok || v != `"hello"` {
		t.Fatalf("setting = %q,%v,%v, want it to survive", v, ok, err)
	}
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

// Once the main drops P entirely (no row with P's PluginID), the leftover
// stale L1 record must not keep it alive: the next tick prunes it.
func TestConvergePluginSet_7012_RelistedThenDroppedPluginIsPruned(t *testing.T) {
	dp := newDeps6666(t, "7012-relist-drop.db")
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"L1": presetManifest6930("com.t.p", "/tables"),
		"L2": presetManifest6930("com.t.p", "/tables"),
	})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L1", "com.t.p", boolp6666(true))}) {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))}) || len(removes) != 0 {
		t.Fatalf("relist tick: removes=%v %+v", removes, logging.Recent())
	}
	if !convergePluginSet(t.Context(), dp, nil) {
		t.Fatalf("drop tick did not converge: %+v", logging.Recent())
	}
	if len(removes) != 1 || removes[0] != "remove:com.t.p" {
		t.Fatalf("removes = %v, want one remove of com.t.p", removes)
	}
	if active6666(t, dp, "com.t.p") {
		t.Fatal("P still active after the main dropped it")
	}
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

// A relist that also bumps the version: P at 1.0 under stale L1, main lists
// it under L2 at 1.1. One tick installs L2 once (in place), removes nothing,
// and keeps the settings.
func TestConvergePluginSet_7012_RelistedAtNewVersionUpgradesInPlace(t *testing.T) {
	dp := newDeps6666(t, "7012-relist-upgrade.db")
	// An in-place upgrade reconciles settings to the manifest's declared
	// keys, so the manifest declares the one under test.
	withSetting := func() *plugins.Manifest {
		m := presetManifest6930("com.t.p", "/tables")
		m.Settings = []plugins.ManifestSetting{{Key: "greeting", DefaultValue: "hi", Scope: "global"}}
		return m
	}
	calls := stubPersistingSyncInstall(t, map[string]*plugins.Manifest{"L1": withSetting(), "L2": withSetting()})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L1", "com.t.p", boolp6666(true))}) {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	repo := data.NewPluginRepo(dp.Db)
	if err := repo.UpsertPluginSetting(t.Context(), "com.t.p", "greeting", `"hello"`); err != nil {
		t.Fatal(err)
	}
	installsBefore := len(*calls)
	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2}) {
		t.Fatalf("upgrade tick did not converge: %+v", logging.Recent())
	}
	if len(removes) != 0 {
		t.Fatalf("removes = %v, want none", removes)
	}
	if got := (*calls)[installsBefore:]; len(got) != 1 || got[0].listing != "L2" {
		t.Fatalf("installs this tick = %+v, want exactly one, of L2", got)
	}
	rec, ok, err := plugins.NewInstallStatusStore(dp.Db).Get(t.Context(), "L2")
	if err != nil || !ok || rec.CurrentVersion != "1.1.0" {
		t.Fatalf("L2 record = %+v,%v,%v, want version 1.1.0", rec, ok, err)
	}
	if !active6666(t, dp, "com.t.p") {
		t.Fatal("P not active after upgrade tick")
	}
	if v, ok, err := repo.GetPluginSetting(t.Context(), "com.t.p", "greeting"); err != nil || !ok || v != `"hello"` {
		t.Fatalf("setting = %q,%v,%v, want it to survive", v, ok, err)
	}
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}
