package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/data"
)

func TestParseManifest_Valid(t *testing.T) {
	manifestJSON := `{
		"id": "com.example.loyalty",
		"name": "Loyalty Plugin",
		"version": "1.0.0",
		"description": "Award loyalty points",
		"author": "Example Inc",
		"website": "https://example.com",
		"entrypoint": "./loyalty",
		"runtime": "go",
		"entries": [
			{
				"type": "page",
				"key": "loyalty.page",
				"label": "Loyalty",
				"menu_group": "Sales",
				"route": "/loyalty"
			},
			{
				"type": "button",
				"key": "loyalty.award_button",
				"label": "Award Points",
				"parent_page_key": "sales",
				"target_action": "loyalty.awardPoints"
			}
		],
		"settings": [
			{
				"key": "apiKey",
				"default_value": "",
				"scope": "global"
			}
		],
		"hooks": [
			{
				"event": "sale.completed",
				"action": "loyalty.awardPoints",
				"priority": 50
			}
		],
		"permissions": [
			"sales:read",
			"sales:write",
			"customers:read"
		]
	}`

	m, err := ParseManifest(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if m.ID != "com.example.loyalty" {
		t.Errorf("expected id 'com.example.loyalty', got '%s'", m.ID)
	}
	if m.Name != "Loyalty Plugin" {
		t.Errorf("expected name 'Loyalty Plugin', got '%s'", m.Name)
	}
	if m.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got '%s'", m.Version)
	}
	if len(m.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m.Entries))
	}
	if len(m.Settings) != 1 {
		t.Errorf("expected 1 setting, got %d", len(m.Settings))
	}
	if len(m.Hooks) != 1 {
		t.Errorf("expected 1 hook, got %d", len(m.Hooks))
	}
	if len(m.Permissions) != 3 {
		t.Errorf("expected 3 permissions, got %d", len(m.Permissions))
	}
}

func TestParseManifest_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name         string
		manifestJSON string
		expectedErr  string
	}{
		{
			name:         "missing id",
			manifestJSON: `{"name": "Test", "version": "1.0.0", "entrypoint": "./test"}`,
			expectedErr:  "missing required field: id",
		},
		{
			name:         "missing name",
			manifestJSON: `{"id": "test", "version": "1.0.0", "entrypoint": "./test"}`,
			expectedErr:  "missing required field: name",
		},
		{
			name:         "missing version",
			manifestJSON: `{"id": "test", "name": "Test", "entrypoint": "./test"}`,
			expectedErr:  "missing required field: version",
		},
		{
			name:         "missing entrypoint",
			manifestJSON: `{"id": "test", "name": "Test", "version": "1.0.0"}`,
			expectedErr:  "missing required field: entrypoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifest(strings.NewReader(tt.manifestJSON))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expectedErr) {
				t.Errorf("expected error containing '%s', got '%v'", tt.expectedErr, err)
			}
		})
	}
}

func TestParseManifest_DefaultRuntime(t *testing.T) {
	manifestJSON := `{
		"id": "test",
		"name": "Test",
		"version": "1.0.0",
		"entrypoint": "./test"
	}`

	m, err := ParseManifest(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if m.Runtime != "go" {
		t.Errorf("expected default runtime 'go', got '%s'", m.Runtime)
	}
}

func TestComputeSHA256(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "hello world"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	hash, err := ComputeSHA256(testFile)
	if err != nil {
		t.Fatalf("ComputeSHA256 failed: %v", err)
	}

	// Expected SHA256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("expected hash '%s', got '%s'", expected, hash)
	}
}

func TestComputeSHA256_NonExistentFile(t *testing.T) {
	_, err := ComputeSHA256("/nonexistent/file.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestPersistManifest(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// A replica's pull cursor: the install must clear it so the next LAN-sync
	// pull re-applies the primary's bundle (shared plugin settings).
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.pull_version', 'abc123')`); err != nil {
		t.Fatalf("seed pull cursor: %v", err)
	}

	manifest := &Manifest{
		ID:          "com.test.plugin",
		Name:        "Test Plugin",
		Version:     "1.0.0",
		Description: "A test plugin",
		Author:      "Test Author",
		Website:     "https://test.com",
		Entrypoint:  "./test",
		Runtime:     "go",
		Entries: []ManifestEntry{
			{
				Type:      "page",
				Key:       "test.page",
				Label:     "Test Page",
				MenuGroup: "Admin",
				Route:     "/test",
			},
			{
				Type:          "button",
				Key:           "test.button",
				Label:         "Test Button",
				ParentPageKey: "sales",
				TargetAction:  "test.action",
			},
		},
		Settings: []ManifestSetting{
			{
				Key:          "apiKey",
				DefaultValue: "default-key",
				Scope:        "global",
			},
		},
		Hooks: []ManifestHook{
			{
				Event:    "sale.completed",
				Action:   "test.onSale",
				Priority: 100,
			},
		},
		Permissions: []string{
			"sales:read",
			"sales:write",
		},
	}

	opts := InstallOptions{
		InstalledFromURL: "https://example.com/plugin.tar.gz",
		SHA256:           "abc123def456",
		TrustLevel:       "untrusted",
		Uploader:         "admin",
	}

	ctx := context.Background()
	if err := PersistManifest(ctx, db, manifest, opts); err != nil {
		t.Fatalf("PersistManifest failed: %v", err)
	}

	// Verify plugin record
	var pluginID, name, version, trustLevel string
	err := db.QueryRowContext(ctx, `
		SELECT id, name, version, trust_level
		FROM plugins
		WHERE id = ?
	`, manifest.ID).Scan(&pluginID, &name, &version, &trustLevel)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}

	if pluginID != "com.test.plugin" {
		t.Errorf("expected plugin id 'com.test.plugin', got '%s'", pluginID)
	}
	if name != "Test Plugin" {
		t.Errorf("expected name 'Test Plugin', got '%s'", name)
	}
	if version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got '%s'", version)
	}
	if trustLevel != "untrusted" {
		t.Errorf("expected trust_level 'untrusted', got '%s'", trustLevel)
	}

	// Verify entries
	var entryCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = ?
	`, manifest.ID).Scan(&entryCount)
	if err != nil {
		t.Fatalf("query entries count: %v", err)
	}
	if entryCount != 2 {
		t.Errorf("expected 2 entries, got %d", entryCount)
	}

	// Verify settings
	var settingCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = ?
	`, manifest.ID).Scan(&settingCount)
	if err != nil {
		t.Fatalf("query settings count: %v", err)
	}
	if settingCount != 1 {
		t.Errorf("expected 1 setting, got %d", settingCount)
	}

	// Verify hooks
	var hookCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM plugin_hooks WHERE plugin_id = ?
	`, manifest.ID).Scan(&hookCount)
	if err != nil {
		t.Fatalf("query hooks count: %v", err)
	}
	if hookCount != 1 {
		t.Errorf("expected 1 hook, got %d", hookCount)
	}

	// Verify the LAN-sync pull cursor was cleared by the install
	var cursor string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.pull_version'`).Scan(&cursor); err != nil {
		t.Fatalf("query pull cursor: %v", err)
	}
	if cursor != "" {
		t.Errorf("expected sync.pull_version cleared after install, got %q", cursor)
	}

	// Verify permissions (not granted by default)
	var permCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM plugin_permissions WHERE plugin_id = ? AND granted = 0
	`, manifest.ID).Scan(&permCount)
	if err != nil {
		t.Fatalf("query permissions count: %v", err)
	}
	if permCount != 2 {
		t.Errorf("expected 2 ungranteed permissions, got %d", permCount)
	}
}

// TestPersistManifest_ImportEntryDeclarationsRoundtrip covers ut-docs#599's
// manifest half: an import entry's entities/file_formats declarations
// (ManifestEntry.Entities/.FileFormats) parse from plugin.json, survive
// PersistManifest (folded into plugin_entries.config_json alongside any
// author config), and come back typed from data.PluginRepo.ListImportEntries
// — which is where the /api/data/import dispatcher reads them.
func TestPersistManifest_ImportEntryDeclarationsRoundtrip(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	manifestJSON := `{
		"id": "com.test.importer",
		"name": "Speedy Importer",
		"version": "1.0.0",
		"entrypoint": "./plugin.wasm",
		"runtime": "wasm",
		"entries": [
			{
				"type": "import",
				"key": "bkp",
				"label": "Speedy .bkp Import",
				"entities": ["items", "categories", "tax_codes"],
				"file_formats": [".bkp"],
				"config": {"vendor": "speedy"}
			},
			{
				"type": "export",
				"key": "csv",
				"label": "CSV Export"
			}
		]
	}`
	m, err := ParseManifest(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// The fields are optional: the export entry declares neither and must
	// parse (and persist) fine without them.
	if len(m.Entries[1].Entities) != 0 || len(m.Entries[1].FileFormats) != 0 {
		t.Fatalf("entry without declarations must have empty slices, got %+v", m.Entries[1])
	}
	if got := m.Entries[0].Entities; len(got) != 3 || got[0] != "items" {
		t.Fatalf("parsed entities = %+v", got)
	}
	if got := m.Entries[0].FileFormats; len(got) != 1 || got[0] != ".bkp" {
		t.Fatalf("parsed file_formats = %+v", got)
	}

	if err := PersistManifest(context.Background(), db, m, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}

	rows, err := data.NewPluginRepo(db).ListImportEntries(context.Background())
	if err != nil {
		t.Fatalf("ListImportEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 import entry (the export entry excluded), got %+v", rows)
	}
	r := rows[0]
	if r.PluginID != "com.test.importer" || r.Key != "bkp" {
		t.Fatalf("unexpected row: %+v", r)
	}
	if len(r.Entities) != 3 || r.Entities[0] != "items" || r.Entities[1] != "categories" || r.Entities[2] != "tax_codes" {
		t.Fatalf("entities did not roundtrip: %+v", r.Entities)
	}
	if len(r.FileFormats) != 1 || r.FileFormats[0] != ".bkp" {
		t.Fatalf("file_formats did not roundtrip: %+v", r.FileFormats)
	}

	// The author's own config keys must survive the fold-in untouched.
	var configJSON string
	if err := db.QueryRow(`SELECT config_json FROM plugin_entries WHERE plugin_id = 'com.test.importer' AND key = 'bkp'`).Scan(&configJSON); err != nil {
		t.Fatalf("read config_json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("parse config_json %q: %v", configJSON, err)
	}
	if cfg["vendor"] != "speedy" {
		t.Fatalf("author config key lost in fold-in: %q", configJSON)
	}
}

func TestPersistManifest_Update(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	manifest := &Manifest{
		ID:         "com.test.update",
		Name:       "Update Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Runtime:    "go",
	}

	opts := InstallOptions{
		SHA256:     "hash1",
		TrustLevel: "untrusted",
	}

	ctx := context.Background()

	// First install
	if err := PersistManifest(ctx, db, manifest, opts); err != nil {
		t.Fatalf("first PersistManifest failed: %v", err)
	}

	// Update manifest
	manifest.Name = "Updated Name"
	opts.SHA256 = "hash2"

	if err := PersistManifest(ctx, db, manifest, opts); err != nil {
		t.Fatalf("second PersistManifest failed: %v", err)
	}

	// Verify update
	var name, sha256 string
	err := db.QueryRowContext(ctx, `
		SELECT name, installed_sha256
		FROM plugins
		WHERE id = ?
	`, manifest.ID).Scan(&name, &sha256)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}

	if name != "Updated Name" {
		t.Errorf("expected name 'Updated Name', got '%s'", name)
	}
	if sha256 != "hash2" {
		t.Errorf("expected sha256 'hash2', got '%s'", sha256)
	}
}

// setupTestDB creates an in-memory SQLite database with plugin tables
func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Create plugin tables
	schema := `
	CREATE TABLE plugin_catalog (
		id TEXT NOT NULL,
		version TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		author TEXT,
		website TEXT,
		repository_url TEXT,
		runtime TEXT NOT NULL,
		entrypoint TEXT NOT NULL,
		package_url TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		signature TEXT,
		size_bytes INTEGER,
		min_pos_version TEXT NOT NULL,
		max_pos_version TEXT,
		api_version TEXT,
		tags_json TEXT,
		capabilities_json TEXT,
		published_at TEXT,
		is_deprecated INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (id, version)
	);

	CREATE TABLE plugins (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		install_state TEXT NOT NULL DEFAULT 'installed',
		description TEXT,
		author TEXT,
		website TEXT,
		entrypoint TEXT NOT NULL,
		runtime TEXT NOT NULL DEFAULT 'go',
		installed_from_url TEXT,
		installed_sha256 TEXT,
		is_active INTEGER NOT NULL DEFAULT 1,
		trust_level TEXT NOT NULL DEFAULT 'untrusted',
		installed_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE (id, version)
	);

	CREATE TABLE plugin_entries (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		type TEXT NOT NULL,
		key TEXT NOT NULL,
		label TEXT NOT NULL,
		icon_path TEXT,
		sort_order INTEGER NOT NULL DEFAULT 0,
		is_active INTEGER NOT NULL DEFAULT 1,
		parent_page_key TEXT,
		menu_group TEXT,
		route TEXT,
		target_action TEXT,
		trigger_event TEXT,
		config_json TEXT,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
		UNIQUE (plugin_id, key)
	);

	CREATE TABLE settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE plugin_settings (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT 'global',
		scope_id TEXT,
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
		UNIQUE (plugin_id, key, scope, scope_id)
	);

	CREATE TABLE plugin_hooks (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		event TEXT NOT NULL,
		action TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 100,
		is_active INTEGER NOT NULL DEFAULT 1,
		config_json TEXT,
		FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
		UNIQUE (plugin_id, event, action)
	);

	CREATE TABLE plugin_permissions (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		permission TEXT NOT NULL,
		granted INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
		UNIQUE (plugin_id, permission)
	);

	CREATE TABLE audit_log (
		id TEXT PRIMARY KEY,
		actor_id TEXT,
		entity_type TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		action TEXT NOT NULL,
		data_json TEXT,
		created_at TEXT NOT NULL
	);
	`

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

func TestParseManifest_RuntimeNoneNeedsNoEntrypoint(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(`{
		"id":"com.x.theme","name":"Theme","version":"1.0.0","runtime":"none",
		"entries":[{"type":"theme","key":"x","label":"X","config":{"css":"assets/theme.css"}}]
	}`))
	if err != nil {
		t.Fatalf("runtime none without entrypoint should parse: %v", err)
	}
	if m.Runtime != "none" || len(m.Entries) != 1 || m.Entries[0].Type != "theme" {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	// Runnable runtimes still require an entrypoint.
	if _, err := ParseManifest(strings.NewReader(`{"id":"a","name":"b","version":"1.0.0","runtime":"go"}`)); err == nil {
		t.Fatal("go runtime without entrypoint must fail")
	}
}
