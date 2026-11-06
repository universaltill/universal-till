package storageusage

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel string, n int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func layoutIn(root string) Layout {
	return Layout{
		DBPath:        filepath.Join(root, "pos.db"),
		DataDir:       root,
		BackupsDir:    filepath.Join(root, "backups"),
		LogsDir:       filepath.Join(root, "logs"),
		BugReportsDir: filepath.Join(root, "issue-reports", "pending"),
		UpdatesDir:    filepath.Join(root, "updates"),
	}
}

func bytesOf(t *testing.T, r Report) map[string]int64 {
	t.Helper()
	want := []string{CatDatabase, CatBackups, CatLogs, CatBugReports, CatUpdates, CatOther}
	if len(r.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(r.Rows), len(want))
	}
	m := map[string]int64{}
	for i, row := range r.Rows {
		if row.Category != want[i] {
			t.Fatalf("row %d = %q, want %q", i, row.Category, want[i])
		}
		m[row.Category] = row.Bytes
	}
	return m
}

func TestMeasure_SortsEveryFileIntoItsRow(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pos.db", 1000)
	write(t, root, "pos.db-wal", 200)
	write(t, root, "pos.db-shm", 30)
	write(t, root, "backups/unitill-pos-A.db", 500)
	write(t, root, "backups/unitill-pos-B.db", 500)
	write(t, root, "backups/pre-restore-X.db", 700)
	write(t, root, "logs/till.log", 300)
	write(t, root, "issue-reports/pending/r1/x", 40)
	write(t, root, "updates/attempt-1/setup.exe", 900)
	write(t, root, "photos/a.jpg", 4096)

	rep, err := Measure(context.Background(), layoutIn(root))
	if err != nil {
		t.Fatal(err)
	}
	got := bytesOf(t, rep)
	want := map[string]int64{CatDatabase: 1230, CatBackups: 1700, CatLogs: 300, CatBugReports: 40, CatUpdates: 900, CatOther: 4096}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
	if rep.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", rep.Skipped)
	}
}

func TestMeasure_MissingDirsCountZero(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pos.db", 10)
	rep, err := Measure(context.Background(), layoutIn(root))
	if err != nil {
		t.Fatal(err)
	}
	got := bytesOf(t, rep)
	for _, k := range []string{CatBackups, CatLogs, CatBugReports, CatUpdates, CatOther} {
		if got[k] != 0 {
			t.Errorf("%s = %d, want 0", k, got[k])
		}
	}
	if got[CatDatabase] != 10 {
		t.Errorf("database = %d, want 10", got[CatDatabase])
	}
}

func TestMeasure_NeverFollowsSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "big.bin", 1<<20)
	root := t.TempDir()
	write(t, root, "photos/a.jpg", 100)
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "big.bin"), filepath.Join(root, "linkfile")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rep, err := Measure(context.Background(), layoutIn(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := bytesOf(t, rep)[CatOther]; got != 100 {
		t.Errorf("other = %d, want 100 (symlinks must not be followed or counted)", got)
	}
}

func TestMeasure_UnreadableDirIsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced here")
	}
	root := t.TempDir()
	write(t, root, "photos/a.jpg", 100)
	write(t, root, "locked/b.bin", 50)
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })
	rep, err := Measure(context.Background(), layoutIn(root))
	if err != nil {
		t.Fatalf("an unreadable dir must not fail the report: %v", err)
	}
	if rep.Skipped == 0 {
		t.Error("Skipped = 0, want > 0")
	}
	if got := bytesOf(t, rep)[CatOther]; got != 100 {
		t.Errorf("other = %d, want 100", got)
	}
}

func TestMeasure_StopsWhenTheRequestIsGone(t *testing.T) {
	root := t.TempDir()
	write(t, root, "photos/a.jpg", 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Measure(ctx, layoutIn(root)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestMeasure_BucketOutsideDataDir(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	write(t, other, "r1/x", 77)
	write(t, root, "photos/a.jpg", 10)
	l := layoutIn(root)
	l.BugReportsDir = other
	rep, err := Measure(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	got := bytesOf(t, rep)
	if got[CatBugReports] != 77 || got[CatOther] != 10 {
		t.Errorf("bug_reports=%d other=%d, want 77 and 10", got[CatBugReports], got[CatOther])
	}
}

func TestMeasure_BucketNestedInDataDirIsNotCountedTwice(t *testing.T) {
	root := t.TempDir()
	write(t, root, "updates/x", 5)
	l := layoutIn(root)
	l.UpdatesDir = filepath.Join(root, "updates") + string(filepath.Separator) // unclean on purpose
	rep, err := Measure(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	got := bytesOf(t, rep)
	if got[CatUpdates] != 5 || got[CatOther] != 0 {
		t.Errorf("updates=%d other=%d, want 5 and 0", got[CatUpdates], got[CatOther])
	}
}

// The size walk is file-system only (ut-docs#3122): it must never open the
// database or import anything that can.
func TestPackageNeverTouchesTheDatabase(t *testing.T) {
	forbidden := []string{"database/sql", "/internal/data", "/internal/db", "sqlite"}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if path == bad || strings.Contains(path, bad) {
					t.Errorf("%s imports %q: storageusage must stay file-system only", f, path)
				}
			}
		}
	}
}
