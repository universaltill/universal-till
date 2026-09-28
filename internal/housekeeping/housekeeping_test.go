package housekeeping

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/paths"
)

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// layout points paths and issuereport at a fresh data dir and returns it
// plus the DB path inside it.
func layout(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	origData := paths.DataDir()
	origPending := issuereport.PendingDir
	paths.Init(root)
	issuereport.PendingDir = filepath.Join(root, "issue-reports", "pending")
	t.Cleanup(func() {
		paths.Init(origData)
		issuereport.PendingDir = origPending
	})
	return root, filepath.Join(root, "unitill-pos.db")
}

func put(t *testing.T, root, rel string, mod time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func age(t *testing.T, root, rel string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), mod, mod); err != nil {
		t.Fatal(err)
	}
}

func files(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return out
}

// The guard for AC 2 of ut-docs#3092 on the file side: with every file on
// the device ancient, one run removes exactly the housekeeping kinds past
// their limits — never the live database, its sidecars, a kept snapshot,
// secrets, exports, logs or anything else it doesn't own.
func TestRun_RemovesOnlyWhatTheRetentionTableAllows(t *testing.T) {
	root, dbPath := layout(t)
	ancient := testNow.Add(-1000 * 24 * time.Hour)

	mustKeep := []string{
		"unitill-pos.db", "unitill-pos.db-wal", "unitill-pos.db-shm",
		"restore-pending.db",
		"secrets/device.key",
		"exports/dsfinvk-2020.zip",
		"logs/till.log", "logs/till.log.4",
		"backups/join-snapshot-abc.db",
		"backups/notes.txt",
		"diagnostics/pending/s1/000000000001.json",
		"updates/updater.log",
		"public/assets/items/a.png",
		"plugins/x/plugin.wasm",
		"issue-reports/pending/loose-file.txt",
	}
	var mustGo []string
	// 16 snapshots: the newest DefaultBackupKeep stay, two go.
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("backups/unitill-pos-2020010%d-%02d0000.db", 1, i)
		put(t, root, name, ancient.Add(time.Duration(i)*time.Hour))
		if i < 2 {
			mustGo = append(mustGo, name)
		} else {
			mustKeep = append(mustKeep, name)
		}
	}
	// Pre-restore copies are all past 30 days, so every one goes.
	for i := 0; i < 5; i++ {
		name := "backups/pre-restore-2020010" + strconv.Itoa(i+1) + "-000000.db"
		put(t, root, name, ancient.Add(time.Duration(i)*time.Hour))
		mustGo = append(mustGo, name)
	}
	put(t, root, "issue-reports/pending/old/meta.json", ancient)
	put(t, root, "issue-reports/pending/old/video.webm", ancient)
	age(t, root, "issue-reports/pending/old", ancient)
	mustGo = append(mustGo, "issue-reports/pending/old/meta.json", "issue-reports/pending/old/video.webm")
	put(t, root, "updates/attempt-1/unitill-pos-setup-1.0.0.exe", ancient)
	age(t, root, "updates/attempt-1", ancient)
	mustGo = append(mustGo, "updates/attempt-1/unitill-pos-setup-1.0.0.exe")

	for _, rel := range mustKeep {
		if !strings.HasPrefix(rel, "backups/unitill-pos-") {
			put(t, root, rel, ancient)
		}
	}

	results := Run(dbPath, testNow)
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Kind, r.Err)
		}
	}
	after := files(t, root)
	for _, rel := range mustKeep {
		if !after[rel] {
			t.Errorf("housekeeping removed %s — not in its retention table", rel)
		}
	}
	for _, rel := range mustGo {
		if after[rel] {
			t.Errorf("housekeeping kept %s — past its retention limit", rel)
		}
	}
	if len(after) != len(mustKeep) {
		var got []string
		for k := range after {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Errorf("left %d files, want %d: %v", len(after), len(mustKeep), got)
	}

	removed := map[string]int{}
	for _, r := range results {
		removed[r.Kind] = r.Removed
	}
	want := map[string]int{KindBackups: 2, KindPreRestore: 5, KindIssueReports: 1, KindUpdateDownloads: 1}
	for k, n := range want {
		if removed[k] != n {
			t.Errorf("%s removed %d, want %d", k, removed[k], n)
		}
	}
}

// A second run the same day finds nothing left to do.
func TestRun_Idempotent(t *testing.T) {
	root, dbPath := layout(t)
	put(t, root, "backups/pre-restore-20200101-000000.db", testNow.Add(-60*24*time.Hour))
	Run(dbPath, testNow)
	for _, r := range Run(dbPath, testNow) {
		if r.Removed != 0 || r.Err != nil {
			t.Errorf("second run: %s removed %d, err %v", r.Kind, r.Removed, r.Err)
		}
	}
}

// Every kind Run sweeps has a row in the written retention table, and every
// row names what enforces it.
func TestRetentionTableCoversEverySweep(t *testing.T) {
	_, dbPath := layout(t)
	rows := map[string]Rule{}
	for _, r := range Retention() {
		if r.Kind == "" || r.Where == "" || r.Policy == "" || r.EnforcedBy == "" {
			t.Errorf("incomplete retention row: %+v", r)
		}
		rows[r.Kind] = r
	}
	for _, res := range Run(dbPath, testNow) {
		if _, ok := rows[res.Kind]; !ok {
			t.Errorf("sweep %q has no retention-table row", res.Kind)
		}
	}
}

// AC 2's table side: until a reviewed table allow-list exists (the
// sync-history follow-up), housekeeping deletes files only — it may not
// reach the database at all, so sales, receipts, fiscal data, the audit log
// and Z reports can't be touched from here.
func TestPackageNeverTouchesTheDatabase(t *testing.T) {
	const module = "github.com/universaltill/universal-till/"
	forbidden := []string{"database/sql", module + "internal/data", "modernc.org/sqlite", "github.com/mattn/go-sqlite3"}
	// internal/db also opens the database; only its backup-file functions
	// are allowed here, whatever name it is imported under.
	allowedDB := map[string]bool{"DefaultBackupKeep": true, "ListBackups": true, "PruneBackups": true, "PrunePreRestore": true}
	matches, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		dbName := ""
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %s — housekeeping must not touch the database", f, path)
				}
			}
			if path == module+"internal/db" {
				dbName = "db"
				if imp.Name != nil {
					dbName = imp.Name.Name
				}
				if dbName == "." || dbName == "_" {
					t.Errorf("%s imports internal/db as %q — use a named import", f, dbName)
				}
			}
		}
		if dbName == "" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == dbName && !allowedDB[sel.Sel.Name] {
					t.Errorf("%s uses %s.%s — housekeeping may only call internal/db's backup-file functions", f, dbName, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

func TestSchedule(t *testing.T) {
	var s Schedule
	if !s.Due(testNow) {
		t.Fatal("first check after boot must be due")
	}
	s.Ran(testNow)
	if s.Due(testNow.Add(23 * time.Hour)) {
		t.Error("due again after 23h")
	}
	if !s.Due(testNow.Add(Interval)) {
		t.Error("not due after a full interval")
	}
}
