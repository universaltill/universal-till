package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// RevocationEntry represents one revoked plugin, decoded exactly as
// ut-cloud's GET /v1/revocations actually serves it (ut-docs#2380).
// ut-cloud's HTTP gateway is grpc-gateway's default protojson marshaler
// with no UseProtoNames override (confirmed repo-wide: no such option set
// anywhere in ut-cloud), so wire keys are the proto message's
// json_name/camelCase — never the till-side snake_case a naive
// encoding/json struct tag assumes. ut-cloud's Revocation message
// (pkg/contracts/cloud/v1/cloud.pb.go) carries exactly plugin_id (wire
// name: pluginId), version, action and reason — no developer_id or
// revoked_at field exists on the wire at all. Those two used to be
// declared here and always silently decoded to their zero value; more
// importantly, plugin_id/pluginId being the ONLY mismatched tag meant
// every entry's PluginID decoded as "", so GetPlugin(ctx, "", version)
// never found a match and revocation enforcement was a complete no-op —
// see the ticket for the full trace.
type RevocationEntry struct {
	PluginID string `json:"pluginId"`
	Version  string `json:"version,omitempty"` // Empty means all versions
	// Action is "disable" or "delete" on the wire. Decoded for
	// completeness (it's part of the real contract) but not yet acted on
	// differently — processRevocation treats every entry as a disable,
	// same as before this fix. Differentiating "delete" (a real uninstall,
	// internal/plugins.UninstallPlugin) from "disable" is a deliberate
	// follow-up, not folded into this security fix.
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// RevocationFeed contains the list of revoked plugins from ut-cloud.
// LatestVersion mirrors GetRevocationsResponse.LatestVersion — an int64
// on the proto side, which protojson encodes as a JSON STRING, not a
// number; kept as a string rather than mis-decoding it (marketplace.Client's
// own copy of this field had the same int64 mismatch until ut-docs#2386
// fixed it to match). Nothing here reads it today (it pairs with the
// also-dead since_version cursor on marketplace.Client.GetRevocations,
// ut-docs#2380's own noted follow-up).
type RevocationFeed struct {
	Revocations   []RevocationEntry `json:"revocations"`
	LatestVersion string            `json:"latestVersion,omitempty"`
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

	// Process each revocation. revokedCount counts genuine disables only —
	// processRevocation's (bool, error) split is what makes that possible:
	// "not installed"/"already disabled" are legitimate no-ops (nil error,
	// disabled=false), not failures, but the caller's own "disabled %d
	// revoked plugins" log line (internal/server/server.go) should not
	// count them as if they were. Before this fix every entry with a nil
	// error incremented the count regardless — which, combined with the
	// PluginID decode bug above, meant this log line kept reporting
	// plausible-looking numbers while enforcement was silently a no-op.
	revokedCount := 0
	for _, entry := range feed.Revocations {
		disabled, err := rc.processRevocation(ctx, entry)
		if err != nil {
			log.Warnf("[RevocationChecker] Failed to process revocation for %s: %v", entry.PluginID, err)
			continue
		}
		if disabled {
			revokedCount++
		}
	}

	return revokedCount, nil
}

// processRevocation disables a specific revoked plugin. The returned bool
// reports whether a plugin was actually found, active and disabled — see
// SyncRevocations' own comment on why that's distinct from err == nil.
func (rc *RevocationChecker) processRevocation(ctx context.Context, entry RevocationEntry) (bool, error) {
	log := logging.L()
	repo := data.NewPluginRepo(rc.db)

	// Check if plugin is currently installed and active
	pluginRow, found, err := repo.GetPlugin(ctx, entry.PluginID, entry.Version)
	if err != nil {
		return false, fmt.Errorf("failed to query plugin: %w", err)
	}
	if !found {
		// Plugin not installed, nothing to do
		return false, nil
	}

	if !pluginRow.IsActive {
		// Already disabled
		return false, nil
	}

	// T031a: Check if plugin is currently running critical operations
	// For now, stop immediately - production should check for active hooks
	if rc.supervisor != nil {
		if err := rc.supervisor.StopPlugin(ctx, entry.PluginID); err != nil {
			log.Warnf("[RevocationChecker] Failed to stop plugin %s: %v", entry.PluginID, err)
		}
	}

	// Disable the plugin in database
	if err := repo.SetPluginState(ctx, entry.PluginID, pluginRow.Version, "revoked", false); err != nil {
		return false, fmt.Errorf("failed to disable plugin: %w", err)
	}

	// Add audit log entry. No developer_id: the real feed never carries
	// one (see RevocationEntry's own comment) — recording an always-empty
	// field would be worse than not recording it at all.
	// "requested_action", not "action": the row's own action COLUMN is
	// already the literal string "disable_revoked" (this call's own first
	// arg) — a bare "action" key in the details map next to that reads as
	// if it were describing the same thing, when it's actually the feed
	// entry's requested action ("disable"|"delete"), which this handler
	// currently treats identically either way (see RevocationEntry.Action).
	_ = repo.InsertAuditRaw(ctx, nil, "disable_revoked", "plugin", entry.PluginID, map[string]any{
		"reason":           entry.Reason,
		"version":          pluginRow.Version,
		"requested_action": entry.Action,
		"actor":            "system:revocation",
	}, time.Now())

	log.Infof("[RevocationChecker] Disabled revoked plugin: %s v%s (reason: %s)", entry.PluginID, pluginRow.Version, entry.Reason)
	return true, nil
}
