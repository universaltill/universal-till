package data

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#6566 AC12: the card reader registry is till-local (reader ids are
// device specific). Nothing outside its own repository may read or write the
// table, so it can never reach the LAN journal, a snapshot, the admin bundle,
// the cloud roster or the sales mirror. A new reference must be deliberate:
// add the file here with the reason, after checking it is not a sync path.
var tableRef = regexp.MustCompile(`(?i)\b(from|into|update|join|table)\s+card_readers\b`)

func TestCardReaders_NotInSyncPaths(t *testing.T) {
	allowed := map[string]bool{
		"card_reader_repo.go": true, // the repository itself
		"sync_admin_repo.go":  true, // classifies the table as NOT synced (nonAdminTables, with the reason)
	}
	root := ".." // internal/
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Production code only: a test may read the table to assert on it.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if allowed[filepath.Base(path)] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if tableRef.Match(b) {
			t.Errorf("%s queries the card_readers table: it is till-local and must not appear in sync, snapshot, bundle or roster code", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
