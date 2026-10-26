package db

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// otherTillBackupBytes builds a backup as another till would have written
// it: one completed sale, a shop name, a store id and that till's own
// cloud identity. Returns the file's bytes.
func otherTillBackupBytes(t *testing.T, storeID string, mutate func(d *DB)) []byte {
	t.Helper()
	other := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := Open(other)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"marketplace.device_id": "till-dead-device",
		"marketplace.token":     "dead-till-secret-token-5070",
		"marketplace.store_id":  storeID,
		"store.name":            "Test 5070 Corner Shop",
	} {
		setSetting(t, d.DB, k, v)
	}
	if _, err := d.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'R-1', 'completed', 500, 500, '2026-10-20 17:45:00')`); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	if mutate != nil {
		mutate(d)
	}
	snap, err := Snapshot(d.DB, other)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func noUploadLeftovers(t *testing.T, dbPath string) {
	t.Helper()
	dir, _ := BackupDir(dbPath)
	if m, _ := filepath.Glob(filepath.Join(dir, "upload-*")); len(m) > 0 {
		t.Errorf("upload leftovers: %v", m)
	}
}

// ut-docs#5070: a backup file from another till of the same shop is checked
// and kept as a normal, restorable backup; the other till's credential is
// not stored again; the summary gives the cut-off the manager is told.
func TestImportBackupFileKeepsAValidBackup(t *testing.T) {
	path := testDBPath(t)
	openTest(t, path).Close()
	raw := otherTillBackupBytes(t, "store-5070", nil)

	got, err := ImportBackupFile(path, bytes.NewReader(raw), int64(len(raw)), "store-5070")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !ValidBackupName(got.Name) || !IsUploadedBackupName(got.Name) {
		t.Fatalf("name %q is not a restorable uploaded backup", got.Name)
	}
	if got.ShopName != "Test 5070 Corner Shop" || got.Sales != 1 {
		t.Errorf("summary = %+v", got)
	}
	if want := time.Date(2026, 10, 20, 17, 45, 0, 0, time.UTC); !got.NewestSale.Equal(want) {
		t.Errorf("newest sale %v, want %v", got.NewestSale, want)
	}
	list, err := ListBackups(path)
	if err != nil || len(list) != 1 || list[0].Name != got.Name {
		t.Fatalf("not listed: %v %v", list, err)
	}
	dir, _ := BackupDir(path)
	kept, err := os.ReadFile(filepath.Join(dir, got.Name))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(kept, []byte("dead-till-secret-token-5070")) {
		t.Error("the other till's token is stored again in the kept file")
	}
	noUploadLeftovers(t, path)
	// It restores like any backup.
	if err := StageRestore(path, got.Name); err != nil {
		t.Fatalf("stage the uploaded backup: %v", err)
	}
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	if v, _ := getSetting(t, restored.DB, "store.name"); v != "Test 5070 Corner Shop" {
		t.Errorf("restored shop name %q", v)
	}
}

func TestImportBackupFileRefusals(t *testing.T) {
	valid := otherTillBackupBytes(t, "store-5070", nil)
	newer := otherTillBackupBytes(t, "store-5070", func(d *DB) {
		if _, err := d.Exec(`INSERT INTO schema_migrations (version, name, checksum) VALUES (99999, '99999_from_the_future.sql', 'x')`); err != nil {
			t.Fatal(err)
		}
	})
	foreignSQLite := func() []byte {
		p := filepath.Join(t.TempDir(), "other.db")
		d, err := openBackupFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`CREATE TABLE notes (body TEXT)`); err != nil {
			t.Fatal(err)
		}
		d.Close()
		raw, _ := os.ReadFile(p)
		return raw
	}()
	truncated := append([]byte(nil), valid[:len(valid)/2]...)

	cases := []struct {
		name    string
		body    []byte
		max     int64
		live    string
		wantErr error
	}{
		{"too large", valid, int64(len(valid)) - 1, "", ErrBackupTooLarge},
		{"not a database", []byte("PK\x03\x04 a zip, not a till backup"), 1 << 20, "", ErrNotTillBackup},
		{"another program's sqlite", foreignSQLite, 1 << 20, "", ErrNotTillBackup},
		{"damaged", truncated, 1 << 30, "", ErrNotTillBackup},
		{"made by a newer till", newer, 1 << 30, "", ErrBackupNewer},
		{"another shop", valid, 1 << 30, "store-other", ErrBackupOtherShop},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := testDBPath(t)
			openTest(t, path).Close()
			_, err := ImportBackupFile(path, bytes.NewReader(c.body), c.max, c.live)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if list, _ := ListBackups(path); len(list) != 0 {
				t.Errorf("a refused upload was kept: %v", list)
			}
			noUploadLeftovers(t, path)
		})
	}
}

// A till not paired with any shop, or a backup with no store id, has
// nothing to compare: the backup is accepted.
func TestImportBackupFileWithoutShopIdentityIsAccepted(t *testing.T) {
	for _, c := range []struct{ backup, live string }{{"store-5070", ""}, {"", "store-5070"}} {
		path := testDBPath(t)
		openTest(t, path).Close()
		raw := otherTillBackupBytes(t, c.backup, nil)
		if _, err := ImportBackupFile(path, bytes.NewReader(raw), 1<<30, c.live); err != nil {
			t.Errorf("backup store %q, live %q: %v", c.backup, c.live, err)
		}
	}
}

func TestSweepOrphanedUploads(t *testing.T) {
	path := testDBPath(t)
	dir, _ := BackupDir(path)
	now := time.Now()
	for name, age := range map[string]time.Duration{
		"upload-1.part":      48 * time.Hour,
		"upload-1.part-wal":  48 * time.Hour,
		"upload-2.part":      time.Minute,
		"unitill-pos-x.db":   48 * time.Hour,
		"upload-3.part.keep": 48 * time.Hour,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	n, err := SweepOrphanedUploads(path, now)
	if err != nil || n != 2 {
		t.Fatalf("swept %d, %v; want 2", n, err)
	}
	for _, keep := range []string{"upload-2.part", "unitill-pos-x.db", "upload-3.part.keep"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s removed", keep)
		}
	}
}

// craftedTrigger puts the dead till's token back over whatever is
// inserted into settings — the ut-docs#5070 review's attack.
const craftedTrigger = `CREATE TRIGGER evil_keep_token AFTER INSERT ON settings BEGIN
  UPDATE settings SET value = 'dead-till-secret-token-5070' WHERE key = 'marketplace.token';
END`

// ut-docs#5070 review: a file whose schema carries a trigger, view or table
// this build never creates is refused at upload — a crafted trigger could
// otherwise put another till's credential back after the strip.
func TestImportBackupFileRefusesForeignSchemaObjects(t *testing.T) {
	for name, ddl := range map[string]string{
		"trigger on settings": craftedTrigger,
		"changed real trigger": `DROP TRIGGER trg_sync_admin_version_settings_ins;
CREATE TRIGGER trg_sync_admin_version_settings_ins AFTER INSERT ON settings BEGIN
  UPDATE settings SET value = 'x' WHERE key = 'marketplace.token';
END`,
		"extra view":  `CREATE VIEW leak AS SELECT * FROM settings`,
		"extra table": `CREATE TABLE stash (k TEXT, v TEXT)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := testDBPath(t)
			openTest(t, path).Close()
			raw := otherTillBackupBytes(t, "store-5070", func(d *DB) {
				if _, err := d.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			})
			_, err := ImportBackupFile(path, bytes.NewReader(raw), 1<<30, "")
			if !errors.Is(err, ErrNotTillBackup) {
				t.Fatalf("err = %v, want ErrNotTillBackup", err)
			}
			if list, _ := ListBackups(path); len(list) != 0 {
				t.Errorf("kept: %v", list)
			}
			noUploadLeftovers(t, path)
		})
	}
}

// The same file dropped into the backups folder by hand and restored from
// the list: boot refuses it before rewriting settings, so the live till's
// identity stays and the dead token never comes back.
func TestApplyPendingRestoreRefusesForeignSettingsTrigger(t *testing.T) {
	path := testDBPath(t)
	live := openTest(t, path)
	setSetting(t, live.DB, "marketplace.token", "new-till-token")
	live.Close()
	raw := otherTillBackupBytes(t, "store-5070", func(d *DB) {
		if _, err := d.Exec(craftedTrigger); err != nil {
			t.Fatal(err)
		}
	})
	dir, _ := BackupDir(path)
	name := "unitill-pos-20261020-180000.db"
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPendingRestore(path)
	if applied || err != nil {
		t.Fatalf("a file with a foreign settings trigger was applied: %v %v", applied, err)
	}
	if !RestoreRefused(path) {
		t.Error("refusal not recorded")
	}
	d := openTest(t, path)
	if got, _ := getSetting(t, d.DB, "marketplace.token"); got != "new-till-token" {
		t.Errorf("live token = %q", got)
	}
	noUploadLeftovers(t, path)
}
