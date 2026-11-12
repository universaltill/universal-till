package data

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupRetentionDays(t *testing.T) {
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := BackupRetentionDays(ctx, d); got != 7 {
		t.Fatalf("default = %d, want 7", got)
	}
	for _, c := range []struct {
		val  string
		want int
	}{{"14", 14}, {" 3 ", 3}, {"0", 7}, {"-2", 7}, {"abc", 7}, {"", 7}, {"366", 365}, {"99999", 365}} {
		if _, err := d.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, BackupRetentionDaysSettingsKey, c.val); err != nil {
			t.Fatal(err)
		}
		if got := BackupRetentionDays(ctx, d); got != c.want {
			t.Errorf("setting %q = %d, want %d", c.val, got, c.want)
		}
	}
}

// Each till prunes its own disk, so the setting is never synced.
func TestBackupRetentionDaysIsPerTill(t *testing.T) {
	if SettingScope(BackupRetentionDaysSettingsKey) != SettingPerTill {
		t.Fatal("backup.retention_days must be a per-till setting")
	}
}

// A failed read must not look like "7 days": that would prune a shop's
// 365-day history on a SQLITE_BUSY (review of ut-docs#3092).
func TestBackupRetentionDaysReadErrorIsAnError(t *testing.T) {
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	d.Close() // every query now fails
	if n, err := BackupRetentionDaysChecked(context.Background(), d); err == nil {
		t.Fatalf("got %d, nil; want an error", n)
	}
}

func TestBackupRetentionDaysCheckedRejectsInvalidStoredValue(t *testing.T) {
	d, _ := sql.Open("sqlite", ":memory:")
	t.Cleanup(func() { d.Close() })
	_, _ = d.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`)
	_, _ = d.Exec(`INSERT INTO settings (key, value) VALUES (?, 'abc')`, BackupRetentionDaysSettingsKey)
	if n, err := BackupRetentionDaysChecked(context.Background(), d); err == nil {
		t.Fatalf("got %d, want an error for an invalid stored value", n)
	}
	if n, err := BackupRetentionDaysChecked(context.Background(), nil); err != nil || n != 7 {
		t.Fatalf("nil db = %d, %v; want 7, nil", n, err)
	}
}
