package db

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0082 (ut-docs#1739): the plugin-settings encryption key must never be
// captured by the local backup — asserted for real, not just by "it's a
// file, not a row". With the key file under the same data root as the DB
// (production layout) and a sealed value actually stored in
// plugin_settings, a Snapshot() (VACUUM INTO) must contain the ciphertext
// but not the key in any encoding, and the key file must sit outside the
// backup directory.
func TestSecretsKeyExcludedFromBackupSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "unitill-pos.db")
	d := openTest(t, dbPath)
	ctx := context.Background()

	ks := secrets.NewKeyStoreAt(filepath.Join(dataDir, "secrets", "plugin_settings_key.bin"), nil)
	key, err := ks.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const probe = `"sk_live_backup_probe_XYZZY"`
	sealed, err := secrets.Seal(key, []byte(probe))
	if err != nil {
		t.Fatal(err)
	}
	// Store the sealed value where production stores it. FK enforcement is
	// switched off for the seed only — this test is about what VACUUM INTO
	// copies, not the plugin install chain.
	if _, err := d.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('s1', 'com.example.pay', 'stripe_secret_key', ?, 'global')`, sealed); err != nil {
		t.Fatalf("seed sealed row: %v", err)
	}

	snap, err := Snapshot(d.DB, dbPath)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	blob, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	// The negative assertions below only mean something if the snapshot
	// really carried the table: prove the sealed row is in it.
	if !bytes.Contains(blob, []byte(sealed)) {
		t.Fatal("test premise: the snapshot must contain the sealed plugin_settings row")
	}
	if bytes.Contains(blob, []byte(probe)) || bytes.Contains(blob, []byte("backup_probe")) {
		t.Fatal("the snapshot contains the plaintext credential")
	}
	for name, enc := range map[string][]byte{
		"raw":    key,
		"base64": []byte(base64.StdEncoding.EncodeToString(key)),
		"hex":    []byte(hex.EncodeToString(key)),
	} {
		if bytes.Contains(blob, enc) {
			t.Fatalf("the snapshot contains the encryption key (%s encoding)", name)
		}
	}

	// And the key file lives outside the backup tree, so a "copy the
	// backups folder" workflow never picks it up either.
	backupDir, err := BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(backupDir, ks.Path())
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("key file %q is INSIDE the backup dir %q", ks.Path(), backupDir)
	}
	if err := filepath.WalkDir(backupDir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Equal(b, key) || bytes.Contains(b, key) {
			t.Fatalf("backup file %s carries the encryption key", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
