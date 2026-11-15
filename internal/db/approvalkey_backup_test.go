package db

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/approvalkey"
)

// ut-docs#5127 (design §2, M4): the till's approval key must never travel
// in a backup. A key that left with a .bkp would let whoever holds the file
// plus a leaked token sign a move. Asserted for real: the snapshot (with
// photos), the backups folder and a restore onto another device (the
// replace-main-till / new-hardware path) neither carry the seed nor change
// the receiving till's own key.
func TestApprovalKeyNeverTravelsInBackups(t *testing.T) {
	dbA := testDBPath(t)
	dA := openTest(t, dbA)
	keyAPath := filepath.Join(filepath.Dir(dbA), "secrets", "approval-ed25519.key")
	keyA := approvalkey.NewAt(keyAPath)
	if _, err := keyA.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	seedA, err := os.ReadFile(keyAPath)
	if err != nil || len(seedA) != 32 {
		t.Fatalf("seed: %v %d", err, len(seedA))
	}
	kidA := keyA.Report("d", "s")["kid"]

	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "logo/receipt-logo.png", []byte("logo"))
	snap, err := SnapshotWithAssets(dA.DB, dbA, assets)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	for name, enc := range map[string][]byte{
		"raw":       seedA,
		"base64":    []byte(base64.StdEncoding.EncodeToString(seedA)),
		"base64url": []byte(base64.RawURLEncoding.EncodeToString(seedA)),
		"hex":       []byte(hex.EncodeToString(seedA)),
	} {
		if bytes.Contains(blob, enc) {
			t.Fatalf("the snapshot contains the approval key (%s encoding)", name)
		}
	}
	backupDir, err := BackupDir(dbA)
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(backupDir, keyAPath); err == nil && filepath.IsLocal(rel) {
		t.Fatalf("approval key %q is inside the backup dir %q", keyAPath, backupDir)
	}

	// Another device with its own key restores that backup.
	dbB := testDBPath(t)
	keyBPath := filepath.Join(filepath.Dir(dbB), "secrets", "approval-ed25519.key")
	keyB := approvalkey.NewAt(keyBPath)
	if _, err := keyB.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	seedBefore, _ := os.ReadFile(keyBPath)
	dirB, err := BackupDir(dbB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, filepath.Base(snap)), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(dbB, filepath.Base(snap)); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(dbB); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	seedAfter, _ := os.ReadFile(keyBPath)
	if !bytes.Equal(seedBefore, seedAfter) {
		t.Fatal("a restore changed the receiving till's approval key")
	}
	if bytes.Equal(seedAfter, seedA) || keyB.Report("d", "s")["kid"] == kidA {
		t.Fatal("the restoring till ended up with the source till's approval key")
	}
	// Nothing under the receiving data dir other than its own key file holds A's seed.
	if err := filepath.WalkDir(filepath.Dir(dbB), func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Contains(b, seedA) {
			t.Fatalf("%s carries the source till's approval key", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
