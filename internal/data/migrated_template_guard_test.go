package data

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// freshMigrationOpen matches a test opening a brand-new file through
// db.Open, which replays every migration: ~0.18s plain, ~5s under -race,
// per call. It catches TempDir() used inside the db.Open call itself
// (filepath.Join or string concatenation); a path built into a variable
// first is not seen — reviewers still catch that shape.
var freshMigrationOpen = regexp.MustCompile(`db\.Open\([^)]*TempDir\(\)`)

// TestDataTestsUseMigratedTemplate keeps internal/data's tests on
// testsupport.MigratedDBFile (ut-docs#2196): before it, ~100 per-test
// migration replays were most of the package's -race runtime. A test that
// genuinely needs a from-scratch migration run marks the line
// `// migrated-template:allow <reason>`.
func TestDataTestsUseMigratedTemplate(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if freshMigrationOpen.MatchString(line) && !strings.Contains(line, "migrated-template:allow") {
				t.Errorf("%s:%d: db.Open on a fresh TempDir file replays every migration; use db.Open(testsupport.MigratedDBFile(t, name))", f, i+1)
			}
		}
	}
}
