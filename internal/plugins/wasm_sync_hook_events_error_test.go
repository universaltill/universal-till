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

// ut-docs#2280: a ListPluginHookEvents DB error during Sync's plugin-load
// loop used to be folded into the same "continue" as "this plugin genuinely
// has no hooks" — the module still counted as loaded, but ended up with
// ZERO registered event subscriptions. That reopens, through a different
// door, the exact payment-gate fail-open ut-docs#2278 closed: the blocking
// payment gate (blockingPaymentEventWithResponseAndID) treats "no
// subscribers" as "nothing to check, proceed," so an active payment plugin
// left with no subscriptions by a transient DB error let a sale/refund
// through with no plugin consulted and no error anywhere. Sync must now
// treat this exactly like a module load failure: mark the plugin broken,
// log it, and not leave it counted as cleanly loaded.
func TestWasmSync_ListPluginHookEventsErrorMarksBrokenNotSilentlyLoaded(t *testing.T) {
	guest := buildHostfnGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.hookeventserr"
	seedInstalledPlugin(t, db, pluginID, "HookEventsErr", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('hee1', ?, 'events:receive', 1)`, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	// Payment-provider entry, the same shape the fail-open in ut-docs#2278/
	// this card actually threatens: an active plugin that a blocking
	// payment gate would consult via bus.HasSubscribers.
	if _, err := db.Exec(`INSERT INTO plugin_entries
		(id, plugin_id, type, key, label, trigger_event, is_active) VALUES
		('hee-pay1', ?, 'payment', 'testpay', 'Test Pay', 'payment.testpay.requested', 1)`, pluginID); err != nil {
		t.Fatalf("seed payment entry: %v", err)
	}

	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}

	// Force a genuine DB error on ListPluginHookEvents — same technique
	// ut-docs#2278's own regression tests use (dropping the table the
	// method reads), not a mock.
	if _, err := db.Exec(`DROP TABLE plugin_hooks`); err != nil {
		t.Fatalf("drop plugin_hooks: %v", err)
	}

	start := time.Now()
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()

	repo := data.NewPluginRepo(db)
	info, ok, err := repo.GetPlugin(ctx, pluginID, "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != data.PluginStateBroken {
		t.Fatalf("expected install_state 'broken' after a ListPluginHookEvents error, got %q — the plugin must not silently load with zero subscriptions", info.InstallState)
	}

	// The tally must actually name the failure — not a quiet loaded=1/failed=0,
	// and specifically not a plugin double-counted as both loaded AND failed.
	foundTally := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "wasm sync:") &&
			strings.Contains(p.Msg, "0 plugins loaded") && strings.Contains(p.Msg, "1 failed") &&
			strings.Contains(p.Msg, pluginID) {
			foundTally = true
			break
		}
	}
	if !foundTally {
		t.Fatalf("expected a wasm sync tally log naming the failed plugin; recent logs: %+v", logging.Recent())
	}

	// The specific error must also be logged, not swallowed silently
	// (ut-docs#2280's own complaint about the pre-fix code).
	foundErrLog := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, pluginID) &&
			strings.Contains(p.Msg, "list hook events") {
			foundErrLog = true
			break
		}
	}
	if !foundErrLog {
		t.Fatalf("expected the ListPluginHookEvents error itself to be logged; recent logs: %+v", logging.Recent())
	}

	// The bus must have no subscribers for this plugin's would-be event —
	// same as before the fix — but now that's paired with a visible
	// 'broken' state instead of a silent, indistinguishable-from-healthy
	// zero-hooks load.
	if SharedBus(db).HasSubscribers("payment.testpay.authorize") {
		t.Fatal("a plugin whose hook events failed to list must not end up subscribed")
	}
}
