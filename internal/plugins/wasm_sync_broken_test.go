package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#368: WasmRuntime.Sync used to silently `continue` on a load
// failure — a joined replica whose plugins row arrived in the snapshot with
// no file on disk logged one error line and otherwise looked healthy, while
// tax silently fell back to base rates. Sync must now (1) flip the row to
// install_state='broken' and count the failure in a real tally, and (2) on
// a later SUCCESSFUL load of a previously-broken plugin, flip it back to
// 'installed' — self-heal must be visible, not just functional.

func TestWasmSync_MarksMissingBinaryBrokenAndHealsOnRecovery(t *testing.T) {
	guest := buildHostfnGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.brokenwasm"
	seedInstalledPlugin(t, db, pluginID, "BrokenWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('bw1', ?, 'events:receive', 1)`, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES
		('bwh1', ?, 'tax.rate.ask', 'tax.rate', 1)`, pluginID); err != nil {
		t.Fatalf("seed hooks: %v", err)
	}
	// A listing-linked install-status record (marketplace install). The
	// broken/heal transitions must leave it UNTOUCHED (round-2 review
	// BLOCKER): its State tracks the install lifecycle, and this plugin DID
	// install fine before breaking at load time — demoting it to Failed made
	// convergePluginSet's prune loop (which only prunes Active records) skip
	// the plugin forever, so it could never be uninstalled from a replica.
	statusStore := NewInstallStatusStore(db)
	if err := statusStore.Save(ctx, InstallStatusRecord{
		ListingID: "listing-broken", PluginID: pluginID, PluginName: "BrokenWasm",
		CurrentVersion: "1.0.0", State: InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}

	// NO file on disk — exactly what a join snapshot leaves behind.
	start := time.Now()
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()

	repo := data.NewPluginRepo(db)
	info, ok, err := repo.GetPlugin(ctx, pluginID, "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != data.PluginStateBroken {
		t.Fatalf("expected install_state 'broken' after a failed load, got %q", info.InstallState)
	}
	// The install-status record is left exactly as it was: still Active, no
	// message — plugins.install_state='broken' is the single source of truth
	// for the broken condition (the plugins-page chip and the tax
	// fail-closed check both read it directly).
	if rec, ok, _ := statusStore.Get(ctx, "listing-broken"); !ok || rec.State != InstallStateActive || rec.MessageKey != "" {
		t.Fatalf("expected the install-status record untouched (Active, no message) while broken, got %+v (ok=%v)", rec, ok)
	}
	// The tally names the failure instead of pretending everything loaded —
	// and, for a MARKETPLACE-installed plugin (this one has a listing-linked
	// install-status record), says it will self-heal via the sync loop's
	// re-fetch, NOT that it needs a manual file re-import (ut-docs#368
	// round-2: recovery differs by provenance and the tally must say which).
	foundTally := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "wasm sync:") &&
			strings.Contains(p.Msg, "1 failed") && strings.Contains(p.Msg, pluginID) {
			foundTally = true
			if !strings.Contains(p.Msg, "re-fetched from the marketplace automatically") {
				t.Fatalf("marketplace-installed broken plugin's tally must say it self-heals via the marketplace re-fetch, got: %q", p.Msg)
			}
			if strings.Contains(p.Msg, "re-import the plugin file") {
				t.Fatalf("marketplace-installed broken plugin's tally must not tell the operator to re-import a file, got: %q", p.Msg)
			}
			break
		}
	}
	if !foundTally {
		t.Fatalf("expected a wasm sync tally log naming the failed plugin; recent logs: %+v", logging.Recent())
	}
	// Broken ≠ unsubscribed-only: the module must not be registered.
	w.mu.Lock()
	_, loaded := w.modules[pluginID]
	w.mu.Unlock()
	if loaded {
		t.Fatal("a plugin whose file is missing must not appear loaded")
	}

	// --- self-heal: the file appears (e.g. re-fetched by the sync loop) ---
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}

	w.Sync(ctx, db)

	info, _, _ = repo.GetPlugin(ctx, pluginID, "")
	if info.InstallState != data.PluginStateInstalled {
		t.Fatalf("expected install_state healed back to 'installed' after a successful load, got %q", info.InstallState)
	}
	// Still untouched after the heal — there was never anything to flip back.
	if rec, ok, _ := statusStore.Get(ctx, "listing-broken"); !ok || rec.State != InstallStateActive || rec.MessageKey != "" {
		t.Fatalf("expected the install-status record still untouched (Active, no message) after heal, got %+v (ok=%v)", rec, ok)
	}
	w.mu.Lock()
	_, loaded = w.modules[pluginID]
	w.mu.Unlock()
	if !loaded {
		t.Fatal("healed plugin should be loaded")
	}
	if !SharedBus(db).HasSubscribers("tax.rate.ask") {
		t.Fatal("healed plugin should be subscribed to its hook events again")
	}
}

// A plugin with NO listing (file-imported) still flips to broken/installed
// in the plugins table — and Sync must not invent an install-status record
// for it (nor touch any other).
func TestWasmSync_BrokenWithoutListingSkipsStatusStore(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	w := NewWasmRuntime(t.TempDir())

	const pluginID = "com.test.brokennolisting"
	seedInstalledPlugin(t, db, pluginID, "BrokenNoListing", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}

	start := time.Now()
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()

	info, ok, err := data.NewPluginRepo(db).GetPlugin(ctx, pluginID, "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != data.PluginStateBroken {
		t.Fatalf("expected install_state 'broken', got %q", info.InstallState)
	}
	records, err := NewInstallStatusStore(db).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("no listing: expected no install-status records, got %+v", records)
	}

	// A FILE-IMPORTED broken plugin has no listing to re-fetch from — the
	// tally must send the operator to a manual re-import, and must not
	// promise the marketplace self-heal it can never get (ut-docs#368
	// round-2: recovery differs by provenance and the tally must say which).
	// The phrasing must also read correctly for this single-ID bucket — no
	// bare plural verb like "[id] have no marketplace listing".
	foundTally := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "wasm sync:") &&
			strings.Contains(p.Msg, "1 failed") && strings.Contains(p.Msg, pluginID) {
			foundTally = true
			if !strings.Contains(p.Msg, "re-import the plugin file") {
				t.Fatalf("file-imported broken plugin's tally must tell the operator to re-import the plugin file, got: %q", p.Msg)
			}
			if strings.Contains(p.Msg, "re-fetched from the marketplace automatically") {
				t.Fatalf("file-imported broken plugin's tally must not promise a marketplace self-heal, got: %q", p.Msg)
			}
			if strings.Contains(p.Msg, "] have no marketplace listing") {
				t.Fatalf("tally phrasing must be number-agnostic (single plugin + plural verb), got: %q", p.Msg)
			}
			break
		}
	}
	if !foundTally {
		t.Fatalf("expected a wasm sync tally log naming the failed plugin; recent logs: %+v", logging.Recent())
	}
}
