package data

import (
	"context"
	"database/sql"
	"time"
)

type PluginRepo struct {
	db *sql.DB
}

func NewPluginRepo(db *sql.DB) *PluginRepo {
	return &PluginRepo{db: db}
}

func (r *PluginRepo) InstallPlugin(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugins (
    id, name, version, install_state, description,
    author, website, entrypoint, runtime,
    installed_from_url, installed_sha256, is_active,
    trust_level, installed_at, updated_at
)
SELECT
    pc.id, pc.name, pc.version, 'installed', pc.description,
    pc.author, pc.website, pc.entrypoint, pc.runtime,
    pc.package_url, pc.sha256, 1,
    'trusted', ?, ?
FROM plugin_catalog pc
WHERE pc.id = ?
ON CONFLICT(id) DO UPDATE SET
    version = excluded.version,
    install_state = 'installed',
    description = excluded.description,
    author = excluded.author,
    website = excluded.website,
    entrypoint = excluded.entrypoint,
    runtime = excluded.runtime,
    installed_from_url = excluded.installed_from_url,
    installed_sha256 = excluded.installed_sha256,
    is_active = 1,
    trust_level = 'trusted',
    updated_at = excluded.updated_at
`, time.Now(), time.Now(), id)
	return err
}
