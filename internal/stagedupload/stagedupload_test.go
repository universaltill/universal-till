package stagedupload

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func put(t *testing.T, dir, name string, mod time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func left(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// ut-docs#3955: a staged upload a crash left behind is removed once it is
// past the cutoff; a fresh one, and anything that isn't ours, is kept.
func TestPruneOlderThan(t *testing.T) {
	dir := t.TempDir()
	old := testNow.Add(-48 * time.Hour)
	fresh := testNow.Add(-time.Minute)

	put(t, dir, "ut-view-upload-111.upload", old)
	put(t, dir, "ut-import-222.upload", old)
	put(t, dir, "ut-import-stage-333.upload", old)
	put(t, dir, "ut-view-upload-444.upload", fresh)
	put(t, dir, "ut-import-stage-555.upload", fresh)
	// Not ours, however old: another program's temp files, our own other
	// temp kinds, a near-miss suffix.
	put(t, dir, "ut-bkp-666.db", old)
	put(t, dir, "ut-import-777.upload.keep", old)
	put(t, dir, "ut-import-888.csv", old)
	put(t, dir, "someone-else.upload", old)
	// A directory with a matching name is never removed.
	if err := os.Mkdir(filepath.Join(dir, "ut-import-999.upload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "ut-import-999.upload"), old, old); err != nil {
		t.Fatal(err)
	}

	orig := Dir
	Dir = func() string { return dir }
	t.Cleanup(func() { Dir = orig })

	n, freed, err := PruneOlderThan(testNow.Add(-MaxAge))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || freed != 12 {
		t.Errorf("removed %d files, %d bytes; want 3, 12", n, freed)
	}
	want := []string{
		"someone-else.upload",
		"ut-bkp-666.db",
		"ut-import-777.upload.keep",
		"ut-import-888.csv",
		"ut-import-999.upload",
		"ut-import-stage-555.upload",
		"ut-view-upload-444.upload",
	}
	if got := left(t, dir); !equal(got, want) {
		t.Errorf("left %v\nwant %v", got, want)
	}
}

// A symlink with a matching name is left alone: the sweep removes only
// regular files it could have created itself.
func TestPruneOlderThan_SkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ut-import-1.upload")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	orig := Dir
	Dir = func() string { return dir }
	t.Cleanup(func() { Dir = orig })

	if n, _, err := PruneOlderThan(testNow.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("removed %d, err %v; want 0, nil", n, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("symlink target removed: %v", err)
	}
}

// A missing temp directory is nothing to do, not an error.
func TestPruneOlderThan_MissingDir(t *testing.T) {
	orig := Dir
	Dir = func() string { return filepath.Join(t.TempDir(), "gone") }
	t.Cleanup(func() { Dir = orig })
	if n, _, err := PruneOlderThan(testNow); err != nil || n != 0 {
		t.Fatalf("removed %d, err %v; want 0, nil", n, err)
	}
}

// The patterns the request handlers create files with are exactly the ones
// the sweep matches: a CreateTemp name for each one is recognised.
func TestPatternsMatchCreateTempNames(t *testing.T) {
	dir := t.TempDir()
	for _, p := range Patterns() {
		f, err := os.CreateTemp(dir, p)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f.Name())
		_ = f.Close()
		if !Owns(name) {
			t.Errorf("CreateTemp(%q) made %q, which the sweep does not match", p, name)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
