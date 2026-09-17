package plugins

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	sqlited "modernc.org/sqlite"
)

// ut-docs#2304: the same fail-open ut-docs#2280 closed for a
// ListPluginHookEvents error survives one step later in the same Sync loop.
// After ListPluginHookEvents succeeds, the loop calls
// bus.SubscribeWithHandler, which internally calls repo.HasActiveHook per
// event — a second read against plugin_hooks that can independently fail
// (partial corruption, a row-level I/O fault landing between the two
// reads). Before this fix that error path was a bare `continue` reached
// AFTER the plugin was already counted `loaded` (and healed, if it had
// been broken) — so a subscribe-time DB error left the plugin marked
// 'installed' with zero registered subscriptions, indistinguishable from a
// healthy zero-hook plugin. Sync must treat it exactly like the
// ListPluginHookEvents error one step earlier: mark broken, log, and do
// not count the plugin as loaded.
func TestWasmSync_SubscribeErrorMarksBrokenNotSilentlyLoaded(t *testing.T) {
	guest := buildHostfnGuest(t)

	path := filepath.Join(t.TempDir(), "plugins.db")
	setupDB, err := appdb.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = setupDB.Close() })
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.subscribeerr"
	seedInstalledPlugin(t, setupDB.DB, pluginID, "SubscribeErr", "1.0.0", "wasm", true)
	if _, err := setupDB.DB.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := setupDB.DB.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('se1', ?, 'events:receive', 1)`, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	// A real, active hook — ListPluginHookEvents must succeed and return
	// this event, so the loop actually reaches the SubscribeWithHandler
	// call this test is exercising (a plugin with zero events never
	// subscribes at all).
	if _, err := setupDB.DB.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES
		('se-hook1', ?, 'payment.testpay.authorize', 'tender', 1)`, pluginID); err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	if _, err := setupDB.DB.Exec(`INSERT INTO plugin_entries
		(id, plugin_id, type, key, label, trigger_event, is_active) VALUES
		('se-pay1', ?, 'payment', 'testpay', 'Test Pay', 'payment.testpay.requested', 1)`, pluginID); err != nil {
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

	// A second connection to the same on-disk DB (WAL mode makes this
	// safe), through a driver.Connector that fails only the ONE query
	// HasActiveHook issues (a COUNT(*) against plugin_hooks) — every other
	// statement, including ListPluginHookEvents' own SELECT DISTINCT
	// against the same table, passes through untouched. This isolates the
	// subscribe-time failure from the earlier ListPluginHookEvents call
	// ut-docs#2280 already covers — dropping the whole table would fail
	// both and never reach the code this test targets.
	syncDB := openHasActiveHookFailingConn(t, path)

	start := time.Now()
	w.Sync(ctx, syncDB)
	defer SharedBus(syncDB).ResetSubscribers()

	repo := data.NewPluginRepo(setupDB.DB)
	info, ok, err := repo.GetPlugin(ctx, pluginID, "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != data.PluginStateBroken {
		t.Fatalf("expected install_state 'broken' after a SubscribeWithHandler/HasActiveHook error, got %q — the plugin must not silently load with zero subscriptions", info.InstallState)
	}

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

	foundErrLog := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, pluginID) &&
			strings.Contains(p.Msg, "wasm subscribe") {
			foundErrLog = true
			break
		}
	}
	if !foundErrLog {
		t.Fatalf("expected the SubscribeWithHandler error itself to be logged; recent logs: %+v", logging.Recent())
	}

	// The bus must have no subscribers for this plugin's event — same as
	// before the fix — but now paired with a visible 'broken' state
	// instead of a silent, indistinguishable-from-healthy zero-subscriber
	// load.
	if SharedBus(syncDB).HasSubscribers("payment.testpay.authorize") {
		t.Fatal("a plugin whose subscribe call failed must not end up subscribed")
	}
}

// openHasActiveHookFailingConn opens a second *sql.DB against the same
// on-disk SQLite file already migrated by appdb.Open, through a
// driver.Connector that fails PrepareContext for any statement containing
// "COUNT(*) FROM plugin_hooks" — HasActiveHook's own query text, and
// nothing else's (ListPluginHookEvents' query is a SELECT DISTINCT against
// the same table, never a COUNT(*)). Mirrors the countingConn pattern in
// internal/data/export_repo_querycount_test.go, fault-injecting instead of
// counting.
func openHasActiveHookFailingConn(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	failingDB := sql.OpenDB(&failingConnector{dsn: dsn, driver: &sqlited.Driver{}})
	t.Cleanup(func() { _ = failingDB.Close() })
	return failingDB
}

type failingConnector struct {
	dsn    string
	driver driver.Driver
}

func (c *failingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &failingConn{Conn: conn}, nil
}

func (c *failingConnector) Driver() driver.Driver { return c.driver }

// failingConn wraps a driver.Conn, injecting a fault on the specific
// HasActiveHook query text (see openHasActiveHookFailingConn) while
// passing every other statement through unchanged.
type failingConn struct {
	driver.Conn
}

const failingQuerySubstring = "COUNT(*) FROM plugin_hooks"

func (c *failingConn) Prepare(query string) (driver.Stmt, error) {
	if strings.Contains(query, failingQuerySubstring) {
		return nil, fmt.Errorf("injected fault: %s", failingQuerySubstring)
	}
	return c.Conn.Prepare(query)
}

func (c *failingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if strings.Contains(query, failingQuerySubstring) {
		return nil, fmt.Errorf("injected fault: %s", failingQuerySubstring)
	}
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}
