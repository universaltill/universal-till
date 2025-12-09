package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// RevocationEntry represents a revoked plugin
type RevocationEntry struct {
	PluginID    string    `json:"plugin_id"`
	DeveloperID string    `json:"developer_id"`
	Version     string    `json:"version,omitempty"` // Empty means all versions
	Reason      string    `json:"reason"`
	RevokedAt   time.Time `json:"revoked_at"`
}

// RevocationFeed contains the list of revoked plugins from marketplace
type RevocationFeed struct {
	Revocations []RevocationEntry `json:"revocations"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// RevocationChecker handles plugin revocation synchronization
type RevocationChecker struct {
	db             *sql.DB
	marketplaceURL string
	httpClient     *http.Client
	supervisor     *Supervisor
}

// NewRevocationChecker creates a new revocation checker
func NewRevocationChecker(db *sql.DB, marketplaceURL string, supervisor *Supervisor) *RevocationChecker {
	return &RevocationChecker{
		db:             db,
		marketplaceURL: marketplaceURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		supervisor: supervisor,
	}
}

// SyncRevocations fetches revocation feed and disables revoked plugins (T030)
func (rc *RevocationChecker) SyncRevocations(ctx context.Context) (int, error) {
	log := logging.L()

	// Fetch revocation feed from marketplace
	endpoint := fmt.Sprintf("%s/v1/revocations", rc.marketplaceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch revocations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("marketplace returned status %d", resp.StatusCode)
	}

	var feed RevocationFeed
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return 0, fmt.Errorf("failed to parse revocation feed: %w", err)
	}

	log.Infof("[RevocationChecker] Fetched %d revocation entries", len(feed.Revocations))

	// Process each revocation
	revokedCount := 0
	for _, entry := range feed.Revocations {
		if err := rc.processRevocation(ctx, entry); err != nil {
			log.Warnf("[RevocationChecker] Failed to process revocation for %s: %v", entry.PluginID, err)
		} else {
			revokedCount++
		}
	}

	return revokedCount, nil
}

// processRevocation disables a specific revoked plugin
func (rc *RevocationChecker) processRevocation(ctx context.Context, entry RevocationEntry) error {
	log := logging.L()

	// Check if plugin is currently installed and active
	var pluginID, version string
	var isActive int

	query := `SELECT id, version, is_active FROM plugins WHERE id = ? LIMIT 1`
	if entry.Version != "" {
		query = `SELECT id, version, is_active FROM plugins WHERE id = ? AND version = ? LIMIT 1`
	}

	var err error
	if entry.Version != "" {
		err = rc.db.QueryRowContext(ctx, query, entry.PluginID, entry.Version).Scan(&pluginID, &version, &isActive)
	} else {
		err = rc.db.QueryRowContext(ctx, query, entry.PluginID).Scan(&pluginID, &version, &isActive)
	}

	if err == sql.ErrNoRows {
		// Plugin not installed, nothing to do
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to query plugin: %w", err)
	}

	if isActive == 0 {
		// Already disabled
		return nil
	}

	// T031a: Check if plugin is currently running critical operations
	// For now, stop immediately - production should check for active hooks
	if rc.supervisor != nil {
		if err := rc.supervisor.StopPlugin(ctx, pluginID); err != nil {
			log.Warnf("[RevocationChecker] Failed to stop plugin %s: %v", pluginID, err)
		}
	}

	// Disable the plugin in database
	_, err = rc.db.ExecContext(ctx, `
		UPDATE plugins 
		SET is_active = 0, 
		    install_state = 'revoked'
		WHERE id = ? AND version = ?
	`, pluginID, version)
	if err != nil {
		return fmt.Errorf("failed to disable plugin: %w", err)
	}

	// Add audit log entry
	_, err = rc.db.ExecContext(ctx, `
		INSERT INTO audit_log (timestamp, actor, action, entity_type, entity_id, details)
		VALUES (?, ?, ?, ?, ?, ?)
	`, time.Now(), "system:revocation", "disable_revoked", "plugin", pluginID,
		fmt.Sprintf("Plugin revoked: %s (version: %s)", entry.Reason, version))
	if err != nil {
		log.Warnf("[RevocationChecker] Failed to log audit entry: %v", err)
	}

	log.Infof("[RevocationChecker] Disabled revoked plugin: %s v%s (reason: %s)", pluginID, version, entry.Reason)
	return nil
}

// GetRevokedPlugins returns list of currently revoked plugins
func (rc *RevocationChecker) GetRevokedPlugins(ctx context.Context) ([]RevocationEntry, error) {
	rows, err := rc.db.QueryContext(ctx, `
		SELECT id, version, install_state 
		FROM plugins 
		WHERE install_state = 'revoked'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var revoked []RevocationEntry
	for rows.Next() {
		var id, version, state string
		if err := rows.Scan(&id, &version, &state); err != nil {
			continue
		}
		revoked = append(revoked, RevocationEntry{
			PluginID: id,
			Version:  version,
			Reason:   "Revoked by marketplace",
		})
	}

	return revoked, rows.Err()
}
