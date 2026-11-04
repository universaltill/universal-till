package data

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// DefaultBackupRetentionDays is how many days of snapshots the till keeps
// unless the shop changes the setting (ut-docs#3092); MaxBackupRetentionDays
// caps the setting. housekeeping.DefaultBackupRetentionDays must equal the
// default (TestBackupRetentionDefaultsAgree in internal/server).
const (
	DefaultBackupRetentionDays = 7
	MaxBackupRetentionDays     = 365
)

// BackupRetentionDaysChecked is the number of days of backups this till keeps
// (setting backup.retention_days, ut-docs#3092) for code that DELETES by it:
// DefaultBackupRetentionDays when the key is unset, at most
// MaxBackupRetentionDays, and an error when the setting cannot be read or
// is not a positive whole number. A caller that prunes skips the prune on
// error: a failed read must never fall back to a shorter window than the
// shop chose (fail toward retaining).
func BackupRetentionDaysChecked(ctx context.Context, d *sql.DB) (int, error) {
	if d == nil {
		return DefaultBackupRetentionDays, nil
	}
	v, ok, err := NewSettingsRepo(d).Get(ctx, BackupRetentionDaysSettingsKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return DefaultBackupRetentionDays, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("setting %s is %q, not a positive whole number of days", BackupRetentionDaysSettingsKey, v)
	}
	return min(n, MaxBackupRetentionDays), nil
}

// BackupRetentionDays is BackupRetentionDaysChecked for display: an unreadable
// or invalid value shows as the default. Never use it to decide what to delete.
func BackupRetentionDays(ctx context.Context, d *sql.DB) int {
	if n, err := BackupRetentionDaysChecked(ctx, d); err == nil {
		return n
	}
	return DefaultBackupRetentionDays
}
