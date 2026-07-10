package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
)

type Manager struct {
	MenuPlugins map[string]MenuPlugin // key = plugin entry key
	Installed   map[string]Plugin     // key = plugin id
	Catalog     map[string]CatalogEntry
	db          *sql.DB
}

type Plugin struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	IsActive bool   `json:"isActive"`
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

	repo := data.NewPluginRepo(db)
	m := &Manager{
		MenuPlugins: make(map[string]MenuPlugin),
		Installed:   make(map[string]Plugin),
		Catalog:     make(map[string]CatalogEntry),
		db:          db,
	}

	if err := m.loadCatalog(ctx, repo); err != nil {
		return nil, err
	}
	if err := m.loadInstalled(ctx, repo); err != nil {
		return nil, err
	}
	if err := m.loadMenuEntries(ctx, repo); err != nil {
		return nil, err
	}
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload refreshes installed plugins and menu entries (used after install/uninstall).
func (m *Manager) Reload(ctx context.Context) error {
	m.Installed = make(map[string]Plugin)
	m.MenuPlugins = make(map[string]MenuPlugin)
	repo := data.NewPluginRepo(m.db)
	if err := m.loadInstalled(ctx, repo); err != nil {
		return err
	}
	if err := m.loadMenuEntries(ctx, repo); err != nil {
		return err
	}
	// Payment methods are derived state: every lifecycle change funnels
	// through Reload, so syncing here covers install/enable/disable/uninstall.
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		return err
	}
	return nil
}

func (m *Manager) loadInstalled(ctx context.Context, repo *data.PluginRepo) error {
	rows, err := repo.ListInstalledPlugins(ctx)
	if err != nil {
		return fmt.Errorf("load plugins: %w", err)
	}
	for _, row := range rows {
		m.Installed[row.ID] = Plugin{
			ID:       row.ID,
			Name:     row.Name,
			Version:  row.Version,
			IsActive: true,
		}
	}
	return nil
}

func (m *Manager) loadMenuEntries(ctx context.Context, repo *data.PluginRepo) error {
	entries, err := repo.ListMenuEntries(ctx)
	if err != nil {
		return fmt.Errorf("load plugin menu entries: %w", err)
	}

	for _, row := range entries {
		mp := MenuPlugin{
			PluginID: row.PluginID,
			Key:      row.Key,
			Route:    row.Route,
			Label:    row.Label,
			Menu:     row.MenuGroup,
		}
		permsStr := row.RequiredPermissions
		grantedStr := row.GrantedFlags

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
	return nil
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

func (m *Manager) loadCatalog(ctx context.Context, repo *data.PluginRepo) error {
	rows, err := repo.ListCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load plugin catalog: %w", err)
	}
	for _, row := range rows {
		entry := CatalogEntry{
			ID:          row.ID,
			Version:     row.Version,
			Name:        row.Name,
			Description: row.Description,
			Runtime:     row.Runtime,
			Entrypoint:  row.Entrypoint,
			PackageURL:  row.PackageURL,
			SHA256:      row.SHA256,
			Author:      row.Author,
			Website:     row.Website,
		}
		if strings.TrimSpace(row.TagsJSON) != "" {
			entry.Tags = parseTags(row.TagsJSON)
		}
		m.Catalog[entry.ID] = entry
	}
	return nil
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

	rows, total, err := data.NewPluginRepo(m.db).CatalogPage(ctx, offset, limit, tag)
	if err != nil {
		return nil, 0, fmt.Errorf("load catalog page: %w", err)
	}

	var out []CatalogView
	for _, row := range rows {
		c := CatalogEntry{
			ID:          row.ID,
			Version:     row.Version,
			Name:        row.Name,
			Description: row.Description,
			Runtime:     row.Runtime,
			Entrypoint:  row.Entrypoint,
			PackageURL:  row.PackageURL,
			SHA256:      row.SHA256,
			Author:      row.Author,
			Website:     row.Website,
		}
		if strings.TrimSpace(row.TagsJSON) != "" {
			c.Tags = parseTags(row.TagsJSON)
		}
		_, installed := m.Installed[c.ID]
		out = append(out, CatalogView{CatalogEntry: c, Installed: installed})
	}
	return out, total, nil
}
