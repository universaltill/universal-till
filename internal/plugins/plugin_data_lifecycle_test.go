package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

type lifecycleClock struct{ t time.Time }

func (c *lifecycleClock) now() time.Time { return c.t }

// lifecycleFixture installs a plugin with a data file under a temp data
// manager and a manifest carrying retention under its versions/ snapshot.
func lifecycleFixture(t *testing.T, id string, keepYears int, reason string) (base string, clk *lifecycleClock, root string) {
	t.Helper()
	base = t.TempDir()
	root = filepath.Join(t.TempDir(), "plugin-data")
	clk = &lifecycleClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	mgr := plugindb.NewManager(root, clk.now)
	restore := SetDataManagerForTest(mgr)
	t.Cleanup(func() { restore(); mgr.CloseAll() })

	st, err := mgr.Open(context.Background(), id, plugindb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(context.Background(), `CREATE TABLE logbook(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(context.Background(), `INSERT INTO logbook VALUES ('entry')`); err != nil {
		t.Fatal(err)
	}

	vdir := filepath.Join(base, id, "versions", "1.0.0")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := `{"id":"` + id + `","name":"X","version":"1.0.0"`
	if keepYears > 0 {
		mf += `,"retention":{"keep_years":` + strconv.Itoa(keepYears) + `,"reason_key":"` + reason + `"}`
	}
	mf += `}`
	if err := os.WriteFile(filepath.Join(vdir, "manifest.json"), []byte(mf), 0o644); err != nil {
		t.Fatal(err)
	}
	return base, clk, root
}

func removedEntries(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, ".removed"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestUninstallPluginTree_KeepsDataReadOnlyWhileRetained(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	id := "com.test.fiscal"
	base, clk, root := lifecycleFixture(t, id, 6, "plugins.retention.fiscal")
	seedInstalledPlugin(t, db, id, "FISCAL", "1.0.0", "none", true)

	if err := UninstallPluginTree(ctx, db, base, id); err != nil {
		t.Fatal(err)
	}
	// Code tree gone, data moved (never deleted).
	if _, err := os.Stat(filepath.Join(base, id)); !os.IsNotExist(err) {
		t.Fatalf("code tree should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
		t.Fatalf("live data dir should be moved away: %v", err)
	}
	rm := removedEntries(t, root)
	if len(rm) != 1 {
		t.Fatalf(".removed = %v", rm)
	}
	if _, err := os.Stat(filepath.Join(root, ".removed", rm[0], "plugin.db")); err != nil {
		t.Fatalf("plugin.db must be kept: %v", err)
	}

	info, err := DataManager().RemovedInfo(id)
	if err != nil || !info.Retained || info.ReasonKey != "plugins.retention.fiscal" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	// Delete-now refused while retained, even long after the soft-delete window.
	clk.t = clk.t.AddDate(2, 0, 0)
	if err := DeletePluginDataNow(ctx, id); !errors.Is(err, plugindb.ErrRetained) {
		t.Fatalf("DeletePluginDataNow = %v, want ErrRetained", err)
	}
	if purged, err := PrunePluginData(); err != nil || len(purged) != 0 {
		t.Fatalf("prune inside retention purged %v err %v", purged, err)
	}
	if len(removedEntries(t, root)) != 1 {
		t.Fatal("retained data must survive the sweep")
	}
	// After the period: allowed.
	clk.t = clk.t.AddDate(5, 0, 0)
	if err := DeletePluginDataNow(ctx, id); err != nil {
		t.Fatalf("after retention: %v", err)
	}
	if len(removedEntries(t, root)) != 0 {
		t.Fatal("data should be gone")
	}
}

func TestUninstallPluginTree_NoRetentionKeepsDataForSoftDeleteWindow(t *testing.T) {
	db := managerTestDB(t)
	id := "com.test.plain"
	base, clk, root := lifecycleFixture(t, id, 0, "")
	seedInstalledPlugin(t, db, id, "PLAIN", "1.0.0", "none", true)
	if err := UninstallPluginTree(context.Background(), db, base, id); err != nil {
		t.Fatal(err)
	}
	if len(removedEntries(t, root)) != 1 {
		t.Fatal("data must be moved, not deleted")
	}
	if purged, _ := PrunePluginData(); len(purged) != 0 {
		t.Fatalf("pruned inside the soft-delete window: %v", purged)
	}
	clk.t = clk.t.AddDate(0, 0, plugindb.SoftDeleteDays+1)
	if purged, err := PrunePluginData(); err != nil || len(purged) != 1 {
		t.Fatalf("expired data not purged: %v %v", purged, err)
	}
}

func TestPrunePluginData_DeletesOnlyExpired(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base, clk, root := lifecycleFixture(t, "com.test.old", 0, "")
	seedInstalledPlugin(t, db, "com.test.old", "OLD", "1.0.0", "none", true)
	if err := UninstallPluginTree(ctx, db, base, "com.test.old"); err != nil {
		t.Fatal(err)
	}
	// A second plugin, removed 20 days later with a 3-year hold, and a third without.
	clk.t = clk.t.AddDate(0, 0, 20)
	mgr := DataManager()
	for _, c := range []struct {
		id    string
		years int
	}{{"com.test.held", 3}, {"com.test.recent", 0}} {
		st, err := mgr.Open(ctx, c.id, plugindb.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Exec(ctx, `CREATE TABLE t(v)`); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.UninstallRetained(c.id, c.years, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Day 35: only "old" (removed day 0) is past 30 days.
	clk.t = clk.t.AddDate(0, 0, 15)
	purged, err := PrunePluginData()
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || !strings.HasPrefix(purged[0], "com.test.old-") {
		t.Fatalf("purged %v, want only com.test.old", purged)
	}
	if got := len(removedEntries(t, root)); got != 2 {
		t.Fatalf("remaining %d, want 2", got)
	}
	// Day 55: recent is expired, held (3y) is not.
	clk.t = clk.t.AddDate(0, 0, 20)
	purged, _ = PrunePluginData()
	if len(purged) != 1 || !strings.HasPrefix(purged[0], "com.test.recent-") {
		t.Fatalf("purged %v, want only com.test.recent", purged)
	}
	if got := removedEntries(t, root); len(got) != 1 || !strings.HasPrefix(got[0], "com.test.held-") {
		t.Fatalf("remaining %v", got)
	}
}

func TestDataLifecycle_IDTraversalRefused(t *testing.T) {
	db := managerTestDB(t)
	base, _, root := lifecycleFixture(t, "com.test.keep", 0, "")
	victim := filepath.Join(root, "com.test.keep", "plugin.db")
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "com.test.keep/../..", "CON"} {
		if err := DeletePluginDataNow(context.Background(), bad); err == nil {
			t.Errorf("DeletePluginDataNow(%q) accepted", bad)
		}
		if err := UninstallPluginTree(context.Background(), db, base, bad); err == nil {
			t.Errorf("UninstallPluginTree(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("other plugin's data touched: %v", err)
	}
}

func TestRetentionFromTree_LongestWins(t *testing.T) {
	base := t.TempDir()
	id := "com.test.r"
	for v, y := range map[string]string{"1.0.0": `{"retention":{"keep_years":2,"reason_key":"a"}}`, "2.0.0": `{"retention":{"keep_years":6,"reason_key":"b"}}`, "3.0.0": `{}`, "4.0.0": `not json`} {
		d := filepath.Join(base, id, "versions", v)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "manifest.json"), []byte(y), 0o644)
	}
	if y, r := retentionFromTree(base, id); y != 6 || r != "b" {
		t.Fatalf("got %d %q", y, r)
	}
}
