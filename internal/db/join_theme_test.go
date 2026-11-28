package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

func seedMainTillLook(t *testing.T, d *DB) {
	t.Helper()
	for k, v := range map[string]string{"theme": "amber", shoptemplate.ThemeSeededKey: "amber", "store.locale": "en-GB"} {
		if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
}

func settingRow(t *testing.T, q interface {
	QueryRow(string, ...any) *sql.Row
}, key string) (string, bool) {
	t.Helper()
	var v string
	err := q.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return v, true
}

// The theme is per till and never synced (ut-docs#2783): a joined till must
// not keep the main till's theme or its seeded marker from the snapshot, so
// its own template seed sees an unset theme (ut-docs#6347).
func TestApplyReplicaIdentityDropsMainTillTheme(t *testing.T) {
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	path := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedMainTillLook(t, d)
	if err := StageReplicaIdentity(path, ReplicaIdentity{PrimaryURL: "http://primary.local", TillID: "till-2", Bearer: "b", ReceiptPrefix: "T2-"}); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyReplicaIdentity(d.DB, path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	for _, k := range []string{"theme", shoptemplate.ThemeSeededKey} {
		if v, ok := settingRow(t, d, k); ok {
			t.Errorf("%s = %q after join, want no row", k, v)
		}
	}
	if v, _ := settingRow(t, d, "store.locale"); v != "en-GB" {
		t.Errorf("shop-wide store.locale = %q, want kept", v)
	}
}

// Source side: the copy served to a joining till never carries them either.
func TestRedactedJoinSnapshotDropsTheme(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	seedMainTillLook(t, d)
	copyPath, cleanup, err := RedactedJoinSnapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cd, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cd.Close()
	for _, k := range []string{"theme", shoptemplate.ThemeSeededKey} {
		if v, ok := settingRow(t, cd, k); ok {
			t.Errorf("snapshot copy %s = %q, want no row", k, v)
		}
	}
	// The main till's own DB keeps its look.
	if v, _ := settingRow(t, d, "theme"); v != "amber" {
		t.Errorf("main theme = %q, want amber", v)
	}
}

func TestPerTillLookKeysMatchShopTemplate(t *testing.T) {
	if PerTillLookKeys[0] != "theme" || PerTillLookKeys[1] != shoptemplate.ThemeSeededKey {
		t.Fatalf("PerTillLookKeys = %v, out of step with the theme key / shoptemplate.ThemeSeededKey", PerTillLookKeys)
	}
}
