package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirBytes sums the size of every file in dir.
func dirBytes(t *testing.T, dir string) (total int64, files int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
		files++
	}
	return total, files
}

// ut-docs#2720 (binding memory rule: till caches/buffers are bounded): the
// log folder never holds more than maxFiles files of at most maxBytes each,
// however much is logged — here ~50× the budget.
func TestRotatingWriterIsBounded(t *testing.T) {
	dir := t.TempDir()
	const maxBytes, maxFiles = 1024, 3
	w, err := NewRotatingWriter(filepath.Join(dir, "till.log"), maxBytes, maxFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := []byte(strings.Repeat("x", 99) + "\n") // 100 bytes
	for range 1500 {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	total, files := dirBytes(t, dir)
	if files != maxFiles {
		t.Fatalf("files = %d, want exactly %d (till.log + %d rotated)", files, maxFiles, maxFiles-1)
	}
	if total > maxBytes*maxFiles {
		t.Fatalf("log folder holds %d bytes, budget is %d", total, maxBytes*maxFiles)
	}
	for _, name := range []string{"till.log", "till.log.1", "till.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "till.log.3")); err == nil {
		t.Fatal("till.log.3 exists — rotation kept more than maxFiles files")
	}
}

// Rotation must not split or lose a line: the newest line is always the
// last line of till.log, and a rotated file ends on a line boundary.
func TestRotatingWriterKeepsWholeLinesAndNewestLast(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(filepath.Join(dir, "till.log"), 256, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if _, err := w.Write([]byte(strings.Repeat("a", 20) + string(rune('A'+i%26)) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write([]byte("LAST LINE\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	cur, _ := os.ReadFile(filepath.Join(dir, "till.log"))
	if !bytes.HasSuffix(cur, []byte("LAST LINE\n")) {
		t.Fatalf("till.log does not end with the newest line: %q", cur)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "till.log.1"))
	if len(old) == 0 || old[len(old)-1] != '\n' {
		t.Fatalf("rotated file not on a line boundary: %q", old)
	}
}

// One oversized write can't blow the per-file budget either.
func TestRotatingWriterTruncatesOversizedWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(filepath.Join(dir, "till.log"), 128, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write(bytes.Repeat([]byte("z"), 10_000)); err != nil {
		t.Fatal(err)
	}
	total, _ := dirBytes(t, dir)
	if total > 128*2 {
		t.Fatalf("oversized write left %d bytes, budget 256", total)
	}
}

// Reopening an existing log appends and counts the bytes already there, so a
// till that restarts often still rotates on time.
func TestRotatingWriterResumesExistingSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "till.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), 200), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(path, 256, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write(bytes.Repeat([]byte("n"), 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("existing 200 bytes + 100 must rotate at 256: %v", err)
	}
}

// ut-docs#2720 review: on Windows the live-file rename fails while another
// program holds till.log without FILE_SHARE_DELETE. The fallback must keep
// the newest content — copied to till.log.1 — and restart the live file
// empty, not truncate it away. Older files still shift as usual.
func TestRotatingWriterRenameFailureKeepsNewestContent(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "till.log")
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(from, to string) error {
		if from == live {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
		}
		return os.Rename(from, to)
	}

	const maxBytes = 16
	w, err := NewRotatingWriter(live, maxBytes, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, s := range []string{"first-line-0123\n", "second-line-012\n", "third-line-0123\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}
	if got := read(live + ".1"); got != "second-line-012\n" {
		t.Fatalf("till.log.1 = %q, want the newest rotated-out content", got)
	}
	if got := read(live + ".2"); got != "first-line-0123\n" {
		t.Fatalf("till.log.2 = %q, want the older content shifted", got)
	}
	if got := read(live); got != "third-line-0123\n" {
		t.Fatalf("live file = %q, want it restarted with only the new line", got)
	}
}
