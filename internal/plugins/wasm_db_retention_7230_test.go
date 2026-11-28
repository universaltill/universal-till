package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ut-docs#7230: the store a guest's db_exec/db_query opens carries the
// manifest's retention.keep_years into plugindb.Options, so a retention
// (fiscal) plugin's database runs synchronous=FULL (2) and every other
// plugin stays NORMAL (1). Read back through the real db_query host
// function with the read-only pragma table function (PRAGMA itself is
// refused for guests).
func TestDBOpenPathAppliesRetentionDurability_7230(t *testing.T) {
	f := newDBFixture(t)
	install := func(id, retention string) *hostState {
		t.Helper()
		dir := filepath.Join(f.root, "plugins", id, "1.0.0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","db":{"migrations":"migrations"}%s}`, id, retention)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		seedPlugin(t, f.db, id)
		grantPerm(t, f.db, id, "db:own")
		return &hostState{pluginID: id, db: f.db}
	}
	ctx := context.Background()
	for _, tc := range []struct {
		id, retention, want string
	}{
		{"com.test.db.fiscal", `,"retention":{"keep_years":10}`, `[{"s":2}]`},
		{"com.test.db.plain", ``, `[{"s":1}]`},
	} {
		hs := install(tc.id, tc.retention)
		mustExec(t, f.exec(ctx, hs, "CREATE TABLE t(id INTEGER PRIMARY KEY)", ""), 0)
		n, out := f.query(ctx, hs, "SELECT synchronous AS s FROM pragma_synchronous", "", dbDstCap)
		if n <= 0 || out != tc.want {
			t.Errorf("%s: synchronous = %d %q (%s), want %s", tc.id, n, out, f.lastError(hs), tc.want)
		}
	}
}
