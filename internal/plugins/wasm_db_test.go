package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 §3/§4 (ut-docs#3157): db_exec / db_query / db_error /
// db_open_import / db_query_h, driven directly against a linear-memory
// stand-in for the guest (like the event_publish tests).

const (
	dbSQLOff  = 0
	dbArgsOff = 8192
	dbDstOff  = 16384
	dbDstCap  = 128 << 10
)

type dbFixture struct {
	t    *testing.T
	root string
	db   *sql.DB
	mem  api.Module
}

func newDBFixture(t *testing.T) *dbFixture {
	t.Helper()
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() {
		closeAllPluginData()
		paths.Init(prev)
	})
	return &dbFixture{t: t, root: root, db: hostfnTestDB(t), mem: publishTestMemory(t)}
}

// plugin installs id; withDB adds the manifest db block, perms are granted.
func (f *dbFixture) plugin(id string, withDB bool, perms ...string) *hostState {
	f.t.Helper()
	dir := filepath.Join(f.root, "plugins", id, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	extra := ""
	if withDB {
		extra = `,"db":{"migrations":"migrations"}`
	}
	manifest := fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm"%s}`, id, extra)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		f.t.Fatal(err)
	}
	seedPlugin(f.t, f.db, id)
	for _, p := range perms {
		grantPerm(f.t, f.db, id, p)
	}
	return &hostState{pluginID: id, db: f.db}
}

func (f *dbFixture) put(off uint32, s string) uint32 {
	f.t.Helper()
	if !f.mem.Memory().Write(off, []byte(s)) {
		f.t.Fatal("write guest memory")
	}
	return uint32(len(s))
}

func (f *dbFixture) exec(ctx context.Context, hs *hostState, q, args string) int64 {
	f.t.Helper()
	ql := f.put(dbSQLOff, q)
	al := f.put(dbArgsOff, args)
	return hostDBExec(withHostState(ctx, hs), f.mem, dbSQLOff, ql, dbArgsOff, al)
}

func (f *dbFixture) query(ctx context.Context, hs *hostState, q, args string, dstCap uint32) (int32, string) {
	f.t.Helper()
	ql := f.put(dbSQLOff, q)
	al := f.put(dbArgsOff, args)
	n := hostDBQuery(withHostState(ctx, hs), f.mem, dbSQLOff, ql, dbArgsOff, al, dbDstOff, dstCap)
	return n, f.out(n, dstCap)
}

func (f *dbFixture) out(n int32, dstCap uint32) string {
	if n <= 0 {
		return ""
	}
	if uint32(n) > dstCap {
		n = int32(dstCap)
	}
	b, _ := f.mem.Memory().Read(dbDstOff, uint32(n))
	return string(b)
}

func (f *dbFixture) lastError(hs *hostState) string {
	n := hostDBError(withHostState(context.Background(), hs), f.mem, dbDstOff, dbDstCap)
	return f.out(n, dbDstCap)
}

func (f *dbFixture) dbFile(id string) string {
	return paths.Data("plugin-data", id, "plugin.db")
}

func mustExec(t *testing.T, got int64, want int64) {
	t.Helper()
	if got != want {
		t.Fatalf("db_exec = %d, want %d", got, want)
	}
}

func TestDBRequiresDBOwnPermission(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.noperm", true) // db block, no grant
	ctx := context.Background()
	if got := f.exec(ctx, hs, "CREATE TABLE t(a)", ""); got != hostErrDenied {
		t.Fatalf("db_exec without db:own = %d, want -2", got)
	}
	if n, _ := f.query(ctx, hs, "SELECT 1", "", dbDstCap); n != hostErrDenied {
		t.Fatalf("db_query without db:own = %d, want -2", n)
	}
	if h := hostDBOpenImport(withHostState(ctx, hs), 0); h != hostErrDenied {
		t.Fatalf("db_open_import without db:own = %d, want -2", h)
	}
	if _, err := os.Stat(filepath.Join(f.root, "plugin-data", hs.pluginID)); !os.IsNotExist(err) {
		t.Fatalf("a plugin without db:own got a data dir: %v", err)
	}
}

func TestDBPermissionRevokedLiveTakesEffect(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.revoke", true, "db:own")
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE t(a)", ""), 0)
	if _, err := f.db.Exec(`UPDATE plugin_permissions SET granted=0 WHERE plugin_id=? AND permission='db:own'`, hs.pluginID); err != nil {
		t.Fatal(err)
	}
	if got := f.exec(ctx, hs, "INSERT INTO t VALUES (1)", ""); got != hostErrDenied {
		t.Fatalf("db_exec after revoke = %d, want -2", got)
	}
}

func TestDBManifestWithoutDBBlockGetsNoFile(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.noblock", false, "db:own")
	if got := f.exec(context.Background(), hs, "CREATE TABLE t(a)", ""); got != hostErrDenied {
		t.Fatalf("db_exec with no db block = %d, want -2", got)
	}
	if msg := f.lastError(hs); !strings.Contains(msg, "no db block") {
		t.Fatalf("db_error = %q, want it to say why", msg)
	}
	if _, err := os.Stat(filepath.Join(f.root, "plugin-data", hs.pluginID)); !os.IsNotExist(err) {
		t.Fatalf("no db block must create no file: %v", err)
	}
}

func TestDBRoundTripAndPluginsAreIsolated(t *testing.T) {
	f := newDBFixture(t)
	a := f.plugin("com.test.db.a", true, "db:own")
	b := f.plugin("com.test.db.b", true, "db:own")
	ctx := context.Background()
	for _, hs := range []*hostState{a, b} {
		mustExec(t, f.exec(ctx, hs, "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)", ""), 0)
	}
	mustExec(t, f.exec(ctx, a, "INSERT INTO notes(body) VALUES (?)", `["secret-of-a"]`), 1)

	n, out := f.query(ctx, a, "SELECT body FROM notes", "", dbDstCap)
	if n <= 0 || out != `[{"body":"secret-of-a"}]` {
		t.Fatalf("A reads %d %q", n, out)
	}
	// B has the same table name but must see only its own (empty) file: the
	// id comes from the host state, the guest has no way to name another.
	n, out = f.query(ctx, b, "SELECT body FROM notes", "", dbDstCap)
	if out != `[]` {
		t.Fatalf("B reads %d %q, want its own empty table", n, out)
	}
	if f.dbFile(a.pluginID) == f.dbFile(b.pluginID) {
		t.Fatal("two plugins share a database file")
	}
	// ATTACHing the other plugin's file is refused, by path or by name.
	other := f.dbFile(a.pluginID)
	if got := f.exec(ctx, b, fmt.Sprintf("ATTACH DATABASE '%s' AS x", other), ""); got >= 0 {
		t.Fatalf("ATTACH = %d, want a refusal", got)
	}
	if n, out := f.query(ctx, b, "SELECT body FROM x.notes", "", dbDstCap); n >= 0 {
		t.Fatalf("B reached A's table through x: %q", out)
	}
}

func TestDBStatementErrorsMapAndDBErrorExplains(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.errs", true, "db:own")
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE t(a INTEGER PRIMARY KEY)", ""), 0)
	mustExec(t, f.exec(ctx, hs, "INSERT INTO t VALUES (1)", ""), 1)
	if got := f.exec(ctx, hs, "INSERT INTO t VALUES (1)", ""); got != hostErrInvalid {
		t.Fatalf("constraint = %d, want -4", got)
	}
	if msg := f.lastError(hs); !strings.Contains(strings.ToLower(msg), "constraint") {
		t.Fatalf("db_error = %q", msg)
	}
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "VACUUM", "SELECT load_extension('x')"} {
		if got := f.exec(ctx, hs, q, ""); got != hostErrInvalid {
			t.Fatalf("%q = %d, want -4", q, got)
		}
	}
	if got := f.exec(ctx, hs, "INSERT INTO t VALUES (?)", `[{"x":[1]}]`); got != hostErrInvalid {
		t.Fatalf("bad args = %d, want -4", got)
	}
}

func TestDBResultCapIs1MiB(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.cap", true, "db:own")
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE big(id INTEGER PRIMARY KEY, v TEXT)", ""), 0)
	// 60 rows x ~20 KB = ~1.2 MiB of JSON.
	got := f.exec(ctx, hs, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<60)
		INSERT INTO big(v) SELECT hex(zeroblob(10000)) FROM n`, "")
	mustExec(t, got, 60)
	if n, _ := f.query(ctx, hs, "SELECT v FROM big", "", dbDstCap); n != hostErrInvalid {
		t.Fatalf("oversized result = %d, want -4", n)
	}
	if msg := f.lastError(hs); !strings.Contains(msg, "1 MiB") {
		t.Fatalf("db_error = %q, want the cap named", msg)
	}
	// Paging with LIMIT works.
	if n, _ := f.query(ctx, hs, "SELECT v FROM big LIMIT 5", "", 256<<10); n <= 0 {
		t.Fatalf("paged read = %d", n)
	}
}

func TestDBQuotaMapsTo5(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.quota", true, "db:own")
	hs.dbev.once.Do(func() {}) // resolve below with a tiny quota
	hs.dbev.declared = true
	hs.dbev.quota = 128 << 10
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE q(v BLOB)", ""), 0)
	var last int64
	for i := 0; i < 40 && last >= 0; i++ {
		last = f.exec(ctx, hs, "INSERT INTO q VALUES (zeroblob(60000))", "")
	}
	if last != hostErrQuota {
		t.Fatalf("filling past the quota = %d, want -5", last)
	}
}

func TestDBDeadlineInterruptsAndStoreStaysUsable(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.deadline", true, "db:own")
	mustExec(t, f.exec(context.Background(), hs, "CREATE TABLE t(a)", ""), 0)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	n, _ := f.query(ctx, hs, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`, "", dbDstCap)
	if el := time.Since(start); el > 300*time.Millisecond+250*time.Millisecond {
		t.Fatalf("runaway query returned after %v, want within deadline+250ms", el)
	}
	if n != hostErrInternal {
		t.Fatalf("interrupted query = %d, want -3", n)
	}
	mustExec(t, f.exec(context.Background(), hs, "INSERT INTO t VALUES (1)", ""), 1)
}

func TestDBRetryCacheServesOverflowAndNeverReRunsAWrite(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.retry", true, "db:own")
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE w(id INTEGER PRIMARY KEY, v TEXT)", ""), 0)

	// An INSERT..RETURNING through db_query with a buffer that is too small.
	ins := "INSERT INTO w(v) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaa') RETURNING id, v"
	n, _ := f.query(ctx, hs, ins, "", 8)
	if n <= 8 {
		t.Fatalf("first call = %d, want the full length > 8", n)
	}
	n2, out := f.query(ctx, hs, ins, "", uint32(n))
	if n2 != n || !strings.Contains(out, "aaaa") {
		t.Fatalf("retry = %d %q, want the same %d bytes", n2, out, n)
	}
	_, out = f.query(ctx, hs, "SELECT count(*) AS c FROM w", "", dbDstCap)
	if out != `[{"c":1}]` {
		t.Fatalf("rows after retry = %s, the write ran twice", out)
	}
	// The cache is spent: the same statement now really runs again.
	f.query(ctx, hs, ins, "", dbDstCap)
	_, out = f.query(ctx, hs, "SELECT count(*) AS c FROM w", "", dbDstCap)
	if out != `[{"c":2}]` {
		t.Fatalf("rows = %s, want a fresh run after the retry completed", out)
	}
}

func TestDBHandleClosedOnUnloadAndReopens(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.unload", true, "db:own")
	ctx := context.Background()
	mustExec(t, f.exec(ctx, hs, "CREATE TABLE t(a)", ""), 0)
	mustExec(t, f.exec(ctx, hs, "INSERT INTO t VALUES (1)", ""), 1)

	st, err := DataManager().Open(ctx, hs.pluginID, plugindb.Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	closePluginData(hs.pluginID) // what Sync's drop loop and load's reload call
	if _, err := st.Query(ctx, "SELECT 1"); err == nil {
		t.Fatal("the store still answers after unload")
	}
	// The file can be renamed away while unloaded, and the next call reopens.
	dbPath := f.dbFile(hs.pluginID)
	if err := os.Rename(dbPath, dbPath+".moved"); err != nil {
		t.Fatalf("file still held after unload: %v", err)
	}
	if err := os.Rename(dbPath+".moved", dbPath); err != nil {
		t.Fatal(err)
	}
	if n, out := f.query(ctx, hs, "SELECT a FROM t", "", dbDstCap); n <= 0 || out != `[{"a":1}]` {
		t.Fatalf("after reopen: %d %q", n, out)
	}

	// WasmRuntime.Close closes every open database too.
	st, _ = DataManager().Open(ctx, hs.pluginID, plugindb.Options{NoMigrate: true})
	NewWasmRuntime(t.TempDir()).Close(ctx)
	if _, err := st.Query(ctx, "SELECT 1"); err == nil {
		t.Fatal("WasmRuntime.Close left a database open")
	}
}

func TestDBUnavailableInsideFileImportSandbox(t *testing.T) {
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.sandbox", true, "db:own")
	hs.fileImport = true
	ctx := context.Background()
	if got := f.exec(ctx, hs, "CREATE TABLE t(a)", ""); got != hostErrDenied {
		t.Fatalf("db_exec in the import sandbox = %d, want -2", got)
	}
	if n, _ := f.query(ctx, hs, "SELECT 1", "", dbDstCap); n != hostErrDenied {
		t.Fatalf("db_query in the import sandbox = %d, want -2", n)
	}
	if h := hostDBOpenImport(withHostState(ctx, hs), 0); h != hostErrDenied {
		t.Fatalf("db_open_import in the import sandbox = %d, want -2", h)
	}
}

// stageImportDB writes a real SQLite file and registers it as pluginID's
// staged import handle.
func stageImportDB(t *testing.T, pluginID string, rows int) int32 {
	t.Helper()
	srcRoot := t.TempDir()
	m := plugindb.NewManager(srcRoot, nil)
	st, err := m.Open(context.Background(), "src", plugindb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(context.Background(), "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := st.Exec(context.Background(), "INSERT INTO items(name) VALUES (?)", fmt.Sprintf("item-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "staged.db")
	if err := plugindb.SnapshotFile(context.Background(), filepath.Join(srcRoot, "src", plugindb.DBFileName), dest); err != nil {
		t.Fatal(err)
	}
	m.CloseAll()
	h, err := OpenImportFile(pluginID, dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseImportFile(pluginID, h) })
	return h
}

func (f *dbFixture) queryH(ctx context.Context, hs *hostState, dbh int32, q string) (int32, string) {
	f.t.Helper()
	ql := f.put(dbSQLOff, q)
	n := hostDBQueryH(withHostState(ctx, hs), f.mem, dbh, dbSQLOff, ql, dbArgsOff, 0, dbDstOff, dbDstCap)
	return n, f.out(n, dbDstCap)
}

func TestDBOpenImportIsConfinedToStagedHandlesAndReadOnly(t *testing.T) {
	f := newDBFixture(t)
	a := f.plugin("com.test.db.imp.a", true, "db:own")
	b := f.plugin("com.test.db.imp.b", true, "db:own")
	ctx := context.Background()
	handle := stageImportDB(t, a.pluginID, 3)

	h := hostDBOpenImport(withHostState(ctx, a), handle)
	if h < 0 {
		t.Fatalf("db_open_import = %d", h)
	}
	n, out := f.queryH(ctx, a, h, "SELECT name FROM items ORDER BY id")
	if n <= 0 || out != `[{"name":"item-0"},{"name":"item-1"},{"name":"item-2"}]` {
		t.Fatalf("db_query_h = %d %q", n, out)
	}
	// Read-only: writes and PRAGMA query_only=0 both fail, rows unchanged.
	for _, q := range []string{"INSERT INTO items(name) VALUES ('x')", "DELETE FROM items", "PRAGMA query_only=0", "ATTACH DATABASE ':memory:' AS m"} {
		if n, _ := f.queryH(ctx, a, h, q); n >= 0 {
			t.Fatalf("%q on an import handle = %d, want a failure", q, n)
		}
	}
	if _, out := f.queryH(ctx, a, h, "SELECT count(*) AS c FROM items"); out != `[{"c":3}]` {
		t.Fatalf("import file changed: %s", out)
	}

	// Plugin B cannot open A's staged handle, nor use A's database handle.
	if got := hostDBOpenImport(withHostState(ctx, b), handle); got != hostErrNotFound {
		t.Fatalf("other plugin db_open_import(A's handle) = %d, want -1", got)
	}
	if got := hostDBOpenImport(withHostState(ctx, a), 99); got != hostErrNotFound {
		t.Fatalf("forged import handle = %d, want -1", got)
	}
	if n, _ := f.queryH(ctx, b, h, "SELECT 1"); n != hostErrNotFound {
		t.Fatalf("other plugin db_query_h(A's dbh) = %d, want -1", n)
	}
	if n, _ := f.queryH(ctx, a, -1, "SELECT 1"); n != hostErrNotFound {
		t.Fatalf("negative dbh = %d, want -1", n)
	}

	// Handles close with the event.
	a.dbev.imports.closeAll()
	if n, _ := f.queryH(ctx, a, h, "SELECT 1"); n != hostErrNotFound {
		t.Fatalf("db_query_h after the event = %d, want -1", n)
	}
}

func TestDBOpenImportRejectsNonSQLiteAndCapsHandles(t *testing.T) {
	f := newDBFixture(t)
	a := f.plugin("com.test.db.imp.cap", true, "db:own")
	ctx := context.Background()

	junk := filepath.Join(t.TempDir(), "junk.bin")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not sqlite ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	jh, err := OpenImportFile(a.pluginID, junk)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseImportFile(a.pluginID, jh)
	if got := hostDBOpenImport(withHostState(ctx, a), jh); got >= 0 {
		t.Fatalf("junk file opened as a database: %d", got)
	}

	good := stageImportDB(t, a.pluginID, 1)
	var last int32
	for i := 0; i < maxDBImportHandles+1; i++ {
		last = hostDBOpenImport(withHostState(ctx, a), good)
	}
	if last != hostErrBusy {
		t.Fatalf("handle past the cap = %d, want -6", last)
	}
	a.dbev.imports.closeAll()
}
