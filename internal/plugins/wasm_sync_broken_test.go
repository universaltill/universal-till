package plugins

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// minimalWasmModule is the smallest valid wasm binary (magic + version, no
// sections) — it compiles cleanly, which is all Sync's load step does.
var minimalWasmModule = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// ut-docs#368: a till whose DB says a wasm plugin is installed while its
// binary is missing (the join-snapshot gap: a replica inherits the primary's
// plugin rows but not its files) used to fail the load with one log line and
// nothing else — no state flip, no tally, checkout silently degraded. Sync
// must flip the row to install_state 'broken' (and mirror it onto the
// marketplace install-status record when one exists), tally the failure
// loudly, and flip everything back once a later Sync loads the restored
// binary successfully.
func TestWasmRuntimeSync_MarksMissingBinaryBrokenAndHealsOnRestore(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.brokenwasm"
	const listingID = "listing-brokenwasm"
	seedInstalledPlugin(t, db, pluginID, "BrokenWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	// A marketplace-installed plugin carries an install-status record; the
	// broken/healed flips must mirror onto it (plugins.status.broken_binary).
	store := NewInstallStatusStore(db)
	if err := store.Save(ctx, InstallStatusRecord{
		ListingID: listingID, PluginID: pluginID, PluginName: "BrokenWasm",
		CurrentVersion: "1.0.0", State: InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install-status record: %v", err)
	}

	installState := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT install_state FROM plugins WHERE id = ?`, pluginID).Scan(&s); err != nil {
			t.Fatalf("read install_state: %v", err)
		}
		return s
	}

	logging.ResetRecent()
	start := time.Now()
	bus := SharedBus(db)
	genBefore := bus.Generation()

	// NO file on disk — this card's exact failure mode.
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()

	if got := installState(); got != "broken" {
		t.Fatalf("after a failed load install_state = %q, want %q", got, "broken")
	}
	// Review finding (2026-08-13): the bus generation must move at least
	// TWICE across a Sync pass that marks a plugin broken — once for
	// ResetSubscribers' own bump at the top (always happens), and once more
	// STRICTLY AFTER the broken flip lands (Sync's own bus.BumpGeneration()
	// call, guarded by failedCount>0). Without the second bump, a consumer
	// (pluginTaxRateAsker) that cached a "not broken" verdict against the
	// generation ResetSubscribers settled on — a concurrent ask landing
	// between that bump and the flip — never invalidates: the flip becomes
	// invisible to every ask that follows, for as long as this generation
	// number stands, which on a standalone till can be the rest of the
	// trading day. This is the only plugin Sync touches, so it never
	// Subscribes and never contributes a bump of its own — a delta of
	// exactly 1 here is the fail-open bug reproduced against real Sync, not
	// a hand-simulated marker.
	if got := bus.Generation() - genBefore; got < 2 {
		t.Fatalf("FAIL-OPEN: bus generation only moved by %d across a Sync pass that flipped a plugin broken "+
			"(want >= 2: ResetSubscribers' bump + a bump strictly after the flip) — a concurrent asker's cache "+
			"from the window between those two events would never invalidate", got)
	}
	rec, ok, err := store.Get(ctx, listingID)
	if err != nil || !ok {
		t.Fatalf("install-status record gone: ok=%v err=%v", ok, err)
	}
	if rec.State != InstallStateFailed || rec.MessageKey != brokenBinaryMessageKey {
		t.Fatalf("install-status record = (%s, %q), want (%s, %q)",
			rec.State, rec.MessageKey, InstallStateFailed, brokenBinaryMessageKey)
	}
	// The failure is tallied loudly (and thereby lands in the Problems ring).
	tallied := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(start.Add(-time.Second)) &&
			strings.Contains(p.Msg, "marked broken") {
			tallied = true
			break
		}
	}
	if !tallied {
		t.Fatal("expected a loud, tallied ERROR about wasm plugins failing to load")
	}

	// Restore the binary (what the sync converge loop's reinstall does) and
	// re-run Sync: self-healing must be visible — state and record flip back.
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, minimalWasmModule); err != nil {
		t.Fatalf("write module: %v", err)
	}
	w.Sync(ctx, db)

	if got := installState(); got != "installed" {
		t.Fatalf("after a successful load of a broken plugin install_state = %q, want %q", got, "installed")
	}
	rec, ok, err = store.Get(ctx, listingID)
	if err != nil || !ok {
		t.Fatalf("install-status record gone after heal: ok=%v err=%v", ok, err)
	}
	if rec.State != InstallStateActive || rec.MessageKey != "" {
		t.Fatalf("healed install-status record = (%s, %q), want (%s, \"\")", rec.State, rec.MessageKey, InstallStateActive)
	}

	// A repeat Sync with the healthy module stays quiet — no flip-flopping.
	w.Sync(ctx, db)
	if got := installState(); got != "installed" {
		t.Fatalf("steady-state Sync changed install_state to %q", got)
	}
}

// A failed-load mark must never overwrite the bookkeeping of an UNRELATED
// install failure: healing only restores a record that carries this
// runtime's own broken-binary message.
func TestWasmRuntimeSync_HealLeavesForeignFailureRecordsAlone(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.foreignfail"
	const listingID = "listing-foreignfail"
	seedInstalledPlugin(t, db, pluginID, "ForeignFail", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm', install_state = 'broken' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("seed broken state: %v", err)
	}
	store := NewInstallStatusStore(db)
	if err := store.Save(ctx, InstallStatusRecord{
		ListingID: listingID, PluginID: pluginID, State: InstallStateFailed,
		MessageKey: "plugins.install.error.version_mismatch", Retryable: true,
	}); err != nil {
		t.Fatalf("seed foreign failure record: %v", err)
	}

	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, minimalWasmModule); err != nil {
		t.Fatalf("write module: %v", err)
	}
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()

	rec, ok, err := store.Get(ctx, listingID)
	if err != nil || !ok {
		t.Fatalf("record gone: ok=%v err=%v", ok, err)
	}
	if rec.State != InstallStateFailed || rec.MessageKey != "plugins.install.error.version_mismatch" {
		t.Fatalf("foreign failure record was overwritten: (%s, %q)", rec.State, rec.MessageKey)
	}
}
