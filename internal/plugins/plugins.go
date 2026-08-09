package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

type Manager struct {
	MenuPlugins map[string]MenuPlugin // key = plugin entry key
	Installed   map[string]Plugin     // key = plugin id
	Catalog     map[string]CatalogEntry
	db          *sql.DB
	Wasm        *WasmRuntime // in-process runtime for runtime:"wasm" plugins
	localizer   Localizer    // receives language-pack translations
}

// Localizer is where plugin-shipped locale files land (the config.I18n).
type Localizer interface {
	SetOverlays(map[string]map[string]string)
}

// SetLocalizer wires the translator and applies current plugin locales.
func (m *Manager) SetLocalizer(l Localizer) {
	m.localizer = l
	m.syncLocales()
}

// syncLocales merges locales/*.json from every ACTIVE installed plugin into
// the translator as overlays (base files win on conflict). Language packs are
// asset-only plugins (runtime "none", canonical_type "language") but any
// active plugin may ship translations for its own strings.
func (m *Manager) syncLocales() {
	if m.localizer == nil {
		return
	}
	overlays := map[string]map[string]string{}
	for id, p := range m.Installed {
		dir := filepath.Join(paths.Plugins(), id, p.Version, "locales")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var msgs map[string]string
			if err := json.Unmarshal(raw, &msgs); err != nil {
				log.Printf("plugin %s: bad locale file %s: %v", id, e.Name(), err)
				continue
			}
			locale := strings.TrimSuffix(e.Name(), ".json")
			if overlays[locale] == nil {
				overlays[locale] = map[string]string{}
			}
			for k, v := range msgs {
				overlays[locale][k] = v
			}
		}
	}
	m.localizer.SetOverlays(overlays)
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

// DocsEntryKey is the reserved page-entry key (ADR-0037) a plugin uses to
// register its documentation page, surfaced by /plugins' Docs button
// (internal/pages/plugins_page.go). It is deliberately excluded from the
// /menu launcher in loadMenuEntries below — a docs entry is not navigation,
// and every type:"page" entry is otherwise also a menu candidate.
const DocsEntryKey = "docs"

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
		Wasm:        NewWasmRuntime(paths.Plugins()),
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
	warnPaymentMethodAnomalies(ctx, repo)
	m.Wasm.Sync(ctx, db)
	return m, nil
}

// warnPaymentMethodAnomalies logs startup warnings for payment_methods
// states that SyncPluginPaymentMethods deliberately leaves alone rather
// than erroring on (ut-docs#16) — silent otherwise, which is exactly what
// the independent review of the first pass at this fix flagged as a
// regression (a suppressed rename/materialization vanishing with no error
// and no log line).
func warnPaymentMethodAnomalies(ctx context.Context, repo *data.PluginRepo) {
	orphans, err := repo.FindOrphanedPaymentMethods(ctx)
	if err != nil {
		log.Printf("check for orphaned plugin-owned payment methods: %v", err)
	}
	for _, o := range orphans {
		log.Print(orphanPaymentMethodWarning(o))
	}

	suppressed, err := repo.FindSuppressedPaymentNameEntries(ctx)
	if err != nil {
		log.Printf("check for suppressed plugin payment-method labels: %v", err)
	}
	for _, s := range suppressed {
		log.Printf("plugin %s's payment entry %s cannot use label %q — already used by payment method %s; pick a distinct label or rename the conflicting tender", s.PluginID, s.Key, s.Label, s.BlockingID)
	}
}

// orphanPaymentMethodWarning builds the startup log line for a
// payment_methods row stamped with a plugin_id that has no corresponding
// plugins row (data.OrphanedPaymentMethod). ut-docs#170: the previous
// wording told the operator to "reassign/rename the tender", but no such
// affordance exists anywhere in web/ui/ — plugin-owned rows are
// materialized by SyncPluginPaymentMethods, and nothing in the data layer
// or UI can rename one afterward. It also read as an alarm on every boot
// even when the plugin itself was uninstalled on purpose, a case where
// ADR-0031 deliberately retains the tender row for sales history. This
// wording drops the non-actionable instruction and says plainly that a
// deliberate uninstall is expected, leaving reinstalling the plugin as the
// one real remedy for a stale/accidental capture.
func orphanPaymentMethodWarning(o data.OrphanedPaymentMethod) string {
	return fmt.Sprintf("payment method %s (%q) is plugin-owned by %s, which is not installed on this till — if that plugin was uninstalled on purpose, this is expected (the tender is kept for sales history); otherwise, reinstall the plugin to make this tender usable again", o.ID, o.Name, o.PluginID)
}

// Close releases the manager's shutdown-lifecycle resources: today that is
// stopping the wasm runtime's event drainer goroutines (WasmRuntime.Close,
// ut-docs#380). server.Start calls this from its graceful-shutdown goroutine;
// nil-safe so callers without a plugin manager (tests) can pass nil through.
func (m *Manager) Close(ctx context.Context) {
	if m == nil {
		return
	}
	m.Wasm.Close(ctx)
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
	warnPaymentMethodAnomalies(ctx, repo)
	m.Wasm.Sync(ctx, m.db)
	m.syncLocales()
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
		// A "docs" page entry is a Docs-button target (ADR-0037), not a
		// navigation destination — without this, every plugin adopting the
		// convention adds an unlocalized, unmapped-icon tile to /menu.
		if row.Key == DocsEntryKey {
			continue
		}
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

		// validatePageEntryKeys (manifest.go) rejects a NEW collision at
		// install time, but a till that already has one from before that
		// guard existed would otherwise lose an entry here with no signal
		// at all — same class of silent-drop warnPaymentMethodAnomalies
		// exists to surface for payment entries (ut-docs#472 review finding).
		if existing, ok := m.MenuPlugins[mp.Key]; ok && existing.PluginID != mp.PluginID {
			log.Printf("plugin %s's page entry %q is shadowed by plugin %s's entry of the same key — only %s's menu tile/route is reachable; uninstall or reinstall one of them with a different key", existing.PluginID, mp.Key, mp.PluginID, mp.PluginID)
		}
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
