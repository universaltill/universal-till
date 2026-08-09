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
	"github.com/universaltill/universal-till/internal/logging"
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
// validatePaymentEntryKeys enforces ADR-0031's install-time half for every
// path that writes plugin_entries type='payment' (PersistManifest AND
// Rollback — a legacy on-disk manifest can carry keys that predate
// validation). Keys must be non-empty, whitespace-clean, contain no ':'
// (reserved namespace separator), and not collide with a non-plugin tender
// or another plugin's payment key. Labels (payment_methods.name is also
// UNIQUE) get the same collision check — ut-docs#16, ADR-0031's documented
// residual: without this, two tenders wanting the same label hard-failed
// the sync at every startup instead of getting a clear install-time error.
// The plugin's own keys/labels never self-conflict, so reinstall/upgrade
// pass. Labels must also be non-empty and unique WITHIN this manifest
// (ut-docs#168): FindPaymentNameConflicts only ever compares a candidate
// label against OTHER plugins' rows, so two sibling entries sharing one
// label — or an entry with an empty Label — previously installed cleanly
// and then silently lost one entry at sync time with no error and no
// warning, since neither case is a cross-plugin collision.
//
// Keys must also be unique WITHIN this manifest (ut-docs#363) — unlike the
// label case, this was never a silent-data-loss bug (plugin_entries has a
// real UNIQUE(plugin_id, key) constraint, so PersistManifest already failed
// and rolled back the whole transaction), but the caller saw the raw SQLite
// constraint message instead of a clean, actionable one naming the key like
// every check above. Checked here so the message is consistent and the DB
// round-trip is skipped for a mistake that's already fully knowable from
// the manifest alone.
func validatePaymentEntryKeys(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, entries []ManifestEntry) error {
	var paymentKeys, paymentNames []string
	seenKeys := make(map[string]bool, len(entries))
	seenLabels := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Type != "payment" {
			continue
		}
		if e.Key == "" {
			return fmt.Errorf("payment entry has an empty key")
		}
		if e.Key != strings.TrimSpace(e.Key) {
			return fmt.Errorf("payment entry key %q has surrounding whitespace", e.Key)
		}
		if strings.Contains(e.Key, ":") {
			return fmt.Errorf("payment entry key %q must not contain ':'", e.Key)
		}
		if seenKeys[e.Key] {
			return fmt.Errorf("payment entry key %q is used by more than one entry in this manifest — pick distinct keys", e.Key)
		}
		seenKeys[e.Key] = true
		if strings.TrimSpace(e.Label) == "" {
			return fmt.Errorf("payment entry %q has an empty label", e.Key)
		}
		if seenLabels[e.Label] {
			return fmt.Errorf("payment entry label %q is used by more than one entry in this manifest — pick distinct labels", e.Label)
		}
		seenLabels[e.Label] = true
		paymentKeys = append(paymentKeys, e.Key)
		paymentNames = append(paymentNames, e.Label)
	}
	if len(paymentKeys) == 0 {
		return nil
	}
	conflicts, err := repo.FindPaymentKeyConflicts(ctx, tx, pluginID, paymentKeys)
	if err != nil {
		return fmt.Errorf("check payment key conflicts: %w", err)
	}
	if len(conflicts) > 0 {
		c := conflicts[0]
		switch {
		case c.Owner == "":
			return fmt.Errorf("payment entry key %q collides with an existing built-in or shop-configured tender — pick a plugin-specific key", c.Key)
		case !c.OwnerInstalled:
			return fmt.Errorf("payment entry key %q belongs to plugin %s, which is no longer installed — its tender row is retained for sales history; reinstall it or pick a different key", c.Key, c.Owner)
		default:
			return fmt.Errorf("payment entry key %q is already provided by plugin %s — pick a different key", c.Key, c.Owner)
		}
	}
	nameConflicts, err := repo.FindPaymentNameConflicts(ctx, tx, pluginID, paymentNames)
	if err != nil {
		return fmt.Errorf("check payment name conflicts: %w", err)
	}
	if len(nameConflicts) == 0 {
		return nil
	}
	c := nameConflicts[0]
	switch {
	case c.Owner == "":
		return fmt.Errorf("payment entry label %q collides with an existing built-in or shop-configured tender's name — pick a distinct label", c.Key)
	case !c.OwnerInstalled:
		return fmt.Errorf("payment entry label %q belongs to plugin %s, which is no longer installed — its tender row is retained for sales history; reinstall it or pick a different label", c.Key, c.Owner)
	default:
		return fmt.Errorf("payment entry label %q is already used by plugin %s — pick a distinct label", c.Key, c.Owner)
	}
}

// validatePageEntryKeys enforces the install-time half of ut-docs#472:
// internal/plugins.Manager.MenuPlugins (and the /ext/{key} dispatch it
// backs) is keyed by bare entry key across ALL plugins, so an unchecked
// collision between two plugins' type:"page" entries silently overwrites
// one plugin's menu tile/route with another's — no error, no warning.
// Called from both PersistManifest and Rollback, same shape as
// validatePaymentEntryKeys, including the same key-format hygiene checks
// (non-empty, no surrounding whitespace, no ':' — the reserved namespace
// separator) applied to every type:"page" entry, docs included, since a
// malformed key is invalid regardless of the collision exemption below.
//
// DocsEntryKey ("docs", ADR-0037) is EXEMPT from the cross-plugin
// collision check specifically (not the format checks above): a docs
// entry never enters MenuPlugins (loadMenuEntries skips it explicitly)
// and the Docs-button feature looks docs entries up per plugin ID
// (plugins_page.go's docsRouteByPlugin), not by bare key — so two plugins
// both using key:"docs" never collide via MenuPlugins/GET /ext/{key} in
// any live code path today, and rejecting on it would break the very
// convention ADR-0037 asks every plugin author to reuse verbatim. This is
// an implementation decision for the MenuPlugins namespace specifically,
// not a resolution of ADR-0037's own "Not decided here" footnote (a
// different question — a single plugin's manifest declaring two docs
// entries — already prevented by the DB's UNIQUE(plugin_id, key)
// constraint); that footnote stays open for the docs author to settle in
// the ADR itself. Plugins can also still collide on a shared docs
// *route* rather than key (internal/pages/plugin_page.go's /plugin/{...}
// dispatch matches by route, first-row-wins) — a distinct gap, filed
// separately rather than folded into this key-collision fix.
//
// Only type:"page" is checked — it's the only entry type ListMenuEntries
// feeds into MenuPlugins. A within-manifest duplicate page key (two
// entries in the SAME manifest sharing a key) is out of scope here, same
// as it was for payment keys before ut-docs#363: the DB's existing
// UNIQUE(plugin_id, key) constraint already rejects it, just with a raw
// SQLite error rather than a clean one — unchanged by this fix.
func validatePageEntryKeys(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, entries []ManifestEntry) error {
	var checkKeys []string
	for _, e := range entries {
		if e.Type != "page" {
			continue
		}
		if e.Key == "" {
			return fmt.Errorf("page entry has an empty key")
		}
		if e.Key != strings.TrimSpace(e.Key) {
			return fmt.Errorf("page entry key %q has surrounding whitespace", e.Key)
		}
		if strings.Contains(e.Key, ":") {
			return fmt.Errorf("page entry key %q must not contain ':'", e.Key)
		}
		if e.Key == DocsEntryKey {
			continue
		}
		checkKeys = append(checkKeys, e.Key)
	}
	if len(checkKeys) == 0 {
		return nil
	}
	conflicts, err := repo.FindPageKeyConflicts(ctx, tx, pluginID, checkKeys)
	if err != nil {
		return fmt.Errorf("check page key conflicts: %w", err)
	}
	if len(conflicts) == 0 {
		return nil
	}
	c := conflicts[0]
	return fmt.Errorf("page entry key %q is already provided by plugin %s — pick a different key", c.Key, c.Owner)
}

// validatePageEntryRoutes enforces the install-time guard for ut-docs#499: a
// second, independent namespace from validatePageEntryKeys above.
// internal/pages/plugin_page.go's findPageEntry resolves GET /plugin/… by
// matching route against ListPageEntries' rows and returning the first hit
// (ORDER BY sort_order, plugin_id, key) — unguarded, so two plugins
// declaring *different* keys but the *same* route both install cleanly, and
// whichever sorts first silently serves every request to that route; the
// second plugin's page (its Docs button included) opens the first plugin's
// content instead, with no error and no signal to either author or the shop
// owner.
//
// Unlike validatePageEntryKeys, there is no docs exemption here: ADR-0037
// has every docs entry declare its own route ("/plugin/<its-usual-route>"),
// so two plugins sharing a route — key:"docs" or otherwise — is always a
// genuine authoring conflict, never the expected shape.
//
// A page entry with no route (Route == "") isn't dispatchable via
// findPageEntry at all — it can't collide, so it's skipped rather than
// checked or rejected.
//
// Routes must also be unique WITHIN this manifest (independent review
// finding, ut-docs#499): unlike page entry keys, plugin_entries has no
// unique constraint on route, so two entries in the SAME manifest
// declaring distinct keys but the same route would otherwise install
// cleanly — the identical silent-shadowing bug this whole check exists to
// close, just intra-plugin instead of cross-plugin. Checked the same way
// validatePaymentEntryKeys' seenKeys/seenLabels catches the analogous
// within-manifest case for payment entries.
func validatePageEntryRoutes(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, entries []ManifestEntry) error {
	var checkRoutes []string
	seenRoutes := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Type != "page" || e.Route == "" {
			continue
		}
		if seenRoutes[e.Route] {
			return fmt.Errorf("page entry route %q is used by more than one entry in this manifest — pick distinct routes", e.Route)
		}
		seenRoutes[e.Route] = true
		checkRoutes = append(checkRoutes, e.Route)
	}
	if len(checkRoutes) == 0 {
		return nil
	}
	conflicts, err := repo.FindPageRouteConflicts(ctx, tx, pluginID, checkRoutes)
	if err != nil {
		return fmt.Errorf("check page route conflicts: %w", err)
	}
	if len(conflicts) == 0 {
		return nil
	}
	c := conflicts[0]
	return fmt.Errorf("page entry route %q is already provided by plugin %s — pick a different route", c.Route, c.Owner)
}

func PersistManifest(ctx context.Context, db *sql.DB, m *Manifest, opts InstallOptions) error {
	repo := data.NewPluginRepo(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	// 0. Payment-entry keys become payment_methods ids verbatim — a key
	// colliding with a built-in tender or another plugin's method must be
	// rejected here, loudly, instead of silently never materializing
	// (the sync layer's ownership guard refuses the capture; ADR-0031).
	if err := validatePaymentEntryKeys(ctx, repo, tx, m.ID, m.Entries); err != nil {
		return err
	}

	// 0b. Page-entry keys feed the global MenuPlugins map (/menu, /ext/{key})
	// keyed by bare key alone — a key colliding with another installed
	// plugin's page entry must be rejected here, loudly, instead of
	// silently overwriting that plugin's menu tile/route (ut-docs#472).
	if err := validatePageEntryKeys(ctx, repo, tx, m.ID, m.Entries); err != nil {
		return err
	}

	// 0c. Page-entry routes back GET /plugin/… dispatch (first-row-wins) —
	// a route colliding with another installed plugin's page entry must be
	// rejected here too, the same shape as the key check above but a
	// distinct namespace (ut-docs#499).
	if err := validatePageEntryRoutes(ctx, repo, tx, m.ID, m.Entries); err != nil {
		return err
	}

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
	if err := repo.ReconcilePluginSettings(ctx, tx, m.ID, settingRows); err != nil {
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

	// A LAN-sync replica pulls the primary's admin bundle only when its
	// fingerprint moves, and shop-wide (global) plugin settings for a plugin
	// this till lacks are skipped on apply. Clearing the pull cursor makes
	// the next tick re-apply the bundle, so a freshly installed plugin picks
	// up its shared settings within one pull instead of waiting for the
	// primary to change something. No-op on a primary/standalone till, and
	// best-effort — an install must not fail over sync bookkeeping.
	if err := data.NewSettingsRepo(db).Set(ctx, "sync.pull_version", ""); err != nil {
		logging.L().Warnf("plugin install: could not reset sync pull cursor: %v", err)
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
