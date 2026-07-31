package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockedDBPath returns a db path whose parent "directory" is a regular file,
// so any MkdirAll under it fails — the cheap, real way to reach the
// filesystem error branches without fault injection.
func blockedDBPath(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	return filepath.Join(blocker, "unitill-pos.db")
}

func TestBackupDirFailsWhenParentIsFile(t *testing.T) {
	if _, err := BackupDir(blockedDBPath(t)); err == nil {
		t.Fatal("BackupDir under a file: want error")
	}
}

func TestListBackupsPropagatesBackupDirError(t *testing.T) {
	if _, err := ListBackups(blockedDBPath(t)); err == nil {
		t.Fatal("ListBackups under a file: want error")
	}
}

func TestPruneBackupsPropagatesListError(t *testing.T) {
	if err := PruneBackups(blockedDBPath(t), 3); err == nil {
		t.Fatal("PruneBackups under a file: want error")
	}
}

func TestSnapshotFailsOnClosedDB(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB.Close()
	if _, err := Snapshot(sqlDB, filepath.Join(dir, "unitill-pos.db")); err == nil {
		t.Fatal("Snapshot on closed DB: want error")
	}
}

func TestStageRestoreRejectsMissingBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	err := StageRestore(dbPath, "unitill-pos-20990101-000000.db")
	if err == nil || !strings.Contains(err.Error(), "backup not found") {
		t.Fatalf("StageRestore missing backup = %v; want backup not found", err)
	}
}

func TestApplyReplicaIdentityNoFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	applied, err := ApplyReplicaIdentity(d.DB, path)
	if err != nil || applied {
		t.Fatalf("ApplyReplicaIdentity without identity file: applied=%v err=%v; want false,nil", applied, err)
	}
}

func TestApplyReplicaIdentityRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := os.WriteFile(ReplicaIdentityPath(path), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}
	if _, err := ApplyReplicaIdentity(d.DB, path); err == nil {
		t.Fatal("ApplyReplicaIdentity with corrupt identity: want error")
	}
}
