package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/restoredsecrets"
)

// U4 / S7: an applied restore owes the "enter these keys again" scan; a boot
// with nothing staged does not.
func TestApplyPendingRestoreOwesRestoredSecretsScan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	name := makeBackup(t, d, dbPath, "20260101-000000")
	_ = d.Close()

	if applied, err := ApplyPendingRestore(dbPath); err != nil || applied {
		t.Fatalf("nothing staged: want (false,nil), got %v %v", applied, err)
	}
	if restoredsecrets.Pending(dbPath) {
		t.Fatal("no restore applied: no scan may be owed")
	}
	if err := StageRestore(dbPath, name); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(dbPath); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	if !restoredsecrets.Pending(dbPath) {
		t.Fatal("an applied restore must owe the restored-secrets scan")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "secrets", "plugin_settings_key.bin")); err == nil {
		t.Fatal("the restore must never bring a sealing key file")
	}
}
