package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
)

type Manager struct {
	MenuPlugins map[string]MenuPlugin // key = plugin entry key
	Installed   map[string]Plugin     // key = plugin id
	Catalog     map[string]CatalogEntry
	db          *sql.DB
}

type Plugin struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type MenuPlugin struct {
	PluginID string `json:"pluginId"`
	Key      string `json:"key"`
	Route    string `json:"route"`
	Label    string `json:"label"`
	Menu     string `json:"menu"`
}

type CatalogEntry struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Runtime     string   `json:"runtime"`
	Entrypoint  string   `json:"entrypoint"`
	PackageURL  string   `json:"packageUrl"`
	SHA256      string   `json:"sha256"`
	Author      string   `json:"author"`
	Website     string   `json:"website"`
	Tags        []string `json:"tags,omitempty"`
}

type CatalogView struct {
	CatalogEntry
	Installed bool `json:"installed"`
}

// Init loads plugin metadata and menu entries from the database.
func Init(ctx context.Context, cfg *config.Config, db *sql.DB) (*Manager, error) {
	log.Printf("initialising plugins for env=%s", cfg.Env)

	m := &Manager{
		MenuPlugins: make(map[string]MenuPlugin),
		Installed:   make(map[string]Plugin),
		Catalog:     make(map[string]CatalogEntry),
		db:          db,
	}

	if err := m.loadCatalog(ctx); err != nil {
		return nil, err
	}
	if err := m.loadInstalled(ctx); err != nil {
		return nil, err
	}
	if err := m.loadMenuEntries(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload refreshes installed plugins and menu entries (used after install/uninstall).
func (m *Manager) Reload(ctx context.Context) error {
	m.Installed = make(map[string]Plugin)
	m.MenuPlugins = make(map[string]MenuPlugin)
	if err := m.loadInstalled(ctx); err != nil {
		return err
	}
	if err := m.loadMenuEntries(ctx); err != nil {
		return err
	}
	return nil
}

func (m *Manager) loadInstalled(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
SELECT id, name, version
FROM plugins
WHERE is_active = 1
`)
	if err != nil {
		return fmt.Errorf("load plugins: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Plugin
		if err := rows.Scan(&p.ID, &p.Name, &p.Version); err != nil {
			return err
		}
		m.Installed[p.ID] = p
	}
	return rows.Err()
}

func (m *Manager) loadMenuEntries(ctx context.Context) error {
	// Load all menu entries with permission requirements
	rows, err := m.db.QueryContext(ctx, `
SELECT 
    pe.plugin_id, 
    pe.key, 
    pe.route, 
    pe.label, 
    pe.menu_group,
    GROUP_CONCAT(pp.permission) as required_permissions,
    GROUP_CONCAT(pp.granted) as granted_flags
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
LEFT JOIN plugin_permissions pp ON pp.plugin_id = pe.plugin_id
WHERE pe.type = 'page' AND pe.is_active = 1 AND p.is_active = 1
GROUP BY pe.plugin_id, pe.key, pe.route, pe.label, pe.menu_group
ORDER BY pe.sort_order, pe.label
`)
	if err != nil {
		return fmt.Errorf("load plugin menu entries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mp MenuPlugin
		var permsStr, grantedStr sql.NullString

		if err := rows.Scan(&mp.PluginID, &mp.Key, &mp.Route, &mp.Label, &mp.Menu, &permsStr, &grantedStr); err != nil {
			return err
		}

		// Check if plugin has any required permissions
		// For now, we only show menu entries if the plugin has at least one granted permission
		// This prevents untrusted/zero-permission plugins from appearing in menus
		// If plugin has permissions, verify at least one is granted
		if permsStr.Valid && permsStr.String != "" {
			if !grantedStr.Valid {
				// Has permissions but none granted
				continue
			}

			perms := strings.Split(permsStr.String, ",")
			grantedFlags := strings.Split(grantedStr.String, ",")

			hasGranted := false
			for i, perm := range perms {
				if perm != "" && i < len(grantedFlags) && grantedFlags[i] == "1" {
					hasGranted = true
					break
				}
			}

			// Skip entries from plugins with no granted permissions
			if !hasGranted {
				continue
			}
		}
		// If no permissions required (permsStr is NULL/empty), allow the entry

		m.MenuPlugins[mp.Key] = mp
	}
	return rows.Err()
}

// InstalledIDs returns a stable sorted list of installed plugin IDs.
func (m *Manager) InstalledIDs() []string {
	out := make([]string, 0, len(m.Installed))
	for id := range m.Installed {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CatalogList returns catalog entries with installed flag.
func (m *Manager) CatalogList() []CatalogView {
	out := make([]CatalogView, 0, len(m.Catalog))
	for id, c := range m.Catalog {
		_, ok := m.Installed[id]
		out = append(out, CatalogView{CatalogEntry: c, Installed: ok})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) loadCatalog(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
SELECT id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, COALESCE(tags_json,'')
FROM plugin_catalog
WHERE is_deprecated = 0
`)
	if err != nil {
		return fmt.Errorf("load plugin catalog: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CatalogEntry
		var tags string
		if err := rows.Scan(&c.ID, &c.Version, &c.Name, &c.Description, &c.Runtime, &c.Entrypoint, &c.PackageURL, &c.SHA256, &c.Author, &c.Website, &tags); err != nil {
			return err
		}
		if strings.TrimSpace(tags) != "" {
			c.Tags = parseTags(tags)
		}
		m.Catalog[c.ID] = c
	}
	return rows.Err()
}

func parseTags(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, ` "`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CatalogPage fetches catalog entries with pagination and optional tag filter from DB.
func (m *Manager) CatalogPage(ctx context.Context, offset, limit int, tag string) ([]CatalogView, int, error) {
	if limit <= 0 {
		limit = 12
	}
	if offset < 0 {
		offset = 0
	}
	tag = strings.TrimSpace(tag)

	where := "WHERE is_deprecated = 0"
	args := []any{}
	if tag != "" {
		where += " AND tags_json LIKE ?"
		args = append(args, "%"+tag+"%")
	}

	var total int
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM plugin_catalog %s`, where)
	if err := m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count catalog: %w", err)
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(`
SELECT id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, COALESCE(tags_json,'')
FROM plugin_catalog
%s
ORDER BY name
LIMIT ? OFFSET ?
`, where)
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("load catalog page: %w", err)
	}
	defer rows.Close()

	var out []CatalogView
	for rows.Next() {
		var c CatalogEntry
		var tags string
		if err := rows.Scan(&c.ID, &c.Version, &c.Name, &c.Description, &c.Runtime, &c.Entrypoint, &c.PackageURL, &c.SHA256, &c.Author, &c.Website, &tags); err != nil {
			return nil, 0, err
		}
		if strings.TrimSpace(tags) != "" {
			c.Tags = parseTags(tags)
		}
		_, installed := m.Installed[c.ID]
		out = append(out, CatalogView{CatalogEntry: c, Installed: installed})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
