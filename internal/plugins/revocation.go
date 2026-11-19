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
	"github.com/universaltill/universal-till/internal/netaccess"
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
//
// ID kinds (ut-docs#5653): PluginID is the marketplace LISTING UUID, not
// the plugin manifest id the till's plugins table is keyed by. ManifestID
// is the manifest id (the listing slug); older ut-cloud builds omit it.
// processRevocation therefore resolves the target through ManifestID, then
// the plugin_install_status row for the listing, then PluginID directly.
// Contract: ut-docs reference/contracts/plugin-revocation-feed.md.
type RevocationEntry struct {
	PluginID   string `json:"pluginId"`
	ManifestID string `json:"manifestId,omitempty"`
	Version    string `json:"version,omitempty"` // Empty means all versions
	// Action is "disable" or "delete" on the wire. "delete" is reserved and
	// is treated as "disable" (ut-docs#5658): revocation only disables, the
	// till never uninstalls on its own (it would destroy settings, grants
	// and storage on an unattended poll), and nothing sends "delete" today.
	// The value is kept in the audit row as requested_action.
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
	// onRevoked runs once after a sync that disabled at least one plugin,
	// so loaded WASM plugins are unloaded without a restart (the app wires
	// Deps.ReloadPlugins here). Optional.
	onRevoked func(context.Context) error
}

// SetOnRevoked registers the hook SyncRevocations calls after genuine
// disables. A hook error is logged and never fails the sync.
func (rc *RevocationChecker) SetOnRevoked(fn func(context.Context) error) {
	rc.onRevoked = fn
}

// NewRevocationChecker creates a new revocation checker
func NewRevocationChecker(db *sql.DB, marketplaceURL string, supervisor *Supervisor) *RevocationChecker {
	return &RevocationChecker{
		db:             db,
		marketplaceURL: marketplaceURL,
		httpClient:     netaccess.NewClient(30 * time.Second),
		supervisor:     supervisor,
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

	// ut-docs#5658: persist the feed as the durable revoked-release set
	// BEFORE any per-entry disable, so the record exists even when nothing
	// is installed. Replacing the whole set is also the lift: a release the
	// feed no longer lists loses its record (it never re-enables anything).
	// Any failure here aborts the poll and keeps the old records (fail
	// closed); the fetch/decode errors above return before this point, so a
	// network failure never touches them either.
	if err := rc.recordRevocations(ctx, feed.Revocations); err != nil {
		return 0, err
	}

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

	if revokedCount > 0 && rc.onRevoked != nil {
		if err := rc.onRevoked(ctx); err != nil {
			log.Warnf("[RevocationChecker] Reload after revocation failed: %v", err)
		}
	}

	return revokedCount, nil
}

// recordRevocations stores the feed as the durable revoked-release set.
func (rc *RevocationChecker) recordRevocations(ctx context.Context, entries []RevocationEntry) error {
	repo := data.NewPluginRepo(rc.db)
	rows := make([]data.RevokedRelease, 0, len(entries))
	for _, e := range entries {
		id, err := rc.recordID(ctx, e)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		rows = append(rows, data.RevokedRelease{ManifestID: id, Version: e.Version, ListingID: e.PluginID, Reason: e.Reason, Action: e.Action})
	}
	removed, err := repo.ReplaceRevokedReleases(ctx, rows, time.Now())
	if err != nil {
		return fmt.Errorf("failed to record revocations: %w", err)
	}
	if removed > 0 {
		logging.L().Infof("[RevocationChecker] lifted %d revoked release(s) no longer in the feed", removed)
	}
	return nil
}

// recordID is the manifest id a feed entry is recorded under: ManifestID,
// else the id from plugin_install_status for the listing UUID, else
// PluginID. It needs no plugins row.
func (rc *RevocationChecker) recordID(ctx context.Context, entry RevocationEntry) (string, error) {
	if entry.ManifestID != "" {
		return entry.ManifestID, nil
	}
	if entry.PluginID == "" {
		return "", nil
	}
	row, ok, err := data.NewInstallStatusRepo(rc.db).Get(ctx, entry.PluginID)
	if err != nil {
		return "", fmt.Errorf("failed to query install status: %w", err)
	}
	if ok && row.PluginID != "" {
		return row.PluginID, nil
	}
	return entry.PluginID, nil
}

// resolveTarget maps a feed entry to the installed plugin's manifest id.
// Candidates in order: the entry's ManifestID; the manifest id recorded in
// plugin_install_status for the entry's listing UUID; the raw PluginID.
// The first candidate with a plugins row matching entry.Version wins.
// Returns ("", "", nil) when nothing is installed under any of them.
func (rc *RevocationChecker) resolveTarget(ctx context.Context, entry RevocationEntry) (string, string, error) {
	repo := data.NewPluginRepo(rc.db)
	type cand struct{ id, by string }
	var cands []cand
	if entry.ManifestID != "" {
		cands = append(cands, cand{entry.ManifestID, "manifest_id"})
	}
	if entry.PluginID != "" {
		row, ok, err := data.NewInstallStatusRepo(rc.db).Get(ctx, entry.PluginID)
		if err != nil {
			return "", "", fmt.Errorf("failed to query install status: %w", err)
		}
		if ok && row.PluginID != "" {
			cands = append(cands, cand{row.PluginID, "listing_id"})
		}
		cands = append(cands, cand{entry.PluginID, "plugin_id"})
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		_, found, err := repo.GetPlugin(ctx, c.id, entry.Version)
		if err != nil {
			return "", "", fmt.Errorf("failed to query plugin: %w", err)
		}
		if found {
			return c.id, c.by, nil
		}
	}
	return "", "", nil
}

// processRevocation disables a specific revoked plugin. The returned bool
// reports whether a plugin was actually found, active and disabled — see
// SyncRevocations' own comment on why that's distinct from err == nil.
func (rc *RevocationChecker) processRevocation(ctx context.Context, entry RevocationEntry) (bool, error) {
	log := logging.L()
	repo := data.NewPluginRepo(rc.db)

	pluginID, matchedBy, err := rc.resolveTarget(ctx, entry)
	if err != nil {
		return false, err
	}
	if pluginID == "" {
		// Not installed under any id the entry can name: silent no-op.
		log.Debugf("[RevocationChecker] No installed plugin for revocation %s (manifest %q)", entry.PluginID, entry.ManifestID)
		return false, nil
	}
	pluginRow, found, err := repo.GetPlugin(ctx, pluginID, entry.Version)
	if err != nil {
		return false, fmt.Errorf("failed to query plugin: %w", err)
	}
	if !found {
		return false, nil
	}

	if !pluginRow.IsActive {
		// Already disabled
		return false, nil
	}

	// T031a: Check if plugin is currently running critical operations
	// For now, stop immediately - production should check for active hooks
	if rc.supervisor != nil {
		if err := rc.supervisor.StopPlugin(ctx, pluginID); err != nil {
			log.Warnf("[RevocationChecker] Failed to stop plugin %s: %v", pluginID, err)
		}
	}

	// Disable the plugin in database
	if err := repo.SetPluginState(ctx, pluginID, pluginRow.Version, "revoked", false); err != nil {
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
	_ = repo.InsertAuditRaw(ctx, nil, "disable_revoked", "plugin", pluginID, map[string]any{
		"reason":           entry.Reason,
		"version":          pluginRow.Version,
		"listing_id":       entry.PluginID,
		"matched_by":       matchedBy,
		"requested_action": entry.Action,
		"actor":            "system:revocation",
	}, time.Now())

	log.Infof("[RevocationChecker] Disabled revoked plugin: %s v%s (reason: %s)", pluginID, pluginRow.Version, entry.Reason)
	return true, nil
}
