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
	// A listing-linked install-status record (marketplace install) so the
	// broken transition also surfaces on the store page's status surface.
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
	// The install-status record follows (listing-linked plugins only).
	if rec, ok, _ := statusStore.Get(ctx, "listing-broken"); !ok || rec.State != InstallStateFailed || rec.MessageKey != "plugins.status.broken_binary" {
		t.Fatalf("expected a Failed/broken_binary install-status record, got %+v (ok=%v)", rec, ok)
	}
	// The tally names the failure instead of pretending everything loaded.
	foundTally := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "wasm sync:") &&
			strings.Contains(p.Msg, "1 failed") && strings.Contains(p.Msg, pluginID) {
			foundTally = true
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
	if rec, ok, _ := statusStore.Get(ctx, "listing-broken"); !ok || rec.State != InstallStateActive || rec.MessageKey != "" {
		t.Fatalf("expected the install-status record restored to Active after heal, got %+v (ok=%v)", rec, ok)
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
// in the plugins table — it just has no install-status record to update.
func TestWasmSync_BrokenWithoutListingSkipsStatusStore(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	w := NewWasmRuntime(t.TempDir())

	const pluginID = "com.test.brokennolisting"
	seedInstalledPlugin(t, db, pluginID, "BrokenNoListing", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}

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
}
