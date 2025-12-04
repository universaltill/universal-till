package plugins

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
)

// Manifest represents the plugin.json schema
type Manifest struct {
	ID          string `json:"id"`          // e.g. "com.example.loyalty"
	Name        string `json:"name"`        // Human-readable name
	Version     string `json:"version"`     // Semver e.g. "1.0.0"
	Description string `json:"description"` // Short description
	Author      string `json:"author"`
	Website     string `json:"website"`
	Entrypoint  string `json:"entrypoint"` // Executable path or module
	Runtime     string `json:"runtime"`    // go|wasm|node|python|native

	// Entries: pages, buttons, popups, etc.
	Entries []ManifestEntry `json:"entries,omitempty"`

	// Settings: configuration keys
	Settings []ManifestSetting `json:"settings,omitempty"`

	// Hooks: event subscriptions
	Hooks []ManifestHook `json:"hooks,omitempty"`

	// Permissions: requested capabilities
	Permissions []string `json:"permissions,omitempty"`
}

// ManifestEntry represents a UI/integration entry
type ManifestEntry struct {
	Type          string                 `json:"type"`  // page|button|popup|payment|device|etc.
	Key           string                 `json:"key"`   // unique within plugin
	Label         string                 `json:"label"` // display name
	IconPath      string                 `json:"icon_path,omitempty"`
	SortOrder     int                    `json:"sort_order,omitempty"`
	ParentPageKey string                 `json:"parent_page_key,omitempty"` // for buttons/popups
	MenuGroup     string                 `json:"menu_group,omitempty"`      // for pages
	Route         string                 `json:"route,omitempty"`           // for pages
	TargetAction  string                 `json:"target_action,omitempty"`   // for buttons
	TriggerEvent  string                 `json:"trigger_event,omitempty"`   // for popups/hooks
	Config        map[string]interface{} `json:"config,omitempty"`          // arbitrary JSON
}

// ManifestSetting represents a configuration key
type ManifestSetting struct {
	Key          string      `json:"key"`
	DefaultValue interface{} `json:"default_value,omitempty"`
	Scope        string      `json:"scope,omitempty"` // global|register|user
}

// ManifestHook represents an event subscription
type ManifestHook struct {
	Event    string                 `json:"event"`  // e.g. "sale.completed"
	Action   string                 `json:"action"` // e.g. "loyalty.awardPoints"
	Priority int                    `json:"priority,omitempty"`
	Config   map[string]interface{} `json:"config,omitempty"`
}

// ParseManifest reads plugin.json and returns a Manifest struct
func ParseManifest(r io.Reader) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest json: %w", err)
	}

	// Basic validation
	if m.ID == "" {
		return nil, fmt.Errorf("manifest missing required field: id")
	}
	if m.Name == "" {
		return nil, fmt.Errorf("manifest missing required field: name")
	}
	if m.Version == "" {
		return nil, fmt.Errorf("manifest missing required field: version")
	}
	if m.Entrypoint == "" {
		return nil, fmt.Errorf("manifest missing required field: entrypoint")
	}
	if m.Runtime == "" {
		m.Runtime = "go" // default
	}

	return &m, nil
}

// ComputeSHA256 calculates the SHA256 checksum of a file
func ComputeSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// PersistManifest saves the manifest to database tables
func PersistManifest(ctx context.Context, db *sql.DB, m *Manifest, opts InstallOptions) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	// 1. Insert into plugins table
	trustLevel := "untrusted"
	if opts.TrustLevel != "" {
		trustLevel = opts.TrustLevel
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO plugins (
			id, name, version, install_state, description, author, website,
			entrypoint, runtime, installed_from_url, installed_sha256,
			is_active, trust_level, installed_at, updated_at
		) VALUES (?, ?, ?, 'installed', ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id, version) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			author = excluded.author,
			website = excluded.website,
			entrypoint = excluded.entrypoint,
			runtime = excluded.runtime,
			installed_from_url = excluded.installed_from_url,
			installed_sha256 = excluded.installed_sha256,
			updated_at = excluded.updated_at,
			install_state = 'installed',
			is_active = 1
	`, m.ID, m.Name, m.Version, m.Description, m.Author, m.Website,
		m.Entrypoint, m.Runtime, opts.InstalledFromURL, opts.SHA256,
		trustLevel, now, now)
	if err != nil {
		return fmt.Errorf("insert plugin: %w", err)
	}

	// 2. Insert entries
	for _, e := range m.Entries {
		configJSON := ""
		if len(e.Config) > 0 {
			b, _ := json.Marshal(e.Config)
			configJSON = string(b)
		}

		entryID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO plugin_entries (
				id, plugin_id, type, key, label, icon_path, sort_order, is_active,
				parent_page_key, menu_group, route, target_action, trigger_event,
				config_json, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(plugin_id, key) DO UPDATE SET
				label = excluded.label,
				icon_path = excluded.icon_path,
				sort_order = excluded.sort_order,
				parent_page_key = excluded.parent_page_key,
				menu_group = excluded.menu_group,
				route = excluded.route,
				target_action = excluded.target_action,
				trigger_event = excluded.trigger_event,
				config_json = excluded.config_json,
				updated_at = excluded.updated_at
		`, entryID, m.ID, e.Type, e.Key, e.Label, e.IconPath, e.SortOrder,
			e.ParentPageKey, e.MenuGroup, e.Route, e.TargetAction, e.TriggerEvent,
			configJSON, now, now)
		if err != nil {
			return fmt.Errorf("insert plugin entry %s: %w", e.Key, err)
		}
	}

	// 3. Insert settings with defaults
	for _, s := range m.Settings {
		scope := s.Scope
		if scope == "" {
			scope = "global"
		}

		valueJSON := ""
		if s.DefaultValue != nil {
			b, _ := json.Marshal(s.DefaultValue)
			valueJSON = string(b)
		}

		settingID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO plugin_settings (
				id, plugin_id, key, value_json, scope, scope_id, updated_at
			) VALUES (?, ?, ?, ?, ?, NULL, ?)
			ON CONFLICT(plugin_id, key, scope, scope_id) DO UPDATE SET
				value_json = excluded.value_json,
				updated_at = excluded.updated_at
		`, settingID, m.ID, s.Key, valueJSON, scope, now)
		if err != nil {
			return fmt.Errorf("insert plugin setting %s: %w", s.Key, err)
		}
	}

	// 4. Insert hooks
	for _, h := range m.Hooks {
		configJSON := ""
		if len(h.Config) > 0 {
			b, _ := json.Marshal(h.Config)
			configJSON = string(b)
		}

		priority := h.Priority
		if priority == 0 {
			priority = 100 // default
		}

		hookID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO plugin_hooks (
				id, plugin_id, event, action, priority, is_active, config_json
			) VALUES (?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(plugin_id, event, action) DO UPDATE SET
				priority = excluded.priority,
				config_json = excluded.config_json
		`, hookID, m.ID, h.Event, h.Action, priority, configJSON)
		if err != nil {
			return fmt.Errorf("insert plugin hook %s/%s: %w", h.Event, h.Action, err)
		}
	}

	// 5. Insert permissions (initially not granted)
	for _, perm := range m.Permissions {
		permID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO plugin_permissions (
				id, plugin_id, permission, granted
			) VALUES (?, ?, ?, 0)
			ON CONFLICT(plugin_id, permission) DO NOTHING
		`, permID, m.ID, perm)
		if err != nil {
			return fmt.Errorf("insert plugin permission %s: %w", perm, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// InstallOptions holds metadata about a plugin installation
type InstallOptions struct {
	InstalledFromURL string
	SHA256           string
	TrustLevel       string // untrusted|trusted|system
	Uploader         string // user/source identifier
}
