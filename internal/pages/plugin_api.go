package pages

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

func registerPluginAPI(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := installPlugin(r.Context(), d.db, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = d.pm.Reload(r.Context())
		d.menu = buildMenu(d.baseMenu, d.pm)
		w.WriteHeader(http.StatusNoContent)
	})
}

func installPlugin(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `
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
