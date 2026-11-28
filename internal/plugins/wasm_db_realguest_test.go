package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
)

// Real wasip1 guest (testdata/db_guest) calling db_exec / db_query /
// db_error across the wasm boundary, then a real backup (SnapshotWithAssets)
// and a restore into an emptied plugin-data directory, then the same guest
// reading the evidence rows back (ut-docs#3157, tester).
func TestDBRealWasmGuestRoundTripAndBackupRestore_3157(t *testing.T) {
	guest := filepath.Join(t.TempDir(), "db_guest.wasm")
	cmd := exec.Command("go", "build", "-o", guest, "./testdata/db_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}

	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() {
		closeAllPluginData()
		paths.Init(prev)
	})
	dbPath := filepath.Join(root, "till.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	const id = "com.test.dbguest"
	dir := filepath.Join(root, "plugins", id, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","db":{"migrations":"migrations"}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	seedPlugin(t, database.DB, id)
	grantPerm(t, database.DB, id, "storage")
	grantPerm(t, database.DB, id, "db:own")

	run := func(mode string) map[string]any {
		t.Helper()
		w := NewWasmRuntime(t.TempDir())
		defer w.Close(context.Background())
		if err := w.load(id, "1.0.0", guest); err != nil {
			t.Fatalf("load: %v", err)
		}
		return runGuestPayload(t, w, database.DB, id, map[string]any{"mode": mode, "sql": dbGuestStatements})
	}

	// 1. write + read through the real module.
	res := run("write")
	for k, want := range map[string]float64{"create": 0, "insert1": 1, "insert2": 1, "insert3": 1, "update": 2} {
		if res[k] != want {
			t.Errorf("%s = %v, want %v (all: %v)", k, res[k], want, res)
		}
	}
	wantRows := `[{"amount":1251,"id":1,"note":"till roll sold"},{"amount":-300,"id":2,"note":"refund, damaged"},{"amount":8,"id":3,"note":"with blob"}]`
	var gotRows, want []map[string]any
	_ = json.Unmarshal([]byte(res["query_out"].(string)), &gotRows)
	_ = json.Unmarshal([]byte(wantRows), &want)
	if len(gotRows) != 3 || gotRows[0]["note"] != "till roll sold" || gotRows[0]["amount"] != float64(1251) || gotRows[2]["amount"] != float64(8) {
		t.Fatalf("query rows = %s", res["query_out"])
	}

	// 2. the escapes are refused with negative codes and no file appears.
	res = run("attach")
	for _, k := range []string{"attach", "vacuum", "pragma", "multi"} {
		if c, _ := res[k].(float64); c >= 0 {
			t.Errorf("%s code = %v, want negative", k, res[k])
		}
	}
	if !strings.Contains(strings.ToLower(res["attach_err"].(string)), "refused") {
		t.Errorf("db_error after ATTACH = %q", res["attach_err"])
	}
	for _, p := range []string{"/tmp/escape.db", "/tmp/escape2.db"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created by the guest", p)
			_ = os.Remove(p)
		}
	}
	res = run("bad")
	if c, _ := res["syntax"].(float64); c >= 0 || res["syntax_err"] == "" {
		t.Errorf("syntax error: code %v err %q", res["syntax"], res["syntax_err"])
	}

	// 2b. revoking db:own takes effect on the next call, through the real module.
	if err := data.NewPluginRepo(database.DB).SetPermission(context.Background(), id, "db:own", false); err != nil {
		t.Fatal(err)
	}
	res = run("write")
	if res["create"] != float64(-2) || res["insert1"] != float64(-2) {
		t.Errorf("without db:own: create=%v insert1=%v, want -2", res["create"], res["insert1"])
	}
	if err := data.NewPluginRepo(database.DB).SetPermission(context.Background(), id, "db:own", true); err != nil {
		t.Fatal(err)
	}

	// 3. real backup, then wipe plugin-data (disaster), then restore.
	closeAllPluginData()
	snap, err := db.SnapshotWithAssets(database.DB, dbPath, filepath.Join(root, "public", "assets"))
	if err != nil {
		t.Fatalf("SnapshotWithAssets: %v", err)
	}
	pd := filepath.Join(root, "plugin-data")
	if _, err := os.Stat(filepath.Join(pd, id, "plugin.db")); err != nil {
		t.Fatalf("plugin.db missing before wipe: %v", err)
	}
	if err := os.RemoveAll(pd); err != nil {
		t.Fatal(err)
	}
	if res := run("read"); res["query_out"] != "[]" && res["query_n"] != float64(-3) {
		// An empty store has no table: the query errors. It must NOT still have rows.
		if strings.Contains(res["query_out"].(string), "till roll sold") {
			t.Fatal("rows still present after the wipe: the test would prove nothing")
		}
	}
	closeAllPluginData()
	if err := os.RemoveAll(pd); err != nil { // the probe above recreated an empty db
		t.Fatal(err)
	}
	n, err := db.RestoreBackupPluginData(snap, pd)
	if err != nil || n < 1 {
		t.Fatalf("RestoreBackupPluginData n=%d err=%v", n, err)
	}

	// 4. a fresh runtime reads the evidence rows back through the guest.
	res = run("read")
	var back []map[string]any
	if err := json.Unmarshal([]byte(res["query_out"].(string)), &back); err != nil || len(back) != 3 {
		t.Fatalf("after restore: %v (%v)", res, err)
	}
	if back[0]["note"] != "till roll sold" || back[0]["amount"] != float64(1251) || back[1]["note"] != "refund, damaged" || back[1]["amount"] != float64(-300) {
		t.Fatalf("restored rows differ: %s", res["query_out"])
	}
	if b, _ := back[2]["blob"].(map[string]any); b["b64"] != "AAEC/w==" {
		t.Fatalf("restored blob = %v, want b64 AAEC/w==", back[2]["blob"])
	}
}

// dbGuestStatements are the statements the db_guest module runs, by name:
// [statement, args JSON]. They live here because guard-data-access.sh allows
// query text in test files only, and the guest is a non-test Go file.
var dbGuestStatements = map[string][2]string{
	"create":     {`CREATE TABLE IF NOT EXISTS evidence(id INTEGER PRIMARY KEY, note TEXT NOT NULL, amount INTEGER, blob BLOB)`, ""},
	"insert1":    {`INSERT INTO evidence(note, amount) VALUES(?, ?)`, `["till roll sold", 1250]`},
	"insert2":    {`INSERT INTO evidence(note, amount) VALUES(?, ?)`, `["refund, damaged", -300]`},
	"insert3":    {`INSERT INTO evidence(note, amount, blob) VALUES(?, ?, ?)`, `["with blob", 7, {"b64":"AAEC/w=="}]`},
	"update":     {`UPDATE evidence SET amount = amount + 1 WHERE amount > ?`, `[0]`},
	"read_plain": {`SELECT id, note, amount FROM evidence ORDER BY id`, ""},
	"read_blob":  {`SELECT id, note, amount, blob FROM evidence ORDER BY id`, ""},
	"multi":      {`SELECT 1; SELECT 2`, ""},
	"syntax":     {`INSERT INTO nope(`, ""},
}
