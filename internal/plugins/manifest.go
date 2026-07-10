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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
)

// Manifest represents the plugin.json schema
type Manifest struct {
	ID          string `json:"id"`          // e.g. "com.example.loyalty"
	Name        string `json:"name"`        // Human-readable name
	Version     string `json:"version"`     // Semver e.g. "1.0.0"
	Description string `json:"description"` // Short description
	Author      string `json:"author"`
	Website     string `json:"website"`
	Entrypoint  string `json:"entrypoint"` // Executable path or module (deprecated, use Executable)
	Executable  string `json:"executable"` // Executable filename (marketplace standard)
	Runtime     string `json:"runtime"`    // go|wasm|node|python|native

	// Marketplace fields (T016 - 009-cloud-marketplace)
	CanonicalType string `json:"canonical_type"`  // one of CanonicalTypes (see manifest_verifier.go)
	DeviceArch    string `json:"device_arch"`     // linux/amd64, darwin/arm64, any
	MinPOSVersion string `json:"min_pos_version"` // Minimum POS version required
	Signature     string `json:"signature"`       // Ed25519 signature of manifest (hex)
	ArtifactHash  string `json:"artifact_hash"`   // SHA256 of the plugin artifact

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
	// Asset-only plugins (runtime "none", e.g. themes) ship no executable, so
	// entrypoint is only required for runnable runtimes.
	if m.Entrypoint == "" && m.Runtime != "none" {
		return nil, fmt.Errorf("manifest missing required field: entrypoint")
	}
	if m.Runtime == "" {
		m.Runtime = "go" // default
	}
	// Entry types must be in the canonical taxonomy — fail here with a clear
	// message instead of a DB CHECK-constraint error at persist time.
	for _, e := range m.Entries {
		if !isValidCanonicalType(e.Type) {
			return nil, fmt.Errorf("manifest entry %q has invalid type %q (allowed: %s)",
				e.Key, e.Type, strings.Join(CanonicalTypes, "|"))
		}
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
	repo := data.NewPluginRepo(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	// 1. Ensure a plugin_catalog row exists — plugins(id,version) has a
	// foreign key onto plugin_catalog, and locally imported plugins have no
	// marketplace catalog entry. Existing rows are left untouched.
	if err := repo.EnsureCatalogEntry(ctx, tx, data.CatalogUpsertRow{
		ID:            m.ID,
		Version:       m.Version,
		Name:          m.Name,
		Description:   m.Description,
		Author:        m.Author,
		Website:       m.Website,
		Runtime:       m.Runtime,
		Entrypoint:    m.Entrypoint,
		PackageURL:    opts.InstalledFromURL,
		SHA256:        opts.SHA256,
		MinPOSVersion: m.MinPOSVersion,
		PublishedAt:   now.Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("ensure catalog entry: %w", err)
	}

	// 2. Insert into plugins table
	trustLevel := "untrusted"
	if opts.TrustLevel != "" {
		trustLevel = opts.TrustLevel
	}

	if err := repo.UpsertPluginManifest(ctx, tx, data.ManifestRow{
		ID:           m.ID,
		Name:         m.Name,
		Version:      m.Version,
		InstallState: "installed",
		Description:  m.Description,
		Author:       m.Author,
		Website:      m.Website,
		Entrypoint:   m.Entrypoint,
		Runtime:      m.Runtime,
		PackageURL:   opts.InstalledFromURL,
		SHA256:       opts.SHA256,
		TrustLevel:   trustLevel,
		InstalledAt:  now,
		UpdatedAt:    now,
	}); err != nil {
		return fmt.Errorf("insert plugin: %w", err)
	}

	// 2. Insert entries
	var entryRows []data.PluginEntryRow
	for _, e := range m.Entries {
		configJSON := ""
		if len(e.Config) > 0 {
			b, _ := json.Marshal(e.Config)
			configJSON = string(b)
		}

		entryRows = append(entryRows, data.PluginEntryRow{
			ID:            uuid.NewString(),
			Type:          e.Type,
			Key:           e.Key,
			Label:         e.Label,
			IconPath:      e.IconPath,
			SortOrder:     e.SortOrder,
			ParentPageKey: e.ParentPageKey,
			MenuGroup:     e.MenuGroup,
			Route:         e.Route,
			TargetAction:  e.TargetAction,
			TriggerEvent:  e.TriggerEvent,
			ConfigJSON:    configJSON,
		})
	}
	if err := repo.ReplacePluginEntries(ctx, tx, m.ID, entryRows); err != nil {
		return fmt.Errorf("insert plugin entry: %w", err)
	}

	// 3. Insert settings with defaults
	var settingRows []data.PluginSettingRow
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

		settingRows = append(settingRows, data.PluginSettingRow{
			ID:        uuid.NewString(),
			PluginID:  m.ID,
			Key:       s.Key,
			ValueJSON: valueJSON,
			Scope:     scope,
			ScopeID:   sql.NullString{Valid: false},
			UpdatedAt: now,
		})
	}
	if err := repo.InsertPluginSettings(ctx, tx, settingRows); err != nil {
		return fmt.Errorf("insert plugin setting: %w", err)
	}

	// 4. Insert hooks
	var hookRows []data.PluginHookRow
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

		hookRows = append(hookRows, data.PluginHookRow{
			ID:         uuid.NewString(),
			PluginID:   m.ID,
			Event:      h.Event,
			Action:     h.Action,
			Priority:   priority,
			IsActive:   true,
			ConfigJSON: configJSON,
		})
	}
	if err := repo.InsertPluginHooks(ctx, tx, hookRows); err != nil {
		return fmt.Errorf("insert plugin hook: %w", err)
	}

	// 5. Insert permissions (initially not granted)
	if err := repo.InsertPluginPermissions(ctx, tx, m.ID, m.Permissions); err != nil {
		return fmt.Errorf("insert plugin permission: %w", err)
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
