package housekeeping

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/stagedupload"
)

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// layout points paths and issuereport at a fresh data dir, and the
// staged-upload sweep at its tmp/ subdirectory (never the real temp dir),
// and returns it plus the DB path inside it.
func layout(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	origData := paths.DataDir()
	origPending := issuereport.PendingDir
	origStaged := stagedupload.Dir
	paths.Init(root)
	issuereport.PendingDir = filepath.Join(root, "issue-reports", "pending")
	stagedupload.Dir = func() string { return filepath.Join(root, "tmp") }
	t.Cleanup(func() {
		paths.Init(origData)
		issuereport.PendingDir = origPending
		stagedupload.Dir = origStaged
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
		// The system temp dir (tmp/ here) holds other programs' files.
		"tmp/someone-else.upload",
		"tmp/ut-bkp-1.db",
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
	put(t, root, "tmp/ut-import-stage-1.upload", ancient)
	mustGo = append(mustGo, "tmp/ut-import-stage-1.upload")
	// A staged upload younger than its limit may still be in use.
	put(t, root, "tmp/ut-view-upload-2.upload", testNow.Add(-time.Hour))
	mustKeep = append(mustKeep, "tmp/ut-view-upload-2.upload")

	for _, rel := range mustKeep {
		if !strings.HasPrefix(rel, "backups/unitill-pos-") && !strings.HasPrefix(rel, "tmp/ut-view-upload-") {
			put(t, root, rel, ancient)
		}
	}

	results := Run(dbPath, testNow, 0)
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
	want := map[string]int{KindBackups: 2, KindPreRestore: 5, KindIssueReports: 1, KindUpdateDownloads: 1, KindStagedUploads: 1}
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
	Run(dbPath, testNow, 0)
	for _, r := range Run(dbPath, testNow, 0) {
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
	for _, res := range Run(dbPath, testNow, 0) {
		if _, ok := rows[res.Kind]; !ok {
			t.Errorf("sweep %q has no retention-table row", res.Kind)
		}
	}
}

// syncHistoryTables are the till's sync-history tables that don't carry
// the sync_ prefix (ut-docs#3123's inventory); every sync_* table counts
// too, found from the migrations so a new one can't slip past.
var syncHistoryTables = []string{"held_sales_tombstones", "sales_aggregate_uploads"}

// legalRecords returns the tables that hold records a shop must keep by
// law (ADR-0040): sales and payments, invoices, shifts (Z reports are built
// from them), fiscal/TSE data, the audit log, Z reports — every table with an
// *_archive copy, every archive itself, and the named ones below.
func legalRecords(t *testing.T) func(string) bool {
	t.Helper()
	archived := map[string]bool{}
	for name := range createdTables(t) {
		if base, ok := strings.CutSuffix(name, "_archive"); ok && base != "held_sales" {
			archived[base] = true
		}
	}
	for _, name := range []string{"sales", "shifts", "payments", "invoices", "audit_log", "report_archive", "reset_batches", "voucher_transactions", "price_history"} {
		archived[name] = true
	}
	return func(name string) bool {
		return archived[name] || strings.HasPrefix(name, "sale_") || strings.HasPrefix(name, "fiscal_") || strings.HasSuffix(name, "_archive")
	}
}

// createdTables is every table a shipped migration creates.
func createdTables(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "db", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found (%v)", err)
	}
	create := regexp.MustCompile("(?i)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?[\"`\\[]?([a-z0-9_]+)")
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range create.FindAllStringSubmatch(string(b), -1) {
			out[strings.ToLower(m[1])] = true
		}
	}
	if !out["sync_journal_quarantine"] || !out["sales_archive"] {
		t.Fatalf("migration scan found %d tables — the CREATE TABLE pattern no longer matches", len(out))
	}
	return out
}

const dbWhere = "database: "

// ut-docs#3123: every sync-history table has a row in the retention table
// saying how it stays bounded (or why it is kept), so one that grows without
// limit is a reviewed decision, not an accident. A new sync_* migration
// fails here until its row is written.
func TestRetentionTableCoversEverySyncHistoryTable(t *testing.T) {
	want := map[string]bool{}
	for name := range createdTables(t) {
		if strings.HasPrefix(name, "sync_") {
			want[name] = true
		}
	}
	for _, name := range syncHistoryTables {
		want[name] = true
	}
	have := map[string]bool{}
	for _, r := range Retention() {
		if name, ok := strings.CutPrefix(r.Where, dbWhere); ok {
			have[name] = true
		}
	}
	for name := range want {
		if !have[name] {
			t.Errorf("sync-history table %s has no retention-table row (Where %q)", name, dbWhere+name)
		}
	}
}

// The database rows in the retention table are a reviewed allow-list:
// none of them may name a table that holds records a shop must keep by law.
func TestRetentionTableNeverListsLegalRecords(t *testing.T) {
	legal := legalRecords(t)
	for _, r := range Retention() {
		if name, ok := strings.CutPrefix(r.Where, dbWhere); ok && legal(name) {
			t.Errorf("retention row %q names %s, which ADR-0040 keeps", r.Kind, name)
		}
	}
	for _, name := range []string{"sales", "sale_lines", "shifts", "no_sale_events", "stock_movements", "payments_archive", "fiscal_tse_signatures", "audit_log", "report_archive"} {
		if !legal(name) {
			t.Errorf("legal-record check misses %q", name)
		}
	}
	for _, name := range []string{"sync_asset_ledger", "held_sales_tombstones", "sales_aggregate_uploads", "held_sales"} {
		if legal(name) {
			t.Errorf("legal-record check wrongly includes %q", name)
		}
	}
}

// The table side of ut-docs#3092 AC 2: housekeeping deletes files only — it
// may not reach the database at all, so sales, receipts, fiscal data, the
// audit log and Z reports can't be touched from here. The sync-history
// tables are bounded by the code that owns them (ut-docs#3123), listed in
// the retention table above.
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
