package data

import (
	"context"
	"database/sql"
	"fmt"
)

// InstallStatusRow mirrors plugin_install_status: the marketplace installer's
// bookkeeping, one row per listing this till has interacted with.
type InstallStatusRow struct {
	ListingID      string
	PluginID       string
	PluginName     string
	TargetVersion  string
	CurrentVersion string
	State          string
	MessageKey     string
	Retryable      bool
	UpdatedAt      string
}

type InstallStatusRepo struct {
	db *sql.DB
}

func NewInstallStatusRepo(db *sql.DB) *InstallStatusRepo {
	return &InstallStatusRepo{db: db}
}

func (r *InstallStatusRepo) Upsert(ctx context.Context, row InstallStatusRow) error {
	retryable := 0
	if row.Retryable {
		retryable = 1
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugin_install_status
    (listing_id, plugin_id, plugin_name, target_version, current_version,
     state, message_key, retryable, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(listing_id) DO UPDATE SET
    plugin_id = excluded.plugin_id,
    plugin_name = excluded.plugin_name,
    target_version = excluded.target_version,
    current_version = excluded.current_version,
    state = excluded.state,
    message_key = excluded.message_key,
    retryable = excluded.retryable,
    updated_at = excluded.updated_at`,
		row.ListingID, row.PluginID, row.PluginName, row.TargetVersion,
		row.CurrentVersion, row.State, row.MessageKey, retryable, row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert install status: %w", err)
	}
	return nil
}

const installStatusCols = `listing_id, COALESCE(plugin_id,''), COALESCE(plugin_name,''),
COALESCE(target_version,''), COALESCE(current_version,''), state,
COALESCE(message_key,''), retryable, updated_at`

func scanInstallStatus(scan func(dest ...any) error) (InstallStatusRow, error) {
	var row InstallStatusRow
	var retryable int
	err := scan(&row.ListingID, &row.PluginID, &row.PluginName, &row.TargetVersion,
		&row.CurrentVersion, &row.State, &row.MessageKey, &retryable, &row.UpdatedAt)
	row.Retryable = retryable != 0
	return row, err
}

func (r *InstallStatusRepo) Get(ctx context.Context, listingID string) (InstallStatusRow, bool, error) {
	row, err := scanInstallStatus(r.db.QueryRowContext(ctx,
		`SELECT `+installStatusCols+` FROM plugin_install_status WHERE listing_id = ?`, listingID).Scan)
	if err == sql.ErrNoRows {
		return InstallStatusRow{}, false, nil
	}
	if err != nil {
		return InstallStatusRow{}, false, fmt.Errorf("get install status: %w", err)
	}
	return row, true, nil
}

func (r *InstallStatusRepo) List(ctx context.Context) ([]InstallStatusRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+installStatusCols+` FROM plugin_install_status`)
	if err != nil {
		return nil, fmt.Errorf("list install status: %w", err)
	}
	defer rows.Close()
	var out []InstallStatusRow
	for rows.Next() {
		row, err := scanInstallStatus(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan install status: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteForPlugin removes records for a plugin id (records are keyed by
// listing id; uninstall clears by the installed plugin's id).
func (r *InstallStatusRepo) DeleteForPlugin(ctx context.Context, pluginID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM plugin_install_status WHERE plugin_id = ?`, pluginID)
	if err != nil {
		return fmt.Errorf("delete install status: %w", err)
	}
	return nil
}
